// Package server exposes the REST API over HTTP: the router built from the
// api.Built route table under server.base_path, /healthz and /readyz at the
// root, GET {base}/openapi.json, problem+json errors, and the middleware
// chain (recover, timeout, CORS, gzip, body limit, auth). The Runtime is
// stored atomically so watch mode can swap in a new API without a restart;
// requests in flight keep the runtime they started with.
package server

import (
	"bytes"
	"compress/gzip"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"log/slog"
	"net/http"
	"strconv"
	"strings"
	"sync/atomic"

	"github.com/suprbdev/pdbcore/auth"
	"github.com/suprbdev/pdbcore/httpx"

	"github.com/suprbdev/pdbr/internal/api"
	"github.com/suprbdev/pdbr/internal/config"
)

// RouteHandler serves a request that matched an API route whose method is
// allowed. Handler implements it: parse -> compile -> execute -> map
// status/headers/body. Runtime.Handler nil answers 501.
type RouteHandler interface {
	ServeRoute(w http.ResponseWriter, r *http.Request, m *api.Match)
}

// RouteHandlerFunc adapts a function to RouteHandler.
type RouteHandlerFunc func(w http.ResponseWriter, r *http.Request, m *api.Match)

func (f RouteHandlerFunc) ServeRoute(w http.ResponseWriter, r *http.Request, m *api.Match) {
	f(w, r, m)
}

// Runtime is everything a request needs; built once per catalog and shared
// read-only across requests. Swap replaces it atomically.
type Runtime struct {
	Built *api.Built
	// OpenAPI is the compact JSON document served at {base}/openapi.json.
	OpenAPI []byte
	// OpenAPIGzip is OpenAPI compressed once at build time.
	OpenAPIGzip []byte
	// OpenAPIETag is the strong ETag (quoted) of OpenAPI.
	OpenAPIETag string
	// Handler serves matched routes (nil answers 501).
	Handler RouteHandler
}

// NewRuntime renders the OpenAPI document once and wraps the model.
func NewRuntime(built *api.Built, h RouteHandler) (*Runtime, error) {
	doc, err := built.OpenAPI.CompactJSON()
	if err != nil {
		return nil, err
	}
	var zbuf bytes.Buffer
	zw, err := gzip.NewWriterLevel(&zbuf, gzip.BestCompression)
	if err != nil {
		return nil, err
	}
	if _, err := zw.Write(doc); err != nil {
		return nil, err
	}
	if err := zw.Close(); err != nil {
		return nil, err
	}
	sum := sha256.Sum256(doc)
	return &Runtime{
		Built:       built,
		OpenAPI:     doc,
		OpenAPIGzip: zbuf.Bytes(),
		OpenAPIETag: `"` + hex.EncodeToString(sum[:16]) + `"`,
		Handler:     h,
	}, nil
}

// Server is the HTTP front end.
type Server struct {
	cfg  config.Server
	auth *auth.Authenticator
	log  *slog.Logger
	rt   atomic.Pointer[Runtime]
}

// New creates a server over an initial runtime. authn may be nil (no auth:
// every request runs without claims or a role).
func New(cfg config.Server, authn *auth.Authenticator, rt *Runtime, log *slog.Logger) *Server {
	if log == nil {
		log = slog.Default()
	}
	s := &Server{cfg: cfg, auth: authn, log: log}
	s.rt.Store(rt)
	return s
}

// Swap atomically replaces the runtime (watch mode).
func (s *Server) Swap(rt *Runtime) { s.rt.Store(rt) }

// Runtime returns the current runtime.
func (s *Server) Runtime() *Runtime { return s.rt.Load() }

// Handler returns the full HTTP handler (middleware chain + mux).
func (s *Server) Handler() http.Handler {
	base := s.cfg.BasePath
	mux := http.NewServeMux()
	mux.HandleFunc("/healthz", readOnlyRoute(httpx.Healthz))
	mux.HandleFunc("/readyz", readOnlyRoute(httpx.Healthz))
	apiRouter := s.withAuth(http.HandlerFunc(s.handleAPI))
	if base == "" {
		mux.Handle("/", apiRouter)
	} else {
		mux.Handle(base+"/", apiRouter)
		mux.HandleFunc("/", func(w http.ResponseWriter, r *http.Request) {
			WriteProblem(w, r, http.StatusNotFound, CodeNotFound, "no such route")
		})
	}
	var h http.Handler = mux
	h = s.withBodyLimit(h)
	if s.cfg.Compression {
		h = httpx.Gzip(h)
	}
	if len(s.cfg.CORSOrigins) > 0 {
		h = httpx.CORSWithPreflight(s.cfg.CORSOrigins, "GET, HEAD, POST, PUT, PATCH, DELETE, OPTIONS", h)
	}
	if s.cfg.RequestTimeout > 0 {
		h = withTimeout(h, s.cfg.RequestTimeout)
	}
	if s.cfg.OpenAPI {
		var doc http.Handler = readOnlyRoute(s.handleOpenAPI)
		if len(s.cfg.CORSOrigins) > 0 {
			doc = httpx.CORSWithPreflight(s.cfg.CORSOrigins, "GET, HEAD, POST, PUT, PATCH, DELETE, OPTIONS", doc)
		}
		full, docPath := h, base+"/openapi.json"
		h = http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			if r.URL.Path == docPath {
				doc.ServeHTTP(w, r)
				return
			}
			full.ServeHTTP(w, r)
		})
	}
	return s.withRecover(h)
}

