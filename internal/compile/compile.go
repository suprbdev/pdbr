// Package compile turns a plan.Request into exactly one parameterized SQL
// statement whose single jsonb column `data` is the response body (or a
// small wrapper object around it, see Shape). Selections become
// jsonb_build_object trees, embeds LEFT JOIN LATERAL subqueries, lists
// jsonb_agg — the database returns the response JSON directly and there is
// no N+1 to paper over. Values only ever travel as parameters: the SQL text
// is a pure function of the request shape (FuzzFilter).
package compile

import (
	"context"
	"fmt"
	"strconv"
	"strings"

	"github.com/suprbdev/pdbcore/introspect"

	"github.com/suprbdev/pdbr/internal/api"
	"github.com/suprbdev/pdbr/internal/plan"
)

// Shape tells the exec layer what the statement's `data` column holds and
// how to unwrap it into the client body.
type Shape string

const (
	// ShapeRow: data is the client JSON object verbatim; zero rows means 404.
	ShapeRow Shape = "row"
	// ShapeRowEtag: data is {"etag": "<md5 hex>", "row": {...}}; zero rows
	// means 404. exec sets `ETag: W/"<etag>"`, answers 304 when
	// If-None-Match matches, and writes row as the body.
	ShapeRowEtag Shape = "row_etag"
	// ShapePage: data is
	//
	//	{"rows": [...], "has_next": bool, "has_prev": bool,
	//	 "first": [pk...] | null, "last": [pk...] | null,   -- only when Statement.Cursors
	//	 "total": n}                                          -- only when Statement.Total
	//
	// rows is the client body (already in the requested order). has_next /
	// has_prev say whether a page exists after / before this one under the
	// current paging mode. first / last are the primary-key values of the
	// first / last row in rows (JSON arrays, decode with UseNumber): pass
	// them to plan.EncodeCursor for X-Prev-Cursor (when has_prev) and
	// X-Next-Cursor (when has_next). total feeds X-Total-Count and the
	// Content-Range total.
	ShapePage Shape = "page"
	// ShapeScalar: data is the client JSON value verbatim — an RPC scalar,
	// composite object, single table row (object or null) or a SETOF
	// scalar/composite array. exec answers 200 with the value; zero rows
	// (never produced by these statements) render as null.
	ShapeScalar Shape = "scalar"
	// ShapeVoid: the statement called a void function; data is a constant.
	// exec answers 204 without a body.
	ShapeVoid Shape = "void"
	// ShapeJWT / ShapeJWTSet: data is the raw claims object (or array of
	// claims objects) of an rls.auth.jwt_type-returning function; exec signs
	// each into a JWT string via pdbcore/jwtmint (null stays null).
	ShapeJWT    Shape = "jwt"
	ShapeJWTSet Shape = "jwt_set"
	// ShapeMutRow: data is the mutated row's client JSON verbatim (keyed
	// PATCH/DELETE with return=representation and no If-Match); zero rows
	// means 404. exec answers 200.
	ShapeMutRow Shape = "mut_row"
	// ShapeMutList: data is the client JSON array of a bulk POST / PUT
	// (always emitted, '[]' when nothing was written). exec answers 201 for
	// POST and 200 for PUT.
	ShapeMutList Shape = "mut_list"
	// ShapeMutWrap: data is a wrapper whose members depend on the request:
	//
	//	{"row": obj | null,      -- the client JSON (or true under return=minimal with If-Match)
	//	 "found": bool,          -- If-Match: the row exists (cur CTE); absent otherwise
	//	 "inserted": bool,       -- single-object PUT: xmax = 0 (fresh insert)
	//	 "key": [pk],            -- single POST/PUT on a single-column-PK table (Location)
	//	 "n": int}               -- return=minimal without If-Match: rows written
	//
	// exec: found == false -> 404; row null with found == true -> 412
	// etag_mismatch; row null otherwise (or n == 0) -> 404; status 201 for
	// POST and for PUT with inserted, else 200 (204 under return=minimal,
	// which never carries a body); Location on 201 from key.
	ShapeMutWrap Shape = "mut_wrap"
	// ShapeMutBulk: data is {"n": int, "rows": [...]} for a PATCH or DELETE
	// on a list route (rows filtered by the query string); rows is absent
	// under return=minimal. exec answers 200 with rows (an empty array when
	// nothing matched — never 404) or 204 under return=minimal.
	ShapeMutBulk Shape = "mut_bulk"
)

