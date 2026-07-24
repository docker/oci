package ociserver

import (
	"context"
	"encoding/json"
	"log/slog"
	"net/http"
	"slices"
	"strings"

	"github.com/docker/oci"
	"github.com/docker/oci/ociref"
	"github.com/docker/oci/ociserver/mux"
)

// ServerConfig configures a [Server].
type ServerConfig struct {
	// Logger receives diagnostics for unexpected internal failures. A nil
	// Logger disables logging.
	Logger *slog.Logger

	// Middlewares are applied to all routes on the server's root mux, including
	// routes added through [Server.Mux]. They are applied in the order provided;
	// the first middleware is the outermost middleware.
	Middlewares []func(http.Handler) http.Handler

	// AuthMiddleware, when non-nil, is applied only to the built-in OCI routes.
	AuthMiddleware func(http.Handler) http.Handler
}

// Server contains everything necessary to run the API portion of the service
type Server struct {
	cfg      ServerConfig
	mux      *mux.Mux
	db       oci.Interface
	redirect Redirecter
}

var _ http.Handler = (*Server)(nil)

// New returns a new server backed by pers. A nil cfg is equivalent to a
// pointer to a zero [ServerConfig]. The configuration is copied before New
// returns and may be modified by the caller afterward.
func New(pers oci.Interface, cfg0 *ServerConfig) (*Server, error) {
	var cfg ServerConfig
	if cfg0 != nil {
		cfg = *cfg0
		cfg.Middlewares = slices.Clone(cfg0.Middlewares)
	}
	s := &Server{
		cfg: cfg,
		db:  pers,
	}
	if redirecter, ok := pers.(Redirecter); ok {
		s.redirect = redirecter
	}
	s.addRoutes()
	return s, nil
}

// ServeHTTP implements [http.Handler].
func (s *Server) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	s.mux.ServeHTTP(w, r)
}

// Mux returns the server's root router. Callers may use it to add routes outside
// the built-in /v2 namespace, which is owned by a mounted child router. Root
// middleware must be configured through [ServerConfig.Middlewares] because the
// router does not allow middleware to be added after routes are registered.
func (s *Server) Mux() *mux.Mux {
	return s.mux
}

func (s *Server) logError(ctx context.Context, message string, err error, args ...any) {
	if s.cfg.Logger == nil {
		return
	}
	safeArgs := make([]any, len(args))
	copy(safeArgs, args)
	for i, arg := range safeArgs {
		if str, ok := arg.(string); ok {
			safeArgs[i] = sanitizeLogInput(str)
		}
	}
	s.cfg.Logger.ErrorContext(ctx, message, append(safeArgs, "error", err)...)
}

// sanitizeLogInput replaces newline characters to prevent log injection
func sanitizeLogInput(input string) string {
	escaped := strings.NewReplacer("\n", "\\n", "\r", "\\r")
	return escaped.Replace(input)
}

func (s *Server) addRoutes() {
	r := mux.New()
	if len(s.cfg.Middlewares) > 0 {
		r.Use(s.cfg.Middlewares...)
	}

	r.Route("/v2", func(r mux.Router) {
		if s.cfg.AuthMiddleware != nil {
			r.Use(s.cfg.AuthMiddleware)
		}
		r.Use(validateRepositoryName)

		r.Handle("/", s.root())

		// blob uploads
		r.Post("/*name/blobs/uploads/", s.blobUploadPost())
		r.Get("/*name/blobs/uploads/:session", s.blobUploadGet())
		r.Patch("/*name/blobs/uploads/:session", s.blobUploadPatch())
		r.Put("/*name/blobs/uploads/:session", s.blobUploadPut())
		r.Delete("/*name/blobs/uploads/:session", s.blobUploadDelete())

		// blobs
		r.Head("/*name/blobs/:digest", s.blobHeadGet())
		r.Get("/*name/blobs/:digest", s.blobHeadGet())
		r.Delete("/*name/blobs/:digest", s.blobDelete())

		// manifests
		r.Head("/*name/manifests/:reference", s.manifestHeadGet())
		r.Get("/*name/manifests/:reference", s.manifestHeadGet())
		r.Put("/*name/manifests/:reference", s.manifestPut())
		r.Delete("/*name/manifests/:reference", s.manifestDelete())

		// tags
		r.Get("/*name/tags/list", s.tagsGet())

		// referrers
		r.Get("/*name/referrers/:digest", s.referrersGet())
	})
	s.mux = r
}

func validateRepositoryName(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		name := mux.URLParam(r, "name")
		if name != "" && !ociref.IsValidRepository(name) {
			returnError(w, ErrNameInvalid(name))
			return
		}
		next.ServeHTTP(w, r)
	})
}

func (s *Server) root() http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte("{}"))
	}
}

func returnError(w http.ResponseWriter, oe *OCIError) {
	b, err := json.Marshal(map[string]any{
		"errors": []*OCIError{oe},
	})
	if err != nil {
		w.WriteHeader(http.StatusInternalServerError)
		return
	}
	w.Header().Set("Content-Type", "application/json")
	if oe.status == http.StatusRequestedRangeNotSatisfiable {
		w.Header().Set("Content-Range", "bytes */*")
	}
	w.WriteHeader(oe.status)
	_, _ = w.Write(b)
}

// Redirecter provides storage-backed URL generation for blob redirects.
type Redirecter interface {
	// Redirect returns whether the request should redirect, the redirect URL, and
	// any error encountered while deciding.
	Redirect(r *http.Request, desc oci.Descriptor, repo string) (bool, string, error)
}
