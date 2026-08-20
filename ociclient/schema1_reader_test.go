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
	"bytes"
	"context"
	"encoding/json"
	"io"
	"net/http"
	"os"
	"testing"

	"github.com/docker/oci/ocidigest"
	"github.com/stretchr/testify/require"
)

// schema1TestDigest is the real Docker-Content-Digest Docker Hub returns
// for library/php:5.3, which is the digest of the *unsigned* form of the
// manifest, not of the signed bytes on the wire.
const schema1TestDigest = ocidigest.Digest("sha256:ba952a8970f2fc35e3703b2650495c64d6e015eb52a4ee03f750c69e863b3237")

func readSchema1Fixture(t *testing.T) []byte {
	t.Helper()
	data, err := os.ReadFile("testdata/schema1-signed-manifest.json")
	require.NoError(t, err)
	return data
}

func TestGetManifestSchema1Signed(t *testing.T) {
	signed := readSchema1Fixture(t)

	c, err := New("registry.example", &Options{
		Transport: transportFunc(func(req *http.Request) (*http.Response, error) {
			require.Equal(t, http.MethodGet, req.Method)
			require.Equal(t, "/v2/library/php/manifests/"+schema1TestDigest.String(), req.URL.Path)
			return &http.Response{
				StatusCode:    http.StatusOK,
				Status:        "200 OK",
				ContentLength: int64(len(signed)),
				Header: http.Header{
					"Content-Type":          {mediaTypeSchema1SignedManifest},
					"Docker-Content-Digest": {schema1TestDigest.String()},
				},
				Body:    io.NopCloser(bytes.NewReader(signed)),
				Request: req,
			}, nil
		}),
	})
	require.NoError(t, err)

	r, err := c.GetManifest(context.Background(), "library/php", schema1TestDigest)
	require.NoError(t, err)
	defer r.Close()

	desc := r.Descriptor()
	require.Equal(t, mediaTypeSchema1Manifest, desc.MediaType)
	require.Equal(t, schema1TestDigest, desc.Digest)

	data, err := io.ReadAll(r)
	require.NoError(t, err)

	// The bytes handed back must actually hash to the reported digest -
	// the entire point of unsigning at read time.
	require.Equal(t, schema1TestDigest, ocidigest.FromBytes(data))
	require.Equal(t, int64(len(data)), desc.Size)
	require.Less(t, len(data), len(signed), "unsigned content should be smaller than the signed wire form")

	var m map[string]any
	require.NoError(t, json.Unmarshal(data, &m))
	require.NotContains(t, m, "signatures")
}

func TestGetTagSchema1Signed(t *testing.T) {
	signed := readSchema1Fixture(t)

	c, err := New("registry.example", &Options{
		Transport: transportFunc(func(req *http.Request) (*http.Response, error) {
			require.Equal(t, "/v2/library/php/manifests/5.3", req.URL.Path)
			return &http.Response{
				StatusCode:    http.StatusOK,
				Status:        "200 OK",
				ContentLength: int64(len(signed)),
				Header: http.Header{
					"Content-Type":          {mediaTypeSchema1SignedManifest},
					"Docker-Content-Digest": {schema1TestDigest.String()},
				},
				Body:    io.NopCloser(bytes.NewReader(signed)),
				Request: req,
			}, nil
		}),
	})
	require.NoError(t, err)

	r, err := c.GetTag(context.Background(), "library/php", "5.3")
	require.NoError(t, err)
	defer r.Close()

	data, err := io.ReadAll(r)
	require.NoError(t, err)
	require.Equal(t, schema1TestDigest, ocidigest.FromBytes(data))
	require.Equal(t, mediaTypeSchema1Manifest, r.Descriptor().MediaType)
}

// TestGetTagSchema1SignedNoDigestHeader exercises registries (like AWS ECR,
// per the comment in [Client.read]) that omit Docker-Content-Digest on a
// tag GET: the digest must be derived from the unsigned content, not the
// signed wire bytes.
func TestGetTagSchema1SignedNoDigestHeader(t *testing.T) {
	signed := readSchema1Fixture(t)

	c, err := New("registry.example", &Options{
		Transport: transportFunc(func(req *http.Request) (*http.Response, error) {
			return &http.Response{
				StatusCode:    http.StatusOK,
				Status:        "200 OK",
				ContentLength: int64(len(signed)),
				Header: http.Header{
					"Content-Type": {mediaTypeSchema1SignedManifest},
				},
				Body:    io.NopCloser(bytes.NewReader(signed)),
				Request: req,
			}, nil
		}),
	})
	require.NoError(t, err)

	r, err := c.GetTag(context.Background(), "library/php", "5.3")
	require.NoError(t, err)
	defer r.Close()

	require.Equal(t, schema1TestDigest, r.Descriptor().Digest)
	data, err := io.ReadAll(r)
	require.NoError(t, err)
	require.Equal(t, schema1TestDigest, ocidigest.FromBytes(data))
}

func TestGetManifestSchema1SignedDigestMismatch(t *testing.T) {
	signed := readSchema1Fixture(t)
	const wrongDigest = ocidigest.Digest("sha256:0000000000000000000000000000000000000000000000000000000000000000")

	c, err := New("registry.example", &Options{
		Transport: transportFunc(func(req *http.Request) (*http.Response, error) {
			return &http.Response{
				StatusCode:    http.StatusOK,
				Status:        "200 OK",
				ContentLength: int64(len(signed)),
				Header: http.Header{
					"Content-Type":          {mediaTypeSchema1SignedManifest},
					"Docker-Content-Digest": {wrongDigest.String()},
				},
				Body:    io.NopCloser(bytes.NewReader(signed)),
				Request: req,
			}, nil
		}),
	})
	require.NoError(t, err)

	r, err := c.GetManifest(context.Background(), "library/php", wrongDigest)
	require.NoError(t, err)
	_, err = io.ReadAll(r)
	require.ErrorContains(t, err, "digest mismatch")
}

func TestResolveManifestSchema1SignedNormalizesMediaType(t *testing.T) {
	c, err := New("registry.example", &Options{
		Transport: transportFunc(func(req *http.Request) (*http.Response, error) {
			require.Equal(t, http.MethodHead, req.Method)
			return &http.Response{
				StatusCode:    http.StatusOK,
				Status:        "200 OK",
				ContentLength: 20841,
				Header: http.Header{
					"Content-Type":          {mediaTypeSchema1SignedManifest},
					"Docker-Content-Digest": {schema1TestDigest.String()},
				},
				Body:    http.NoBody,
				Request: req,
			}, nil
		}),
	})
	require.NoError(t, err)

	desc, err := c.ResolveManifest(context.Background(), "library/php", schema1TestDigest)
	require.NoError(t, err)
	// HEAD has no body to unsign, but the reported media type is
	// normalized to match what a subsequent GetManifest will return,
	// so Resolve and Get stay consistent for callers.
	require.Equal(t, mediaTypeSchema1Manifest, desc.MediaType)
	require.Equal(t, schema1TestDigest, desc.Digest)
}
