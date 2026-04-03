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

// Package ocidebug is an OCI registry wrapper that prints log messages
// on registry operations.
package ocidebug

import (
	"context"
	"fmt"
	"io"
	"iter"
	"log"
	"sync/atomic"

	"github.com/docker/oci"
)

// New returns a new [oci.Interface] that wraps r and logs all operations
// using the given logf function. If logf is nil, [log.Printf] is used.
func New(r oci.Interface, logf func(f string, a ...any)) oci.Interface {
	if logf == nil {
		logf = log.Printf
	}
	return &logger{
		logf: logf,
		r:    r,
	}
}

var blobWriterID int32

type logger struct {
	logf func(f string, a ...any)
	r    oci.Interface
	*oci.Funcs
}

func (r *logger) DeleteBlob(ctx context.Context, repoName string, digest oci.Digest) error {
	r.logf("DeleteBlob %s %s {", repoName, digest)
	err := r.r.DeleteBlob(ctx, repoName, digest)
	r.logf("} -> %v", err)
	return err
}

func (r *logger) DeleteManifest(ctx context.Context, repoName string, digest oci.Digest) error {
	r.logf("DeleteManifest %s %s {", repoName, digest)
	err := r.r.DeleteManifest(ctx, repoName, digest)
	r.logf("} -> %v", err)
	return err
}

func (r *logger) DeleteTag(ctx context.Context, repoName string, tagName string) error {
	r.logf("DeleteTag %s %s {", repoName, tagName)
	err := r.r.DeleteTag(ctx, repoName, tagName)
	r.logf("} -> %v", err)
	return err
}

func (r *logger) GetBlob(ctx context.Context, repoName string, dig oci.Digest) (oci.BlobReader, error) {
	r.logf("GetBlob %s %s {", repoName, dig)
	rd, err := r.r.GetBlob(ctx, repoName, dig)
	r.logf("} -> %T, %v", rd, err)
	return rd, err
}

func (r *logger) GetBlobRange(ctx context.Context, repoName string, dig oci.Digest, o0, o1 int64) (oci.BlobReader, error) {
	r.logf("GetBlob %s %s [%d, %d] {", repoName, dig, o0, o1)
	rd, err := r.r.GetBlobRange(ctx, repoName, dig, o0, o1)
	r.logf("} -> %T, %v", rd, err)
	return rd, err
}

func (r *logger) GetManifest(ctx context.Context, repoName string, dig oci.Digest) (oci.BlobReader, error) {
	r.logf("GetManifest %s %s {", repoName, dig)
	rd, err := r.r.GetManifest(ctx, repoName, dig)
	r.logf("} -> %T, %v", rd, err)
	return rd, err
}

func (r *logger) GetTag(ctx context.Context, repoName string, tagName string) (oci.BlobReader, error) {
	r.logf("GetTag %s %s {", repoName, tagName)
	rd, err := r.r.GetTag(ctx, repoName, tagName)
	r.logf("} -> %T, %v", rd, err)
	return rd, err
}

func (r *logger) MountBlob(ctx context.Context, fromRepo, toRepo string, dig oci.Digest) (oci.Descriptor, error) {
	r.logf("MountBlob from=%s to=%s digest=%s {", fromRepo, toRepo, dig)
	desc, err := r.r.MountBlob(ctx, fromRepo, toRepo, dig)
	r.logf("} -> %#v, %v", desc, err)
	return desc, err
}

func (r *logger) PushBlob(ctx context.Context, repoName string, desc oci.Descriptor, content io.Reader) (oci.Descriptor, error) {
	r.logf("PushBlob %s %#v %T {", repoName, desc, content)
	desc, err := r.r.PushBlob(ctx, repoName, desc, content)
	if err != nil {
		r.logf("} -> %v", err)
	} else {
		r.logf("} -> %#v", desc)
	}
	return desc, err
}

func (r *logger) PushBlobChunked(ctx context.Context, repoName string, chunkSize int) (oci.BlobWriter, error) {
	bwid := fmt.Sprintf("bw%d", atomic.AddInt32(&blobWriterID, 1))
	r.logf("PushBlobChunked %s chunkSize=%d {", repoName, chunkSize)
	w, err := r.r.PushBlobChunked(ctx, repoName, chunkSize)
	r.logf("} -> %T(%s), %v", w, bwid, err)
	return blobWriter{
		id: bwid,
		w:  w,
		r:  r,
	}, err
}

func (r *logger) PushBlobChunkedResume(ctx context.Context, repoName, id string, offset int64, chunkSize int) (oci.BlobWriter, error) {
	bwid := fmt.Sprintf("bw%d", atomic.AddInt32(&blobWriterID, 1))
	r.logf("PushBlobChunkedResume %s id=%q offset=%d chunkSize=%d {", repoName, id, offset, chunkSize)
	w, err := r.r.PushBlobChunkedResume(ctx, repoName, id, offset, chunkSize)
	r.logf("} -> %T(%s), %v", w, bwid, err)
	return blobWriter{
		id: bwid,
		w:  w,
		r:  r,
	}, err
}

