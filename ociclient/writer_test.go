package ociclient

import (
	"context"
	"net/http"
	"testing"

	"github.com/docker/oci"
	"github.com/docker/oci/ociauth"
	"github.com/stretchr/testify/require"
)

func TestCopyImage(t *testing.T) {
	c, err := New("registry.example", &Options{
		Transport: transportFunc(func(req *http.Request) (*http.Response, error) {
			require.Equal(t, http.MethodPost, req.Method)
			require.Equal(t, "https", req.URL.Scheme)
			require.Equal(t, "registry.example", req.URL.Host)
			require.Equal(t, "/v2/dst/repo/_docker/copy/image", req.URL.Path)
			require.Equal(t, "src/repo", req.URL.Query().Get("from"))
			require.Equal(t, "stable", req.URL.Query().Get("reference"))
			require.Nil(t, req.Body)
			require.Empty(t, req.Header.Get("Content-Type"))

			scope := ociauth.RequestInfoFromContext(req.Context()).RequiredScope
			require.Equal(t, "repository:dst/repo:pull,push repository:src/repo:pull", scope.Canonical().String())

			return &http.Response{
				StatusCode: http.StatusCreated,
				Status:     "201 Created",
				Header: http.Header{
					"Content-Type":          {oci.MediaTypeImageManifest},
					"Docker-Content-Digest": {testDigest.String()},
				},
				Body:    http.NoBody,
				Request: req,
			}, nil
		}),
	})
	require.NoError(t, err)

	desc, err := c.CopyImage(context.Background(), "src/repo", "dst/repo", "stable")
	require.NoError(t, err)
	require.Equal(t, testDigest, desc.Digest)
	require.Equal(t, oci.MediaTypeImageManifest, desc.MediaType)
	require.Zero(t, desc.Size)
}
