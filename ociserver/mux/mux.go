package mux

import (
	"context"
	"fmt"
	"net/http"
	"regexp"
	"strings"
)

var _ Router = &Mux{}

// methodAll is the internal wildcard method key used by Handle and HandleFunc
// to register a handler for every HTTP method. It is "*" rather than a word
// like "all" so it cannot be confused with, or shadowed by, a real HTTP method
// name (which Method normalizes to upper case).
const methodAll = "*"

// contextKey is an unexported type used for the router's own context keys so
// they cannot collide with keys defined in other packages.
type contextKey int

const (
	// ctxKeyRequestPath carries the remaining path a sub-Router should match
	// against, set by Route before delegating to the sub-Router.
	ctxKeyRequestPath contextKey = iota

	// ctxKeyParams carries named regular-expression captures.
	ctxKeyParams
)

// URLParam returns the value of the named regex capture group for the current
// request, or "" if no such group matched.
func URLParam(r *http.Request, name string) string {
	return URLParamFromCtx(r.Context(), name)
}

// URLParamFromCtx returns the value of the named regex capture group stored in
// ctx, or "" if no such group matched.
func URLParamFromCtx(ctx context.Context, name string) string {
	params, _ := ctx.Value(ctxKeyParams).(map[string]string)
	return params[name]
}

// Mux routes HTTP requests using path templates.
type Mux struct {
	// Custom method not allowed handler
	methodNotAllowedHandler http.HandlerFunc

	// A reference to the parent mux used by subrouters when mounting
	// to a parent mux
	parent *Mux

	// Custom route not found handler
	notFoundHandler http.HandlerFunc

	// Debug logger; nil means fall back to the parent's, then a no-op. Set via
	// WithLogger. Resolved through log().
	logger Logger

	// The middleware stack
	middlewares []func(http.Handler) http.Handler

	// Controls the behaviour of middleware chain generation when a mux
	// is registered as an inline group inside another mux.
	inline bool

	// Set once any route has been registered through this mux (or, for an
	// inline mux, through the parent it appends to). Used to reject Use()
	// calls made after routes, whose middleware would otherwise be dropped.
	hasRoutes bool

	// constraints contains inherited and locally registered parameter matchers.
	constraints map[string]string

	// localConstraints distinguishes a duplicate registration in this scope
	// from an intentional override of an inherited constraint.
	localConstraints map[string]struct{}

	routes routes
}

type routes struct {
	rts []route
}

func (r *routes) append(rt route) {
	r.rts = append(r.rts, rt)
}

type route struct {
	regex         *regexp.Regexp
	methodhandler map[string]http.Handler
	varNames      []string
	template      string
	remainder     int
}

// Logger is the minimal logging surface mux uses. *slog.Logger
// satisfies it directly, so New(WithLogger(slog.Default())) works without an
// adapter; other loggers need only a small shim.
type Logger interface {
	Debug(msg string, args ...any)
}

// sanitizeLogInput replaces newline characters to prevent log injection
func sanitizeLogInput(input string) string {
	escaped := strings.NewReplacer("\n", "\\n", "\r", "\\r")
	return escaped.Replace(input)
}

// noopLogger is the default logger: a library should not write to the global
// logger unless the caller asks it to.
type noopLogger struct{}

func (noopLogger) Debug(string, ...any) {}

// Option configures a Mux at construction time. Pass options to New.
type Option func(*Mux)

// WithNotFoundHandler sets the handler invoked when no route matches the
// request path.
func WithNotFoundHandler(h http.HandlerFunc) Option {
	return func(mx *Mux) { mx.notFoundHandler = h }
}

// WithMethodNotAllowedHandler sets the handler invoked when a route matches the
// request path but not its method.
func WithMethodNotAllowedHandler(h http.HandlerFunc) Option {
	return func(mx *Mux) { mx.methodNotAllowedHandler = h }
}

// WithLogger sets the debug logger. By default the router logs nothing.
func WithLogger(l Logger) Option {
	return func(mx *Mux) { mx.logger = l }
}

