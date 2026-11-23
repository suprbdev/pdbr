// Package plan holds the transport-neutral request plan: what a parsed HTTP
// request asks for, with every name already resolved against api.Built.
// The compiler consumes only this; it never sees an *http.Request. The
// package is pure data and imports nothing beyond pdbcore/introspect and
// pdbr/internal/api.
package plan

import (
	"github.com/suprbdev/pdbcore/introspect"

	"github.com/suprbdev/pdbr/internal/api"
)

// Method is the request method (OPTIONS never reaches the plan).
type Method string

const (
	GET    Method = "GET"
	HEAD   Method = "HEAD"
	POST   Method = "POST"
	PATCH  Method = "PATCH"
	PUT    Method = "PUT"
	DELETE Method = "DELETE"
)

// Request is one HTTP request, fully resolved.
type Request struct {
	Method   Method
	Resource *api.Resource // nil for RPC
	RPC      *api.RPC      // nil for resources
	// Key identifies a single row: primary key values (pk path) or a unique
	// constraint + values (/by/{cols}/{vals}). Nil for list paths.
	Key *Key
	// Body is the parsed and validated JSON body: one row (Object) or a bulk
	// list (Objects) of column->value maps already coerced by pg type; for
	// RPC the arg map. Nested-mutation subtrees ride along as Nested.
	Body *Body
	// Select is the projection tree (nil = "*", all columns).
	Select *Node
	Filter Expr // nil = no filter
	Order  []OrderTerm
	Page   Page
	// Count asks for the total row count of a list (count=true or Prefer:
	// count=); CountMode says how it is computed.
	Count     bool
	CountMode CountMode
	// Distinct lists DISTINCT ON columns in request order (nil = none).
	Distinct []*introspect.Column
	// OnConflict is the upsert target (nil = primary key).
	OnConflict *introspect.Constraint
	// Near is the reference geometry for distance ordering (postgis plugin).
	Near any
	// Trgm is the reference text (a string) for trigram distance ordering
	// (trgm=; nil when absent).
	Trgm        any
	IfMatch     string // raw ETag from If-Match ("" = absent)
	IfNoneMatch string
	Prefer      Prefer
	Accept      Accept
}

// Key is a single-row selector.
type Key struct {
	Constraint *introspect.Constraint // the PK or unique constraint matched
	Values     []any                  // coerced, positional to Constraint.Columns
}

// Body is a parsed request body.
type Body struct {
	Object  map[string]any   // single row / RPC args
	Objects []map[string]any // bulk insert / bulk upsert
	// Nested holds nested-mutation subtrees keyed by relation key, populated
	// only when the nested-mutations plugin is enabled.
	Nested map[string]*Nested
	// Columns is the union of body keys in catalog column order (bulk).
	Columns []*introspect.Column
}

// Nested is a nested-mutation subtree (nested-mutations plugin).
type Nested struct {
	Relation *api.Relation
	// Create holds rows to insert (one for forward relations, many for
	// backward); Connect holds the referenced key columns of an existing row.
	Create  []map[string]any
	Connect map[string]any
	// Children are deeper subtrees keyed by relation key.
	Children map[string]*Nested
	// Raw is the decoded JSON value of the body key as the parser saw it
	// (parse.Options.NestedKeys); the nested-mutations plugin resolves it
	// into Create/Connect/Children.
	Raw any
}

// Node is one level of the select= projection tree.
type Node struct {
	Resource *api.Resource
	Alias    string // response key for this node ("" at root)
	Fields   []FieldSel
	Embeds   []Embed
	Star     bool // "*" present: all columns of Resource, plus explicit Fields/Embeds
}

// FieldSel is one selected field.
type FieldSel struct {
	Alias    string             // response key ("" = key of the selected item)
	Column   *introspect.Column // real column
	Computed *api.Computed      // computed column (row-type function)
	Pseudo   *api.PseudoColumn  // postgis derived accessor (<col>_<accessor>)
	// Args are spatial projection args: "transform" (int SRID) and/or
	// "simplify" (float64 tolerance). Nil when absent.
	Args map[string]any
	// Node is the nested select= of a SETOF <table> computed column
	// (Computed.SetOf != nil); nil means "*".
	Node *Node
}

