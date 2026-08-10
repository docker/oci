package mux

import (
	"fmt"
	"net/http"
	"net/http/httptest"
	"testing"
)

func TestTemplateParametersAndLiterals(t *testing.T) {
	m := New()
	m.Get("/literal.+/:id/*rest", func(w http.ResponseWriter, r *http.Request) {
		_, _ = fmt.Fprintf(w, "%s|%s|%s", URLParam(r, "id"), URLParam(r, "rest"), r.Pattern)
	})

	rec := httptest.NewRecorder()
	m.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/literal.+/42/a/b", nil))
	if got, want := rec.Body.String(), "42|a/b|/literal.+/:id/*rest"; got != want {
		t.Fatalf("body = %q, want %q", got, want)
	}

	rec = httptest.NewRecorder()
	m.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/literalZZ/42/a/b", nil))
	if rec.Code != http.StatusNotFound {
		t.Fatalf("literal metacharacters were not escaped: status = %d", rec.Code)
	}
}

func TestConstraintsAreScopedAndInherited(t *testing.T) {
	m := New()
	m.RegisterConstraint("id", `[0-9]+`)
	m.Get("/numbers/:id", echoParam("id"))

	m.Group(func(r Router) {
		r.RegisterConstraint("id", `[a-z]+`)
		r.Get("/letters/:id", echoParam("id"))
	})

	m.Route("/api", func(r Router) {
		r.Get("/items/:id", echoParam("id"))
	})

	tests := []struct {
		path   string
		status int
		body   string
	}{
		{path: "/numbers/123", status: http.StatusOK, body: "123"},
		{path: "/numbers/abc", status: http.StatusNotFound, body: "not found"},
		{path: "/letters/abc", status: http.StatusOK, body: "abc"},
		{path: "/letters/123", status: http.StatusNotFound, body: "not found"},
		{path: "/api/items/123", status: http.StatusOK, body: "123"},
		{path: "/api/items/abc", status: http.StatusNotFound, body: "not found"},
	}
	for _, tt := range tests {
		t.Run(tt.path, func(t *testing.T) {
			rec := httptest.NewRecorder()
			m.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, tt.path, nil))
			if rec.Code != tt.status || rec.Body.String() != tt.body {
				t.Fatalf("response = (%d, %q), want (%d, %q)", rec.Code, rec.Body.String(), tt.status, tt.body)
			}
		})
	}
}

func TestRouteStripsPrefixAndRetainsOriginalRequest(t *testing.T) {
	m := New()
	m.Route("/api/:version", func(r Router) {
		r.Use(func(next http.Handler) http.Handler {
			return http.HandlerFunc(func(w http.ResponseWriter, req *http.Request) {
				_, _ = fmt.Fprintf(w, "middleware=%s;", URLParam(req, "id"))
				next.ServeHTTP(w, req)
			})
		})
		r.Get("/items/:id", func(w http.ResponseWriter, req *http.Request) {
			_, _ = fmt.Fprintf(w, "version=%s;id=%s;path=%s;pattern=%s",
				URLParam(req, "version"), URLParam(req, "id"), req.URL.Path, req.Pattern)
		})
	})

	rec := httptest.NewRecorder()
	m.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/api/v2/items/42", nil))
	want := "middleware=42;version=v2;id=42;path=/api/v2/items/42;pattern=/api/:version/items/:id"
	if got := rec.Body.String(); got != want {
		t.Fatalf("body = %q, want %q", got, want)
	}
}

func TestNestedRoute(t *testing.T) {
	m := New()
	m.Route("/api", func(r Router) {
		r.Route("/v1", func(r Router) {
			r.Get("/widgets/:id", func(w http.ResponseWriter, req *http.Request) {
				_, _ = fmt.Fprintf(w, "%s %s", URLParam(req, "id"), req.Pattern)
			})
		})
	})

	rec := httptest.NewRecorder()
	m.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/api/v1/widgets/7", nil))
	if got, want := rec.Body.String(), "7 /api/v1/widgets/:id"; got != want {
		t.Fatalf("body = %q, want %q", got, want)
	}
}

func TestRouteOwnsMatchedPrefix(t *testing.T) {
	m := New()
	m.Route("/api", func(r Router) {
		r.Get("/known", func(w http.ResponseWriter, _ *http.Request) {
			_, _ = w.Write([]byte("known"))
		})
		r.NotFound(func(w http.ResponseWriter, _ *http.Request) {
			w.WriteHeader(http.StatusTeapot)
			_, _ = w.Write([]byte("child not found"))
		})
	})
	m.Get("/api/shadowed", func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write([]byte("root"))
	})

	rec := httptest.NewRecorder()
	m.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/api/shadowed", nil))
	if rec.Code != http.StatusTeapot || rec.Body.String() != "child not found" {
		t.Fatalf("response = (%d, %q), want child not found", rec.Code, rec.Body.String())
	}
}

func TestRouteRejectsDuplicateMount(t *testing.T) {
	m := New()
	m.Route("/api", func(Router) {})
	assertPanics(t, func() { m.Route("/api", func(Router) {}) })
	assertPanics(t, func() { m.Route("/nil", nil) })
	assertPanics(t, func() { m.Route("/trailing/", func(Router) {}) })
}

func TestRouteAtRoot(t *testing.T) {
	m := New()
	m.Route("/", func(r Router) {
		r.Get("/items/:id", func(w http.ResponseWriter, r *http.Request) {
			_, _ = fmt.Fprintf(w, "%s %s", URLParam(r, "id"), r.Pattern)
		})
	})

	rec := httptest.NewRecorder()
	m.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/items/9", nil))
	if rec.Code != http.StatusOK || rec.Body.String() != "9 /items/:id" {
		t.Fatalf("response = (%d, %q), want (200, %q)", rec.Code, rec.Body.String(), "9 /items/:id")
	}
}

func TestValidPattern(t *testing.T) {
	for _, pattern := range []string{"/", "/users/:id", "/files/*path", "/literal.+"} {
		if err := ValidPattern(pattern); err != nil {
			t.Errorf("ValidPattern(%q) = %v", pattern, err)
		}
	}
	for _, pattern := range []string{"", "users/:id", "/users/:", "/:id/:id", "/files/*"} {
		if err := ValidPattern(pattern); err == nil {
			t.Errorf("ValidPattern(%q) unexpectedly succeeded", pattern)
		}
	}
}

func TestRegisterConstraintPanics(t *testing.T) {
	tests := []func(*Mux){
		func(m *Mux) { m.RegisterConstraint("bad-name", `[a-z]+`) },
		func(m *Mux) { m.RegisterConstraint("id", ``) },
		func(m *Mux) { m.RegisterConstraint("id", `([a-z]+)`) },
		func(m *Mux) { m.RegisterConstraint("id", `^[a-z]+$`) },
		func(m *Mux) { m.RegisterConstraint("id", `[a-z]*`) },
		func(m *Mux) {
			m.RegisterConstraint("id", `[a-z]+`)
			m.RegisterConstraint("id", `[0-9]+`)
		},
		func(m *Mux) {
			m.Get("/items/:id", echoParam("id"))
			m.RegisterConstraint("id", `[0-9]+`)
		},
	}
	for i, test := range tests {
		t.Run(fmt.Sprint(i), func(t *testing.T) {
			assertPanics(t, func() { test(New()) })
		})
	}
}

func echoParam(name string) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte(URLParam(r, name)))
	}
}

func assertPanics(t *testing.T, fn func()) {
	t.Helper()
	defer func() {
		if recover() == nil {
			t.Fatal("function did not panic")
		}
	}()
	fn()
}
