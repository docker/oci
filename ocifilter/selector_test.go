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
	"fmt"
	"strings"
	"testing"

	"github.com/docker/oci"
	"github.com/stretchr/testify/require"
)

func ExampleSelector() {
	local := &oci.Funcs{
		DeleteTag_: func(_ context.Context, repo, tag string) error {
			fmt.Printf("local: %s:%s\n", repo, tag)
			return nil
		},
	}
	remote := &oci.Funcs{
		DeleteTag_: func(_ context.Context, repo, tag string) error {
			fmt.Printf("remote: %s:%s\n", repo, tag)
			return nil
		},
	}
	r := Selector(func(repo string) oci.Interface {
		namespace, _, _ := strings.Cut(repo, "/")
		if namespace == "local" {
			return local
		}
		return remote
	})

	_ = r.DeleteTag(context.Background(), "local/example", "latest")
	_ = r.DeleteTag(context.Background(), "shared/example", "latest")

	// Output:
	// local: local/example:latest
	// remote: shared/example:latest
}

func TestSelector(t *testing.T) {
	ctx := context.Background()
	var calls []string
	r0 := &oci.Funcs{
		ResolveTag_: func(_ context.Context, repo, tag string) (oci.Descriptor, error) {
			calls = append(calls, "r0:"+repo+":"+tag)
			return oci.Descriptor{Size: 1}, nil
		},
	}
	r1 := &oci.Funcs{
		ResolveTag_: func(_ context.Context, repo, tag string) (oci.Descriptor, error) {
			calls = append(calls, "r1:"+repo+":"+tag)
			return oci.Descriptor{Size: 2}, nil
		},
	}
	r := Selector(func(repo string) oci.Interface {
		if repo == "local/foo" {
			return r0
		}
		return r1
	})

	desc, err := r.ResolveTag(ctx, "local/foo", "latest")
	require.NoError(t, err)
	require.Equal(t, int64(1), desc.Size)

	desc, err = r.ResolveTag(ctx, "other/foo", "latest")
	require.NoError(t, err)
	require.Equal(t, int64(2), desc.Size)
	require.Equal(t, []string{
		"r0:local/foo:latest",
		"r1:other/foo:latest",
	}, calls)
}

func TestSelectorMountUsesDestinationRepository(t *testing.T) {
	ctx := context.Background()
	var gotFrom, gotTo string
	destination := &oci.Funcs{
		MountBlob_: func(_ context.Context, fromRepo, toRepo string, _ oci.Digest) (oci.Descriptor, error) {
			gotFrom, gotTo = fromRepo, toRepo
			return oci.Descriptor{Size: 42}, nil
		},
	}
	r := Selector(func(repo string) oci.Interface {
		if repo == "destination/repo" {
			return destination
		}
		return (*oci.Funcs)(nil)
	})

	desc, err := r.MountBlob(ctx, "source/repo", "destination/repo", "")
	require.NoError(t, err)
	require.Equal(t, int64(42), desc.Size)
	require.Equal(t, "source/repo", gotFrom)
	require.Equal(t, "destination/repo", gotTo)
}

func TestSelectorRepositoriesUnsupported(t *testing.T) {
	r := Selector(func(string) oci.Interface {
		t.Fatal("selector unexpectedly called")
		return nil
	})

	_, err := oci.All(r.Repositories(context.Background(), ""))
	require.ErrorIs(t, err, oci.ErrUnsupported)
}