func (r *logger) PushManifest(ctx context.Context, repoName string, data []byte, mediaType string, params *oci.PushManifestParameters) (oci.Descriptor, error) {
	r.logf("PushManifest %s params=%+v mediaType=%q data=%q {", repoName, params, mediaType, data)
	desc, err := r.r.PushManifest(ctx, repoName, data, mediaType, params)
	if err != nil {
		r.logf("} -> %v", err)
	} else {
		r.logf("} -> %#v", desc)
	}
	return desc, err
}

func (r *logger) Referrers(ctx context.Context, repoName string, digest oci.Digest, params *oci.ReferrersParameters) iter.Seq2[oci.Descriptor, error] {
	var artifactType string
	if params != nil {
		artifactType = params.ArtifactType
	}
	return logIterReturn(
		r,
		fmt.Sprintf("Referrers %s %s %q", repoName, digest, artifactType),
		r.r.Referrers(ctx, repoName, digest, params),
	)
}

func (r *logger) Repositories(ctx context.Context, startAfter string) iter.Seq2[string, error] {
	return logIterReturn(
		r,
		fmt.Sprintf("Repositories startAfter: %q", startAfter),
		r.r.Repositories(ctx, startAfter),
	)
}

func (r *logger) Tags(ctx context.Context, repoName string, params *oci.TagsParameters) iter.Seq2[string, error] {
	var startAfter string
	var limit int
	if params != nil {
		startAfter = params.StartAfter
		limit = params.Limit
	}
	return logIterReturn(
		r,
		fmt.Sprintf("Tags %s startAfter: %q limit: %d", repoName, startAfter, limit),
		r.r.Tags(ctx, repoName, params),
	)
}

func (r *logger) ResolveBlob(ctx context.Context, repoName string, digest oci.Digest) (oci.Descriptor, error) {
	r.logf("ResolveBlob %s %s {", repoName, digest)
	desc, err := r.r.ResolveBlob(ctx, repoName, digest)
	if err != nil {
		r.logf("} -> %v", err)
	} else {
		r.logf("} -> %#v", desc)
	}
	return desc, err
}

func (r *logger) ResolveManifest(ctx context.Context, repoName string, digest oci.Digest) (oci.Descriptor, error) {
	r.logf("ResolveManifest %s %s {", repoName, digest)
	desc, err := r.r.ResolveManifest(ctx, repoName, digest)
	if err != nil {
		r.logf("} -> %v", err)
	} else {
		r.logf("} -> %#v", desc)
	}
	return desc, err
}

func (r *logger) ResolveTag(ctx context.Context, repoName string, tagName string) (oci.Descriptor, error) {
	r.logf("ResolveTag %s %s {", repoName, tagName)
	desc, err := r.r.ResolveTag(ctx, repoName, tagName)
	if err != nil {
		r.logf("} -> %v", err)
	} else {
		r.logf("} -> %#v", desc)
	}
	return desc, err
}

type blobWriter struct {
	id string
	r  *logger
	w  oci.BlobWriter
}

func (w blobWriter) logf(f string, a ...any) {
	w.r.logf("%s: %s", w.id, fmt.Sprintf(f, a...))
}

func (w blobWriter) Write(buf []byte) (int, error) {
	w.logf("Write %q {", buf)
	n, err := w.w.Write(buf)
	w.logf("} -> %v, %v", n, err)
	return n, err
}

func (w blobWriter) ID() string {
	return w.w.ID()
}

func (w blobWriter) Size() int64 {
	size := w.w.Size()
	w.logf("Size -> %v", size)
	return size
}

func (w blobWriter) ChunkSize() int {
	chunkSize := w.w.ChunkSize()
	w.logf("ChunkSize -> %v", chunkSize)
	return chunkSize
}

func (w blobWriter) Close() error {
	w.logf("Close {")
	err := w.w.Close()
	w.logf("} -> %v", err)
	return err
}

func (w blobWriter) Commit(digest oci.Digest) (oci.Descriptor, error) {
	w.logf("Commit %q {", digest)
	desc, err := w.w.Commit(digest)
	w.logf("} -> %#v, %v", desc, err)
	return desc, err
}

func (w blobWriter) Cancel() error {
	w.logf("Cancel {")
	err := w.w.Cancel()
	w.logf("} -> %v", err)
	return err
}

func logIterReturn[T any](r *logger, initialMsg string, it iter.Seq2[T, error]) iter.Seq2[T, error] {
	return func(yield func(T, error) bool) {
		r.logf("%s {", initialMsg)
		items := []T{}
		var _err error
		for item, err := range it {
			if err != nil {
				yield(*new(T), err)
				_err = err
				break
			}
			if !yield(item, nil) {
				break
			}
			items = append(items, item)
		}
		if _err != nil {
			if len(items) > 0 {
				r.logf("} -> %#v, %v", items, _err)
			} else {
				r.logf("} -> %v", _err)
			}
		} else {
			r.logf("} -> %#v", items)
		}
	}
}
