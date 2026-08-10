package mux

import (
	"context"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

type testCase struct {
	name           string
	path           string
	method         string
	body           io.Reader
	expectedStatus int
	expectedBody   string
}

type testContextKey string

const middlewaresContextKey testContextKey = "middlewares"

func TestMuxBasic(t *testing.T) {
	m := New()

	m.Get(`/`, func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(200)
		w.Write([]byte("ok"))
	})
	m.Get(`/path`, func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(200)
		w.Write([]byte("get path"))
	})
	m.Post(`/path`, func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(200)
		w.Write([]byte("post path"))
	})
	m.Patch(`/path`, func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(200)
		w.Write([]byte("patch path"))
	})
	m.Get(`/*var1/:var2/path`, func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(200)
		_, _ = fmt.Fprintf(w, "%s %s", URLParam(r, "var1"), URLParam(r, "var2"))
	})
	m.HandleFunc(`/allmethods`, func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(200)
		w.Write([]byte("all methods"))
	})

	ts := httptest.NewServer(m)
	defer ts.Close()

	testCases := []testCase{
		{
			name:           "get root",
			path:           "/",
			method:         "GET",
			expectedStatus: 200,
			expectedBody:   "ok",
		},
		{
			name:           "not found",
			path:           "/notfound",
			method:         "GET",
			expectedStatus: 404,
			expectedBody:   "not found",
		}, {
			name:           "get path",
			path:           "/path",
			method:         "GET",
			expectedStatus: 200,
			expectedBody:   "get path",
		}, {
			name:           "post path",
			path:           "/path",
			method:         "POST",
			expectedStatus: 200,
			expectedBody:   "post path",
		}, {
			name:           "patch path",
			path:           "/path",
			method:         "PATCH",
			expectedStatus: 200,
			expectedBody:   "patch path",
		}, {
			name:           "delete path",
			path:           "/path",
			method:         "DELETE",
			expectedStatus: 405,
			expectedBody:   "not allowed",
		}, {
			name:           "get path with var1 and var2",
			path:           "/foo/bar/path",
			method:         "GET",
			expectedStatus: 200,
			expectedBody:   "foo bar",
		}, {
			name:           "all methods",
			path:           "/allmethods",
			method:         "OPTIONS",
			expectedStatus: 200,
			expectedBody:   "all methods",
		},
	}

	runTestCases(t, ts, testCases)
}

func TestGrouping(t *testing.T) {
	m := New()

	m.Use(func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			v, ok := r.Context().Value(middlewaresContextKey).([]string)
			if !ok {
				v = []string{}
			}
			v = append(v, "1")
			r = r.WithContext(context.WithValue(r.Context(), middlewaresContextKey, v))
			next.ServeHTTP(w, r)
		})
	})

	m.Group(func(r Router) {
		r.Use(func(next http.Handler) http.Handler {
			return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				v, ok := r.Context().Value(middlewaresContextKey).([]string)
				if !ok {
					t.Fatalf("failed to get middlewares from context")
				}
				v = append(v, "a")
				r = r.WithContext(context.WithValue(r.Context(), middlewaresContextKey, v))
				next.ServeHTTP(w, r)
			})
		})
		r.Get(`/foo`, returnMWs(t))
	})

	m.Group(func(r Router) {
		r.Use(func(next http.Handler) http.Handler {
			return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				v, ok := r.Context().Value(middlewaresContextKey).([]string)
				if !ok {
					t.Fatalf("failed to get middlewares from context")
				}
				v = append(v, "b")
				r = r.WithContext(context.WithValue(r.Context(), middlewaresContextKey, v))
				next.ServeHTTP(w, r)
			})
		})
		r.Get(`/bar`, returnMWs(t))
	})
	m.Get(`/`, returnMWs(t))
	ts := httptest.NewServer(m)
	defer ts.Close()

	testCases := []testCase{
		{
			name:           "get root",
			path:           "/",
			method:         "GET",
			expectedStatus: 200,
			expectedBody:   "1",
		}, {
			name:           "get foo",
			path:           "/foo",
			method:         "GET",
			expectedStatus: 200,
			expectedBody:   "1 a",
		}, {
			name:           "get bar",
			path:           "/bar",
			method:         "GET",
			expectedStatus: 200,
			expectedBody:   "1 b",
		},
	}

	runTestCases(t, ts, testCases)
}

