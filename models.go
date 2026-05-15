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

package oci

import (
	"encoding/json"
	"fmt"

	"github.com/docker/oci/ocidigest"
)

const (
	// MediaTypeImageIndex specifies the media type for an image index.
	MediaTypeImageIndex = "application/vnd.oci.image.index.v1+json"
	// MediaTypeImageManifest specifies the media type for an image manifest.
	MediaTypeImageManifest = "application/vnd.oci.image.manifest.v1+json"
	// MediaTypeDockerManifestList is the media type Docker used to use for an index
	MediaTypeDockerManifestList = "application/vnd.docker.distribution.manifest.list.v2+json"
	// MediaTypeDockerManifest is the mediat type Docker used to use for a manifest
	MediaTypeDockerManifest = "application/vnd.docker.distribution.manifest.v2+json"

	// MediaTypeImageConfig specifies the media type for the image configuration.
	MediaTypeImageConfig = "application/vnd.oci.image.config.v1+json"

	// MaxMediaTypeLen is the maximum allowed length for any mediaType field.
	MaxMediaTypeLen = 255
	// MaxArtifactTypeLen is the maximum allowed length for any artifactType field.
	MaxArtifactTypeLen = 255
	// MaxAnnotationKeyLen is the maximum allowed length for an annotation key.
	MaxAnnotationKeyLen = 255
)

// Digest is a content-addressable digest.
type Digest = ocidigest.Digest

// Descriptor describes the disposition of targeted content.
type Descriptor struct {
	// MediaType is the media type of the object this schema refers to.
	MediaType string `json:"mediaType"`

	// Digest is the digest of the targeted content.
	Digest Digest `json:"digest"`

	// Size specifies the size in bytes of the blob.
	Size int64 `json:"size"`

	// URLs specifies a list of URLs from which this object MAY be downloaded.
	URLs []string `json:"urls,omitempty"`

	// Annotations contains arbitrary metadata relating to the targeted content.
	Annotations map[string]string `json:"annotations,omitempty"`

	// Data is an embedding of the targeted content.
	Data []byte `json:"data,omitempty"`

	// Platform describes the platform which the image in the manifest runs on.
	Platform *Platform `json:"platform,omitempty"`

	// ArtifactType is the IANA media type of this artifact.
	ArtifactType string `json:"artifactType,omitempty"`
}

// Platform describes the platform which the image in the manifest runs on.
type Platform struct {
	// Architecture field specifies the CPU architecture, for example amd64 or ppc64le.
	Architecture string `json:"architecture"`

	// OS specifies the operating system, for example linux or windows.
	OS string `json:"os"`

	// OSVersion is an optional field specifying the operating system version.
	OSVersion string `json:"os.version,omitempty"`

	// OSFeatures is an optional field specifying an array of strings listing required OS features.
	OSFeatures []string `json:"os.features,omitempty"`

	// Variant is an optional field specifying a variant of the CPU.
	Variant string `json:"variant,omitempty"`
}

// IndexOrManifest parses the required fields out of a manifest json file. It handles indexes and manifests.
type IndexOrManifest struct {
	SchemaVersion int `json:"schemaVersion"`

	// MediaType specifies the type of this document data structure e.g. `application/vnd.oci.image.manifest.v1+json` // TODO: add validation... if index, make sure it has manifests instead of layers?
	MediaType string `json:"mediaType,omitempty"`

	// ArtifactType specifies the IANA media type of artifact when the manifest is used for an artifact.
	ArtifactType string `json:"artifactType,omitempty"`

	// Manifests references platform specific manifests.
	Manifests []Descriptor `json:"manifests"`

	// Config references a configuration object for a container, by digest.
	// The referenced configuration object is a JSON blob that the runtime uses to set up the container.
	Config *Descriptor `json:"config"`

	// Layers is an indexed list of layers referenced by the manifest.
	Layers []Descriptor `json:"layers"`

	// Subject is an optional link from the image manifest to another manifest forming an association between the image manifest and the other manifest.
	Subject *Descriptor `json:"subject,omitempty"`

	// Annotations contains arbitrary metadata for the image manifest.
	Annotations map[string]string `json:"annotations,omitempty"`
}