// Statement is one compiled SQL statement. It always returns one row with
// one jsonb column named `data` (zero rows for missed single-row lookups).
type Statement struct {
	SQL  string
	Args []any
	// Mutation drives the transaction policy and the read-only gate.
	Mutation bool
	// Shape says how exec unwraps data (see Shape).
	Shape Shape
	// Cursors reports that a ShapePage wrapper carries first/last key arrays
	// (keyset-capable resource).
	Cursors bool
	// Total reports that a ShapePage wrapper carries total.
	Total bool
}

// Request is the compiler input.
type Request struct {
	Plan  *plan.Request
	Built *api.Built
	// MaxEmbedDepth is server.max_embed_depth (<= 0: 5).
	MaxEmbedDepth int
	// MaxPageSize is server.max_page_size: the largest accepted limit and the
	// default when the plan carries none (<= 0: 100).
	MaxPageSize int
}

// Func compiles one request; CompileHook plugins wrap it middleware-style.
type Func func(ctx context.Context, req *Request) (*Statement, error)

// Compiler is the core compiler over a built API model.
type Compiler struct {
	Built *api.Built
}

// New creates a Compiler.
func New(b *api.Built) *Compiler { return &Compiler{Built: b} }

const (
	defaultMaxEmbedDepth = 5
	defaultMaxPageSize   = 100
)

// Compile dispatches on the request kind: (Method, Key != nil, RPC != nil).
func (c *Compiler) Compile(_ context.Context, req *Request) (*Statement, error) {
	if req == nil || req.Plan == nil {
		return nil, fmt.Errorf("compile: nil request")
	}
	s := c.newStmt(req, &ParamSet{})
	p := req.Plan
	switch {
	case p.RPC != nil:
		return s.finish(s.rpc())
	case p.Resource == nil:
		return nil, fmt.Errorf("compile: request has neither resource nor rpc")
	}
	switch p.Method {
	case plan.GET, plan.HEAD, "":
		if p.Key != nil {
			return s.finish(s.rowByKey())
		}
		return s.finish(s.list())
	case plan.POST, plan.PUT, plan.PATCH, plan.DELETE:
		return s.finish(s.mutation())
	}
	return nil, fmt.Errorf("compile: unsupported method %q", p.Method)
}

func (c *Compiler) newStmt(req *Request, ps *ParamSet) *stmt {
	s := &stmt{c: c, req: req, plan: req.Plan, ps: ps, maxDepth: req.MaxEmbedDepth, maxPageSize: req.MaxPageSize}
	if s.maxDepth <= 0 {
		s.maxDepth = defaultMaxEmbedDepth
	}
	if s.maxPageSize <= 0 {
		s.maxPageSize = defaultMaxPageSize
	}
	return s
}

// stmt carries per-statement state: the parameter list and an alias counter.
type stmt struct {
	c           *Compiler
	req         *Request
	plan        *plan.Request
	ps          *ParamSet
	aliasN      int
	maxDepth    int
	maxPageSize int
	// overrides remaps "schema.table" to an alternate row source (a CTE
	// holding rows inserted earlier in the same statement, which a plain
	// table scan could not see within one snapshot).
	overrides map[string]string
	// rootSource, when set, replaces the root table scan of a list with a
	// set-returning function call (RPC SETOF <table>); cursors are off since
	// function output has no keyset.
	rootSource string
	// deferred holds the first error raised from a helper with no error
	// return (order rendering and the spatial builders it calls); Compile
	// surfaces it so a bad value never reaches the database.
	deferred error
}

func (s *stmt) fail(err error) {
	if s.deferred == nil {
		s.deferred = err
	}
}

func (s *stmt) finish(st *Statement, err error) (*Statement, error) {
	if err != nil {
		return nil, err
	}
	if s.deferred != nil {
		return nil, s.deferred
	}
	return st, nil
}

func (s *stmt) param(v any) string { return s.ps.Add(v) }

func (s *stmt) alias(prefix string) string {
	a := prefix + strconv.Itoa(s.aliasN)
	s.aliasN++
	return a
}

