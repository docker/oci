// Copyright 2023 CUE Labs AG
//
// Licensed under the Apache License, Version 2.0 (the "License");
// you may not use this file except in compliance with the License.
// You may obtain a copy of the License at
//
//     http://www.apache.org/licenses/LICENSE-2.0
//
// Unless required by applicable law or agreed to in writing, software
// distributed under the License is distributed on an "AS IS" BASIS,
// WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
// See the License for the specific language governing permissions and
// limitations under the License.

package ocilayout

import (
	"context"
	"fmt"
	"io"
	"os"
	"path/filepath"

	"github.com/docker/oci"
	"github.com/docker/oci/ocidigest"
	"github.com/docker/oci/ociref"
)

// PushBlob pushes a blob described by desc to the given repository.
func (r *Registry) PushBlob(ctx context.Context, repo string, desc oci.Descriptor, content io.Reader) (oci.Descriptor, error) {
	if err := contextErr(ctx); err != nil {
		return oci.Descriptor{}, err
	}
	r.mu.Lock()
	st, err := r.layoutForRepoLocked(repo, true)
	r.mu.Unlock()
	if err != nil {
		return oci.Descriptor{}, err
	}
	return writeBlob(st.dir, desc, content)
}

// PushBlobChunked starts a chunked blob upload to the given repository.
func (r *Registry) PushBlobChunked(ctx context.Context, repo string, chunkSize int) (oci.BlobWriter, error) {
	return r.PushBlobChunkedResume(ctx, repo, "", 0, chunkSize)
}

// PushBlobChunkedResume resumes a previous chunked blob upload.
func (r *Registry) PushBlobChunkedResume(ctx context.Context, repo string, id string, offset int64, chunkSize int) (oci.BlobWriter, error) {
	if err := contextErr(ctx); err != nil {
		return nil, err
	}
	r.mu.Lock()
	st, err := r.layoutForRepoLocked(repo, true)
	r.mu.Unlock()
	if err != nil {
		return nil, err
	}
	if chunkSize <= 0 {
		chunkSize = 8 * 1024
	}
	if id == "" {
		id = newUploadID()
	}
	path := filepath.Join(st.dir, "blobs", "uploads", id)
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return nil, err
	}
	f, err := os.OpenFile(path, os.O_CREATE|os.O_RDWR|os.O_APPEND, 0o600)
	if err != nil {
		return nil, err
	}
	info, err := f.Stat()
	if err != nil {
		f.Close()
		return nil, err
	}
	if offset >= 0 && info.Size() != offset {
		f.Close()
		return nil, fmt.Errorf("invalid upload offset %d; actual offset %d: %w", offset, info.Size(), oci.ErrRangeInvalid)
	}
	return &blobWriter{
		registry:  r,
		layoutDir: st.dir,
		id:        id,
		path:      path,
		f:         f,
		chunkSize: chunkSize,
		size:      info.Size(),
	}, nil
}

// MountBlob makes a blob from one repository available in another.
func (r *Registry) MountBlob(ctx context.Context, fromRepo, toRepo string, dig oci.Digest) (oci.Descriptor, error) {
	desc, fromLayout, err := r.resolveBlob(ctx, fromRepo, dig)
	if err != nil {
		return oci.Descriptor{}, err
	}
	r.mu.Lock()
	toLayout, err := r.layoutForRepoLocked(toRepo, true)
	r.mu.Unlock()
	if err != nil {
		return oci.Descriptor{}, err
	}
	if fromLayout.dir == toLayout.dir {
		return desc, nil
	}
	src, err := blobPath(fromLayout.dir, dig)
	if err != nil {
		return oci.Descriptor{}, err
	}
	in, err := os.Open(src)
	if err != nil {
		return oci.Descriptor{}, err
	}
	defer in.Close()
	return writeBlob(toLayout.dir, desc, in)
}

// PushManifest pushes a manifest to the named repository, optionally tagging it.
func (r *Registry) PushManifest(ctx context.Context, repo string, data []byte, mediaType string, params *oci.PushManifestParameters) (oci.Descriptor, error) {
	if err := contextErr(ctx); err != nil {
		return oci.Descriptor{}, err
	}
	if mediaType == "" {
		return oci.Descriptor{}, fmt.Errorf("%w: empty media type", oci.ErrManifestInvalid)
	}
	var dig oci.Digest
	if params != nil && params.Digest != "" {
		dig = params.Digest
		if err := verifyBytesDigest(data, dig); err != nil {
			return oci.Descriptor{}, err
		}
	} else {
		dig = ocidigest.FromBytes(data)
	}
	desc := oci.Descriptor{
		MediaType: mediaType,
		Digest:    dig,
		Size:      int64(len(data)),
	}
	var tags []string
	if params != nil {
		tags = params.Tags
	}
	for _, tag := range tags {
		if !ociref.IsValidTag(tag) {
			return oci.Descriptor{}, fmt.Errorf("%w: invalid tag %q", oci.ErrNameInvalid, tag)
		}
	}

	r.mu.Lock()
	defer r.mu.Unlock()
	st, err := r.layoutForRepoLocked(repo, true)
	if err != nil {
		return oci.Descriptor{}, err
	}
	if err := r.checkManifestReferences(st, mediaType, data); err != nil {
		return oci.Descriptor{}, fmt.Errorf("%w: %v", oci.ErrManifestInvalid, err)
	}
	if _, err := writeBlobBytes(st.dir, desc, data); err != nil {
		return oci.Descriptor{}, err
	}
	if len(tags) == 0 {
		r.upsertManifestRef(st, repo, "", desc)
	} else {
		for _, tag := range tags {
			r.upsertManifestRef(st, repo, tag, desc)
		}
	}
	if err := saveIndex(st.dir, st.index); err != nil {
		return oci.Descriptor{}, err
	}
	return desc, nil
}

