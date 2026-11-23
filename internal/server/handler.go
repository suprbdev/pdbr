package server

import (
	"errors"
	"log/slog"
	"net/http"
	"strings"

	"github.com/suprbdev/pdbr/internal/api"
	"github.com/suprbdev/pdbr/internal/compile"
	"github.com/suprbdev/pdbr/internal/exec"
	"github.com/suprbdev/pdbr/internal/parse"
	"github.com/suprbdev/pdbr/internal/plan"
)

// ContentTypeJSON is the media type of every successful JSON response.
const ContentTypeJSON = "application/json; charset=utf-8"

// ContentTypeGeoJSON is the media type of Accept: application/geo+json
// responses (RFC 7946 defines no charset parameter).
const ContentTypeGeoJSON = "application/geo+json"

// Handler is the RouteHandler: parse -> Operation -> Executor -> response.
type Handler struct {
	Built *api.Built
	Exec  *exec.Executor
	// Parse carries the server limits and the plugin gates (Built.Surface,
	// set by the advanced_filters / postgis APIHooks).
	Parse    parse.Options
	BasePath string
	Log      *slog.Logger
	// CSVRawFormulas writes CSV cells verbatim; by default a cell that a
	// spreadsheet would evaluate as a formula is escaped (server.csv_escape_formulas).
	CSVRawFormulas bool
}

// ParseOptions derives the parser options from the server section and the
// plugin gates (relation/computed filters and order, spatial) from
// Built.Surface, which the advanced_filters / postgis APIHooks set.
func ParseOptions(built *api.Built, maxEmbedDepth, maxPageSize int, etags, csv bool) parse.Options {
	return parse.Options{
		MaxEmbedDepth:     maxEmbedDepth,
		MaxPageSize:       maxPageSize,
		Etags:             etags,
		CSV:               csv,
		RelationFilters:   built.Surface.RelationFilters,
		ComputedFilters:   built.Surface.ComputedFilters,
		RelationOrder:     built.Surface.RelationOrder,
		ComputedOrder:     built.Surface.ComputedOrder,
		Spatial:           built.Surface.Spatial,
		SpatialFilters:    built.Surface.SpatialFilters,
		SpatialOrder:      built.Surface.SpatialOrder,
		SpatialProjection: built.Surface.SpatialProjection,
		SpatialAggregates: built.Surface.SpatialAggregates,
		NestedKeys:        built.Surface.NestedKeys,
	}
}

// NewHandler builds the route handler.
func NewHandler(built *api.Built, ex *exec.Executor, opts parse.Options, basePath string, log *slog.Logger) *Handler {
	if log == nil {
		log = slog.Default()
	}
	return &Handler{Built: built, Exec: ex, Parse: opts, BasePath: basePath, Log: log}
}

// Operation parses r (already matched to m) into the executor's Operation.
// Errors are *exec.Error.
func (h *Handler) Operation(r *http.Request, m *api.Match) (*exec.Operation, error) {
	p, err := parse.Parse(h.Built, m, r, h.Parse)
	if err != nil {
		return nil, toExecError(err)
	}
	id := IdentityFrom(r.Context())
	return &exec.Operation{
		Method:   r.Method,
		Path:     strings.TrimPrefix(r.URL.EscapedPath(), h.BasePath),
		RawQuery: r.URL.RawQuery,
		Plan:     p,
		Resource: m.Resource,
		RPC:      m.RPC,
		Claims:   id.Claims,
		Role:     id.Role,
		Mutation: isMutation(r.Method, m),
	}, nil
}

func isMutation(method string, m *api.Match) bool {
	if m.RPC != nil {
		return m.RPC.Mutation
	}
	switch method {
	case http.MethodPost, http.MethodPut, http.MethodPatch, http.MethodDelete:
		return true
	}
	return false
}

