// Package mux routes HTTP requests using path templates compiled to Go
// regular expressions.
//
// # Templates
//
// Templates match the complete path visible to their router and are anchored
// automatically. Literal segments are escaped. A :placeholder matches one
// non-empty segment, while a *wildcard matches one or more segments:
//
//	m.Get("/users/:id", func(w http.ResponseWriter, r *http.Request) {
//		id := mux.URLParam(r, "id")
//		// ...
//	})
//
//	m.Get("/files/*path", func(w http.ResponseWriter, r *http.Request) {
//		path := mux.URLParam(r, "path")
//		// ...
//	})
//
// A placeholder or wildcard must occupy its complete path segment. Use
// RegisterConstraint before registering routes to replace the default matcher
// for parameters with a particular name.
//
// Routes are evaluated in registration order and the first matching route with
// a handler for the request method wins.
//
// # Sub-routers
//
// Route mounts a child router at a path prefix. The prefix is removed from the
// path used for matching in the child, while handlers retain the original URL:
//
//	m.Route("/api", func(r mux.Router) {
//		r.Get("/widgets/:id", ...) // matches GET /api/widgets/123
//	})
//
// Once a Route prefix matches, its child owns the namespace and produces the
// final 404 or 405 response when no child endpoint handles the request.
package mux

import "net/http"

// Router is the routing surface implemented by Mux.
type Router interface {
	http.Handler

	// Use appends one or more middlewares to the Router stack.
	Use(middlewares ...func(http.Handler) http.Handler)

	// Group adds an inline Router with a fresh middleware and constraint scope.
	Group(fn func(r Router)) Router

	// Route creates and mounts a child Router at pattern.
	Route(pattern string, fn func(r Router)) Router

	// RegisterConstraint defines the regular-expression fragment used for
	// parameters with name in routes registered through this scope.
	RegisterConstraint(name, expression string)

	// Handle and HandleFunc add routes matching all HTTP methods.
	Handle(pattern string, h http.Handler)
	HandleFunc(pattern string, h http.HandlerFunc)

	// Method and MethodFunc add routes matching method.
	Method(method, pattern string, h http.Handler)
	MethodFunc(method, pattern string, h http.HandlerFunc)

	Connect(pattern string, h http.HandlerFunc)
	Delete(pattern string, h http.HandlerFunc)
	Get(pattern string, h http.HandlerFunc)
	Head(pattern string, h http.HandlerFunc)
	Options(pattern string, h http.HandlerFunc)
	Patch(pattern string, h http.HandlerFunc)
	Post(pattern string, h http.HandlerFunc)
	Put(pattern string, h http.HandlerFunc)
	Trace(pattern string, h http.HandlerFunc)

	// NotFound defines a handler for unmatched routes.
	NotFound(h http.HandlerFunc)

	// MethodNotAllowed defines a handler for matched paths without a handler for
	// the request method.
	MethodNotAllowed(h http.HandlerFunc)
}

// Middlewares is a slice of standard middleware handlers.
type Middlewares []func(http.Handler) http.Handler
