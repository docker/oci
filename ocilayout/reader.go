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
	"errors"
	"os"

	"github.com/docker/oci"
)

// GetBlob returns the content of the blob with the given digest.
func (r *Registry) GetBlob(ctx context.Context, repo string, digest oci.Digest) (oci.BlobReader, error) {
	desc, st, err := r.resolveBlob(ctx, repo, digest)
	if err != nil {
		return nil, err
	}
	return openBlob(st.dir, desc)
}

// GetBlobRange returns a range of bytes from the blob with the given digest.
func (r *Registry) GetBlobRange(ctx context.Context, repo string, digest oci.Digest, offset0, offset1 int64) (oci.BlobReader, error) {
	desc, st, err := r.resolveBlob(ctx, repo, digest)
	if err != nil {
		return nil, err
	}
	return openBlobRange(st.dir, desc, offset0, offset1)
}

// GetManifest returns the content of the manifest with the given digest.
func (r *Registry) GetManifest(ctx context.Context, repo string, digest oci.Digest) (oci.BlobReader, error) {
	desc, st, err := r.resolveManifest(ctx, repo, digest)
	if err != nil {
		return nil, err
	}
	return openBlob(st.dir, desc)
}

// GetTag returns the content of the manifest with the given tag.
func (r *Registry) GetTag(ctx context.Context, repo string, tagName string) (oci.BlobReader, error) {
	desc, err := r.ResolveTag(ctx, repo, tagName)
	if err != nil {
		return nil, err
	}
	r.mu.Lock()
	st, err := r.layoutForRepoLocked(repo, false)
	r.mu.Unlock()
	if err != nil {
		return nil, err
	}
	return openBlob(st.dir, desc)
}

// ResolveBlob returns the descriptor for the blob with the given digest.
func (r *Registry) ResolveBlob(ctx context.Context, repo string, digest oci.Digest) (oci.Descriptor, error) {
	desc, _, err := r.resolveBlob(ctx, repo, digest)
	return desc, err
}

// ResolveManifest returns the descriptor for the manifest with the given digest.
func (r *Registry) ResolveManifest(ctx context.Context, repo string, digest oci.Digest) (oci.Descriptor, error) {
	desc, _, err := r.resolveManifest(ctx, repo, digest)
	return desc, err
}

// ResolveTag returns the descriptor for the manifest with the given tag.
func (r *Registry) ResolveTag(ctx context.Context, repo string, tagName string) (oci.Descriptor, error) {
	if err := contextErr(ctx); err != nil {
		return oci.Descriptor{}, err
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	st, err := r.layoutForRepoLocked(repo, false)
	if err != nil {
		return oci.Descriptor{}, err
	}
	return r.tagDescriptor(st, repo, tagName)
}

func (r *Registry) resolveBlob(ctx context.Context, repo string, digest oci.Digest) (oci.Descriptor, *layoutState, error) {
	if err := contextErr(ctx); err != nil {
		return oci.Descriptor{}, nil, err
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	st, err := r.layoutForRepoLocked(repo, false)
	if err != nil {
		return oci.Descriptor{}, nil, err
	}
	path, err := blobPath(st.dir, digest)
	if err != nil {
		return oci.Descriptor{}, nil, err
	}
	info, err := os.Stat(path) // #nosec G703 -- path is derived from a validated OCI digest.
	if errors.Is(err, os.ErrNotExist) {
		return oci.Descriptor{}, nil, oci.ErrBlobUnknown
	}
	if err != nil {
		return oci.Descriptor{}, nil, err
	}
	if info.IsDir() {
		return oci.Descriptor{}, nil, oci.ErrBlobUnknown
	}
	desc := oci.Descriptor{
		Digest: digest,
		Size:   info.Size(),
	}
	return desc, st, nil
}

func (r *Registry) resolveManifest(ctx context.Context, repo string, digest oci.Digest) (oci.Descriptor, *layoutState, error) {
	if err := contextErr(ctx); err != nil {
		return oci.Descriptor{}, nil, err
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	st, err := r.layoutForRepoLocked(repo, false)
	if err != nil {
		return oci.Descriptor{}, nil, err
	}
	manifests, err := r.reachable(st, repo)
	if err != nil {
		return oci.Descriptor{}, nil, err
	}
	desc, ok := manifests[digest]
	if !ok {
		return oci.Descriptor{}, nil, oci.ErrManifestUnknown
	}
	if err := ensureBlobExists(st.dir, digest); err != nil {
		return oci.Descriptor{}, nil, err
	}
	return desc, st, nil
}