// ServeRoute implements RouteHandler.
func (h *Handler) ServeRoute(w http.ResponseWriter, r *http.Request, m *api.Match) {
	w.Header().Add("Vary", "Accept")
	if h.rejectReadOnly(w, r, m) {
		return
	}
	op, err := h.Operation(r, m)
	if err != nil {
		h.writeError(w, r, m, err)
		return
	}
	res := h.Exec.Execute(r.Context(), op)
	if res.Err != nil {
		h.writeError(w, r, m, res.Err)
		return
	}
	for k, vs := range res.Headers {
		for _, v := range vs {
			w.Header().Add(k, v)
		}
	}
	status := res.Status
	if status == 0 {
		status = http.StatusOK
	}
	if status == http.StatusNotModified || status == http.StatusNoContent || res.Data == nil {
		w.WriteHeader(status)
		return
	}
	if op.Plan != nil && op.Plan.Accept == plan.AcceptCSV {
		// The parser only negotiates CSV for list shapes without embeds; the
		// SQL is unchanged and the json array is re-encoded on the way out.
		w.Header().Set("Content-Type", ContentTypeCSV)
		w.WriteHeader(status)
		if r.Method == http.MethodHead {
			return
		}
		if err := writeCSV(w, csvColumns(op), res.Data, !h.CSVRawFormulas); err != nil {
			h.Log.Debug("write csv response", "err", err)
		}
		return
	}
	if op.Plan != nil && op.Plan.Accept == plan.AcceptGeoJSON {
		w.Header().Set("Content-Type", ContentTypeGeoJSON)
	} else {
		w.Header().Set("Content-Type", ContentTypeJSON)
	}
	w.WriteHeader(status)
	if r.Method == http.MethodHead {
		return
	}
	if _, err := writeBody(w, res.Data); err != nil {
		h.Log.Debug("write response", "err", err)
	}
}

// CompileSQL resolves r against the route table, parses it and compiles
// the statement without executing it, rendered in the golden format
// (pdbr request --print-sql). Routing and parse failures are *exec.Error.
func (h *Handler) CompileSQL(r *http.Request) (string, error) {
	below := strings.TrimPrefix(r.URL.EscapedPath(), h.BasePath)
	m, ok := h.Built.Match(below)
	if !ok {
		return "", &exec.Error{Status: http.StatusNotFound, Code: CodeNotFound, Detail: "no such route"}
	}
	if !m.Route.Allows(api.Method(r.Method)) {
		return "", &exec.Error{Status: http.StatusMethodNotAllowed, Code: CodeMethodNotAllowed,
			Detail: "method " + r.Method + " is not available on this path", Allow: m.Route.AllowHeader()}
	}
	op, err := h.Operation(r, m)
	if err != nil {
		return "", err
	}
	stmt, err := h.Exec.Prepare(r.Context(), op)
	if err != nil {
		return "", err
	}
	return compile.RenderGolden(r.Method, r.URL.RequestURI(), stmt), nil
}

// writeError renders an *exec.Error (or anything else as 500) as
// problem+json; 405s carry Allow.
func (h *Handler) writeError(w http.ResponseWriter, r *http.Request, m *api.Match, err error) {
	var xe *exec.Error
	if !errors.As(err, &xe) {
		h.Log.Error("request failed", "method", r.Method, "path", r.URL.Path, "err", err)
		xe = &exec.Error{Status: http.StatusInternalServerError, Code: CodeInternal, Detail: "internal error"}
	}
	if xe.Status == http.StatusMethodNotAllowed {
		// Every 405 carries Allow (RFC 9110 §15.5.6); an empty value is the
		// legal way to say "no method is available here right now" (a
		// volatile RPC route under read-only).
		allow := xe.Allow
		if allow == "" && m != nil && m.Route != nil {
			allow = m.Route.AllowHeader()
			if xe.Code == exec.CodeReadOnly {
				allow = readMethods(m.Route)
			}
		}
		w.Header().Set("Allow", allow)
	}
	for k, vs := range xe.Headers {
		for _, v := range vs {
			w.Header().Add(k, v)
		}
	}
	WriteProblemPG(w, r, xe.Status, xe.Code, xe.Detail, xe.PG)
}

func readMethods(rt *api.Route) string {
	var out []string
	for _, m := range rt.Methods {
		if m == api.MethodGet || m == api.MethodHead {
			out = append(out, string(m))
		}
	}
	return strings.Join(out, ", ")
}

// toExecError converts parse errors (compile.UserError) to *exec.Error.
func toExecError(err error) error {
	var xe *exec.Error
	if errors.As(err, &xe) {
		return xe
	}
	var ue *compile.UserError
	if errors.As(err, &ue) {
		status := ue.Status
		if status == 0 {
			status = http.StatusBadRequest
		}
		return &exec.Error{Status: status, Code: ue.Code, Detail: ue.Detail}
	}
	return err
}

// bodyWriter is implemented by response writers that can take over a
// complete body without copying it (the timeout buffer).
type bodyWriter interface {
	WriteBody(b []byte) (int, error)
}

// writeBody writes b, handing it over without a copy when w supports that;
// b must not be modified afterwards. Behind the gzip middleware w is the
// compressor and b is copied as usual.
func writeBody(w http.ResponseWriter, b []byte) (int, error) {
	if bw, ok := w.(bodyWriter); ok {
		return bw.WriteBody(b)
	}
	return w.Write(b)
}
