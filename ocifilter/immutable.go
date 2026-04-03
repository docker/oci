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

// Package ocifilter implements "filter" functions that wrap or combine oci
// implementations in different ways.
package ocifilter

import (
	"context"
	"fmt"

	"github.com/docker/oci"
	"github.com/opencontainers/go-digest"
)

// Immutable returns a registry wrap r but only allows content to be
// added but not changed once added: nothing can be deleted and tags
// can't be changed.
func Immutable(r oci.Interface) oci.Interface {
	return immutable{r}
}

type immutable struct {
	oci.Interface
}

func (r immutable) PushManifest(ctx context.Context, repo string, contents []byte, mediaType string, params *oci.PushManifestParameters) (oci.Descriptor, error) {
	var tags []string
	if params != nil {
		tags = params.Tags
	}
	if len(tags) == 0 {
		return r.Interface.PushManifest(ctx, repo, contents, mediaType, params)
	}
	var dig oci.Digest
	if params != nil && params.Digest != "" {
		dig = params.Digest
	} else {
		dig = digest.FromBytes(contents)
	}

	for _, tag := range tags {
		if desc, err := r.ResolveTag(ctx, repo, tag); err == nil {
			if desc.Digest == dig {
				// We're trying to push exactly the same content. That's OK.
				continue
			}
			return oci.Descriptor{}, fmt.Errorf("this store is immutable: %w", oci.ErrDenied)
		}
	}
	desc, err := r.Interface.PushManifest(ctx, repo, contents, mediaType, params)
	if err != nil {
		return oci.Descriptor{}, err
	}
	// We've pushed the tags but someone else might also have pushed them at the same time.
	// UNFORTUNATELY if there was a race, then there's a small window in time where
	// some client might have seen the tag change underfoot.
	for _, tag := range tags {
		tagDesc, err := r.ResolveTag(ctx, repo, tag)
		if err != nil {
			return oci.Descriptor{}, fmt.Errorf("cannot resolve tag %q that's just been pushed: %v", tag, err)
		}
		if tagDesc.Digest != dig {
			// We lost the race.
			return oci.Descriptor{}, fmt.Errorf("this store is immutable: %w", oci.ErrDenied)
		}
	}
	return desc, nil
}

func (r immutable) DeleteBlob(ctx context.Context, repo string, digest oci.Digest) error {
	return oci.ErrDenied
}

func (r immutable) DeleteManifest(ctx context.Context, repo string, digest oci.Digest) error {
	return oci.ErrDenied
}

func (r immutable) DeleteTag(ctx context.Context, repo string, name string) error {
	return oci.ErrDenied
}
