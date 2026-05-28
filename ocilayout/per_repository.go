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
	"fmt"
	"io"
	"io/fs"
	"iter"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"sync"

	"github.com/docker/oci"
	"github.com/docker/oci/ocifilter"
	"github.com/docker/oci/ociref"
)

// NewPerRepository opens an OCI Image Layout registry that stores each
// repository in a separate OCI layout under dir.
func NewPerRepository(dir string, _ *PerRepoOptions) (oci.Interface, error) {
	if dir == "" {
		return nil, fmt.Errorf("directory must not be empty")
	}
	abs, err := filepath.Abs(dir)
	if err != nil {
		return nil, err
	}
	return &perRepositoryRegistry{
		dir:    abs,
		layout: make(map[string]*repoLayout),
	}, nil
}

// PerRepoOptions holds configuration for opening a per-repository layout
// registry.
type PerRepoOptions struct{}

type perRepositoryRegistry struct {
	*oci.Funcs
	mu     sync.Mutex
	dir    string
	layout map[string]*repoLayout
}

var _ oci.Interface = (*perRepositoryRegistry)(nil)

type repoLayout struct {
	raw oci.Interface
	sub oci.Interface
}

func (r *perRepositoryRegistry) GetBlob(ctx context.Context, repo string, digest oci.Digest) (oci.BlobReader, error) {
	layout, err := r.layoutForRepo(repo)
	if err != nil {
		return nil, err
	}
	return layout.GetBlob(ctx, ".", digest)
}

func (r *perRepositoryRegistry) GetBlobRange(ctx context.Context, repo string, digest oci.Digest, offset0, offset1 int64) (oci.BlobReader, error) {
	layout, err := r.layoutForRepo(repo)
	if err != nil {
		return nil, err
	}
	return layout.GetBlobRange(ctx, ".", digest, offset0, offset1)
}

func (r *perRepositoryRegistry) GetManifest(ctx context.Context, repo string, digest oci.Digest) (oci.BlobReader, error) {
	layout, err := r.layoutForRepo(repo)
	if err != nil {
		return nil, err
	}
	return layout.GetManifest(ctx, ".", digest)
}

func (r *perRepositoryRegistry) GetTag(ctx context.Context, repo string, tagName string) (oci.BlobReader, error) {
	layout, err := r.layoutForRepo(repo)
	if err != nil {
		return nil, err
	}
	return layout.GetTag(ctx, ".", tagName)
}

func (r *perRepositoryRegistry) ResolveBlob(ctx context.Context, repo string, digest oci.Digest) (oci.Descriptor, error) {
	layout, err := r.layoutForRepo(repo)
	if err != nil {
		return oci.Descriptor{}, err
	}
	return layout.ResolveBlob(ctx, ".", digest)
}

func (r *perRepositoryRegistry) ResolveManifest(ctx context.Context, repo string, digest oci.Digest) (oci.Descriptor, error) {
	layout, err := r.layoutForRepo(repo)
	if err != nil {
		return oci.Descriptor{}, err
	}
	return layout.ResolveManifest(ctx, ".", digest)
}

func (r *perRepositoryRegistry) ResolveTag(ctx context.Context, repo string, tagName string) (oci.Descriptor, error) {
	layout, err := r.layoutForRepo(repo)
	if err != nil {
		return oci.Descriptor{}, err
	}
	return layout.ResolveTag(ctx, ".", tagName)
}

func (r *perRepositoryRegistry) PushBlob(ctx context.Context, repo string, desc oci.Descriptor, content io.Reader) (oci.Descriptor, error) {
	layout, err := r.layoutForRepo(repo)
	if err != nil {
		return oci.Descriptor{}, err
	}
	return layout.PushBlob(ctx, ".", desc, content)
}

func (r *perRepositoryRegistry) PushBlobChunked(ctx context.Context, repo string, chunkSize int) (oci.BlobWriter, error) {
	layout, err := r.layoutForRepo(repo)
	if err != nil {
		return nil, err
	}
	return layout.PushBlobChunked(ctx, ".", chunkSize)
}

func (r *perRepositoryRegistry) PushBlobChunkedResume(ctx context.Context, repo, id string, offset int64, chunkSize int) (oci.BlobWriter, error) {
	layout, err := r.layoutForRepo(repo)
	if err != nil {
		return nil, err
	}
	return layout.PushBlobChunkedResume(ctx, ".", id, offset, chunkSize)
}