// sourceRef resolves the SQL row source for a table, honouring overrides.
func (s *stmt) sourceRef(t *introspect.Table) string {
	if ov, ok := s.overrides[t.Schema+"."+t.Name]; ok {
		return ov
	}
	return tableRef(t)
}

// rootRef is sourceRef for the root list scan, honouring rootSource.
func (s *stmt) rootRef(t *introspect.Table) string {
	if s.rootSource != "" {
		return s.rootSource
	}
	return s.sourceRef(t)
}

func quoteIdent(s string) string {
	return `"` + strings.ReplaceAll(s, `"`, `""`) + `"`
}

func quoteLiteral(s string) string {
	return "'" + strings.ReplaceAll(s, "'", "''") + "'"
}

func tableRef(t *introspect.Table) string {
	return quoteIdent(t.Schema) + "." + quoteIdent(t.Name)
}

func colRef(alias string, col string) string {
	return alias + "." + quoteIdent(col)
}

func joinLaterals(laterals []string) string {
	if len(laterals) == 0 {
		return ""
	}
	return "\n" + strings.Join(laterals, "\n")
}

func indent(sql string) string {
	return "  " + strings.ReplaceAll(sql, "\n", "\n  ")
}

// rootNode returns the projection node for the root resource (nil select
// means every column).
func (s *stmt) rootNode() *plan.Node {
	if s.plan.Select != nil {
		n := *s.plan.Select
		if n.Resource == nil {
			n.Resource = s.plan.Resource
		}
		return &n
	}
	return &plan.Node{Resource: s.plan.Resource, Star: true}
}

// ---- single row ----

// rowByKey compiles GET /{t}/{pk} and /{t}/by/{cols}/{vals}: the row JSON
// (or the {etag,row} wrapper) over the resource filtered by the key columns
// in constraint order. Zero rows surface as 404 in exec.
func (s *stmt) rowByKey() (*Statement, error) {
	r := s.plan.Resource
	key := s.plan.Key
	if key == nil || key.Constraint == nil {
		return nil, fmt.Errorf("compile: row request without key")
	}
	if len(key.Values) != len(key.Constraint.Columns) {
		return nil, errUser(CodeInvalidFilter, "key has %d values, want %d", len(key.Values), len(key.Constraint.Columns))
	}
	alias := s.alias("t")
	jsonExpr, laterals, err := s.rowJSON(s.rootNode(), alias, 0)
	if err != nil {
		return nil, err
	}
	conds, err := s.keyConds(r.Table, key.Constraint.Columns, key.Values, alias)
	if err != nil {
		return nil, err
	}
	extra, err := s.parentConds(s.rootNode(), s.plan.Filter, alias)
	if err != nil {
		return nil, err
	}
	conds = append(conds, extra...)
	shape := ShapeRow
	sel := jsonExpr
	if s.etagWanted() {
		shape = ShapeRowEtag
		// Hash over the whole row regardless of select=, so the ETag is a
		// property of the row rather than of the projection.
		sel = fmt.Sprintf("jsonb_build_object('etag', md5(to_jsonb(%s)::text), 'row', %s)", alias, jsonExpr)
	}
	sql := fmt.Sprintf("SELECT %s AS data\nFROM %s %s%s\nWHERE %s\nLIMIT 1",
		sel, s.sourceRef(r.Table), alias, joinLaterals(laterals), strings.Join(conds, " AND "))
	return &Statement{SQL: sql, Args: s.ps.Args(), Shape: shape}, nil
}

func (s *stmt) etagWanted() bool {
	return s.c.Built != nil && s.c.Built.Options.Etags && s.plan.Resource.Etag
}

// keyConds builds `alias."col" = $n` per key column.
func (s *stmt) keyConds(t *introspect.Table, cols []string, vals []any, alias string) ([]string, error) {
	conds := make([]string, len(cols))
	for i, name := range cols {
		col := t.Column(name)
		if col == nil {
			return nil, fmt.Errorf("compile: key column %q not found on %s", name, t.Name)
		}
		conds[i] = colRef(alias, name) + " = " + s.param(coerceInput(col, vals[i]))
	}
	return conds, nil
}

// ---- list ----

