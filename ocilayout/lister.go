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
	"iter"
	"slices"
	"strings"

	"github.com/docker/oci"
)

// Repositories returns an iterator over repository names in the layout registry.
func (r *Registry) Repositories(ctx context.Context, startAfter string) iter.Seq2[string, error] {
	r.mu.Lock()
	defer r.mu.Unlock()
	if err := contextErr(ctx); err != nil {
		return oci.ErrorSeq[string](err)
	}
	repos, err := r.repositoriesLocked()
	if err != nil {
		return oci.ErrorSeq[string](err)
	}
	repos = slices.DeleteFunc(repos, func(repo string) bool {
		return strings.Compare(startAfter, repo) >= 0
	})
	slices.Sort(repos)
	return oci.SliceSeq(repos)
}

// Tags returns an iterator over tags in the named repository.
func (r *Registry) Tags(ctx context.Context, repo string, params *oci.TagsParameters) iter.Seq2[string, error] {
	r.mu.Lock()
	defer r.mu.Unlock()
	if err := contextErr(ctx); err != nil {
		return oci.ErrorSeq[string](err)
	}
	st, err := r.layoutForRepoLocked(repo, false)
	if err != nil {
		return oci.ErrorSeq[string](err)
	}
	refs, err := r.refsForRepo(st, repo)
	if err != nil {
		return oci.ErrorSeq[string](err)
	}
	if len(refs) == 0 {
		return oci.ErrorSeq[string](oci.ErrNameUnknown)
	}
	var startAfter string
	var limit int
	if params != nil {
		startAfter = params.StartAfter
		limit = params.Limit
	}
	var tags []string
	for _, ref := range refs {
		if ref.tag == "" || strings.Compare(startAfter, ref.tag) >= 0 {
			continue
		}
		tags = append(tags, ref.tag)
	}
	slices.Sort(tags)
	tags = slices.Compact(tags)
	return oci.LimitIter(oci.SliceSeq(tags), limit)
}

// Referrers returns descriptors that refer to the given digest.
func (r *Registry) Referrers(ctx context.Context, repo string, digest oci.Digest, params *oci.ReferrersParameters) iter.Seq2[oci.Descriptor, error] {
	r.mu.Lock()
	defer r.mu.Unlock()
	if err := contextErr(ctx); err != nil {
		return oci.ErrorSeq[oci.Descriptor](err)
	}
	st, err := r.layoutForRepoLocked(repo, false)
	if err != nil {
		return oci.ErrorSeq[oci.Descriptor](err)
	}
	manifests, err := r.reachable(st, repo)
	if err != nil {
		return oci.ErrorSeq[oci.Descriptor](err)
	}
	var artifactType string
	if params != nil {
		artifactType = params.ArtifactType
	}
	var refs []oci.Descriptor
	for _, desc := range manifests {
		data, err := osReadBlob(st.dir, desc.Digest)
		if err != nil {
			return oci.ErrorSeq[oci.Descriptor](err)
		}
		info, err := manifestInfoFromBytes(desc.MediaType, data)
		if err != nil {
			return oci.ErrorSeq[oci.Descriptor](err)
		}
		if info.subject != digest {
			continue
		}
		if artifactType != "" && info.artifactType != artifactType {
			continue
		}
		desc.ArtifactType = info.artifactType
		desc.Annotations = info.annotations
		refs = append(refs, desc)
	}
	slices.SortFunc(refs, func(a, b oci.Descriptor) int {
		return strings.Compare(a.Digest.String(), b.Digest.String())
	})
	return oci.SliceSeq(refs)
}

func (r *Registry) repositoriesLocked() ([]string, error) {
	st, err := r.loadLayoutLocked(false)
	if err != nil {
		return nil, err
	}
	seen := make(map[string]bool)
	for _, desc := range st.index.Manifests {
		ref := ""
		if desc.Annotations != nil {
			ref = desc.Annotations[refNameAnnotation]
		}
		repo, _, err := r.parseRef(ref)
		if err != nil {
			return nil, err
		}
		seen[repo] = true
	}
	return mapKeys(seen), nil
}

func mapKeys(m map[string]bool) []string {
	xs := make([]string, 0, len(m))
	for k := range m {
		xs = append(xs, k)
	}
	return xs
}
