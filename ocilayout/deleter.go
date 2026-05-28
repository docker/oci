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

	"github.com/docker/oci"
)

// DeleteBlob deletes the blob with the given digest from the named repository.
func (r *Registry) DeleteBlob(ctx context.Context, repo string, digest oci.Digest) error {
	if err := contextErr(ctx); err != nil {
		return err
	}
	if _, _, err := r.resolveBlob(ctx, repo, digest); err != nil {
		return err
	}
	return fmt.Errorf("%w: blob garbage collection is not implemented", oci.ErrDenied)
}

// DeleteManifest deletes top-level index entries for the manifest digest.
func (r *Registry) DeleteManifest(ctx context.Context, repo string, digest oci.Digest) error {
	if err := contextErr(ctx); err != nil {
		return err
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	st, err := r.layoutForRepoLocked(repo, false)
	if err != nil {
		return err
	}
	refs, err := r.refsForRepo(st, repo)
	if err != nil {
		return err
	}
	if len(refs) == 0 {
		return oci.ErrNameUnknown
	}
	found := false
	out := st.index.Manifests[:0]
	for _, desc := range st.index.Manifests {
		match, err := r.refDescriptorMatches(repo, "", digest, desc)
		if err != nil {
			return err
		}
		if match {
			found = true
			continue
		}
		out = append(out, desc)
	}
	if !found {
		return oci.ErrManifestUnknown
	}
	st.index.Manifests = out
	return saveIndex(st.dir, st.index)
}

// DeleteTag deletes the given tag from the named repository.
func (r *Registry) DeleteTag(ctx context.Context, repo string, tagName string) error {
	if err := contextErr(ctx); err != nil {
		return err
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	st, err := r.layoutForRepoLocked(repo, false)
	if err != nil {
		return err
	}
	if _, err := r.tagDescriptor(st, repo, tagName); err != nil {
		return err
	}
	out := st.index.Manifests[:0]
	for _, desc := range st.index.Manifests {
		match, err := r.refDescriptorMatches(repo, tagName, "", desc)
		if err != nil {
			return err
		}
		if match {
			continue
		}
		out = append(out, desc)
	}
	st.index.Manifests = out
	return saveIndex(st.dir, st.index)
}

func (r *Registry) refDescriptorMatches(repo string, tag string, digest oci.Digest, desc oci.Descriptor) (bool, error) {
	ref := ""
	if desc.Annotations != nil {
		ref = desc.Annotations[refNameAnnotation]
	}
	gotRepo, gotTag, err := r.parseRef(ref)
	if err != nil {
		return false, err
	}
	if gotRepo != repo {
		return false, nil
	}
	if tag != "" && gotTag != tag {
		return false, nil
	}
	if digest != "" && desc.Digest != digest {
		return false, nil
	}
	return true, nil
}
