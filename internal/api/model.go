// Package api turns an introspected catalog into pdbr's resource model: the
// resources, relations, computed columns, RPCs, enums and composites the
// server routes to, the lookup tables the parser and compiler consult, the
// route table, and the OpenAPI document. Build is a pure function of catalog
// + options + hooks; everything here is deterministic.
package api

import (
	"github.com/suprbdev/pdbcore/introspect"
)

// Method is an HTTP method a resource or RPC route accepts.
type Method string

const (
	MethodGet    Method = "GET"
	MethodHead   Method = "HEAD"
	MethodPost   Method = "POST"
	MethodPut    Method = "PUT"
	MethodPatch  Method = "PATCH"
	MethodDelete Method = "DELETE"
)

// methodOrder is the canonical order for Allow headers and route tables.
var methodOrder = []Method{MethodGet, MethodHead, MethodPost, MethodPut, MethodPatch, MethodDelete}

// Resource is one exposed table, view or materialized view.
type Resource struct {
	Table *introspect.Table
	// Segment is the inflected path segment (resource_path kind).
	Segment string
	// Path is the resource key: Segment for a bare-aliased resource, or
	// "<schema>.<segment>" — also the OpenAPI schema name.
	Path string
	// Bare reports whether the resource answers on the bare alias /{segment}
	// (schemas[0] only, unless suppressed by a reserved-segment collision).
	Bare bool
	// Methods are the resource-level methods after privilege gating (and
	// plugin edits): GET/HEAD read, POST create, PUT upsert, PATCH update,
	// DELETE delete. Row/unique routes offer PATCH/DELETE of one row; the
	// list route POST/PUT and, while the collection is listable (GET, no
	// OmitList), the filtered bulk PATCH/DELETE.
	Methods map[Method]bool
	// Columns are the exposed columns in catalog order.
	Columns []*Field
	// Computed maps key -> row-type function.
	Computed map[string]*Computed
	// Pseudo maps key -> derived pseudo-column (postgis plugin).
	Pseudo map[string]*PseudoColumn
	// Relations maps relation key -> candidates; more than one candidate
	// means a !hint is required.
	Relations map[string][]*Relation
	// Uniques are the unique constraints backing /by/{cols}/{vals} lookups.
	Uniques []*introspect.Constraint
	// FilterColumns / OrderColumns hold the policy-resolved keys.
	FilterColumns map[string]bool
	OrderColumns  map[string]bool
	// InputColumns maps JSON key -> column for insert/update bodies
	// (generated/identity columns are excluded).
	InputColumns map[string]*introspect.Column
	// Keyset reports whether the resource has a primary key (row path,
	// cursors, Location header).
	Keyset bool
	// Etag reports whether single-row GETs carry an ETag.
	Etag bool
	// CostMultipliers maps relation key -> @costMultiplier.
	CostMultipliers map[string]int
	// OmitList suppresses GET/HEAD on the collection route only (@omit all);
	// row and unique routes keep their read methods.
	OmitList bool
	// NoCreate / NoUpdate list body keys rejected on POST / PATCH bodies
	// (@omit create / @omit update on a column); PUT honours both. See
	// InputColumnsFor.
	NoCreate map[string]bool
	NoUpdate map[string]bool

	Description       string
	Deprecated        bool
	DeprecationReason string
}

// Field is an exposed column.
type Field struct {
	Column *introspect.Column
	// Key is the JSON key / query-parameter name (field_name kind).
	Key string
	// ReadOnly marks generated/identity columns (never in bodies).
	ReadOnly          bool
	Description       string
	Deprecated        bool
	DeprecationReason string
}

// Relation is one FK direction between two resources.
type Relation struct {
	Key     string
	Forward bool
	FK      *introspect.ForeignKey
	// From is the resource carrying the key; To is the embedded resource.
	From, To *Resource
	// Hints are the tokens that select this candidate when the key is
	// ambiguous: the FK constraint name and the FK's referencing columns.
	Hints []string
	// CostMultiplier is the @costMultiplier on the FK (0 = default).
	CostMultiplier int
	Filterable     bool
	Orderable      bool
	Embeddable     bool
}