// Embed is one embedded relation.
type Embed struct {
	Key      string        // relation key as written
	Alias    string        // response key ("" = Key)
	Relation *api.Relation // resolved (direction, FK, hint applied)
	Inner    bool          // !inner
	Node     *Node
	Filter   Expr        // per-embed filters (posts.title=eq.x)
	Order    []OrderTerm // posts.order=
	Page     Page        // posts.limit= / posts.offset=
}

// Expr is a filter tree node: exactly one of And, Or, Not, Cond.
type Expr interface{ isExpr() }

type And struct{ Terms []Expr }
type Or struct{ Terms []Expr }
type Not struct{ Term Expr }

// Cond is a leaf predicate.
//
// Value is coerced by the parser per column type and operator:
//   - eq/neq/gt/gte/lt/lte: int64 (int2/int4/int8/oid), float64 (float4/8),
//     bool, string (text-like, uuid, numeric/money, timestamps, intervals,
//     everything else, enum pg label, JSON text for json/jsonb columns),
//     []any of element values for array columns.
//   - in: []any of element values (never nil; may be empty).
//   - is: nil for is.null, bool for is.true / is.false.
//   - like/ilike/sw/ew: the raw string operand (the compiler translates *
//     to % for like/ilike and escapes sw/ew).
//   - cs/cd/ov: []any for array columns, JSON text (string) for jsonb.
//   - hk: string key; jpe/jpm: the jsonpath string.
//   - spatial ops: a postgis.Value (st_* and geometry-operand bbox_* ops),
//     a DWithin, or a BBox.
//   - isdistinct: as eq.
//   - contrib ops (api.ContribFilterFor resolves the pdbcore/contrib
//     operator): contrib.FullTextQuery for fts/plfts/phfts/wfts; a string
//     for single-text operands (trgm_*, hk, ltree paths and queries, inet
//     addresses, range elements); []string for list operands (hk_all,
//     hk_any, *_any); map[string]any (decoded JSON object) or a range
//     literal string for range operands; map[string]any or an hstore
//     literal string for hstore cs/cd.
type Cond struct {
	Column   *introspect.Column // real column of the current resource
	Computed *api.Computed      // computed-column filter (advanced-filters)
	Relation *RelationPath      // relation filter (advanced-filters)
	Op       Op                 // eq, neq, ..., st_intersects, exists
	Value    any                // coerced; nil for structural ops (is.null ...)
	Negate   bool               // leading not.
}

// DWithin is the operand of st_dwithin / st_beyond.
type DWithin struct {
	Geom     any // postgis.Value
	Distance float64
	// Spheroid is the optional third argument (geography only).
	Spheroid *bool
}

// BBox is the operand of bbox_intersects / bbox_contains / bbox_containedby.
// The other bbox_* operators take a geometry (postgis.Value) and compare
// against its bounding box.
type BBox struct {
	MinX, MinY, MaxX, MaxY float64
	SRID                   int // 0 = unspecified
}

func (And) isExpr()  {}
func (Or) isExpr()   {}
func (Not) isExpr()  {}
func (Cond) isExpr() {}

// Op is a filter operator name as written in the URL (after not.).
type Op string

