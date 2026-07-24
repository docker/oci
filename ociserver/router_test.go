package ociserver

import (
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/docker/oci/ociserver/mux"
)

func serveTestRoute(t testing.TB, pattern string, handler http.Handler, rec *httptest.ResponseRecorder, req *http.Request) {
	t.Helper()
	r := mux.New()
	r.Handle(pattern, handler)
	r.ServeHTTP(rec, req)
}
