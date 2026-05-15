package ociclient

import (
	"context"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"

	"github.com/docker/oci"
	"github.com/docker/oci/ociauth"
	"github.com/docker/oci/ocidigest"
	"github.com/docker/oci/ocimem"
	"github.com/docker/oci/ociserver"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

var testDigest = ocidigest.FromBytes([]byte("test"))

func TestAuthScopes(t *testing.T) {

	// Test that we're passing the expected authorization scopes to the various parts of the API.
	// All the call semantics themselves are tested elsewhere, but we want to be
	// sure that we're passing the right required auth scopes to the authorizer.

	srv := httptest.NewServer(ociserver.New(ocimem.New(), nil))
	defer srv.Close()
	srvURL, _ := url.Parse(srv.URL)

	assertScope := func(scope string, f func(ctx context.Context, r oci.Interface)) {
		assertAuthScope(t, srvURL.Host, scope, f)
	}

	assertScope("repository:foo/bar:pull", func(ctx context.Context, r oci.Interface) {
		r.GetBlob(ctx, "foo/bar", testDigest)
	})
	assertScope("repository:foo/bar:pull", func(ctx context.Context, r oci.Interface) {
		r.GetBlobRange(ctx, "foo/bar", testDigest, 100, 200)
	})
	assertScope("repository:foo/bar:pull", func(ctx context.Context, r oci.Interface) {
		r.GetManifest(ctx, "foo/bar", testDigest)
	})
	assertScope("repository:foo/bar:pull", func(ctx context.Context, r oci.Interface) {
		r.GetTag(ctx, "foo/bar", "sometag")
	})
	assertScope("repository:foo/bar:pull", func(ctx context.Context, r oci.Interface) {
		r.ResolveBlob(ctx, "foo/bar", testDigest)
	})
	assertScope("repository:foo/bar:pull", func(ctx context.Context, r oci.Interface) {
		r.ResolveManifest(ctx, "foo/bar", testDigest)
	})
	assertScope("repository:foo/bar:pull", func(ctx context.Context, r oci.Interface) {
		r.ResolveTag(ctx, "foo/bar", "sometag")
	})
	assertScope("repository:foo/bar:push", func(ctx context.Context, r oci.Interface) {
		r.PushBlob(ctx, "foo/bar", oci.Descriptor{
			MediaType: "application/json",
			Digest:    testDigest,
			Size:      3,
		}, strings.NewReader("foo"))
	})
	assertScope("repository:foo/bar:push", func(ctx context.Context, r oci.Interface) {
		w, err := r.PushBlobChunked(ctx, "foo/bar", 0)
		require.NoError(t, err)
		w.Write([]byte("foo"))
		w.Close()

		id := w.ID()
		w, err = r.PushBlobChunkedResume(ctx, "foo/bar", id, 3, 0)
		require.NoError(t, err)
		w.Write([]byte("bar"))
		_, err = w.Commit(ocidigest.FromBytes([]byte("foobar")))
		require.NoError(t, err)
	})
	assertScope("repository:x/y:pull repository:z/w:push", func(ctx context.Context, r oci.Interface) {
		r.MountBlob(ctx, "x/y", "z/w", testDigest)
	})
	assertScope("repository:foo/bar:push", func(ctx context.Context, r oci.Interface) {
		r.PushManifest(ctx, "foo/bar", []byte("something"), "application/json", &oci.PushManifestParameters{
			Tags: []string{"sometag"},
		})
	})
	assertScope("repository:foo/bar:push", func(ctx context.Context, r oci.Interface) {
		r.DeleteBlob(ctx, "foo/bar", testDigest)
	})
	assertScope("repository:foo/bar:push", func(ctx context.Context, r oci.Interface) {
		r.DeleteManifest(ctx, "foo/bar", testDigest)
	})
	assertScope("repository:foo/bar:push", func(ctx context.Context, r oci.Interface) {
		r.DeleteTag(ctx, "foo/bar", "sometag")
	})
	assertScope("registry:catalog:*", func(ctx context.Context, r oci.Interface) {
		oci.All(r.Repositories(ctx, ""))
	})
	assertScope("repository:foo/bar:pull", func(ctx context.Context, r oci.Interface) {
		oci.All(r.Tags(ctx, "foo/bar", nil))
	})
	assertScope("repository:foo/bar:pull", func(ctx context.Context, r oci.Interface) {
		oci.All(r.Referrers(ctx, "foo/bar", testDigest, nil))
	})
}

// assertAuthScope asserts that the given function makes a client request with the
// given scope to the given URL.
func assertAuthScope(t *testing.T, host string, scope string, f func(ctx context.Context, r oci.Interface)) {
	requestedScopes := make(map[string]bool)

	// Check that the context is passed through with values intact.
	type foo struct{}
	ctx := context.WithValue(context.Background(), foo{}, true)

	client, err := New(host, &Options{
		Insecure: true,
		Transport: transportFunc(func(req *http.Request) (*http.Response, error) {
			ctx := req.Context()
			assert.Equal(t, true, ctx.Value(foo{}))
			scope := ociauth.RequestInfoFromContext(ctx).RequiredScope
			requestedScopes[scope.Canonical().String()] = true
			return http.DefaultTransport.RoundTrip(req)
		}),
	})
	require.NoError(t, err)
	f(ctx, client)
	require.Len(t, requestedScopes, 1)
	t.Logf("requested scopes: %v", requestedScopes)
	require.Equal(t, scope, mapsKeys(requestedScopes)[0])
}

type transportFunc func(req *http.Request) (*http.Response, error)

func (f transportFunc) RoundTrip(req *http.Request) (*http.Response, error) {
	return f(req)
}

// TODO: replace with maps.Keys once Go adds it
func mapsKeys[M ~map[K]V, K comparable, V any](m M) []K {
	r := make([]K, 0, len(m))
	for k := range m {
		r = append(r, k)
	}
	return r
}
