// Package parse turns a matched HTTP request (method, route match, query
// string, headers) into a fully resolved plan.Request. Every name is
// resolved against api.Built here so the compiler only sees catalog
// references; every failure is a *compile.UserError carrying a stable code
// and status (FuzzParse asserts the parser never panics).
//
// Entry point: Parse(built, match, r, opts). The server calls it with the
// api.Match returned by Built.Match for the request path (the match already
// verified the method is offered on the route); Parse consumes Match.Route
// (Kind, Unique), Match.Key, Match.Cols/Vals, Match.Resource and Match.RPC.
// It reads r.Method, r.URL.RawQuery, the Accept / Prefer / If-Match /
// If-None-Match / Content-Type headers, and the JSON body of RPC POSTs and
// resource writes (POST/PATCH/PUT; body.go). DELETE never reads the body.
//
// Conventions the compiler relies on:
//   - Select == nil means "*"; Node.Star adds every column to explicit
//     Fields/Embeds. Embed.Alias / Node.Alias carry the response key.
//   - Filter is nil, a single Expr, or a top-level And in query-string
//     order; embed filters live on Embed.Filter. Values are typed per
//     plan.Cond's documentation (like/ilike/sw/ew keep the raw operand).
//   - Page.Limit is always resolved (request and every embed).
//   - Key.Values and Page.After/Before hold PK-typed Go values (int64,
//     string, bool, float64, ...), positional to the constraint columns.
//   - Near is the validated near= input when any order term uses .dist
//     (or a distance or raster value pseudo-column is selected): a decoded
//     GeoJSON object or a WKT/EWKT string, parsed per distance column by
//     the compiler so each gets its own SRID.
//   - LatLon is the validated latlon=<lat>,<lon> as {lat, lon} floats when
//     an earthdistance order term, score or threshold needs it.
package parse

import (
	"fmt"
	"net/http"

	"github.com/suprbdev/pdbcore/contrib"
	"github.com/suprbdev/pdbcore/introspect"

	"github.com/suprbdev/pdbr/internal/api"
	"github.com/suprbdev/pdbr/internal/compile"
	"github.com/suprbdev/pdbr/internal/plan"
)

// Error codes emitted by the parser (docs/errors.md).
const (
	CodeInvalidFilter        = "invalid_filter"
	CodeInvalidRange         = "invalid_range"
	CodeInvalidSelect        = "invalid_select"
	CodeUnknownColumn        = "unknown_column"
	CodeUnknownRelation      = "unknown_relation"
	CodeColumnNotFilterable  = "column_not_filterable"
	CodeColumnNotOrderable   = "column_not_orderable"
	CodePageSizeExceeded     = "page_size_exceeded"
	CodeKeysetUnsupported    = "keyset_unsupported"
	CodeEmbedDepthExceeded   = "embed_depth_exceeded"
	CodeAmbiguousRelation    = "ambiguous_relation"
	CodeInvalidCursor        = "invalid_cursor"
	CodeInvalidBody          = "invalid_body"
	CodeUnknownArgument      = "unknown_argument"
	CodeNotAcceptable        = "not_acceptable"
	CodeUnsupportedMediaType = "unsupported_media_type"
	CodeNotFound             = "not_found"
	CodeBodyTooLarge         = "body_too_large"
	CodeMethodNotAllowed     = "method_not_allowed"
	CodeInvalidPreference    = "invalid_preference"
	CodeUnfilteredMutation   = "unfiltered_mutation"
	CodeIfMatchUnsupported   = "if_match_unsupported"
	CodeTemporalConflict     = compile.CodeTemporalConflict
	CodeAggregatesDisabled   = "aggregates_disabled"
)

