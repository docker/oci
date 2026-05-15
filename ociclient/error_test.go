package ociclient

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"

	"github.com/docker/oci"
	"github.com/docker/oci/ocidigest"
	"github.com/docker/oci/ociserver"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestErrorStuttering(t *testing.T) {
	// This checks that the stuttering observed in issue #31
	// isn't an issue when ociserver wraps ociclient.
	srv := httptest.NewServer(ociserver.New(&oci.Funcs{
		NewError: func(ctx context.Context, methodName, repo string) error {
			return oci.ErrManifestUnknown
		},
	}, nil))
	defer srv.Close()

	srvURL, _ := url.Parse(srv.URL)
	r, err := New(srvURL.Host, &Options{
		Insecure: true,
	})
	require.NoError(t, err)
	_, err = r.GetTag(context.Background(), "foo", "sometag")
	assert.ErrorIs(t, err, oci.ErrManifestUnknown)
	assert.Regexp(t, `404 Not Found: manifest unknown: manifest unknown to registry`, err.Error())

	// ResolveTag uses HEAD rather than GET, so here we're testing
	// the path where a response with no body gets turned back into
	// something vaguely resembling the original error, which is why
	// the code and message have changed.
	_, err = r.ResolveTag(context.Background(), "foo", "sometag")
	assert.ErrorIs(t, err, oci.ErrNameUnknown)
	assert.Regexp(t, `404 Not Found: name unknown: repository name not known to registry`, err.Error())
}

func TestNonJSONErrorResponse(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, req *http.Request) {
		w.WriteHeader(http.StatusTeapot)
		w.Write([]byte("some body"))
	}))
	defer srv.Close()

	srvURL, _ := url.Parse(srv.URL)
	r, err := New(srvURL.Host, &Options{
		Insecure: true,
	})
	require.NoError(t, err)
	assertStatusCode := func(f func(ctx context.Context, r oci.Interface) error) {
		err := f(context.Background(), r)
		var herr oci.HTTPError
		ok := errors.As(err, &herr)
		require.True(t, ok)
		require.Equal(t, http.StatusTeapot, herr.StatusCode())
	}
	assertStatusCode(func(ctx context.Context, r oci.Interface) error {
		rd, err := r.GetBlob(ctx, "foo/read", testDigest)
		if rd != nil {
			rd.Close()
		}
		return err
	})
	assertStatusCode(func(ctx context.Context, r oci.Interface) error {
		rd, err := r.GetBlobRange(ctx, "foo/read", testDigest, 100, 200)
		if rd != nil {
			rd.Close()
		}
		return err
	})
	assertStatusCode(func(ctx context.Context, r oci.Interface) error {
		rd, err := r.GetManifest(ctx, "foo/read", testDigest)
		if rd != nil {
			rd.Close()
		}
		return err
	})
	assertStatusCode(func(ctx context.Context, r oci.Interface) error {
		rd, err := r.GetTag(ctx, "foo/read", "sometag")
		if rd != nil {
			rd.Close()
		}
		return err
	})
	assertStatusCode(func(ctx context.Context, r oci.Interface) error {
		_, err := r.ResolveBlob(ctx, "foo/read", testDigest)
		return err
	})
	assertStatusCode(func(ctx context.Context, r oci.Interface) error {
		_, err := r.ResolveManifest(ctx, "foo/read", testDigest)
		return err
	})
	assertStatusCode(func(ctx context.Context, r oci.Interface) error {
		_, err := r.ResolveTag(ctx, "foo/read", "sometag")
		return err
	})
	assertStatusCode(func(ctx context.Context, r oci.Interface) error {
		_, err := r.PushBlob(ctx, "foo/write", oci.Descriptor{
			MediaType: "application/json",
			Digest:    testDigest,
			Size:      3,
		}, strings.NewReader("foo"))
		return err
	})
	assertStatusCode(func(ctx context.Context, r oci.Interface) error {
		w, err := r.PushBlobChunked(ctx, "foo/write", 0)
		if err != nil {
			return err
		}
		w.Close()
		return nil
	})
	assertStatusCode(func(ctx context.Context, r oci.Interface) error {
		w, err := r.PushBlobChunkedResume(ctx, "foo/write", "/someid", 3, 0)
		if err != nil {
			return err
		}
		data := []byte("some data")
		if _, err := w.Write(data); err != nil {
			return err
		}
		_, err = w.Commit(ocidigest.FromBytes(data))
		return err
	})
	assertStatusCode(func(ctx context.Context, r oci.Interface) error {
		_, err := r.MountBlob(ctx, "foo/read", "foo/write", testDigest)
		return err
	})
	assertStatusCode(func(ctx context.Context, r oci.Interface) error {
		_, err := r.PushManifest(ctx, "foo/write", []byte("something"), "application/json", &oci.PushManifestParameters{
			Tags: []string{"sometag"},
		})
		return err
	})
	assertStatusCode(func(ctx context.Context, r oci.Interface) error {
		return r.DeleteBlob(ctx, "foo/write", testDigest)
	})
	assertStatusCode(func(ctx context.Context, r oci.Interface) error {
		return r.DeleteManifest(ctx, "foo/write", testDigest)
	})
	assertStatusCode(func(ctx context.Context, r oci.Interface) error {
		return r.DeleteTag(ctx, "foo/write", "sometag")
	})
	assertStatusCode(func(ctx context.Context, r oci.Interface) error {
		_, err := oci.All(r.Repositories(ctx, ""))
		return err
	})
	assertStatusCode(func(ctx context.Context, r oci.Interface) error {
		_, err := oci.All(r.Tags(ctx, "foo/read", nil))
		return err
	})
	assertStatusCode(func(ctx context.Context, r oci.Interface) error {
		_, err := oci.All(r.Referrers(ctx, "foo/read", testDigest, nil))
		return err
	})
}
