package ociclient

import (
	"context"
	"io"
	"net/http"
	"strings"
	"testing"

	"github.com/docker/oci"
	"github.com/docker/oci/ociauth"
	"github.com/stretchr/testify/require"
)

func TestClientDo(t *testing.T) {
	wantScope := ociauth.NewScope(ociauth.ResourceScope{
		ResourceType: ociauth.TypeRepository,
		Resource:     "foo/bar",
		Action:       ociauth.ActionPull,
	})

	c, err := New("registry.example", &Options{
		Transport: transportFunc(func(req *http.Request) (*http.Response, error) {
			require.Equal(t, "https", req.URL.Scheme)
			require.Equal(t, "registry.example", req.URL.Host)
			require.Equal(t, "/v2/foo/bar/custom", req.URL.Path)
			require.Equal(t, "docker/oci", req.Header.Get("User-Agent"))
			require.True(t, wantScope.Equal(ociauth.RequestInfoFromContext(req.Context()).RequiredScope))
			return &http.Response{
				StatusCode: http.StatusTeapot,
				Status:     "418 I'm a teapot",
				Header:     http.Header{"Content-Type": {"application/json"}},
				Body: io.NopCloser(strings.NewReader(`{
					"errors": [{"code": "DENIED", "message": "custom denied"}]
				}`)),
				Request: req,
			}, nil
		}),
	})
	require.NoError(t, err)

	req, err := http.NewRequestWithContext(context.Background(), http.MethodGet, "/v2/foo/bar/custom", nil)
	require.NoError(t, err)

	resp, err := c.Do(req, &RequestOptions{Scope: wantScope})
	require.NoError(t, err)
	require.Equal(t, http.StatusTeapot, resp.StatusCode)

	err = CheckResponse(resp)
	require.Error(t, err)
	var httpErr oci.HTTPError
	require.ErrorAs(t, err, &httpErr)
	require.Equal(t, http.StatusTeapot, httpErr.StatusCode())
}