func (r *Registry) checkManifestReferences(st *layoutState, mediaType string, data []byte) error {
	info, err := manifestInfoFromBytes(mediaType, data)
	if err != nil {
		return err
	}
	for _, child := range info.descriptors {
		switch child.kind {
		case kindBlob:
			if len(child.desc.URLs) > 0 {
				continue
			}
			if err := ensureBlobExists(st.dir, child.desc.Digest); err != nil {
				return fmt.Errorf("blob for %s not found", child.name)
			}
		case kindManifest:
			if err := ensureBlobExists(st.dir, child.desc.Digest); err != nil {
				return fmt.Errorf("manifest for %s not found", child.name)
			}
		case kindSubjectManifest:
		}
	}
	return nil
}

func (r *Registry) upsertManifestRef(st *layoutState, repo string, tag string, desc oci.Descriptor) {
	ref := r.refFor(repo, tag, desc.Digest)
	desc.Annotations = cloneMap(desc.Annotations)
	desc.Annotations[refNameAnnotation] = ref
	for i, existing := range st.index.Manifests {
		existingRef, err := r.refMatches(repo, tag, desc.Digest, existing)
		if err == nil && existingRef {
			st.index.Manifests[i] = desc
			return
		}
	}
	st.index.Manifests = append(st.index.Manifests, desc)
}

func (r *Registry) refMatches(repo string, tag string, digest oci.Digest, desc oci.Descriptor) (bool, error) {
	ref := ""
	if desc.Annotations != nil {
		ref = desc.Annotations[refNameAnnotation]
	}
	gotRepo, gotTag, err := r.parseRef(ref)
	if err != nil {
		return false, err
	}
	if gotRepo != repo || gotTag != tag {
		return false, nil
	}
	if tag == "" && digest != "" && desc.Digest != digest {
		return false, nil
	}
	return true, nil
}

func verifyBytesDigest(data []byte, dig oci.Digest) error {
	if err := dig.Validate(); err != nil {
		return fmt.Errorf("%w: %v", oci.ErrDigestInvalid, err)
	}
	w, err := ocidigest.NewWriter(nil, dig.Algorithm())
	if err != nil {
		return err
	}
	if _, err := w.Write(data); err != nil {
		return err
	}
	got, err := w.Digest()
	if err != nil {
		return err
	}
	if got != dig {
		return fmt.Errorf("digest mismatch: %w", oci.ErrDigestInvalid)
	}
	return nil
}

func cloneMap(m map[string]string) map[string]string {
	n := make(map[string]string, len(m)+1)
	for k, v := range m {
		n[k] = v
	}
	return n
}

type blobWriter struct {
	registry  *Registry
	layoutDir string
	id        string
	path      string
	f         *os.File
	chunkSize int
	size      int64
	closed    bool
}

func (w *blobWriter) Write(p []byte) (int, error) {
	if w.closed {
		return 0, fmt.Errorf("upload closed")
	}
	n, err := w.f.Write(p)
	w.size += int64(n)
	return n, err
}

func (w *blobWriter) Close() error {
	if w.closed {
		return nil
	}
	w.closed = true
	return w.f.Close()
}

func (w *blobWriter) Size() int64 {
	return w.size
}

func (w *blobWriter) ChunkSize() int {
	return w.chunkSize
}

func (w *blobWriter) ID() string {
	return w.id
}

func (w *blobWriter) Commit(digest oci.Digest) (oci.Descriptor, error) {
	if !w.closed {
		if err := w.Close(); err != nil {
			return oci.Descriptor{}, err
		}
	}
	f, err := os.Open(w.path)
	if err != nil {
		return oci.Descriptor{}, err
	}
	defer f.Close()
	desc := oci.Descriptor{
		Digest:    digest,
		Size:      w.size,
		MediaType: "application/octet-stream",
	}
	desc, err = writeBlob(w.layoutDir, desc, f)
	if err != nil {
		return oci.Descriptor{}, err
	}
	_ = os.Remove(w.path)
	return desc, nil
}

func (w *blobWriter) Cancel() error {
	if !w.closed {
		_ = w.Close()
	}
	if err := os.Remove(w.path); err != nil && !os.IsNotExist(err) {
		return err
	}
	return nil
}