const (
	OpEq     Op = "eq"
	OpNeq    Op = "neq"
	OpGt     Op = "gt"
	OpGte    Op = "gte"
	OpLt     Op = "lt"
	OpLte    Op = "lte"
	OpIn     Op = "in"
	OpIs     Op = "is"
	OpLike   Op = "like"
	OpILike  Op = "ilike"
	OpSw     Op = "sw"
	OpEw     Op = "ew"
	OpCs     Op = "cs"
	OpCd     Op = "cd"
	OpOv     Op = "ov"
	OpHk     Op = "hk"
	OpJpe    Op = "jpe"
	OpJpm    Op = "jpm"
	OpExists Op = "exists"

	OpIsDistinct Op = "isdistinct"
	OpFts        Op = "fts"
	OpPlFts      Op = "plfts"
	OpPhFts      Op = "phfts"
	OpWFts       Op = "wfts"
	OpSl         Op = "sl"
	OpSr         Op = "sr"
	OpNxr        Op = "nxr"
	OpNxl        Op = "nxl"
	OpAdj        Op = "adj"

	OpStIntersects       Op = "st_intersects"
	OpStDisjoint         Op = "st_disjoint"
	OpStContains         Op = "st_contains"
	OpStContainsProperly Op = "st_containsproperly"
	OpStWithin           Op = "st_within"
	OpStCovers           Op = "st_covers"
	OpStCoveredBy        Op = "st_coveredby"
	OpStCrosses          Op = "st_crosses"
	OpStOverlaps         Op = "st_overlaps"
	OpStTouches          Op = "st_touches"
	OpStEquals           Op = "st_equals"
	OpStDWithin          Op = "st_dwithin"
	OpStBeyond           Op = "st_beyond"
	OpBBoxIntersects     Op = "bbox_intersects"
	OpBBoxContains       Op = "bbox_contains"
	OpBBoxContainedBy    Op = "bbox_containedby"

	OpBBoxLeftOf            Op = "bbox_leftof"
	OpBBoxRightOf           Op = "bbox_rightof"
	OpBBoxBelow             Op = "bbox_below"
	OpBBoxAbove             Op = "bbox_above"
	OpBBoxOverlapsOrLeftOf  Op = "bbox_overlapsorleftof"
	OpBBoxOverlapsOrRightOf Op = "bbox_overlapsorrightof"
	OpBBoxOverlapsOrBelow   Op = "bbox_overlapsorbelow"
	OpBBoxOverlapsOrAbove   Op = "bbox_overlapsorabove"
	OpBBoxEquals            Op = "bbox_equals"
	OpStExactlyEquals       Op = "st_exactlyequals"
	OpStOrderingEquals      Op = "st_orderingequals"
	OpStIntersects3D        Op = "st_intersects3d"
	OpBBoxIntersectsND      Op = "bbox_intersectsnd"
)

// SpatialOps maps the URL spelling of every spatial operator to the
// pdbcore/postgis Op name (postgis.OpByName). The URL spelling is the
// pdbcore name lower-cased with a "bbox_" prefix for the bounding-box
// operators (bboxLeftOf -> bbox_leftof) and "st_" for the rest
// (exactlyEquals -> st_exactlyequals).
var SpatialOps = map[Op]string{
	OpStIntersects:       "intersects",
	OpStDisjoint:         "disjoint",
	OpStContains:         "contains",
	OpStContainsProperly: "containsProperly",
	OpStWithin:           "within",
	OpStCovers:           "covers",
	OpStCoveredBy:        "coveredBy",
	OpStCrosses:          "crosses",
	OpStOverlaps:         "overlaps",
	OpStTouches:          "touches",
	OpStEquals:           "equals",
	OpStDWithin:          "dwithin",
	OpStBeyond:           "beyond",
	OpBBoxIntersects:     "bboxIntersects",
	OpBBoxContains:       "bboxContains",
	OpBBoxContainedBy:    "bboxContainedBy",

	OpBBoxLeftOf:            "bboxLeftOf",
	OpBBoxRightOf:           "bboxRightOf",
	OpBBoxBelow:             "bboxBelow",
	OpBBoxAbove:             "bboxAbove",
	OpBBoxOverlapsOrLeftOf:  "bboxOverlapsOrLeftOf",
	OpBBoxOverlapsOrRightOf: "bboxOverlapsOrRightOf",
	OpBBoxOverlapsOrBelow:   "bboxOverlapsOrBelow",
	OpBBoxOverlapsOrAbove:   "bboxOverlapsOrAbove",
	OpBBoxEquals:            "bboxEquals",
	OpStExactlyEquals:       "exactlyEquals",
	OpStOrderingEquals:      "orderingEquals",
	OpStIntersects3D:        "intersects3D",
	OpBBoxIntersectsND:      "bboxIntersectsND",
}

// Quantifier selects how a relation filter applies over related rows.
type Quantifier string

const (
	Some   Quantifier = "some"
	None   Quantifier = "none"
	Every  Quantifier = "every"
	Exists Quantifier = "exists"
)

// RelationPath is a relation filter (advanced-filters plugin). The parser
// sets the owning Cond's Op to Leaf.Op (or OpExists) and never sets its
// Negate: a leading not. on the column condition lives on Leaf.Negate, and
// not.exists.true is folded into *Exists. Several Conds sharing Steps[0]
// and Quantifier may be ANDed inside one EXISTS by the compiler.
type RelationPath struct {
	Steps      []*api.Relation // one step in v1 (rel.col); grammar allows more
	Quantifier Quantifier      // some (default) | none | every | exists
	Leaf       *Cond           // nil for exists.true/false
	Exists     *bool           // for exists.true|false
	// Filter is the full predicate over the related table (several
	// conditions on the same key/quantifier ANDed inside one EXISTS); when
	// set it takes precedence over Leaf.
	Filter Expr
}