func (r *perRepositoryRegistry) MountBlob(ctx context.Context, fromRepo, toRepo string, digest oci.Digest) (oci.Descriptor, error) {
	if fromRepo == toRepo {
		layout, err := r.layoutForRepo(fromRepo)
		if err != nil {
			return oci.Descriptor{}, err
		}
		return layout.MountBlob(ctx, ".", ".", digest)
	}
	desc, err := r.ResolveBlob(ctx, fromRepo, digest)
	if err != nil {
		return oci.Descriptor{}, err
	}
	br, err := r.GetBlob(ctx, fromRepo, digest)
	if err != nil {
		return oci.Descriptor{}, err
	}
	defer br.Close()
	return r.PushBlob(ctx, toRepo, desc, br)
}

func (r *perRepositoryRegistry) PushManifest(ctx context.Context, repo string, data []byte, mediaType string, params *oci.PushManifestParameters) (oci.Descriptor, error) {
	layout, err := r.layoutForRepo(repo)
	if err != nil {
		return oci.Descriptor{}, err
	}
	return layout.PushManifest(ctx, ".", data, mediaType, params)
}

func (r *perRepositoryRegistry) DeleteBlob(ctx context.Context, repo string, digest oci.Digest) error {
	layout, err := r.layoutForRepo(repo)
	if err != nil {
		return err
	}
	return layout.DeleteBlob(ctx, ".", digest)
}

func (r *perRepositoryRegistry) DeleteManifest(ctx context.Context, repo string, digest oci.Digest) error {
	layout, err := r.layoutForRepo(repo)
	if err != nil {
		return err
	}
	return layout.DeleteManifest(ctx, ".", digest)
}

func (r *perRepositoryRegistry) DeleteTag(ctx context.Context, repo string, tagName string) error {
	layout, err := r.layoutForRepo(repo)
	if err != nil {
		return err
	}
	return layout.DeleteTag(ctx, ".", tagName)
}

func (r *perRepositoryRegistry) Repositories(ctx context.Context, startAfter string) iter.Seq2[string, error] {
	if err := contextErr(ctx); err != nil {
		return oci.ErrorSeq[string](err)
	}
	repos, err := r.repositories(ctx)
	if err != nil {
		return oci.ErrorSeq[string](err)
	}
	repos = slices.DeleteFunc(repos, func(repo string) bool {
		return strings.Compare(startAfter, repo) >= 0
	})
	slices.Sort(repos)
	return oci.SliceSeq(repos)
}

func (r *perRepositoryRegistry) Tags(ctx context.Context, repo string, params *oci.TagsParameters) iter.Seq2[string, error] {
	layout, err := r.layoutForRepo(repo)
	if err != nil {
		return oci.ErrorSeq[string](err)
	}
	return layout.Tags(ctx, ".", params)
}

func (r *perRepositoryRegistry) Referrers(ctx context.Context, repo string, digest oci.Digest, params *oci.ReferrersParameters) iter.Seq2[oci.Descriptor, error] {
	layout, err := r.layoutForRepo(repo)
	if err != nil {
		return oci.ErrorSeq[oci.Descriptor](err)
	}
	return layout.Referrers(ctx, ".", digest, params)
}

func (r *perRepositoryRegistry) layoutForRepo(repo string) (oci.Interface, error) {
	layout, err := r.openRepoLayout(repo)
	if err != nil {
		return nil, err
	}
	return layout.sub, nil
}

func (r *perRepositoryRegistry) openRepoLayout(repo string) (*repoLayout, error) {
	if !ociref.IsValidRepository(repo) {
		return nil, oci.ErrNameInvalid
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	if layout := r.layout[repo]; layout != nil {
		return layout, nil
	}
	opts := Options{DefaultRepo: repo}
	raw, err := New(filepath.Join(r.dir, filepath.FromSlash(repo)), &opts) // #nosec G305 -- repo has been validated as an OCI repository name.
	if err != nil {
		return nil, err
	}
	layout := &repoLayout{
		raw: raw,
		sub: ocifilter.Sub(raw, repo),
	}
	r.layout[repo] = layout
	return layout, nil
}

func (r *perRepositoryRegistry) repositories(ctx context.Context) ([]string, error) {
	seen := make(map[string]bool)
	if err := filepath.WalkDir(r.dir, func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if d.IsDir() || d.Name() != "index.json" {
			return nil
		}
		repoDir := filepath.Dir(path)
		rel, err := filepath.Rel(r.dir, repoDir)
		if err != nil {
			return err
		}
		repo := filepath.ToSlash(rel)
		if !ociref.IsValidRepository(repo) {
			return nil
		}
		layout, err := r.openRepoLayout(repo)
		if err != nil {
			return err
		}
		repos, err := oci.All(layout.raw.Repositories(ctx, ""))
		if err != nil {
			return err
		}
		for _, repo := range repos {
			seen[repo] = true
		}
		return nil
	}); err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return nil, nil
		}
		return nil, err
	}
	return mapKeys(seen), nil
}
