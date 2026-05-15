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

package ocifilter

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"slices"
	"testing"

	"github.com/docker/oci"
	"github.com/docker/oci/ociauth"
	"github.com/docker/oci/ocimem"
	"github.com/docker/oci/ocitest"
	"github.com/stretchr/testify/require"
)

func TestSub(t *testing.T) {
	ctx := context.Background()
	r := ocitest.NewRegistry(t, ocimem.New())
	r.MustPushContent(ocitest.RegistryContent{
		"foo/bar": {
			Blobs: map[string]string{
				"b1":      "hello",
				"scratch": "{}",
			},
			Manifests: map[string]oci.IndexOrManifest{
				"m1": {
					MediaType: oci.MediaTypeImageManifest,
					Config: ref(oci.Descriptor{
						Digest: ocitest.DigestRef("scratch"),
					}),
					Layers: []oci.Descriptor{{
						Digest: ocitest.DigestRef("b1"),
					}},
				},
			},
			Tags: map[string]string{
				"t1": "m1",
				"t2": "m1",
			},
		},
		"fooey": {
			Blobs: map[string]string{
				"scratch": "{}",
			},
			Manifests: map[string]oci.IndexOrManifest{
				"m1": {
					MediaType: oci.MediaTypeImageManifest,
					Config: ref(oci.Descriptor{
						Digest: ocitest.DigestRef("scratch"),
					}),
				},
			},
			Tags: map[string]string{
				"t1": "m1",
			},
		},
		"other/blah": {
			Blobs: map[string]string{
				"scratch": "{}",
			},
			Manifests: map[string]oci.IndexOrManifest{
				"m1": {
					MediaType: oci.MediaTypeImageManifest,
					Config: ref(oci.Descriptor{
						Digest: ocitest.DigestRef("scratch"),
					}),
				},
			},
			Tags: map[string]string{
				"t1": "m1",
			},
		},
	})
	r1 := Sub(r.R, "foo")
	desc, err := r1.ResolveTag(ctx, "bar", "t1")
	require.NoError(t, err)

	m := getManifest(t, r1, "bar", desc.Digest)
	b1Content := getBlob(t, r1, "bar", m.Layers[0].Digest)
	require.Equal(t, "hello", string(b1Content))

	repos, err := oci.All(r1.Repositories(ctx, ""))
	require.NoError(t, err)
	slices.Sort(repos)
	require.Equal(t, []string{"bar"}, repos)
}

func TestSubMaintainsAuthScope(t *testing.T) {
	var gotScope ociauth.Scope
	r := Sub(contextChecker{
		check: func(ctx context.Context) {
			gotScope = ociauth.ScopeFromContext(ctx)
		},
	}, "foo/bar")
	scope := ociauth.ParseScope("other registry:catalog:* repository:a/b:pull,push repository:foo:delete,push")
	ctx := ociauth.ContextWithScope(context.Background(), scope)

	// As the implementation is so uniform (and easily inspected in the source,
	// we use the GetBlob entry point as a proxy for testing all the entry points.
	// TODO it would be nice to have a reusable way (in ocitest, probably) of testing general properties
	// across all oci.Interface methods.
	_, _ = r.GetBlob(ctx, "some/repo", ocitest.DigestRef("scope"))
	wantScope := ociauth.ParseScope(
		"other registry:catalog:* repository:foo/bar/a/b:pull,push repository:foo/bar/foo:delete,push",
	)
	require.True(t, wantScope.Equal(gotScope), "scope mismatch: got %v, want %v", gotScope, wantScope)
}

type contextChecker struct {
	oci.Interface
	check func(context.Context)
}

func (r contextChecker) GetBlob(ctx context.Context, repo string, digest oci.Digest) (oci.BlobReader, error) {
	r.check(ctx)
	return nil, fmt.Errorf("nope")
}

func getManifest(t *testing.T, r oci.Interface, repo string, dg oci.Digest) oci.IndexOrManifest {
	rd, err := r.GetManifest(context.Background(), repo, dg)
	require.NoError(t, err)
	defer rd.Close()
	var m oci.IndexOrManifest
	data, err := io.ReadAll(rd)
	require.NoError(t, err)
	err = json.Unmarshal(data, &m)
	require.NoError(t, err)
	return m
}

func getBlob(t *testing.T, r oci.Interface, repo string, dg oci.Digest) []byte {
	rd, err := r.GetBlob(context.Background(), repo, dg)
	require.NoError(t, err)
	defer rd.Close()
	data, err := io.ReadAll(rd)
	require.NoError(t, err)
	return data
}

func ref[T any](x T) *T {
	return &x
}
