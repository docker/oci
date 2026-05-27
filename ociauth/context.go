package ociauth

import (
	"context"
)

type scopeKey struct{}

// ContextWithScope returns ctx annotated with the given
// scope. The ociauth transport does not add this scope to a registry
// challenge; challenges remain the source of truth for new token acquisition.
func ContextWithScope(ctx context.Context, s Scope) context.Context {
	return context.WithValue(ctx, scopeKey{}, s)
}

// ScopeFromContext returns any scope associated with the context
// by [ContextWithScope].
func ScopeFromContext(ctx context.Context) Scope {
	s, _ := ctx.Value(scopeKey{}).(Scope)
	return s
}

type requestInfoKey struct{}

// RequestInfo provides information about the OCI request that
// is currently being made. It is expected to be attached to an HTTP
// request context. The [ociclient] package will add this to all
// requests that is makes.
type RequestInfo struct {
	// RequiredScope holds the authorization scope that can satisfy
	// the request for cached-token reuse. When the transport already
	// knows the registry's bearer token realm and has a refresh token,
	// it may use this scope to acquire a token proactively. A
	// Www-Authenticate challenge remains authoritative when present.
	RequiredScope Scope
}

// ContextWithRequestInfo returns ctx annotated with the given
// request informaton. When ociclient receives a request with
// this attached, it will respect info.RequiredScope to determine
// what auth tokens to reuse or proactively acquire.
func ContextWithRequestInfo(ctx context.Context, info RequestInfo) context.Context {
	return context.WithValue(ctx, requestInfoKey{}, info)
}

// RequestInfoFromContext returns any request information associated with the context
// by [ContextWithRequestInfo].
func RequestInfoFromContext(ctx context.Context) RequestInfo {
	info, _ := ctx.Value(requestInfoKey{}).(RequestInfo)
	return info
}
