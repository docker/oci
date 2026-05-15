package oci

import (
	"testing"

	"github.com/docker/oci/ocidigest"
	"github.com/stretchr/testify/require"
)

func TestIndexOrManifestValidateRejectsInvalidDescriptorDigest(t *testing.T) {
	m := IndexOrManifest{
		SchemaVersion: 2,
		MediaType:     MediaTypeImageManifest,
		Config: &Descriptor{
			MediaType: MediaTypeImageConfig,
			Digest:    ocidigest.FromBytes([]byte("config")),
			Size:      6,
		},
		Layers: []Descriptor{{
			MediaType: "application/octet-stream",
			Digest:    Digest("sha256:bad"),
			Size:      4,
		}},
	}

	err := m.Validate()
	require.ErrorContains(t, err, "layers[0].digest invalid")
	require.ErrorIs(t, err, ocidigest.ErrEncodingInvalid)
}
