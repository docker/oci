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
	"fmt"
)

// Legacy Docker Manifest v2 Schema 1 media types.
//
// Schema1 predates the OCI distribution spec and isn't otherwise supported
// by this module (see [oci.IndexOrManifest.Validate]), but some registries
// (notably Docker Hub) still serve very old images this way, so ociclient
// handles reading them.
//
// A schema1 manifest fetched from a registry always arrives wrapped in a
// legacy libtrust JWS-like signature envelope (mediaTypeSchema1SignedManifest),
// even when the content was never meaningfully "signed" (the signature
// algorithm is commonly "none"). The digest that registries and tags
// actually refer to is the digest of the envelope's content with the
// envelope stripped back off, not the digest of the bytes as delivered on
// the wire. ociclient strips this envelope itself (see schema1Unsign and
// its use in [Client.read]) so that everywhere else in this module - and
// every caller of it - only ever sees a plain, unsigned schema1 manifest
// whose bytes hash to the digest reported for it, exactly like every other
// media type.
//
// See https://github.com/tianon/oci-schema1 and
// https://github.com/moby/moby/commit/011bfd666eeb21a111ca450c42a3893ad03c9324
// for more background.
const (
	mediaTypeSchema1Manifest       = "application/vnd.docker.distribution.manifest.v1+json"
	mediaTypeSchema1SignedManifest = "application/vnd.docker.distribution.manifest.v1+prettyjws"
)

// schema1ProtectedHeader is the JSON shape of the base64url-encoded
// "protected" field of each entry in a signed schema1 manifest's
// "signatures" array. FormatTail is base64-encoded (standard, padded
// encoding, as produced by Go's encoding/json for a []byte field) and,
// appended to the first FormatLength bytes of the signed manifest,
// reconstructs the unsigned manifest that the digest actually refers to.
type schema1ProtectedHeader struct {
	FormatLength int    `json:"formatLength"`
	FormatTail   []byte `json:"formatTail"`
}

// schema1Unsign reconstructs the unsigned form of a signed Docker schema1
// manifest (mediaTypeSchema1SignedManifest) from its raw signed bytes. The
// sha256 of the returned bytes is the digest that registries and tags refer
// to for this manifest.
func schema1Unsign(signed []byte) ([]byte, error) {
	var m struct {
		Signatures []struct {
			Protected string `json:"protected"`
		} `json:"signatures"`
	}
	if err := json.Unmarshal(signed, &m); err != nil {
		return nil, fmt.Errorf("invalid schema1 manifest: %w", err)
	}
	if len(m.Signatures) == 0 {
		return nil, fmt.Errorf("signed schema1 manifest has no signatures")
	}
	protected := m.Signatures[0].Protected
	for _, s := range m.Signatures[1:] {
		if s.Protected != protected {
			return nil, fmt.Errorf("signed schema1 manifest has mismatched signatures")
		}
	}
	hdrJSON, err := base64.RawURLEncoding.DecodeString(protected)
	if err != nil {
		return nil, fmt.Errorf("invalid schema1 signature protected header encoding: %w", err)
	}
	var hdr schema1ProtectedHeader
	if err := json.Unmarshal(hdrJSON, &hdr); err != nil {
		return nil, fmt.Errorf("invalid schema1 signature protected header: %w", err)
	}
	if hdr.FormatLength < 0 || hdr.FormatLength > len(signed) {
		return nil, fmt.Errorf("schema1 signature formatLength %d out of range for %d-byte manifest", hdr.FormatLength, len(signed))
	}
	unsigned := make([]byte, 0, hdr.FormatLength+len(hdr.FormatTail))
	unsigned = append(unsigned, signed[:hdr.FormatLength]...)
	unsigned = append(unsigned, hdr.FormatTail...)
	return unsigned, nil
}
