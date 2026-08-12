package ociserver

import (
	"bytes"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/docker/oci"
	"github.com/stretchr/testify/require"
)

func TestAcceptsMediaType(t *testing.T) {
	t.Parallel()

	const manifestMediaType = "application/vnd.oci.image.manifest.v1+json"

	tests := []struct {
		name         string
		acceptHeader []string
		want         bool
	}{
		{
			name: "empty accept allows stored type",
			want: true,
		},
		{
			name:         "exact match",
			acceptHeader: []string{manifestMediaType},
			want:         true,
		},
		{
			name:         "exact match with parameters",
			acceptHeader: []string{manifestMediaType + "; q=0.8"},
			want:         true,
		},
		{
			name:         "multiple values with match",
			acceptHeader: []string{"application/vnd.oci.image.index.v1+json, " + manifestMediaType},
			want:         true,
		},
		{
			name: "multiple header lines with match",
			acceptHeader: []string{
				"application/vnd.oci.image.index.v1+json",
				manifestMediaType,
			},
			want: true,
		},
		{
			name:         "any type wildcard",
			acceptHeader: []string{"*/*"},
			want:         true,
		},
		{
			name:         "subtype wildcard",
			acceptHeader: []string{"application/*"},
			want:         true,
		},
		{
			name:         "quality zero rejects otherwise matching type",
			acceptHeader: []string{manifestMediaType + "; q=0"},
			want:         false,
		},
		{
			name:         "different type",
			acceptHeader: []string{"application/vnd.oci.image.index.v1+json"},
			want:         false,
		},
		{
			name:         "different top level wildcard",
			acceptHeader: []string{"text/*"},
			want:         false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			if got := acceptsMediaType(tt.acceptHeader, manifestMediaType); got != tt.want {
				t.Fatalf("acceptsMediaType(%q, %q) = %v, want %v", tt.acceptHeader, manifestMediaType, got, tt.want)
			}
		})
	}
}

func TestManifestHandlersValidateTags(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name    string
		method  string
		target  string
		handler func(*Server) http.HandlerFunc
		body    []byte
	}{
		{name: "get reference", method: http.MethodGet, target: "/v2/repo/manifests/-bad", handler: (*Server).manifestHeadGet},
		{name: "delete reference", method: http.MethodDelete, target: "/v2/repo/manifests/-bad", handler: (*Server).manifestDelete},
		{
			name:    "put tag query",
			method:  http.MethodPut,
			target:  "/v2/repo/manifests/latest?tag=-bad",
			handler: (*Server).manifestPut,
			body:    []byte(`{}`),
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			s := &Server{db: (*oci.Funcs)(nil)}
			req := httptest.NewRequest(tt.method, tt.target, bytes.NewReader(tt.body))
			rec := httptest.NewRecorder()

			serveTestRoute(t, `/v2/*name/manifests/:reference`, tt.handler(s), rec, req)

			require.Equal(t, http.StatusBadRequest, rec.Code)
			require.Contains(t, rec.Body.String(), `"code":"MANIFEST_INVALID"`)
		})
	}
}

func TestManifestPutValidatesContentType(t *testing.T) {
	t.Parallel()

	s := &Server{db: (*oci.Funcs)(nil)}
	req := httptest.NewRequest(http.MethodPut, "/v2/repo/manifests/latest", bytes.NewReader([]byte(`{}`)))
	req.Header.Set("Content-Type", `application/vnd.oci.image.manifest.v1+json; broken`)
	rec := httptest.NewRecorder()

	serveTestRoute(t, `/v2/*name/manifests/:reference`, s.manifestPut(), rec, req)

	require.Equal(t, http.StatusBadRequest, rec.Code)
	require.Contains(t, rec.Body.String(), `"code":"MANIFEST_INVALID"`)
}