// New returns a newly initialized Mux that implements the Router interface,
// configured by the given options. Call New() for defaults, or pass options
// such as WithNotFoundHandler to customize behavior.
func New(opts ...Option) *Mux {
	mux := &Mux{
		constraints:      make(map[string]string),
		localConstraints: make(map[string]struct{}),
		routes: routes{
			rts: []route{},
		},
	}
	for _, opt := range opts {
		opt(mux)
	}
	return mux
}

// Use appends middleware to the router's middleware stack.
func (mx *Mux) Use(middlewares ...func(http.Handler) http.Handler) {
	// Middleware chains are baked into each handler at registration time, so a
	// middleware added after a route would silently never run. Fail loudly
	// instead of dropping it.
	if mx.hasRoutes {
		panic("mux: all middlewares must be registered before routes")
	}
	mx.middlewares = append(mx.middlewares, middlewares...)
}

// With returns an inline router using the supplied middleware.
func (mx *Mux) With(middlewares ...func(http.Handler) http.Handler) Router {
	return &Mux{
		constraints:      cloneConstraints(mx.constraints),
		localConstraints: make(map[string]struct{}),
		middlewares:      middlewares,
		parent:           mx,
		inline:           true,
	}
}

// Group adds an inline router with its own middleware stack.
func (mx *Mux) Group(fn func(r Router)) Router {
	im := mx.With()
	if fn != nil {
		fn(im)
	}
	return im
}

// RegisterConstraint sets the regular-expression fragment used by parameters
// with name in routes registered through this router scope. Constraints are
// inherited by child scopes and may be overridden there. It panics when the
// name or expression is invalid, when the name was already registered in this
// scope, or when routes have already been registered through this scope.
func (mx *Mux) RegisterConstraint(name, expression string) {
	if mx.hasRoutes {
		panic("mux: all constraints must be registered before routes")
	}
	if err := validateConstraint(name, expression); err != nil {
		panic("mux: " + err.Error())
	}
	if _, ok := mx.localConstraints[name]; ok {
		panic(fmt.Sprintf("mux: constraint %q is already registered", name))
	}
	mx.constraints[name] = expression
	mx.localConstraints[name] = struct{}{}
}

// Route creates a child router and mounts it at pattern. Once pattern matches,
// the child router owns that path namespace, including its 404 and 405
// responses. The matched prefix is removed from the path used for child route
// matching; handlers continue to see the original request URL.
func (mx *Mux) Route(pattern string, fn func(r Router)) Router {
	if fn == nil {
		panic(fmt.Sprintf("mux: Route requires a non-nil function for %q", pattern))
	}
	compiled, err := compileTemplate(pattern, mx.constraints, true)
	if err != nil {
		panic(fmt.Sprintf("mux: invalid route prefix %q: %v", pattern, err))
	}

	sr := &Mux{
		constraints:      cloneConstraints(mx.constraints),
		localConstraints: make(map[string]struct{}),
		parent:           mx,
		routes:           routes{rts: []route{}},
	}
	fn(sr)

	handler := mx.chainHandler(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		sr.ServeHTTP(w, r)
	}))
	for _, registered := range mx.destinationRoutes().rts {
		if registered.remainder > 0 && registered.regex.String() == compiled.regex.String() {
			panic(fmt.Sprintf("mux: route prefix %q is already mounted", pattern))
		}
	}
	mx.appendRoute(methodAll, pattern, compiled, handler)
	return sr
}

// Handle registers handler for all methods matching pattern.
func (mx *Mux) Handle(pattern string, handler http.Handler) {
	mx.Method(methodAll, pattern, handler)
}

// HandleFunc registers handler for all methods matching pattern.
func (mx *Mux) HandleFunc(pattern string, handler http.HandlerFunc) {
	mx.Method(methodAll, pattern, handler)
}

