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

package ocimem

import (
	"context"
	"fmt"

	"github.com/docker/oci"
)

// This file implements the oci.Reader methods.

// GetBlob returns the content of the blob with the given digest.
func (r *Registry) GetBlob(ctx context.Context, repoName string, dig oci.Digest) (oci.BlobReader, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	b, err := r.blobForDigest(repoName, dig)
	if err != nil {
		return nil, err
	}
	return NewBytesReader(b.data, b.descriptor()), nil
}

// GetBlobRange returns a range of bytes from the blob with the given digest.
func (r *Registry) GetBlobRange(ctx context.Context, repoName string, dig oci.Digest, o0, o1 int64) (oci.BlobReader, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	b, err := r.blobForDigest(repoName, dig)
	if err != nil {
		return nil, err
	}
	if o1 < 0 || o1 > int64(len(b.data)) {
		o1 = int64(len(b.data))
	}
	if o0 < 0 || o0 > o1 {
		return nil, fmt.Errorf("invalid range [%d, %d]; have [%d, %d]", o0, o1, 0, len(b.data))
	}
	return NewBytesReader(b.data[o0:o1], b.descriptor()), nil
}

// GetManifest returns the content of the manifest with the given digest.
func (r *Registry) GetManifest(ctx context.Context, repoName string, dig oci.Digest) (oci.BlobReader, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	b, err := r.manifestForDigest(repoName, dig)
	if err != nil {
		return nil, err
	}
	return NewBytesReader(b.data, b.descriptor()), nil
}

// GetTag returns the content of the manifest with the given tag.
func (r *Registry) GetTag(ctx context.Context, repoName string, tagName string) (oci.BlobReader, error) {
	desc, err := r.ResolveTag(ctx, repoName, tagName)
	if err != nil {
		return nil, err
	}
	return r.GetManifest(ctx, repoName, desc.Digest)
}

// ResolveTag returns the descriptor for the manifest with the given tag.
func (r *Registry) ResolveTag(ctx context.Context, repoName string, tagName string) (oci.Descriptor, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	repo, err := r.repo(repoName)
	if err != nil {
		return oci.Descriptor{}, err
	}
	desc, ok := repo.tags[tagName]
	if !ok {
		return oci.Descriptor{}, oci.ErrManifestUnknown
	}
	return desc, nil
}

// ResolveBlob returns the descriptor for the blob with the given digest.
func (r *Registry) ResolveBlob(ctx context.Context, repoName string, digest oci.Digest) (oci.Descriptor, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	b, err := r.blobForDigest(repoName, digest)
	if err != nil {
		return oci.Descriptor{}, err
	}
	return b.descriptor(), nil
}

// ResolveManifest returns the descriptor for the manifest with the given digest.
func (r *Registry) ResolveManifest(ctx context.Context, repoName string, digest oci.Digest) (oci.Descriptor, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	b, err := r.manifestForDigest(repoName, digest)
	if err != nil {
		return oci.Descriptor{}, err
	}
	return b.descriptor(), nil
}
