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
	"cmp"
	"encoding/json"
	"fmt"
	"os"

	"github.com/docker/oci"
)

type refKind int

const (
	kindSubjectManifest refKind = iota
	kindBlob
	kindManifest
)

type descInfo struct {
	name string
	kind refKind
	desc oci.Descriptor
}

type manifestInfo struct {
	descriptors  []descInfo
	subject      oci.Digest
	artifactType string
	annotations  map[string]string
}

type topRef struct {
	desc oci.Descriptor
	repo string
	tag  string
}

func (r *Registry) refsForRepo(st *layoutState, repo string) ([]topRef, error) {
	var refs []topRef
	for _, desc := range st.index.Manifests {
		ref, ok := desc.Annotations[refNameAnnotation]
		if !ok {
			ref = ""
		}
		gotRepo, tag, err := r.parseRef(ref)
		if err != nil {
			return nil, err
		}
		if gotRepo == repo {
			refs = append(refs, topRef{desc: desc, repo: gotRepo, tag: tag})
		}
	}
	return refs, nil
}

func (r *Registry) tagDescriptor(st *layoutState, repo string, tag string) (oci.Descriptor, error) {
	refs, err := r.refsForRepo(st, repo)
	if err != nil {
		return oci.Descriptor{}, err
	}
	if len(refs) == 0 {
		return oci.Descriptor{}, oci.ErrNameUnknown
	}
	for _, ref := range refs {
		if ref.tag == tag {
			return ref.desc, nil
		}
	}
	return oci.Descriptor{}, oci.ErrManifestUnknown
}

func manifestInfoFromBytes(mediaType string, data []byte) (manifestInfo, error) {
	switch mediaType {
	case oci.MediaTypeImageManifest, oci.MediaTypeDockerManifest,
		oci.MediaTypeImageIndex, oci.MediaTypeDockerManifestList:
	default:
		return manifestInfo{}, nil
	}
	var m oci.IndexOrManifest
	if err := json.Unmarshal(data, &m); err != nil {
		return manifestInfo{}, fmt.Errorf("cannot unmarshal manifest: %v", err)
	}
	if m.MediaType == "" {
		m.MediaType = mediaType
	}
	if err := m.Validate(); err != nil {
		return manifestInfo{}, err
	}
	return indexOrManifestInfo(m), nil
}

func indexOrManifestInfo(m oci.IndexOrManifest) manifestInfo {
	var info manifestInfo
	for i, manifest := range m.Manifests {
		info.descriptors = append(info.descriptors, descInfo{
			name: fmt.Sprintf("manifests[%d]", i),
			kind: kindManifest,
			desc: manifest,
		})
	}
	for i, layer := range m.Layers {
		info.descriptors = append(info.descriptors, descInfo{
			name: fmt.Sprintf("layers[%d]", i),
			kind: kindBlob,
			desc: layer,
		})
	}
	if m.Config != nil {
		info.descriptors = append(info.descriptors, descInfo{
			name: "config",
			kind: kindBlob,
			desc: *m.Config,
		})
	}
	if m.Subject != nil {
		info.descriptors = append(info.descriptors, descInfo{
			name: "subject",
			kind: kindSubjectManifest,
			desc: *m.Subject,
		})
		info.subject = m.Subject.Digest
	}
	if m.Config != nil {
		info.artifactType = cmp.Or(m.ArtifactType, m.Config.MediaType)
	} else {
		info.artifactType = m.ArtifactType
	}
	info.annotations = m.Annotations
	return info
}

func (r *Registry) reachable(st *layoutState, repo string) (map[oci.Digest]oci.Descriptor, error) {
	refs, err := r.refsForRepo(st, repo)
	if err != nil {
		return nil, err
	}
	if len(refs) == 0 {
		return nil, oci.ErrNameUnknown
	}
	manifests := make(map[oci.Digest]oci.Descriptor)
	visiting := make(map[oci.Digest]bool)
	for _, ref := range refs {
		if err := r.walkManifest(st, ref.desc, manifests, visiting); err != nil {
			return nil, err
		}
	}
	return manifests, nil
}

func (r *Registry) walkManifest(st *layoutState, desc oci.Descriptor, manifests map[oci.Digest]oci.Descriptor, visiting map[oci.Digest]bool) error {
	if visiting[desc.Digest] {
		return nil
	}
	visiting[desc.Digest] = true
	defer delete(visiting, desc.Digest)
	manifests[desc.Digest] = desc
	data, err := osReadBlob(st.dir, desc.Digest)
	if err != nil {
		return nil
	}
	info, err := manifestInfoFromBytes(desc.MediaType, data)
	if err != nil {
		return err
	}
	for _, child := range info.descriptors {
		if child.kind == kindManifest {
			if err := r.walkManifest(st, child.desc, manifests, visiting); err != nil {
				return err
			}
		}
	}
	return nil
}

func osReadBlob(dir string, digest oci.Digest) ([]byte, error) {
	path, err := blobPath(dir, digest)
	if err != nil {
		return nil, err
	}
	return os.ReadFile(path)
}