// list compiles GET /{t}: the ShapePage wrapper over one paginated scan.
//
// The inner scan fetches limit+1 rows so has_next is exact in the paging
// direction; __rn numbers rows in the emitted order (the window carries the
// same ORDER BY as the scan — a bare OVER () is evaluated below the sort node
// and would follow plan-dependent scan order), and the outer aggregate trims
// the detection row with a FILTER on __rn. `before` reverses the scan and the
// outer aggregate re-reverses, so rows is always in the requested order.
func (s *stmt) list() (*Statement, error) {
	r := s.plan.Resource
	t := r.Table
	node := s.rootNode()
	pp, err := s.planPage(r, s.plan.Page)
	if err != nil {
		return nil, err
	}
	if s.rootSource != "" {
		if pp.hasAfter || pp.hasBefore {
			return nil, errUser(CodeKeysetUnsupported, "set-returning functions support only limit/offset paging")
		}
		pp.keyset = false
	}
	dcols := s.plan.Distinct
	if len(dcols) > 0 {
		// DISTINCT ON changes row identity, so cursors over base rows no
		// longer address result rows: offset paging only, no cursors.
		if pp.hasAfter || pp.hasBefore {
			return nil, errUser(CodeKeysetUnsupported, "distinct supports only limit/offset paging")
		}
		pp.keyset = false
	}

	alias := s.alias("t")
	jsonExpr, laterals, err := s.rowJSON(node, alias, 0)
	if err != nil {
		return nil, err
	}
	terms := reorderForDistinct(s.orderTerms(t, s.plan.Order), dcols)
	selectCols := []string{jsonExpr + " AS data"}
	if pp.keyset {
		selectCols = append(selectCols, keyArraySQL(t, alias)+" AS __key")
	}
	rnName := "__rn"
	if len(dcols) > 0 {
		// The window numbers pre-distinct rows, leaving gaps; a wrapper
		// renumbers the survivors so the FILTER threshold stays dense.
		rnName = "__rn0"
	}
	orderSQL := s.renderOrder(terms, alias, pp.reversed)
	selectCols = append(selectCols, "row_number() OVER ("+strings.TrimPrefix(orderSQL, "\n")+") AS "+rnName)

	var conds []string
	if pp.hasAfter {
		vals, err := s.anchorVals(t, terms, pp.afterKeys)
		if err != nil {
			return nil, err
		}
		conds = append(conds, s.keysetPredicate(alias, vals, terms, false))
	}
	if pp.hasBefore {
		vals, err := s.anchorVals(t, terms, pp.beforeKeys)
		if err != nil {
			return nil, err
		}
		conds = append(conds, s.keysetPredicate(alias, vals, terms, true))
	}
	where, err := s.whereClause(node, s.plan.Filter, alias, conds)
	if err != nil {
		return nil, err
	}
	limit := "\nLIMIT " + s.param(pp.limit+1)
	if pp.offset > 0 {
		limit += "\nOFFSET " + s.param(pp.offset)
	}
	distinct := ""
	if len(dcols) > 0 {
		refs := make([]string, len(dcols))
		for i, c := range dcols {
			refs[i] = colRef(alias, c.Name)
		}
		distinct = "DISTINCT ON (" + strings.Join(refs, ", ") + ") "
	}
	inner := fmt.Sprintf("SELECT %s%s\nFROM %s %s%s%s%s%s",
		distinct, strings.Join(selectCols, ", "), s.rootRef(t), alias,
		joinLaterals(laterals), where, orderSQL, limit)
	if len(dcols) > 0 {
		w := s.alias("d")
		inner = fmt.Sprintf("SELECT %s.*, row_number() OVER (ORDER BY %s.__rn0) AS __rn\nFROM (\n%s\n) %s", w, w, indent(inner), w)
	}

	sub := s.alias("s")
	aggOrder := " ORDER BY " + sub + ".__rn ASC"
	if pp.reversed {
		aggOrder = " ORDER BY " + sub + ".__rn DESC"
	}
	// __rn numbers rows before OFFSET trims, so survivors of
	// `LIMIT limit+1 OFFSET offset` carry __rn in [offset+1, offset+limit+1]:
	// the trim threshold is offset+limit.
	trim := s.param(pp.offset + pp.limit)
	aggFilter := " FILTER (WHERE " + sub + ".__rn <= " + trim + ")"
	agg := func(expr string) string {
		return "jsonb_agg(" + expr + aggOrder + ")" + aggFilter
	}
	exact := "coalesce(bool_or(" + sub + ".__rn > " + trim + "), false)"
	hasNext, hasPrev := exact, boolLit(pp.hasAfter || pp.offset > 0)
	if pp.reversed {
		// Exact in the paging direction; the anchor row exists on the other
		// side by definition.
		hasNext, hasPrev = "true", exact
	}
	pairs := []string{
		"'rows'", "coalesce(" + agg(sub+".data") + ", '[]'::jsonb)",
		"'has_next'", hasNext,
		"'has_prev'", hasPrev,
	}
	if pp.keyset {
		pairs = append(pairs,
			"'first'", "("+agg(sub+".__key")+")->0",
			"'last'", "("+agg(sub+".__key")+")->-1")
	}
	if s.plan.Count {
		countSQL, err := s.totalSQL(node, dcols)
		if err != nil {
			return nil, err
		}
		pairs = append(pairs, "'total'", countSQL)
	}
	sql := fmt.Sprintf("SELECT %s AS data\nFROM (\n%s\n) %s", buildJSONObject(pairs), indent(inner), sub)
	return &Statement{SQL: sql, Args: s.ps.Args(), Shape: ShapePage, Cursors: pp.keyset, Total: s.plan.Count}, nil
}