// OrderTerm is one order= term.
type OrderTerm struct {
	Column     *introspect.Column
	Computed   *api.Computed
	Relation   *RelationOrder     // users(name) — forward: scalar subquery, backward: MIN/MAX
	Distance   *introspect.Column // spatial column for `.dist` (uses Request.Near)
	Contrib    *ContribOrder      // .rank / .rank_cd / .trgm_dist ... (pdbcore/contrib)
	Desc       bool
	NullsFirst bool // both directions default to NULLS LAST
}

// ContribOrder orders by a pdbcore/contrib value expression over Column:
// full-text relevance against Filter (Rank set) or a trigram distance to
// Request.Trgm (Expr set).
type ContribOrder struct {
	Column *introspect.Column
	// Name is the order modifier (rank, rank_cd, trgm_dist, ...).
	Name string
	// Rank is "ts_rank" or "ts_rank_cd" for a relevance term.
	Rank string
	// Filter is the full-text condition on Column whose query a relevance
	// term ranks against (same operator, query and config).
	Filter *Cond
	// Expr names the trigram distance expression (distance, wordDistance,
	// strictWordDistance) of a distance term.
	Expr string
}

// RelationOrder orders by a related table's column.
type RelationOrder struct {
	Relation *api.Relation
	Column   *introspect.Column
}

// Page is the resolved paging. The parser always sets Limit (explicit or
// server.max_page_size), on the request and on every Embed (forward embeds
// carry the default too; the compiler ignores it there).
type Page struct {
	Limit  int // always resolved: explicit or server.max_page_size
	Offset int
	After  []any // decoded cursor pk values (nil = absent)
	Before []any
	// LimitSet reports that limit was given explicitly (cost estimation:
	// an unset backward-embed limit is estimated at 10, not the cap).
	LimitSet bool
}

// Prefer is the parsed Prefer header (RFC 7240, PostgREST vocabulary).
type Prefer struct {
	// Return is "representation" (default; "" means the same), "minimal" or
	// "headers-only" (an alias of minimal: pdbr's minimal already keeps
	// Location).
	Return string
	// Strict reports handling=strict: unknown tokens and invalid values are
	// a 400 instead of being ignored, and max-affected is honoured.
	Strict bool
	// MaxAffected is the max-affected=N cap on the rows a PATCH or DELETE
	// may touch; nil when absent, ignored (lenient handling) or not
	// applicable to the request.
	MaxAffected *int64
	// Count is the requested count= mode ("" = none) as written; the
	// effective mode is Request.CountMode.
	Count CountMode
	// Resolution is the resolution= of a POST: "merge-duplicates" (upsert)
	// or "ignore-duplicates" (ON CONFLICT DO NOTHING); "" = plain insert.
	Resolution string
	// MissingDefault reports missing=default (pdbr always inserts DEFAULT
	// for keys absent from a bulk element, so it changes nothing).
	MissingDefault bool
	// Applied lists the honoured preferences in canonical token=value form,
	// in header order, for the Preference-Applied response header.
	Applied []string
}

// Resolution values of Prefer.Resolution.
const (
	MergeDuplicates  = "merge-duplicates"
	IgnoreDuplicates = "ignore-duplicates"
)

// Minimal reports return=minimal or return=headers-only.
func (p Prefer) Minimal() bool { return p.Return == "minimal" || p.Return == "headers-only" }

// CountMode is how a list total is computed (Prefer: count=). The parser
// resolves it: a mode the request cannot honour falls back to CountExact.
type CountMode string

const (
	// CountExact is a count(*) over the filtered scan ("" means the same).
	CountExact CountMode = "exact"
	// CountPlanned is the table's pg_class.reltuples statistic.
	CountPlanned CountMode = "planned"
	// CountEstimated is exact up to server.max_page_size rows, planned above.
	CountEstimated CountMode = "estimated"
)

// Accept is the negotiated response format.
type Accept string

const (
	AcceptJSON Accept = "json"
	AcceptCSV  Accept = "csv"
)
