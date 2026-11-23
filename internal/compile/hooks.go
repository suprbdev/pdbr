package compile

import (
	"fmt"
	"strconv"
	"strings"

	"github.com/suprbdev/pdbcore/introspect"
	"github.com/suprbdev/pdbcore/postgis"

	"github.com/suprbdev/pdbr/internal/api"
	"github.com/suprbdev/pdbr/internal/plan"
)

// ParamSet lets CompileHook plugins build parameterized SQL fragments whose
// placeholder numbering composes with the core compiler.
type ParamSet struct {
	args []any
}

// Add appends a value and returns its placeholder ("$n").
func (p *ParamSet) Add(v any) string {
	p.args = append(p.args, v)
	return "$" + strconv.Itoa(len(p.args))
}

// Args returns the accumulated parameter list.
func (p *ParamSet) Args() []any { return p.args }

// SpatialValue renders a GeoJSON/WKT input value for a PostGIS column as a
// parameterized constructor expression (e.g. ST_GeomFromGeoJSON($3)),
// appending the single value parameter to the set. Plugins writing
// geometry/geography columns must use this rather than Add: a bare parameter
// would fail type resolution.
func (p *ParamSet) SpatialValue(col *introspect.Column, v any) (string, error) {
	f := postgis.FlavourOf(col.PGType)
	if !f.Spatial() {
		return "", fmt.Errorf("compile: column %q is not a PostGIS type", col.Name)
	}
	val, err := postgis.ParseFor(v, f, spatialSRID(col))
	if err != nil {
		return "", errUser(CodeInvalidFilter, "%s", err.Error())
	}
	return val.SQL(p.Add(val.Param)), nil
}

// RowJSON compiles a projection node over its resource at alias into a
// jsonb expression plus the LEFT JOIN LATERAL clauses it needs (joined by
// newlines), continuing parameter numbering in ps. A nil node means every
// column of the plan's resource.
func (c *Compiler) RowJSON(req *Request, node *plan.Node, alias string, ps *ParamSet) (jsonExpr, laterals string, err error) {
	if ps == nil {
		ps = &ParamSet{}
	}
	s := c.newStmt(req, ps)
	if node == nil {
		node = s.rootNode()
	}
	// Alias numbering must not collide with the caller's alias.
	s.aliasN = 1000
	expr, lats, err := s.rowJSON(node, alias, 0)
	if err != nil {
		return "", "", err
	}
	if s.deferred != nil {
		return "", "", s.deferred
	}
	return expr, strings.Join(lats, "\n"), nil
}

// MutationWithCTEs compiles the response half of a mutation request over a
// caller-supplied CTE chain (the text between WITH and the final SELECT,
// e.g. `__p_1 AS (...),\n__mut AS (...)`): the plan's select= is compiled
// against __mut exactly as the core does, so the resulting Statement has the
// same Shape/status semantics as a core mutation (docs/mutations.md). Rules:
//
//   - the chain must define __mut yielding the mutated row(s); a single-object
//     PUT must expose `__mut.__inserted` (xmax = 0) and an If-Match request a
//     `cur` CTE, exactly like the core DML;
//   - a %%RETURNING%% placeholder anywhere in the chain is replaced with the
//     narrow RETURNING list (columns the response — or the chain itself, as
//     `__mut."col"` — references; `1` when none). Without the placeholder the
//     caller's own RETURNING stands;
//   - params created through ps keep their numbers and the response's params
//     continue after them; ps must be the set used to render the chain;
//   - overrides remaps "schema.table" row sources to CTE names so embeds
//     read rows written earlier in the statement (a plain table scan cannot
//     see them within one snapshot: the visibility CTE of nested-mutations).
//
// This is the extension point the nested-mutations CompileHook uses to
// replace the DML while reusing response compilation.
func (c *Compiler) MutationWithCTEs(req *Request, ctes string, ps *ParamSet, overrides map[string]string) (*Statement, error) {
	if req == nil || req.Plan == nil || req.Plan.Resource == nil {
		return nil, fmt.Errorf("compile: MutationWithCTEs needs a resource request")
	}
	if strings.TrimSpace(ctes) == "" {
		return nil, fmt.Errorf("compile: MutationWithCTEs needs a CTE chain")
	}
	if ps == nil {
		ps = &ParamSet{}
	}
	s := c.newStmt(req, ps)
	s.overrides = overrides
	return s.finish(s.mutationResponse(strings.TrimSpace(ctes)))
}

// CoerceInput converts an input value into a driver-friendly parameter for
// the given column: decoded json/jsonb values are re-encoded as JSON text
// (a string is taken as JSON text already), array elements coerce
// recursively, enum labels / int8 / numeric strings pass through. Plugins
// building their own DML must route column values through this before
// ParamSet.Add.
func CoerceInput(_ *api.Built, col *introspect.Column, v any) any {
	return coerceInput(col, v)
}

// InputValue renders a body value for col as the parameter expression the
// core DML uses (composites via jsonb_populate_record, spatial constructors,
// array and json casts, a bare $n otherwise), appending its parameter(s) to
// ps. Plugins building their own INSERT/UPDATE tuples route every column
// value through it so their SQL stays a pure function of the column type.
func (c *Compiler) InputValue(col *introspect.Column, v any, ps *ParamSet) (string, error) {
	if col == nil {
		return "", fmt.Errorf("compile: InputValue needs a column")
	}
	if ps == nil {
		ps = &ParamSet{}
	}
	s := &stmt{c: c, ps: ps, aliasN: 1000}
	expr := s.inputValue(col, v)
	if s.deferred != nil {
		return "", s.deferred
	}
	return expr, nil
}

// KeyEqualSQL renders ref = value for a key column, as the core key
// predicates do: through the owning extension's = operator with value cast
// to the column type for citext, hstore and ltree (so citext stays
// case-insensitive when its schema is off the search_path), a plain = for
// everything else.
func (c *Compiler) KeyEqualSQL(col *introspect.Column, ref, value string) string {
	s := &stmt{c: c, ps: &ParamSet{}, aliasN: 1000}
	return s.keyEqSQL(col, ref, value)
}

// QuoteIdent double-quotes an SQL identifier.
func QuoteIdent(s string) string { return quoteIdent(s) }

// TableRefSQL renders a quoted schema-qualified relation reference.
func TableRefSQL(schemaName, table string) string {
	return quoteIdent(schemaName) + "." + quoteIdent(table)
}
