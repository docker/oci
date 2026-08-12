package ociserver

import (
	"bytes"
	"context"
	"errors"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/docker/oci"
	"github.com/docker/oci/ocidigest"
	"github.com/stretchr/testify/require"
)

func TestServerDefaultLoggingIsSilent(t *testing.T) {
	var globalLog bytes.Buffer
	oldDefault := slog.Default()
	slog.SetDefault(slog.New(slog.NewTextHandler(&globalLog, nil)))
	t.Cleanup(func() {
		slog.SetDefault(oldDefault)
	})

	srv, err := New(&oci.Funcs{
		ResolveBlob_: func(context.Context, string, oci.Digest) (oci.Descriptor, error) {
			return oci.Descriptor{}, errors.New("backend failed")
		},
	}, nil)
	require.NoError(t, err)

	dgst := ocidigest.FromBytes([]byte("blob"))
	req := httptest.NewRequest(http.MethodHead, "/v2/example/blobs/"+dgst.String(), nil)
	rec := httptest.NewRecorder()
	srv.ServeHTTP(rec, req)

	require.Equal(t, http.StatusInternalServerError, rec.Code)
	require.Empty(t, globalLog.String())
}

func TestServerLogsUnexpectedFailures(t *testing.T) {
	var logs bytes.Buffer
	logger := slog.New(slog.NewTextHandler(&logs, nil))
	srv, err := New(&oci.Funcs{
		ResolveBlob_: func(context.Context, string, oci.Digest) (oci.Descriptor, error) {
			return oci.Descriptor{}, errors.New("backend failed")
		},
	}, &ServerConfig{Logger: logger})
	require.NoError(t, err)

	dgst := ocidigest.FromBytes([]byte("blob"))
	req := httptest.NewRequest(http.MethodHead, "/v2/example/blobs/"+dgst.String(), nil)
	rec := httptest.NewRecorder()
	srv.ServeHTTP(rec, req)

	require.Equal(t, http.StatusInternalServerError, rec.Code)
	require.Contains(t, logs.String(), "msg=\"resolving blob\"")
	require.Contains(t, logs.String(), "repository=example")
	require.Contains(t, logs.String(), "error=\"backend failed\"")
}

func TestServerMiddlewares(t *testing.T) {
	var calls []string
	middleware := func(name string) func(http.Handler) http.Handler {
		return func(next http.Handler) http.Handler {
			return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				calls = append(calls, name+" before")
				next.ServeHTTP(w, r)
				calls = append(calls, name+" after")
			})
		}
	}
	cfg := &ServerConfig{
		Middlewares: []func(http.Handler) http.Handler{
			middleware("first"),
			middleware("second"),
		},
	}
	srv, err := New((*oci.Funcs)(nil), cfg)
	require.NoError(t, err)

	// New must retain its own middleware slice rather than the caller's backing
	// array.
	cfg.Middlewares[0] = func(http.Handler) http.Handler {
		panic("server used the caller's mutated middleware slice")
	}

	req := httptest.NewRequest(http.MethodGet, "/v2/", nil)
	rec := httptest.NewRecorder()
	srv.ServeHTTP(rec, req)

	require.Equal(t, http.StatusOK, rec.Code)
	require.Equal(t, []string{
		"first before",
		"second before",
		"second after",
		"first after",
	}, calls)
	require.True(t, strings.HasPrefix(rec.Header().Get("Content-Type"), "application/json"))
}

func TestServerAuthMiddlewareOnlyAppliesToOCIRoutes(t *testing.T) {
	var calls []string
	middleware := func(name string) func(http.Handler) http.Handler {
		return func(next http.Handler) http.Handler {
			return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				calls = append(calls, name)
				next.ServeHTTP(w, r)
			})
		}
	}
	srv, err := New((*oci.Funcs)(nil), &ServerConfig{
		Middlewares:    []func(http.Handler) http.Handler{middleware("root")},
		AuthMiddleware: middleware("auth"),
	})
	require.NoError(t, err)
	srv.Mux().Get(`/health`, func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusNoContent)
	})

	rec := httptest.NewRecorder()
	srv.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/v2/", nil))
	require.Equal(t, http.StatusOK, rec.Code)
	require.Equal(t, []string{"root", "auth"}, calls)

	calls = nil
	rec = httptest.NewRecorder()
	srv.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/health", nil))
	require.Equal(t, http.StatusNoContent, rec.Code)
	require.Equal(t, []string{"root"}, calls)
}

func TestServerInvalidRepositoryNameReturnsOCIError(t *testing.T) {
	srv, err := New((*oci.Funcs)(nil), nil)
	require.NoError(t, err)

	rec := httptest.NewRecorder()
	srv.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/v2/INVALID/manifests/latest", nil))

	require.Equal(t, http.StatusBadRequest, rec.Code)
	require.JSONEq(t, `{
		"errors": [{
			"code": "NAME_INVALID",
			"message": "repository name is invalid: INVALID"
		}]
	}`, rec.Body.String())
}

func TestServerRejectsOverlongRepositoryName(t *testing.T) {
	srv, err := New((*oci.Funcs)(nil), nil)
	require.NoError(t, err)

	name := strings.Repeat("a", 256)
	rec := httptest.NewRecorder()
	srv.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/v2/"+name+"/tags/list", nil))

	require.Equal(t, http.StatusBadRequest, rec.Code)
	require.Contains(t, rec.Body.String(), `"code":"NAME_INVALID"`)
}