// Validate checks the manifest for structural correctness and field length limits.
func (m IndexOrManifest) Validate() error {
	if m.SchemaVersion == 1 {
		return fmt.Errorf("schema version 1 (Docker V1) manifests are not supported")
	}
	switch m.MediaType {
	case MediaTypeImageIndex, MediaTypeDockerManifestList:
		if m.Config != nil {
			return fmt.Errorf("config not supported on index")
		}
		if len(m.Layers) > 0 {
			return fmt.Errorf("layers not supported on index")
		}
	case MediaTypeImageManifest, MediaTypeDockerManifest:
		if len(m.Manifests) > 0 {
			return fmt.Errorf("manifests field not supported on manifest")
		}
		if m.Config == nil {
			return fmt.Errorf("missing config")
		}
	}
	if len(m.MediaType) > MaxMediaTypeLen {
		return fmt.Errorf("mediaType exceeds maximum length of %d", MaxMediaTypeLen)
	}
	if len(m.ArtifactType) > MaxArtifactTypeLen {
		return fmt.Errorf("artifactType exceeds maximum length of %d", MaxArtifactTypeLen)
	}
	for k := range m.Annotations {
		if len(k) > MaxAnnotationKeyLen {
			return fmt.Errorf("annotation key exceeds maximum length of %d", MaxAnnotationKeyLen)
		}
	}
	for i, d := range m.Manifests {
		if err := validateDescriptor("manifests", i, d); err != nil {
			return err
		}
	}
	if m.Config != nil {
		if err := validateDescriptor("config", -1, *m.Config); err != nil {
			return err
		}
	}
	for i, d := range m.Layers {
		if err := validateDescriptor("layers", i, d); err != nil {
			return err
		}
	}
	if m.Subject != nil {
		if err := validateDescriptor("subject", -1, *m.Subject); err != nil {
			return err
		}
	}
	return nil
}

// MarshalJSON validates m before encoding it as JSON.
func (m IndexOrManifest) MarshalJSON() ([]byte, error) {
	if err := m.Validate(); err != nil {
		return nil, err
	}
	type common struct {
		SchemaVersion int               `json:"schemaVersion"`
		MediaType     string            `json:"mediaType,omitempty"`
		ArtifactType  string            `json:"artifactType,omitempty"`
		Subject       *Descriptor       `json:"subject,omitempty"`
		Annotations   map[string]string `json:"annotations,omitempty"`
	}
	c := common{
		SchemaVersion: m.SchemaVersion,
		MediaType:     m.MediaType,
		ArtifactType:  m.ArtifactType,
		Subject:       m.Subject,
		Annotations:   m.Annotations,
	}
	switch m.MediaType {
	case MediaTypeImageIndex, MediaTypeDockerManifestList:
		return json.Marshal(struct {
			common
			Manifests []Descriptor `json:"manifests"`
		}{
			common:    c,
			Manifests: m.Manifests,
		})
	case MediaTypeImageManifest, MediaTypeDockerManifest:
		return json.Marshal(struct {
			common
			Config *Descriptor  `json:"config"`
			Layers []Descriptor `json:"layers"`
		}{
			common: c,
			Config: m.Config,
			Layers: m.Layers,
		})
	default:
		type indexOrManifest IndexOrManifest
		return json.Marshal(indexOrManifest(m))
	}
}

func validateDescriptor(field string, i int, d Descriptor) error {
	name := descriptorFieldName(field, i)
	if err := d.Digest.Validate(); err != nil {
		return fmt.Errorf("%s.digest invalid: %w", name, err)
	}
	if len(d.MediaType) > MaxMediaTypeLen {
		return fmt.Errorf("%s.mediaType exceeds maximum length of %d", name, MaxMediaTypeLen)
	}
	if len(d.ArtifactType) > MaxArtifactTypeLen {
		return fmt.Errorf("%s.artifactType exceeds maximum length of %d", name, MaxArtifactTypeLen)
	}
	for k := range d.Annotations {
		if len(k) > MaxAnnotationKeyLen {
			return fmt.Errorf("%s annotation key exceeds maximum length of %d", name, MaxAnnotationKeyLen)
		}
	}
	return nil
}

func descriptorFieldName(field string, i int) string {
	if i < 0 {
		return field
	}
	return fmt.Sprintf("%s[%d]", field, i)
}