// CountPlannable reports whether a list request's total can come from the
// planner statistics (Prefer: count=planned|estimated): an unfiltered scan of
// a table or materialized view, without distinct= or !inner embeds. Anything
// else is counted exactly.
func CountPlannable(p *plan.Request) bool {
	if p == nil || p.RPC != nil || p.Resource == nil || p.Filter != nil || len(p.Distinct) > 0 {
		return false
	}
	switch p.Resource.Table.Kind {
	case introspect.RelTable, introspect.RelMaterializedView:
	default:
		return false
	}
	return p.Select == nil || !hasInner(p.Select)
}

func hasInner(n *plan.Node) bool {
	for i := range n.Embeds {
		if n.Embeds[i].Inner {
			return true
		}
	}
	return false
}

// totalSQL renders the list total per Request.CountMode: planned reads
// pg_class.reltuples (falling back to the exact count while the table has
// never been analysed, reltuples = -1); estimated counts exactly up to
// max_page_size rows and switches to the statistic above. The fallback and
// the exact branches are uncorrelated subqueries, which PostgreSQL only
// evaluates when the CASE reaches them.
func (s *stmt) totalSQL(node *plan.Node, dcols []*introspect.Column) (string, error) {
	mode := s.plan.CountMode
	if mode != plan.CountPlanned && mode != plan.CountEstimated || !CountPlannable(s.plan) {
		return s.countSQL(node, dcols)
	}
	exact, err := s.countSQL(node, dcols)
	if err != nil {
		return "", err
	}
	t := s.plan.Resource.Table
	cls := s.alias("pc")
	planned := fmt.Sprintf("(SELECT CASE WHEN %s.reltuples >= 0 THEN %s.reltuples::bigint ELSE %s END FROM pg_catalog.pg_class %s WHERE %s.oid = %s::regclass)",
		cls, cls, exact, cls, cls, quoteLiteral(tableRef(t)))
	if mode == plan.CountPlanned {
		return planned, nil
	}
	threshold := s.param(s.maxPageSize)
	bounded := fmt.Sprintf("(SELECT count(*) FROM (SELECT 1 FROM %s %s LIMIT %s + 1) %s)", s.rootRef(t), s.alias("c"), threshold, s.alias("b"))
	est := s.alias("e")
	return fmt.Sprintf("(SELECT CASE WHEN %s.n <= %s THEN %s.n ELSE greatest(%s, %s + 1) END FROM (SELECT %s AS n) %s)",
		est, threshold, est, planned, threshold, bounded, est), nil
}

