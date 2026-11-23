package compile

import (
	"crypto/rand"
	"encoding/hex"
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
// json expression plus the LEFT JOIN LATERAL clauses it needs (joined by
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
//   - inserted maps "schema.table" to the chain's other CTEs (besides
//     __mut) holding rows the statement inserted into that table, each
//     projecting every column (RETURNING *). A plain table scan cannot see
//     them within one snapshot, so embeds reading such a table, or the
//     mutated one, read a visibility CTE instead: the table UNION ALL those
//     rows (and __mut's), emitted only when the response reads it. __mut
//     must be a plain INSERT: none of the table's existing rows is
//     replaced.
//
// This is the extension point the nested_mutations CompileHook uses to
// replace the DML while reusing response compilation.
func (c *Compiler) MutationWithCTEs(req *Request, ctes string, ps *ParamSet, inserted map[string][]string) (*Statement, error) {
	writes := make(map[string]TableWrites, len(inserted))
	for name, rows := range inserted {
		writes[name] = TableWrites{Rows: rows}
	}
	return c.MutationChain(req, Chain{CTEs: ctes, Params: ps, Writes: writes})
}

// Chain is a caller-built CTE chain for MutationChain.
type Chain struct {
	// CTEs is the text between WITH and the final SELECT; it must define
	// __mut (see MutationWithCTEs for the __mut rules and %%RETURNING%%).
	CTEs string
	// Params is the set the chain was rendered with (nil: none).
	Params *ParamSet
	// RootKeep is set when __mut updates or locks existing rows of the
	// mutated table instead of inserting: a condition over the bare table
	// reference (`"schema"."table"`) that holds for the rows __mut left
	// alone. The visibility CTE of the mutated table keeps those rows and
	// adds __mut's.
	RootKeep string
	// Writes maps "schema.table" to the chain's other writes to that table
	// (the mutated one included).
	Writes map[string]TableWrites
	// Guards are conditions that must hold, rendered with GuardSQL. They
	// are evaluated even when the response yields no row, and a failing one
	// aborts the whole statement.
	Guards []string
}

// TableWrites describes how a chain's CTEs change one table.
type TableWrites struct {
	// Rows are CTEs returning every column of rows that are in the table
	// once the statement completes (inserted or updated rows).
	Rows []string
	// Keep are conditions over the bare table reference that hold for the
	// pre-statement rows the chain left alone (updated and deleted rows
	// fail one of them).
	Keep []string
}

// MutationChain is MutationWithCTEs for a chain that may also update,
// delete or lock rows: the visibility CTE of every table the response
// reads is the table restricted to Chain.Writes[t].Keep (and RootKeep for
// the mutated table) UNION ALL __mut and the Rows CTEs. With guards, the
// response is read through a materialized guard CTE, so a statement whose
// response yields no row still evaluates them; the data column is then
// NULL instead of absent, which unwraps the same way.
func (c *Compiler) MutationChain(req *Request, ch Chain) (*Statement, error) {
	if req == nil || req.Plan == nil || req.Plan.Resource == nil {
		return nil, fmt.Errorf("compile: MutationWithCTEs needs a resource request")
	}
	if strings.TrimSpace(ch.CTEs) == "" {
		return nil, fmt.Errorf("compile: MutationWithCTEs needs a CTE chain")
	}
	ps := ch.Params
	if ps == nil {
		ps = &ParamSet{}
	}
	s := c.newStmt(req, ps)
	s.guards = ch.Guards
	rep := replaced{}
	if ch.RootKeep != "" {
		rep.cond = ch.RootKeep
	}
	return s.finish(s.mutationResponse(strings.TrimSpace(ch.CTEs), rep, ch.Writes))
}

// guardToken is a random per-process secret embedded in every guard
// message, so that a 22P02 raised over a client or stored value (the
// message quotes the offending input) cannot pass for a failed guard: the
// token never leaves the server (RenderGolden masks it).
var guardToken = newGuardToken()

func newGuardToken() string {
	var b [16]byte
	if _, err := rand.Read(b[:]); err != nil {
		panic("compile: guard token: " + err.Error())
	}
	return hex.EncodeToString(b[:])
}

// guardMarker prefixes the message a failed guard raises (GuardSQL).
var guardMarker = "pdbr guard " + guardToken + ": "

// guardMask replaces guardToken in rendered SQL (RenderGolden).
const guardMask = "<token>"

// GuardSQL renders a guard requiring the CTE named cte to yield at least
// one row. A failed guard casts its message (built from count(*), so the
// planner cannot fold the cast into a plan-time error) to boolean, which
// raises SQLSTATE 22P02; the executor answers 404 not_found with
// "<label> matched 0 rows" as the detail. The message carries a secret
// per-process token that GuardFailure requires, so no other 22P02 maps to
// a guard failure. label must not depend on request values (the SQL text
// stays a function of the request shape).
func GuardSQL(cte, label string) string {
	return fmt.Sprintf("(SELECT CASE WHEN count(*) > 0 THEN true ELSE concat(%s, count(*), ' rows')::boolean END FROM %s)",
		quoteLiteral(guardMarker+label+" matched "), cte)
}

// GuardFailure reports whether a database error message is a failed
// GuardSQL guard raised by this process, and returns its "<label> matched
// N rows" text: the message must carry the guard's token and end its
// quoted value with " matched N rows" (the quote characters around it
// depend on the server's lc_messages).
func GuardFailure(message string) (string, bool) {
	i := strings.Index(message, guardMarker)
	if i < 0 {
		return "", false
	}
	rest := message[i+len(guardMarker):]
	j := strings.LastIndex(rest, " rows")
	if j < 0 {
		return "", false
	}
	rest = rest[:j+len(" rows")]
	head, n, ok := cutLast(rest[:j], " matched ")
	if !ok || head == "" || n == "" || strings.Trim(n, "0123456789") != "" {
		return "", false
	}
	return rest, true
}

// cutLast is strings.Cut at the last occurrence of sep.
func cutLast(s, sep string) (before, after string, found bool) {
	if i := strings.LastIndex(s, sep); i >= 0 {
		return s[:i], s[i+len(sep):], true
	}
	return s, "", false
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