// ListenAndServe runs the server until ctx is cancelled.
func (s *Server) ListenAndServe(ctx context.Context) error {
	return httpx.ListenAndServe(ctx, s.Handler(), httpx.ServeOptions{
		Addr:           s.cfg.Addr,
		RequestTimeout: s.cfg.RequestTimeout,
		Log:            s.log,
		LogAttrs:       []any{"base_path", s.cfg.BasePath, "openapi", s.cfg.OpenAPI},
	})
}

// readOnlyRoute serves GET/HEAD and answers every other method with a
// problem+json 405 + Allow, like the API routes do (the mux's own 405 is
// text/plain, and a method-restricted pattern would fall through to the
// API router's 404 instead).
func readOnlyRoute(next http.HandlerFunc) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodGet && r.Method != http.MethodHead {
			w.Header().Set("Allow", "GET, HEAD")
			WriteProblem(w, r, http.StatusMethodNotAllowed, CodeMethodNotAllowed, "method "+r.Method+" is not available on this path")
			return
		}
		next(w, r)
	}
}

// handleOpenAPI writes the pre-rendered document straight to the connection,
// outside the timeout buffer and the gzip middleware: the gzip bytes built
// with the runtime when the client accepts gzip, with ETag/If-None-Match
// revalidation.
func (s *Server) handleOpenAPI(w http.ResponseWriter, r *http.Request) {
	rt := s.rt.Load()
	h := w.Header()
	h.Set("Content-Type", "application/json; charset=utf-8")
	h.Set("ETag", rt.OpenAPIETag)
	h.Set("Cache-Control", "no-cache")
	h.Add("Vary", "Accept-Encoding")
	if etagMatches(r.Header.Get("If-None-Match"), rt.OpenAPIETag) {
		w.WriteHeader(http.StatusNotModified)
		return
	}
	body := rt.OpenAPI
	if s.cfg.Compression && httpx.AcceptsGzip(r.Header.Get("Accept-Encoding")) {
		h.Set("Content-Encoding", "gzip")
		body = rt.OpenAPIGzip
	}
	h.Set("Content-Length", strconv.Itoa(len(body)))
	if r.Method == http.MethodHead {
		return
	}
	w.Write(body) //nolint:errcheck
}

func etagMatches(header, etag string) bool {
	for _, part := range strings.Split(header, ",") {
		part = strings.TrimPrefix(strings.TrimSpace(part), "W/")
		if part == etag || part == "*" {
			return true
		}
	}
	return false
}

// handleAPI resolves the path below base_path against the current runtime's
// route table: unknown -> 404, method not offered -> 405 + Allow, else the
// RouteHandler.
func (s *Server) handleAPI(w http.ResponseWriter, r *http.Request) {
	rt := s.rt.Load()
	below := strings.TrimPrefix(r.URL.EscapedPath(), s.cfg.BasePath)
	m, ok := rt.Built.Match(below)
	if !ok {
		WriteProblem(w, r, http.StatusNotFound, CodeNotFound, "no such route")
		return
	}
	if !m.Route.Allows(api.Method(r.Method)) {
		w.Header().Set("Allow", m.Route.AllowHeader())
		WriteProblem(w, r, http.StatusMethodNotAllowed, CodeMethodNotAllowed,
			"method "+r.Method+" is not available on this path")
		return
	}
	if rt.Handler == nil {
		WriteProblem(w, r, http.StatusNotImplemented, CodeNotImplemented, "request execution is not implemented yet")
		return
	}
	rt.Handler.ServeRoute(w, r, m)
}

// Identity is the authenticated request identity the router stores in the
// request context for the exec layer.
type Identity struct {
	Claims map[string]any
	Role   string
}

type identityKey struct{}

// IdentityFrom returns the request identity set by the auth middleware.
func IdentityFrom(ctx context.Context) Identity {
	id, _ := ctx.Value(identityKey{}).(Identity)
	return id
}

func (s *Server) withAuth(next http.Handler) http.Handler {
	if s.auth == nil {
		return next
	}
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		claims, role, err := s.auth.Authenticate(r)
		if err != nil {
			status, code := http.StatusUnauthorized, CodeInvalidToken
			if errors.Is(err, auth.ErrRoleNotAllowed) {
				status, code = http.StatusForbidden, CodeRoleNotAllowed
			}
			WriteProblem(w, r, status, code, err.Error())
			return
		}
		ctx := context.WithValue(r.Context(), identityKey{}, Identity{Claims: claims, Role: role})
		next.ServeHTTP(w, r.WithContext(ctx))
	})
}

// withBodyLimit caps request bodies at server.max_body_bytes: an oversized
// Content-Length short-circuits with 413, everything else is read through
// MaxBytesReader (the reader's error surfaces as 413 in the exec layer).
func (s *Server) withBodyLimit(next http.Handler) http.Handler {
	max := s.cfg.MaxBodyBytes
	if max <= 0 {
		return next
	}
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.ContentLength > max {
			WriteProblem(w, r, http.StatusRequestEntityTooLarge, CodeBodyTooLarge, "request body exceeds server.max_body_bytes")
			return
		}
		if r.Body != nil {
			r.Body = http.MaxBytesReader(w, r.Body, max)
		}
		next.ServeHTTP(w, r)
	})
}

func (s *Server) withRecover(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		defer func() {
			if rec := recover(); rec != nil {
				if err, ok := rec.(error); ok && errors.Is(err, http.ErrAbortHandler) {
					panic(rec)
				}
				s.log.Error("panic serving request", "method", r.Method, "path", r.URL.Path, "panic", rec)
				WriteProblem(w, r, http.StatusInternalServerError, CodeInternal, "internal error")
			}
		}()
		next.ServeHTTP(w, r)
	})
}
