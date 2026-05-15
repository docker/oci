package ociclient

import (
	"testing"

	"github.com/docker/oci"
	"github.com/docker/oci/ocidigest"
	"github.com/stretchr/testify/require"
)

func mustDigest(s string) oci.Digest {
	digest, err := ocidigest.Parse(s)
	if err != nil {
		panic(err)
	}
	return digest
}

var referrersTagTests = []struct {
	digest oci.Digest
	want   string
}{{
	// Test case from the distribution spec.
	digest: mustDigest("sha256:aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"),
	want:   "sha256-aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa",
}, {
	// Test case from the distribution spec.
	digest: mustDigest("sha512:aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"),
	want:   "sha512-aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa",
}}

func TestReferrersTag(t *testing.T) {
	for _, test := range referrersTagTests {
		t.Run(test.digest.String(), func(t *testing.T) {
			require.Equal(t, test.want, referrersTag(test.digest))
		})
	}
}
