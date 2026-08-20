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

package ociclient

import (
	"encoding/base64"
	"encoding/json"
	"os"
	"testing"

	"github.com/docker/oci/ocidigest"
	"github.com/stretchr/testify/require"
)

// TestSchema1UnsignRealManifest is a golden test against a real signed
// schema1 manifest captured from Docker Hub's library/php:5.3, which is
// known to still be served in schema1 form. The expected digest below is
// the actual Docker-Content-Digest header Docker Hub returned for it.
func TestSchema1UnsignRealManifest(t *testing.T) {
	signed, err := os.ReadFile("testdata/schema1-signed-manifest.json")
	require.NoError(t, err)

	const wantDigest = ocidigest.Digest("sha256:ba952a8970f2fc35e3703b2650495c64d6e015eb52a4ee03f750c69e863b3237")

	unsigned, err := schema1Unsign(signed)
	require.NoError(t, err)
	require.Equal(t, wantDigest, ocidigest.FromBytes(unsigned))

	// The raw signed bytes must NOT hash to the same digest: this is
	// the whole reason schema1Unsign exists.
	require.NotEqual(t, wantDigest, ocidigest.FromBytes(signed))

	// The unsigned form must itself be valid JSON with no "signatures" key.
	var m map[string]any
	require.NoError(t, json.Unmarshal(unsigned, &m))
	require.NotContains(t, m, "signatures")
}

// makeSigned builds a synthetic signed schema1 manifest from unsigned JSON
// content, using the same encoding conventions as real registries
// (base64url, unpadded, for the JWS "protected" header; base64 standard,
// padded, for the "formatTail" field within it, matching Go's
// encoding/json behavior for a []byte field).
func makeSigned(t *testing.T, unsigned []byte, extraSignatures ...string) []byte {
	t.Helper()
	require.True(t, len(unsigned) > 0 && unsigned[len(unsigned)-1] == '}', "unsigned fixture must end in '}'")
	formatLength := len(unsigned) - 1
	formatTail := unsigned[formatLength:] // "}"

	hdr, err := json.Marshal(schema1ProtectedHeader{
		FormatLength: formatLength,
		FormatTail:   formatTail,
	})
	require.NoError(t, err)
	protected := base64.RawURLEncoding.EncodeToString(hdr)

	sigs := `{"header":{"alg":"none"},"protected":"` + protected + `","signature":""}`
	for _, extra := range extraSignatures {
		sigs += "," + extra
	}
	var signed []byte
	signed = append(signed, unsigned[:formatLength]...)
	signed = append(signed, []byte(`,"signatures":[`+sigs+`]}`)...)
	return signed
}

func TestSchema1UnsignSynthetic(t *testing.T) {
	unsigned := []byte(`{"schemaVersion":1,"name":"foo","tag":"bar","fsLayers":[],"history":[]}`)
	signed := makeSigned(t, unsigned)

	got, err := schema1Unsign(signed)
	require.NoError(t, err)
	require.Equal(t, unsigned, got)
}

func TestSchema1UnsignMultipleAgreeingSignatures(t *testing.T) {
	unsigned := []byte(`{"schemaVersion":1,"name":"foo","tag":"bar","fsLayers":[],"history":[]}`)

	// Build a first signed copy just to steal its "protected" value for
	// a second, distinct signature entry that otherwise agrees.
	first := makeSigned(t, unsigned)
	var m struct {
		Signatures []struct {
			Protected string `json:"protected"`
		} `json:"signatures"`
	}
	require.NoError(t, json.Unmarshal(first, &m))
	extra := `{"header":{"alg":"none"},"protected":"` + m.Signatures[0].Protected + `","signature":"different"}`

	signed := makeSigned(t, unsigned, extra)
	got, err := schema1Unsign(signed)
	require.NoError(t, err)
	require.Equal(t, unsigned, got)
}

func TestSchema1UnsignErrors(t *testing.T) {
	unsigned := []byte(`{"schemaVersion":1,"name":"foo","tag":"bar","fsLayers":[],"history":[]}`)
	valid := makeSigned(t, unsigned)

	t.Run("not JSON", func(t *testing.T) {
		_, err := schema1Unsign([]byte("not json"))
		require.Error(t, err)
	})
	t.Run("no signatures field", func(t *testing.T) {
		_, err := schema1Unsign([]byte(`{"schemaVersion":1}`))
		require.ErrorContains(t, err, "no signatures")
	})
	t.Run("empty signatures array", func(t *testing.T) {
		_, err := schema1Unsign([]byte(`{"schemaVersion":1,"signatures":[]}`))
		require.ErrorContains(t, err, "no signatures")
	})
	t.Run("mismatched signatures", func(t *testing.T) {
		_, err := schema1Unsign([]byte(`{"schemaVersion":1,"signatures":[
			{"protected":"aaaa","signature":""},
			{"protected":"bbbb","signature":""}
		]}`))
		require.ErrorContains(t, err, "mismatched signatures")
	})
	t.Run("invalid protected encoding", func(t *testing.T) {
		_, err := schema1Unsign([]byte(`{"schemaVersion":1,"signatures":[{"protected":"not-valid-base64!!","signature":""}]}`))
		require.Error(t, err)
	})
	t.Run("formatLength out of range", func(t *testing.T) {
		hdr, err := json.Marshal(schema1ProtectedHeader{FormatLength: 1 << 20, FormatTail: []byte("}")})
		require.NoError(t, err)
		protected := base64.RawURLEncoding.EncodeToString(hdr)
		_, err = schema1Unsign([]byte(`{"signatures":[{"protected":"` + protected + `","signature":""}]}`))
		require.ErrorContains(t, err, "out of range")
	})
	t.Run("valid input sanity check", func(t *testing.T) {
		_, err := schema1Unsign(valid)
		require.NoError(t, err)
	})
}
