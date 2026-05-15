package ocimem

import (
	"cmp"
	"encoding/json"
	"fmt"
	"iter"

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
	// descriptors iterates over all direct references inside the manifest
	descriptors descIter
	// subject holds the subject (referred-to manifest) of the manifest
	subject oci.Digest
	// artifactType holds the artifact type of the manifest
	artifactType string
	// annotations holds any annotations from the manifest.
	annotations map[string]string
}

type descIter = iter.Seq[descInfo]

// getManifestInfo returns information on the manifest
// described by the given media type and data.
func getManifestInfo(mediaType string, data []byte) (manifestInfo, error) {
	switch mediaType {
	case oci.MediaTypeImageManifest, oci.MediaTypeDockerManifest,
		oci.MediaTypeImageIndex, oci.MediaTypeDockerManifestList:
	default:
		// TODO provide a configuration option to disallow unknown manifest types.
		//return nil, fmt.Errorf("media type %q: %w", mediaType, errUnknownManifestMediaTypeForIteration)
		return manifestInfo{
			descriptors: func(func(descInfo) bool) {},
		}, nil
	}
	var m oci.IndexOrManifest
	if err := json.Unmarshal(data, &m); err != nil {
		return manifestInfo{}, fmt.Errorf("cannot unmarshal into %T: %v", &m, err)
	}
	if m.MediaType == "" {
		m.MediaType = mediaType
	}
	if err := m.Validate(); err != nil {
		return manifestInfo{}, err
	}
	return indexOrManifestInfo(m), nil
}

// repoTagIter returns an iterator that iterates through
// all the tags in the given repository.
func repoTagIter(r *repository) descIter {
	return func(yield func(descInfo) bool) {
		for tag, desc := range r.tags {
			if !yield(descInfo{
				name: tag,
				desc: desc,
				kind: kindManifest,
			}) {
				break
			}
		}
	}
}

func indexOrManifestInfo(m oci.IndexOrManifest) manifestInfo {
	var info manifestInfo
	info.descriptors = func(yield func(descInfo) bool) {
		for i, manifest := range m.Manifests {
			if !yield(descInfo{
				name: fmt.Sprintf("manifests[%d]", i),
				kind: kindManifest,
				desc: manifest,
			}) {
				return
			}
		}
		for i, layer := range m.Layers {
			if !yield(descInfo{
				name: fmt.Sprintf("layers[%d]", i),
				desc: layer,
				kind: kindBlob,
			}) {
				return
			}
		}
		if m.Config != nil {
			if !yield(descInfo{
				name: "config",
				desc: *m.Config,
				kind: kindBlob,
			}) {
				return
			}
		}
		if m.Subject != nil {
			if !yield(descInfo{
				name: "subject",
				kind: kindSubjectManifest,
				desc: *m.Subject,
			}) {
				return
			}
		}
	}
	// From https://github.com/opencontainers/distribution-spec/blob/main/spec.md#listing-referrers
	//
	// The descriptors MUST include an artifactType field that is set to
	// the value of the artifactType in the image manifest or index, if
	// present. If the artifactType is empty or missing in the image
	// manifest, the value of artifactType MUST be set to the config
	// descriptor mediaType value. If the artifactType is empty or
	// missing in an index, the artifactType MUST be omitted. The
	// descriptors MUST include annotations from the image manifest or
	// index.
	if m.Config != nil {
		info.artifactType = cmp.Or(m.ArtifactType, m.Config.MediaType)
	} else {
		info.artifactType = m.ArtifactType
	}
	info.annotations = m.Annotations
	if m.Subject != nil {
		info.subject = m.Subject.Digest
	}
	return info
}