// Options tune the parser; zero values fall back to the documented defaults
// (max_embed_depth 5, max_page_size 100) so a zero Options is usable.
type Options struct {
	// MaxEmbedDepth is server.max_embed_depth (embed nesting limit).
	MaxEmbedDepth int
	// MaxPageSize is server.max_page_size: default and cap for limit and
	// <rel>.limit.
	MaxPageSize int
	// Etags is server.etags; when false If-Match / If-None-Match are ignored.
	Etags bool
	// CSV is server.csv; when false Accept: text/csv is never satisfiable.
	CSV bool
	// Aggregates is server.aggregates: aggregate functions in select=
	// (count(), col.sum(), ...). When false any aggregate is 400
	// aggregates_disabled.
	Aggregates bool
	// SpatialAggregates enables the spatial aggregate functions
	// (col.extent(), col.union(), col.collect(), col.centroid()) on
	// geometry and geography columns (postgis plugin); they also need
	// Aggregates.
	SpatialAggregates bool
	// AllowUnfilteredMutations is server.allow_unfiltered_mutations: when
	// false a PATCH or DELETE on a list route without a root filter is 400
	// unfiltered_mutation.
	AllowUnfilteredMutations bool
	// PartialBulk is server.partial_bulk_writes: whether Prefer: partial=items
	// is honoured on bulk POST and PUT.
	PartialBulk bool
	// PartialBulkMaxItems is server.partial_bulk_max_items: the most
	// elements a Prefer: partial=items body may carry (0 means no limit).
	PartialBulkMaxItems int
	// TxEnd is server.tx_end: commit (default, "" too), commit-allow-override,
	// rollback or rollback-allow-override. It decides whether a write's
	// transaction is rolled back and whether Prefer: tx= is honoured.
	TxEnd string
	// RelationFilters enables rel.col=, rel!none., rel!every. and
	// rel=exists. (advanced-filters plugin).
	RelationFilters bool
	// ComputedFilters enables filtering on computed columns.
	ComputedFilters bool
	// RelationOrder enables order=rel(col).
	RelationOrder bool
	// ComputedOrder enables order=computed.
	ComputedOrder bool
	// Spatial enables spatial filter ops, .dist ordering with near=, and
	// col(transform=,simplify=,buffer=,precision=) projection args (postgis plugin) all at
	// once; SpatialFilters / SpatialOrder / SpatialProjection enable them
	// one at a time (the plugin's filters / ordering / projection settings).
	// Pseudo-columns need no gate: they exist only when the plugin
	// registered them on the resource.
	Spatial           bool
	SpatialFilters    bool
	SpatialOrder      bool
	SpatialProjection bool
	// NestedKeys, when set, names the body keys of a resource that the
	// nested-mutations plugin accepts (relation key -> relation). Such keys
	// in a single-object POST body land in plan.Body.Nested with their raw
	// decoded JSON value instead of being rejected as unknown columns; the
	// FK columns of a forward relation stop being required when the key is
	// present. Nil: every non-column key is invalid_body.
	NestedKeys func(res *api.Resource) map[string]*api.Relation
}

const (
	defaultMaxEmbedDepth = 5
	defaultMaxPageSize   = 100
)

func (o Options) maxEmbedDepth() int {
	if o.MaxEmbedDepth <= 0 {
		return defaultMaxEmbedDepth
	}
	return o.MaxEmbedDepth
}

func (o Options) spatialFilters() bool    { return o.Spatial || o.SpatialFilters }
func (o Options) spatialOrder() bool      { return o.Spatial || o.SpatialOrder }
func (o Options) spatialProjection() bool { return o.Spatial || o.SpatialProjection }

func (o Options) maxPageSize() int {
	if o.MaxPageSize <= 0 {
		return defaultMaxPageSize
	}
	return o.MaxPageSize
}

// errf builds a UserError; the status follows the code.
func errf(code, format string, args ...any) *compile.UserError {
	status := http.StatusBadRequest
	switch code {
	case CodeNotAcceptable:
		status = http.StatusNotAcceptable
	case CodeUnsupportedMediaType:
		status = http.StatusUnsupportedMediaType
	case CodeNotFound:
		status = http.StatusNotFound
	case CodeBodyTooLarge:
		status = http.StatusRequestEntityTooLarge
	case CodeMethodNotAllowed:
		status = http.StatusMethodNotAllowed
	}
	return &compile.UserError{Code: code, Status: status, Detail: fmt.Sprintf(format, args...)}
}