// Computed is a stable/immutable function whose first argument is the
// resource's row type, exposed as a select=-only field.
type Computed struct {
	Key      string
	Function *introspect.Function
	Resource *Resource
	// SetOf is the resource returned by SETOF <table> computed columns.
	SetOf *Resource
	// Filterable / Orderable admit the column to filters / order= (advanced-
	// filters plugin, gated by Surface.ComputedFilters / ComputedOrder);
	// set-returning columns are never either.
	Filterable        bool
	Orderable         bool
	Description       string
	Deprecated        bool
	DeprecationReason string
}

// PseudoColumn is a derived accessor over a real column (postgis plugin:
// <col>_area, <col>_centroid, ...).
type PseudoColumn struct {
	Key      string
	Column   *introspect.Column
	Accessor string
	// Schema is the OpenAPI schema of the value (nil renders as {}).
	Schema      *Schema
	Description string
}

// ReturnKind classifies an RPC's result shape.
type ReturnKind string

const (
	ReturnScalar         ReturnKind = "scalar"
	ReturnVoid           ReturnKind = "void"
	ReturnRow            ReturnKind = "row"
	ReturnSetOfRows      ReturnKind = "setof_rows"
	ReturnComposite      ReturnKind = "composite"
	ReturnSetOfComposite ReturnKind = "setof_composite"
	ReturnSetOfScalar    ReturnKind = "setof_scalar"
)

// RPC is one exposed function.
type RPC struct {
	Function *introspect.Function
	Segment  string
	// Path is the RPC key: Segment (bare) or "<schema>.<segment>".
	Path string
	Bare bool
	// Methods: GET/HEAD for stable/immutable, POST for every function.
	Methods map[Method]bool
	// Mutation marks volatile functions (transaction, read-only gate).
	Mutation bool
	// Mint marks functions returning the rls.auth.jwt_type composite: the
	// exec layer signs each returned row into a JWT string.
	Mint    bool
	Returns ReturnKind
	// Resource is set for row / SETOF <table> returns, and for functions
	// returning record rows described by output parameters (OUT/INOUT
	// parameters or RETURNS TABLE), where it is a route-less resource over
	// Function.ReturnColumns and Record is set.
	Resource *Resource
	Record   bool
	// Composite is set for composite returns.
	Composite         *Composite
	Args              []introspect.FuncArg
	Description       string
	Deprecated        bool
	DeprecationReason string
}

// Enum is an exposed enum type (native or table-backed).
type Enum struct {
	// Name is the OpenAPI schema name (name, or "<schema>.<name>").
	Name string
	Enum *introspect.Enum
	// Values are the client-facing values (enum_value kind), parallel to
	// Enum.Values.
	Values      []string
	Description string
}

// Composite is an exposed composite type.
type Composite struct {
	Name      string
	Composite *introspect.Composite
}

// RouteKind is the shape of a route.
type RouteKind string

const (
	RouteList   RouteKind = "list"   // /{r}
	RouteRow    RouteKind = "row"    // /{r}/{pk}
	RouteUnique RouteKind = "unique" // /{r}/by/{cols}/{vals}
	RouteRPC    RouteKind = "rpc"    // /rpc/{fn}
)

// Route is one entry of the router table. Pattern is the documented form
// (bare alias when the owner is bare, canonical otherwise); the runtime
// answers both forms via Built.Match.
type Route struct {
	Kind     RouteKind
	Pattern  string
	Methods  []Method
	Resource *Resource
	RPC      *RPC
	// Unique is the constraint behind a RouteUnique route.
	Unique *introspect.Constraint
}

// Allows reports whether the route accepts m.
func (r *Route) Allows(m Method) bool {
	for _, x := range r.Methods {
		if x == m {
			return true
		}
	}
	return false
}

