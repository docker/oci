// Copyright 2026 Docker, Inc.
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

package ocifilter

import (
	"context"
	"io"
	"iter"

	"github.com/docker/oci"
)

// Selector returns a registry that delegates each operation to the registry
// returned by selectRegistry. The selector is passed the repository name for
// the operation, and that name is passed unchanged to the selected registry.
// The selector may be called concurrently and must be safe for concurrent use.
//
// For [oci.Writer.MountBlob], the registry is selected using the destination
// repository, toRepo; both fromRepo and toRepo are then passed unchanged to
// that registry. Whether a mount across repositories with different storage
// backends succeeds is determined by the selected registry.
//
// [oci.Extension.Repositories] returns an error wrapping [oci.ErrUnsupported],
// because it has no repository name with which to select a registry.
//
// Selector does not validate selectRegistry. It panics when an operation is
// attempted if selectRegistry is nil or returns a nil registry.
func Selector(selectRegistry func(repo string) oci.Interface) oci.Interface {
	return &selectorRegistry{
		selectRegistry: selectRegistry,
	}
}

type selectorRegistry struct {
	// Embed Funcs so that methods added to oci.Interface in the future default
	// to ErrUnsupported until routing semantics are explicitly implemented.
	*oci.Funcs
	selectRegistry func(repo string) oci.Interface
}

var _ oci.Interface = (*selectorRegistry)(nil)

func (r *selectorRegistry) GetBlob(ctx context.Context, repo string, digest oci.Digest) (oci.BlobReader, error) {
	return r.selectRegistry(repo).GetBlob(ctx, repo, digest)
}

func (r *selectorRegistry) GetBlobRange(ctx context.Context, repo string, digest oci.Digest, offset0, offset1 int64) (oci.BlobReader, error) {
	return r.selectRegistry(repo).GetBlobRange(ctx, repo, digest, offset0, offset1)
}

func (r *selectorRegistry) GetManifest(ctx context.Context, repo string, digest oci.Digest) (oci.BlobReader, error) {
	return r.selectRegistry(repo).GetManifest(ctx, repo, digest)
}

func (r *selectorRegistry) GetTag(ctx context.Context, repo string, tagName string) (oci.BlobReader, error) {
	return r.selectRegistry(repo).GetTag(ctx, repo, tagName)
}

func (r *selectorRegistry) ResolveBlob(ctx context.Context, repo string, digest oci.Digest) (oci.Descriptor, error) {
	return r.selectRegistry(repo).ResolveBlob(ctx, repo, digest)
}

func (r *selectorRegistry) ResolveManifest(ctx context.Context, repo string, digest oci.Digest) (oci.Descriptor, error) {
	return r.selectRegistry(repo).ResolveManifest(ctx, repo, digest)
}

func (r *selectorRegistry) ResolveTag(ctx context.Context, repo string, tagName string) (oci.Descriptor, error) {
	return r.selectRegistry(repo).ResolveTag(ctx, repo, tagName)
}

func (r *selectorRegistry) PushBlob(ctx context.Context, repo string, desc oci.Descriptor, rd io.Reader) (oci.Descriptor, error) {
	return r.selectRegistry(repo).PushBlob(ctx, repo, desc, rd)
}

func (r *selectorRegistry) PushBlobChunked(ctx context.Context, repo string, chunkSize int) (oci.BlobWriter, error) {
	return r.selectRegistry(repo).PushBlobChunked(ctx, repo, chunkSize)
}

func (r *selectorRegistry) PushBlobChunkedResume(ctx context.Context, repo, id string, offset int64, chunkSize int) (oci.BlobWriter, error) {
	return r.selectRegistry(repo).PushBlobChunkedResume(ctx, repo, id, offset, chunkSize)
}

func (r *selectorRegistry) MountBlob(ctx context.Context, fromRepo, toRepo string, digest oci.Digest) (oci.Descriptor, error) {
	return r.selectRegistry(toRepo).MountBlob(ctx, fromRepo, toRepo, digest)
}

func (r *selectorRegistry) PushManifest(ctx context.Context, repo string, contents []byte, mediaType string, params *oci.PushManifestParameters) (oci.Descriptor, error) {
	return r.selectRegistry(repo).PushManifest(ctx, repo, contents, mediaType, params)
}

func (r *selectorRegistry) DeleteBlob(ctx context.Context, repo string, digest oci.Digest) error {
	return r.selectRegistry(repo).DeleteBlob(ctx, repo, digest)
}

func (r *selectorRegistry) DeleteManifest(ctx context.Context, repo string, digest oci.Digest) error {
	return r.selectRegistry(repo).DeleteManifest(ctx, repo, digest)
}

func (r *selectorRegistry) DeleteTag(ctx context.Context, repo string, name string) error {
	return r.selectRegistry(repo).DeleteTag(ctx, repo, name)
}

func (r *selectorRegistry) Tags(ctx context.Context, repo string, params *oci.TagsParameters) iter.Seq2[string, error] {
	return r.selectRegistry(repo).Tags(ctx, repo, params)
}

func (r *selectorRegistry) Referrers(ctx context.Context, repo string, digest oci.Digest, params *oci.ReferrersParameters) iter.Seq2[oci.Descriptor, error] {
	return r.selectRegistry(repo).Referrers(ctx, repo, digest, params)
}