// Parse resolves the request into a plan. m must come from built.Match for
// r's path (its Route decides list / row / unique / rpc handling); r is only
// read (query string, headers, and the body for RPC POST).
func Parse(built *api.Built, m *api.Match, r *http.Request, opts Options) (*plan.Request, error) {
	if built == nil || m == nil || m.Route == nil || r == nil {
		return nil, errf(CodeNotFound, "no such route")
	}
	method, err := parseMethod(r.Method)
	if err != nil {
		return nil, err
	}
	p := &parser{built: built, opts: opts, match: m, method: method}
	req := &plan.Request{Method: method, Resource: m.Resource, RPC: m.RPC}
	p.req = req

	if err := p.checkContentType(r); err != nil {
		return nil, err
	}
	if err := p.parseHeaders(r); err != nil {
		return nil, err
	}

	if m.RPC != nil {
		if err := p.parseRPC(r); err != nil {
			return nil, err
		}
	} else {
		if err := p.parseKey(); err != nil {
			return nil, err
		}
		if err := p.parseQuery(r.URL.RawQuery, m.Resource); err != nil {
			return nil, err
		}
		if err := p.parseBody(r); err != nil {
			return nil, err
		}
		if p.isBulkWrite() {
			if req.Filter == nil && !hasInnerEmbed(req.Select) && !opts.AllowUnfilteredMutations {
				return nil, errf(CodeUnfilteredMutation, "%s on %q without a filter would affect every row; add a filter (or enable server.allow_unfiltered_mutations)", method, resName(m.Resource))
			}
			if req.IfMatch != "" {
				return nil, errf(CodeIfMatchUnsupported, "If-Match applies to single-row paths only; %s on the collection path cannot honour it", method)
			}
		}
		if req.IfMatch != "" && req.Body != nil && len(req.Body.Nested) > 0 {
			return nil, errf(CodeIfMatchUnsupported, "If-Match cannot be combined with nested-mutation keys; send the nested writes in a separate request")
		}
		if method == plan.PUT && req.Key != nil && req.IfMatch != "" {
			return nil, errf(CodeIfMatchUnsupported, "If-Match is not supported on PUT; use PATCH on this path for a conditional update")
		}
	}
	if err := p.resolvePartial(); err != nil {
		return nil, err
	}
	p.resolveCount()
	if err := p.negotiateAccept(r); err != nil {
		return nil, err
	}
	return req, nil
}

type parser struct {
	built  *api.Built
	opts   Options
	match  *api.Match
	method plan.Method
	req    *plan.Request
	// root is the node dotted parameters are resolved against (the select
	// tree, or a bare node over the resource when select is absent).
	root *plan.Node
	// rangeHdr is the Range request header of a GET/HEAD (readRange).
	rangeHdr string
	// columns is the columns= allowlist of a write (nil = every body key);
	// columnsOrder keeps the names as written for validation messages.
	columns      map[string]bool
	columnsOrder []string
	// wantPartial is a honourable Prefer: partial=items; resolvePartial
	// confirms it once the body is known to be an array.
	wantPartial bool
	// scores are the score pseudo-columns selected anywhere in the request
	// (root, embeds, spreads), and vecFilters the vector distance
	// thresholds filtered on: both measure against a reference parameter
	// bound once the whole query is parsed.
	scores     []*api.PseudoColumn
	vecFilters []vecFilter
	// levFilters are the levenshtein_lt thresholds, checked against
	// levenshtein=, and latlonFilters the earth_lt and earth_miles_lt
	// thresholds, against latlon=.
	levFilters    []vecFilter
	latlonFilters []vecFilter
	// headlines are the headline pseudo-columns selected anywhere in the
	// request, bound to the full-text filter of their scope once the whole
	// query is parsed.
	headlines []headlineUse
}

// headlineUse is a selected headline pseudo-column and the projection node
// it was selected in, whose filters it highlights against.
type headlineUse struct {
	node *plan.Node
	pc   *api.PseudoColumn
	h    *plan.Headline
}

// vecFilter is a distance threshold (vec_cos_lt, levenshtein_lt, ...) on
// Column, checked against its reference parameter once it is parsed.
type vecFilter struct {
	Column *introspect.Column
	Op     contrib.Op
	Name   string
}

func parseMethod(m string) (plan.Method, error) {
	switch m {
	case http.MethodGet:
		return plan.GET, nil
	case http.MethodHead:
		return plan.HEAD, nil
	case http.MethodPost:
		return plan.POST, nil
	case http.MethodPatch:
		return plan.PATCH, nil
	case http.MethodPut:
		return plan.PUT, nil
	case http.MethodDelete:
		return plan.DELETE, nil
	}
	return "", errf(CodeMethodNotAllowed, "method %s is not supported", m)
}

// hasInnerEmbed reports a root-level !inner embed, which restricts the root
// rows like a filter does.
func hasInnerEmbed(n *plan.Node) bool {
	if n == nil {
		return false
	}
	for i := range n.Embeds {
		if n.Embeds[i].Inner {
			return true
		}
	}
	return false
}

// isList reports whether the request addresses a list of rows (list route
// or a set-returning RPC).
func (p *parser) isList() bool {
	switch p.match.Route.Kind {
	case api.RouteList:
		return true
	case api.RouteRPC:
		switch p.match.RPC.Returns {
		case api.ReturnSetOfRows, api.ReturnSetOfScalar, api.ReturnSetOfComposite:
			return true
		}
	}
	return false
}