// countSQL renders the total-count subquery over the same FROM/WHERE (the
// filter is re-rendered against a fresh alias, so its parameters repeat).
// count(DISTINCT ...) would skip an all-NULL group that DISTINCT ON keeps, so
// distinct totals count over a DISTINCT subquery instead.
func (s *stmt) countSQL(node *plan.Node, dcols []*introspect.Column) (string, error) {
	t := s.plan.Resource.Table
	alias := s.alias("c")
	where, err := s.whereClause(node, s.plan.Filter, alias, nil)
	if err != nil {
		return "", err
	}
	where = strings.Replace(where, "\nWHERE ", " WHERE ", 1)
	if len(dcols) > 0 {
		refs := make([]string, len(dcols))
		for i, c := range dcols {
			refs[i] = colRef(alias, c.Name)
		}
		return fmt.Sprintf("(SELECT count(*) FROM (SELECT DISTINCT %s FROM %s %s%s) %s)",
			strings.Join(refs, ", "), s.rootRef(t), alias, where, s.alias("c")), nil
	}
	return fmt.Sprintf("(SELECT count(*) FROM %s %s%s)", s.rootRef(t), alias, where), nil
}

// keyArraySQL renders the primary key of alias's row as a jsonb array (the
// cursor payload minus the resource name).
func keyArraySQL(t *introspect.Table, alias string) string {
	cols := make([]string, len(t.PrimaryKey.Columns))
	for i, c := range t.PrimaryKey.Columns {
		cols[i] = colRef(alias, c)
	}
	return "jsonb_build_array(" + strings.Join(cols, ", ") + ")"
}

func boolLit(b bool) string {
	if b {
		return "true"
	}
	return "false"
}

// whereClause renders the WHERE clause for a scan of node's resource at
// alias: extra conditions first (correlation, keyset predicates), then the
// user filter, then the EXISTS restrictions of !inner embeds.
func (s *stmt) whereClause(node *plan.Node, filter plan.Expr, alias string, extra []string) (string, error) {
	conds := append([]string(nil), extra...)
	more, err := s.parentConds(node, filter, alias)
	if err != nil {
		return "", err
	}
	conds = append(conds, more...)
	if len(conds) == 0 {
		return "", nil
	}
	return "\nWHERE " + strings.Join(conds, " AND "), nil
}

// parentConds renders the predicates restricting node's own rows: the
// filter expression plus one EXISTS per !inner embed (recursively carrying
// the embed's filter and its own inner embeds).
func (s *stmt) parentConds(node *plan.Node, filter plan.Expr, alias string) ([]string, error) {
	var conds []string
	if filter != nil {
		cond, err := s.filterSQL(filter, alias)
		if err != nil {
			return nil, err
		}
		if cond != "" {
			conds = append(conds, cond)
		}
	}
	for i := range node.Embeds {
		e := &node.Embeds[i]
		if !e.Inner || e.Relation == nil {
			continue
		}
		cond, err := s.innerExists(e, alias)
		if err != nil {
			return nil, err
		}
		conds = append(conds, cond)
	}
	return conds, nil
}

// innerExists renders the parent-side restriction of a !inner embed:
// EXISTS over the related table with the same correlation and filter the
// lateral uses.
func (s *stmt) innerExists(e *plan.Embed, parentAlias string) (string, error) {
	rel := e.Relation
	sub := s.alias("r")
	conds := []string{joinConds(rel, sub, parentAlias)}
	child := e.Node
	if child == nil {
		child = &plan.Node{Resource: rel.To, Star: true}
	} else if child.Resource == nil {
		n := *child
		n.Resource = rel.To
		child = &n
	}
	more, err := s.parentConds(child, e.Filter, sub)
	if err != nil {
		return "", err
	}
	conds = append(conds, more...)
	return fmt.Sprintf("EXISTS (SELECT 1 FROM %s %s WHERE %s)", s.sourceRef(rel.To.Table), sub, strings.Join(conds, " AND ")), nil
}

// joinConds renders the FK correlation between the embedded row at
// childAlias and the parent row at parentAlias.
func joinConds(rel *api.Relation, childAlias, parentAlias string) string {
	parts := make([]string, len(rel.FK.Columns))
	for i, col := range rel.FK.Columns {
		if rel.Forward {
			parts[i] = colRef(childAlias, rel.FK.RefColumns[i]) + " = " + colRef(parentAlias, col)
		} else {
			parts[i] = colRef(childAlias, col) + " = " + colRef(parentAlias, rel.FK.RefColumns[i])
		}
	}
	return strings.Join(parts, " AND ")
}