// Method registers handler for method and pattern.
func (mx *Mux) Method(method, pattern string, handler http.Handler) {
	// Normalize the method so registrations are case-insensitive and match the
	// upper-case r.Method values used at dispatch time. The wildcard sentinel
	// is upper-case-stable, so this is safe for it too.
	if method != methodAll {
		method = strings.ToUpper(method)
	}
	handler = mx.chainHandler(handler)
	mx.hasRoutes = true
	compiled, err := compileTemplate(pattern, mx.constraints, false)
	if err != nil {
		panic(fmt.Sprintf("mux: invalid route template %q: %v", pattern, err))
	}

	for _, rr := range mx.routes.rts {
		if rr.regex.String() == compiled.regex.String() && rr.remainder == 0 {
			rr.methodhandler[method] = handler
			return
		}
	}
	mx.appendRoute(method, pattern, compiled, handler)
}

func (mx *Mux) appendRoute(method, template string, compiled compiledTemplate, handler http.Handler) {
	mx.hasRoutes = true
	r := route{
		regex:         compiled.regex,
		methodhandler: map[string]http.Handler{method: handler},
		varNames:      compiled.varNames,
		template:      template,
		remainder:     compiled.remainderIndex,
	}
	if destination := mx.destinationMux(); destination != mx {
		destination.routes.append(r)
		destination.hasRoutes = true
		return
	}
	mx.routes.append(r)
}

func (mx *Mux) destinationRoutes() *routes {
	return &mx.destinationMux().routes
}

func (mx *Mux) destinationMux() *Mux {
	destination := mx
	for destination.parent != nil && destination.inline {
		destination = destination.parent
	}
	return destination
}

// captureNames returns the names of a compiled pattern's capture groups (in
// order, excluding the whole-match group at index 0). Unnamed groups yield "".
func captureNames(re *regexp.Regexp) []string {
	names := re.SubexpNames()
	if len(names) <= 1 {
		return nil
	}
	return names[1:]
}

// MethodFunc registers handler for method and pattern.
func (mx *Mux) MethodFunc(method, pattern string, handler http.HandlerFunc) {
	mx.Method(method, pattern, handler)
}

// Connect registers a CONNECT handler for pattern.
func (mx *Mux) Connect(pattern string, handler http.HandlerFunc) {
	mx.MethodFunc(http.MethodConnect, pattern, handler)
}

// Delete registers a DELETE handler for pattern.
func (mx *Mux) Delete(pattern string, handler http.HandlerFunc) {
	mx.MethodFunc(http.MethodDelete, pattern, handler)
}

// Get registers a GET handler for pattern.
func (mx *Mux) Get(pattern string, handler http.HandlerFunc) {
	mx.MethodFunc(http.MethodGet, pattern, handler)
}

// Head registers a HEAD handler for pattern.
func (mx *Mux) Head(pattern string, handler http.HandlerFunc) {
	mx.MethodFunc(http.MethodHead, pattern, handler)
}

// Options registers an OPTIONS handler for pattern.
func (mx *Mux) Options(pattern string, handler http.HandlerFunc) {
	mx.MethodFunc(http.MethodOptions, pattern, handler)
}

// Patch registers a PATCH handler for pattern.
func (mx *Mux) Patch(pattern string, handler http.HandlerFunc) {
	mx.MethodFunc(http.MethodPatch, pattern, handler)
}

// Post registers a POST handler for pattern.
func (mx *Mux) Post(pattern string, handler http.HandlerFunc) {
	mx.MethodFunc(http.MethodPost, pattern, handler)
}

// Put registers a PUT handler for pattern.
func (mx *Mux) Put(pattern string, handler http.HandlerFunc) {
	mx.MethodFunc(http.MethodPut, pattern, handler)
}

// Trace registers a TRACE handler for pattern.
func (mx *Mux) Trace(pattern string, handler http.HandlerFunc) {
	mx.MethodFunc(http.MethodTrace, pattern, handler)
}

// NotFound sets the handler used when no route matches.
func (mx *Mux) NotFound(handler http.HandlerFunc) {
	mx.notFoundHandler = handler
}

