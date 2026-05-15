package ociclient

import (
	"context"
	"fmt"
	"net/http"
	"testing"

	"github.com/docker/oci/ocidigest"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestBadRepoName(t *testing.T) {
	ctx := context.Background()
	r, err := New("never.used", &Options{
		Insecure:  true,
		Transport: noTransport{},
	})
	require.NoError(t, err)
	_, err = r.GetBlob(ctx, "Invalid--Repo", ocidigest.FromBytes(nil))
	assert.Regexp(t, "invalid OCI request: name invalid: invalid repository name", err.Error())
	_, err = r.ResolveTag(ctx, "okrepo", "bad-Tag!")
	assert.Regexp(t, "invalid OCI request: 404 Not Found: page not found", err.Error())
}

type noTransport struct{}

func (noTransport) RoundTrip(req *http.Request) (*http.Response, error) {
	return nil, fmt.Errorf("no can do")
}
