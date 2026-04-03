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

package ociserver

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/docker/oci"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestCustomErrorWriter(t *testing.T) {
	// Test that if an Interface method returns an HTTPError error, the
	// HTTP status code is derived from the OCI error code in preference
	// to the HTTPError status code.
	r := New(&oci.Funcs{}, &Options{
		WriteError: func(w http.ResponseWriter, _ *http.Request, err error) {
			w.Header().Set("Some-Header", "a value")
			oci.WriteError(w, err)
		},
	})
	s := httptest.NewServer(r)
	defer s.Close()
	resp, err := http.Get(s.URL + "/v2/foo/manifests/sometag")
	require.NoError(t, err)
	defer resp.Body.Close()
	require.Equal(t, "a value", resp.Header.Get("Some-Header"))
}

func TestHTTPStatusOverriddenByErrorCode(t *testing.T) {
	// Test that if an Interface method returns an HTTPError error, the
	// HTTP status code is derived from the OCI error code in preference
	// to the HTTPError status code.
	r := New(&oci.Funcs{
		GetTag_: func(ctx context.Context, repo string, tagName string) (oci.BlobReader, error) {
			return nil, oci.NewHTTPError(oci.ErrNameUnknown, http.StatusUnauthorized, nil, nil)
		},
	}, nil)
	s := httptest.NewServer(r)
	defer s.Close()
	resp, err := http.Get(s.URL + "/v2/foo/manifests/sometag")
	require.NoError(t, err)
	defer resp.Body.Close()
	body, _ := io.ReadAll(resp.Body)
	require.Equal(t, http.StatusNotFound, resp.StatusCode)
	expected := &oci.WireErrors{
		Errors: []oci.WireError{{
			Code_:   oci.ErrNameUnknown.Code(),
			Message: "401 Unauthorized: name unknown: repository name not known to registry",
		}},
	}
	expectedJSON, err := json.Marshal(expected)
	require.NoError(t, err)
	assert.JSONEq(t, string(expectedJSON), string(body))
}

func TestHTTPStatusUsedForUnknownErrorCode(t *testing.T) {
	// Test that if an Interface method returns an HTTPError error, that
	// HTTP status code is used when the code isn't known to be
	// associated with a particular HTTP status.
	r := New(&oci.Funcs{
		GetTag_: func(ctx context.Context, repo string, tagName string) (oci.BlobReader, error) {
			return nil, oci.NewHTTPError(oci.NewError("foo", "SOMECODE", nil), http.StatusTeapot, nil, nil)
		},
	}, nil)
	s := httptest.NewServer(r)
	defer s.Close()
	resp, err := http.Get(s.URL + "/v2/foo/manifests/sometag")
	require.NoError(t, err)
	defer resp.Body.Close()
	body, _ := io.ReadAll(resp.Body)
	require.Equal(t, http.StatusTeapot, resp.StatusCode)
	expected := &oci.WireErrors{
		Errors: []oci.WireError{{
			Code_:   "SOMECODE",
			Message: "foo",
		}},
	}
	expectedJSON, err := json.Marshal(expected)
	require.NoError(t, err)
	assert.JSONEq(t, string(expectedJSON), string(body))
}