// AllowHeader renders the Allow header value.
func (r *Route) AllowHeader() string {
	out := ""
	for i, m := range r.Methods {
		if i > 0 {
			out += ", "
		}
		out += string(m)
	}
	return out
}

// Field returns the field with the given JSON key, or nil.
func (r *Resource) Field(key string) *Field {
	for _, f := range r.Columns {
		if f.Key == key {
			return f
		}
	}
	return nil
}

// FieldByColumn returns the field backed by the named pg column, or nil.
func (r *Resource) FieldByColumn(name string) *Field {
	for _, f := range r.Columns {
		if f.Column.Name == name {
			return f
		}
	}
	return nil
}

// InputColumnsFor returns the body keys a write method accepts:
// InputColumns minus the per-method exclusions (NoCreate for POST, NoUpdate
// for PATCH, both for PUT). The map is InputColumns itself when nothing is
// excluded.
func (r *Resource) InputColumnsFor(m Method) map[string]*introspect.Column {
	var excl []map[string]bool
	switch m {
	case MethodPost:
		excl = []map[string]bool{r.NoCreate}
	case MethodPatch:
		excl = []map[string]bool{r.NoUpdate}
	case MethodPut:
		excl = []map[string]bool{r.NoCreate, r.NoUpdate}
	}
	drop := false
	for _, e := range excl {
		if len(e) > 0 {
			drop = true
		}
	}
	if !drop {
		return r.InputColumns
	}
	out := make(map[string]*introspect.Column, len(r.InputColumns))
	for k, c := range r.InputColumns {
		out[k] = c
	}
	for _, e := range excl {
		for k := range e {
			delete(out, k)
		}
	}
	return out
}

// Resolve picks the relation for key, applying hint when given. It returns
// the candidates when the key is ambiguous and no hint selects exactly one
// (the caller renders 400 ambiguous_relation), or nil, nil when the key is
// unknown.
func (r *Resource) Resolve(key, hint string) (rel *Relation, candidates []*Relation) {
	cands := r.Relations[key]
	if len(cands) == 0 {
		return nil, nil
	}
	if hint == "" {
		if len(cands) == 1 {
			return cands[0], nil
		}
		return nil, cands
	}
	var hit []*Relation
	for _, c := range cands {
		for _, h := range c.Hints {
			if h == hint {
				hit = append(hit, c)
				break
			}
		}
	}
	if len(hit) == 1 {
		return hit[0], nil
	}
	// A self-referencing FK yields one candidate per direction with identical
	// hints. Convention (docs/api.md): a column hint names the forward side
	// (the FK column is on this row), the constraint name names the backward
	// side (comments!parent_id = parent, comments!comments_parent_id_fkey =
	// replies).
	if len(hit) == 2 && hit[0].FK == hit[1].FK && hit[0].Forward != hit[1].Forward {
		wantForward := hint != hit[0].FK.Name
		for _, h := range hit {
			if h.Forward == wantForward {
				return h, nil
			}
		}
	}
	return nil, cands
}

// UniqueFor returns the unique constraint whose column set equals cols
// (order-insensitive), or nil.
func (r *Resource) UniqueFor(cols []string) *introspect.Constraint {
	for _, u := range r.Uniques {
		if sameSet(u.Columns, cols) {
			return u
		}
	}
	return nil
}

func sameSet(a, b []string) bool {
	if len(a) != len(b) {
		return false
	}
	seen := map[string]int{}
	for _, x := range a {
		seen[x]++
	}
	for _, x := range b {
		if seen[x] == 0 {
			return false
		}
		seen[x]--
	}
	return true
}

func methodList(m map[Method]bool, allowed ...Method) []Method {
	var out []Method
	for _, x := range methodOrder {
		if !m[x] {
			continue
		}
		for _, a := range allowed {
			if a == x {
				out = append(out, x)
				break
			}
		}
	}
	return out
}