// MethodNotAllowed sets the handler used when a path matches but its method does not.
func (mx *Mux) MethodNotAllowed(handler http.HandlerFunc) {
	mx.methodNotAllowedHandler = handler
}

func (mx *Mux) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	path := r.URL.Path
	if requestpath, ok := r.Context().Value(ctxKeyRequestPath).(string); ok {
		path = requestpath
	}

	// pathMatched tracks whether any route matched the path but not the
	// method, so we can distinguish 405 (Method Not Allowed) from 404 (Not
	// Found) only after considering every overlapping pattern.
	pathMatched := false

	for _, route := range mx.routes.rts {
		matches := route.regex.FindStringSubmatch(path)
		if len(matches) <= 0 {
			continue
		}
		handler, ok := route.methodhandler[r.Method]
		if !ok {
			handler, ok = route.methodhandler[methodAll]
		}
		if !ok {
			// This pattern matched the path but has no handler for the
			// method. Keep scanning: another overlapping pattern may.
			pathMatched = true
			continue
		}

		params, _ := r.Context().Value(ctxKeyParams).(map[string]string)
		params1 := make(map[string]string, len(params)+len(matches)-1)
		for name, value := range params {
			params1[name] = value
		}
		for i, match := range matches[1:] {
			if i > len(route.varNames)-1 || route.varNames[i] == "" {
				// Unnamed capture group: not exposed as a parameter.
				continue
			}
			params1[route.varNames[i]] = match
		}
		ctx := context.WithValue(r.Context(), ctxKeyParams, params1)
		if route.remainder > 0 {
			ctx = context.WithValue(ctx, ctxKeyRequestPath, matches[route.remainder])
		}
		r.Pattern = appendPattern(r.Pattern, route.template)
		handler.ServeHTTP(w, r.WithContext(ctx))
		return
	}

	if pathMatched {
		mx.handleMethodNotAllowed(w, r)
		mx.log().Debug("method not allowed", "method", r.Method, "path", sanitizeLogInput(path))
		return
	}
	mx.handleNotFound(w, r)
}

func appendPattern(parent, child string) string {
	if parent == "" || parent == "/" {
		return child
	}
	return parent + child
}

// log resolves the logger for this mux: its own if set, otherwise the parent's,
// falling back to a no-op. This mirrors the NotFound/MethodNotAllowed fallback
// so sub-Routers inherit the logger configured on the root.
func (mx *Mux) log() Logger {
	if mx.logger != nil {
		return mx.logger
	}
	if mx.parent != nil {
		return mx.parent.log()
	}
	return noopLogger{}
}

func (mx *Mux) chainHandler(handler http.Handler) http.Handler {
	for i := len(mx.middlewares) - 1; i >= 0; i-- {
		handler = mx.middlewares[i](handler)
	}
	if mx.parent != nil && mx.inline {
		handler = mx.parent.chainHandler(handler)
	}
	return handler
}

func (mx *Mux) handleNotFound(w http.ResponseWriter, r *http.Request) {
	if mx.notFoundHandler != nil {
		mx.notFoundHandler(w, r)
		return
	}
	if mx.parent != nil {
		mx.parent.handleNotFound(w, r)
		return
	}
	defaultNotFoundHandler(w, r)
}

func defaultNotFoundHandler(w http.ResponseWriter, _ *http.Request) {
	w.WriteHeader(http.StatusNotFound)
	_, _ = w.Write([]byte("not found"))
}

func (mx *Mux) handleMethodNotAllowed(w http.ResponseWriter, r *http.Request) {
	if mx.methodNotAllowedHandler != nil {
		mx.methodNotAllowedHandler(w, r)
		return
	}
	if mx.parent != nil {
		mx.parent.handleMethodNotAllowed(w, r)
		return
	}
	defaultMethodNotAllowedHandler(w, r)
}

func defaultMethodNotAllowedHandler(w http.ResponseWriter, _ *http.Request) {
	w.WriteHeader(http.StatusMethodNotAllowed)
	_, _ = w.Write([]byte("not allowed"))
}