func TestOCIDistRouting(t *testing.T) {
	m := New()

	m.Route("/v2", func(r Router) {
		r.Head("/*name/manifests/:reference", func(w http.ResponseWriter, r *http.Request) {
			w.WriteHeader(200)
		})
		r.Get("/*name/manifests/:reference", func(w http.ResponseWriter, r *http.Request) {
			w.WriteHeader(200)
			fmt.Fprintf(w, "manifest get: %s %s", URLParam(r, "name"), URLParam(r, "reference"))
		})
		r.Put("/*name/manifests/:reference", func(w http.ResponseWriter, r *http.Request) {
			w.WriteHeader(200)
			fmt.Fprintf(w, "manifest put: %s %s", URLParam(r, "name"), URLParam(r, "reference"))
		})
		r.Delete("/*name/manifests/:reference", func(w http.ResponseWriter, r *http.Request) {
			w.WriteHeader(200)
			fmt.Fprintf(w, "manifest delete: %s %s", URLParam(r, "name"), URLParam(r, "reference"))
		})
	})

	ts := httptest.NewServer(m)
	defer ts.Close()

	testCases := []testCase{
		{
			name:           "head",
			path:           "/v2/foo/bar/baz/manifests/tag",
			method:         "HEAD",
			expectedStatus: 200,
			expectedBody:   "",
		}, {
			name:           "get",
			path:           "/v2/foo/manifests/tag",
			method:         "GET",
			expectedStatus: 200,
			expectedBody:   "manifest get: foo tag",
		}, {
			name:           "put",
			path:           "/v2/foo/bar/manifests/tag",
			method:         "PUT",
			expectedStatus: 200,
			expectedBody:   "manifest put: foo/bar tag",
		}, {
			name:           "delete",
			path:           "/v2/foo/bar/baz/manifests/tag",
			method:         "DELETE",
			expectedStatus: 200,
			expectedBody:   "manifest delete: foo/bar/baz tag",
		},
	}

	runTestCases(t, ts, testCases)
}

// TestMethodDispatchAcrossOverlappingPatterns guards against the 405
// short-circuit: a request whose method is served by a later, overlapping
// pattern must be dispatched rather than rejected by the first pattern that
// only matched the path.
func TestMethodDispatchAcrossOverlappingPatterns(t *testing.T) {
	m := New()
	m.Get(`/:value`, func(w http.ResponseWriter, r *http.Request) {
		w.Write([]byte("get"))
	})
	m.Post(`/x`, func(w http.ResponseWriter, r *http.Request) {
		w.Write([]byte("post"))
	})

	ts := httptest.NewServer(m)
	defer ts.Close()

	testCases := []testCase{
		{
			name:           "get matches first pattern",
			path:           "/x",
			method:         http.MethodGet,
			expectedStatus: http.StatusOK,
			expectedBody:   "get",
		}, {
			name:           "post falls through to overlapping pattern",
			path:           "/x",
			method:         http.MethodPost,
			expectedStatus: http.StatusOK,
			expectedBody:   "post",
		}, {
			name:           "unhandled method still yields 405",
			path:           "/x",
			method:         http.MethodDelete,
			expectedStatus: http.StatusMethodNotAllowed,
			expectedBody:   "not allowed",
		},
	}

	runTestCases(t, ts, testCases)
}

func returnMWs(t *testing.T) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		v, ok := r.Context().Value(middlewaresContextKey).([]string)
		if !ok {
			t.Fatalf("failed to get middlewares from context")
		}
		w.WriteHeader(200)
		w.Write([]byte(strings.Join(v, " ")))
	}
}

func runTestCases(t *testing.T, ts *httptest.Server, testCases []testCase) {
	for _, tc := range testCases {
		resp, body := testRequest(t, ts, tc.method, tc.path, tc.body)
		if resp.StatusCode != tc.expectedStatus {
			t.Fatalf("test case '%s' failed, expected status %d, got %d", tc.name, tc.expectedStatus, resp.StatusCode)
		}
		if body != tc.expectedBody {
			t.Fatalf("test case '%s' failed, expected body '%s', got '%s'", tc.name, tc.expectedBody, body)
		}
	}
}

func testRequest(t *testing.T, ts *httptest.Server, method, path string, body io.Reader) (*http.Response, string) {
	req, err := http.NewRequest(method, ts.URL+path, body)
	if err != nil {
		t.Fatal(err)
		return nil, ""
	}

	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
		return nil, ""
	}

	respBody, err := io.ReadAll(resp.Body)
	if err != nil {
		t.Fatal(err)
		return nil, ""
	}
	defer resp.Body.Close()

	return resp, string(respBody)
}
