package nestedmutations

import (
	"errors"
	"fmt"
	"slices"
	"sort"
	"strconv"
	"strings"

	"github.com/suprbdev/pdbcore/introspect"

	"github.com/suprbdev/pdbr/internal/api"
	"github.com/suprbdev/pdbr/internal/compile"
	"github.com/suprbdev/pdbr/internal/parse"
)

// Error codes the resolver emits (docs/errors.md).
const (
	CodeInvalidBody        = compile.CodeInvalidBody
	CodeUnknownRelation    = parse.CodeUnknownRelation
	CodeEmbedDepthExceeded = compile.CodeEmbedDepthExceeded
)

// nestedIn is one nested key of a row with its raw JSON value.
type nestedIn struct {
	key string
	rel *api.Relation
	raw any
}

// fkFill wires FK columns of a row to the columns of an earlier CTE.
type fkFill struct {
	cte     string
	cols    []string
	refCols []string
}

// fillFrom wires the equality pairs of rel's FK to cte: the period column
// of a PERIOD key is never copied, the referencing row supplies its own.
func fillFrom(cte string, rel *api.Relation) fkFill {
	f := fkFill{cte: cte}
	for _, p := range rel.EqualityPairs() {
		f.cols = append(f.cols, p.Column)
		f.refCols = append(f.refCols, p.RefColumn)
	}
	return f
}

// rowIn is one row to insert.
type rowIn struct {
	res    *api.Resource
	cols   map[string]any // coerced values by column name
	nested []nestedIn     // sorted by key
	depth  int
	cte    string // fixed CTE name (root: __mut); "" allocates __<kind>_n
	kind   string
	path   string // body path for error messages ("" at the root)
	parent *fkFill
}

// chain accumulates the WITH chain of one nested mutation.
type chain struct {
	p     *Plugin
	c     *compile.Compiler
	built *api.Built
	ps    *compile.ParamSet
	ctes  []string
	n     int
	// inserted lists the insert CTEs per "schema.table" (parents created and
	// children), the row sources the response's embeds must read instead of
	// the table; insertedOrder keeps the map deterministic.
	inserted      map[string][]string
	insertedOrder []string
	// withTable marks the tables whose visibility CTE must also read the
	// table itself: the parents created through a PERIOD relation, which
	// the new row may share with existing rows whose periods overlap it.
	withTable map[string]bool
}

func (g *chain) name(kind string) string {
	g.n++
	return "__" + kind + "_" + strconv.Itoa(g.n)
}

func (g *chain) recordInsert(t *introspect.Table, cte string) {
	key := t.Schema + "." + t.Name
	if _, ok := g.inserted[key]; !ok {
		g.insertedOrder = append(g.insertedOrder, key)
	}
	g.inserted[key] = append(g.inserted[key], cte)
}

func childPath(path, key string) string {
	if path == "" {
		return key
	}
	return path + "." + key
}

func errAt(code, path, format string, args ...any) error {
	return compile.NewUserError(code, fmt.Sprintf("nested %q: ", path)+fmt.Sprintf(format, args...))
}

// wrapErr prefixes a parse error's detail with the body path, keeping its
// code and status.
func wrapErr(err error, path string) error {
	var ue *compile.UserError
	if errors.As(err, &ue) {
		return &compile.UserError{Code: ue.Code, Status: ue.Status, Detail: fmt.Sprintf("nested %q: %s", path, ue.Detail)}
	}
	return err
}

// insertRow emits the CTEs for one row: its forward parents first, then its
// own INSERT (FK columns read from the parent CTEs), then its children. It
// returns the name of the CTE holding the row.
func (g *chain) insertRow(in rowIn) (string, error) {
	t := in.res.Table
	if in.parent != nil {
		for _, c := range in.parent.cols {
			if _, ok := in.cols[c]; ok {
				return "", errAt(CodeInvalidBody, in.path, "column %q is filled from the parent row and cannot be set", keyFor(in.res, c))
			}
		}
	}
	for _, n := range in.nested {
		if !n.rel.Forward {
			continue
		}
		for _, c := range n.rel.EqualityColumns() {
			if _, ok := in.cols[c]; ok {
				return "", errAt(CodeInvalidBody, in.path, "body sets both column %q and nested relation %q", keyFor(in.res, c), n.key)
			}
			if in.parent != nil && slices.Contains(in.parent.cols, c) {
				return "", errAt(CodeInvalidBody, in.path, "nested relation %q sets column %q, which is filled from the parent row", n.key, keyFor(in.res, c))
			}
		}
	}
	if err := g.requireColumns(in); err != nil {
		return "", err
	}

	var fills []fkFill
	if in.parent != nil {
		fills = append(fills, *in.parent)
	}
	var children []nestedIn
	for _, n := range in.nested {
		if !n.rel.Forward {
			children = append(children, n)
			continue
		}
		cte, err := g.resolveParent(n, in.depth, in.path)
		if err != nil {
			return "", err
		}
		fills = append(fills, fillFrom(cte, n.rel))
	}

	name := in.cte
	if name == "" {
		name = g.name(in.kind)
	}
	var cols, vals, from []string
	for _, c := range t.Columns {
		v, ok := in.cols[c.Name]
		if !ok {
			continue
		}
		expr, err := g.c.InputValue(c, v, g.ps)
		if err != nil {
			return "", wrapErr(err, in.path)
		}
		cols = append(cols, compile.QuoteIdent(c.Name))
		vals = append(vals, expr)
	}
	for _, f := range fills {
		for i, c := range f.cols {
			cols = append(cols, compile.QuoteIdent(c))
			vals = append(vals, f.cte+"."+compile.QuoteIdent(f.refCols[i]))
		}
		from = append(from, f.cte)
	}
	returning := "*"
	if name == "__mut" {
		returning = "%%RETURNING%%"
	}
	table := compile.TableRefSQL(t.Schema, t.Name)
	var sql string
	switch {
	case len(cols) == 0:
		sql = "INSERT INTO " + table + " DEFAULT VALUES"
	case len(from) == 0:
		sql = "INSERT INTO " + table + " (" + strings.Join(cols, ", ") + ")\nVALUES (" + strings.Join(vals, ", ") + ")"
	default:
		sql = "INSERT INTO " + table + " (" + strings.Join(cols, ", ") + ")\nSELECT " + strings.Join(vals, ", ") + "\nFROM " + strings.Join(from, ", ")
	}
	g.ctes = append(g.ctes, name+" AS (\n"+indent(sql+"\nRETURNING "+returning)+"\n)")
	if name != "__mut" {
		g.recordInsert(t, name)
	}
	for _, n := range children {
		if err := g.insertChildren(n, name, in.depth, in.path); err != nil {
			return "", err
		}
	}
	return name, nil
}

// requireColumns applies the insert rule (NOT NULL without default) to a
// row, treating FK columns supplied by a parent CTE or by a nested forward
// key as present.
func (g *chain) requireColumns(in rowIn) error {
	covered := map[string]bool{}
	if in.parent != nil {
		for _, c := range in.parent.cols {
			covered[c] = true
		}
	}
	for _, n := range in.nested {
		if n.rel.Forward {
			for _, c := range n.rel.EqualityColumns() {
				covered[c] = true
			}
		}
	}
	var missing []string
	for _, c := range in.res.Table.Columns {
		if !c.NotNull || c.HasDefault || c.Generated || covered[c.Name] {
			continue
		}
		if _, ok := in.cols[c.Name]; !ok {
			missing = append(missing, keyFor(in.res, c.Name))
		}
	}
	if len(missing) > 0 {
		return errAt(CodeInvalidBody, in.path, "missing required column(s): %s", strings.Join(missing, ", "))
	}
	return nil
}

// resolveParent handles a forward nested key: {"create": {…}} inserts the
// referenced row in a __p_n CTE (recursively), {"connect": {…}} selects it.
func (g *chain) resolveParent(n nestedIn, depth int, path string) (string, error) {
	p := childPath(path, n.key)
	obj, ok := n.raw.(map[string]any)
	if !ok {
		return "", errAt(CodeInvalidBody, p, `expected an object with exactly one of "create" or "connect"`)
	}
	for _, k := range sortedKeys(obj) {
		if k != "create" && k != "connect" {
			return "", errAt(CodeInvalidBody, p, `unknown key %q (expected "create" or "connect")`, k)
		}
	}
	create, hasCreate := obj["create"]
	connect, hasConnect := obj["connect"]
	if hasCreate == hasConnect {
		return "", errAt(CodeInvalidBody, p, `exactly one of "create" or "connect" is required`)
	}
	to := n.rel.To
	if hasConnect {
		return g.connectParent(n, connect, p)
	}
	if depth+1 > g.p.maxDepth {
		return "", errAt(CodeEmbedDepthExceeded, p, "nesting depth %d exceeds max_depth %d", depth+1, g.p.maxDepth)
	}
	if !to.Methods[api.MethodPost] {
		return "", errAt(CodeUnknownRelation, p, `%q does not accept "create" (no INSERT on the related table)`, to.Path)
	}
	row, ok := create.(map[string]any)
	if !ok {
		return "", errAt(CodeInvalidBody, p, `"create" must be a JSON object`)
	}
	cols, nested, err := g.splitRow(to, row, p)
	if err != nil {
		return "", err
	}
	if n.rel.FK.Period {
		g.withTable[to.Table.Schema+"."+to.Table.Name] = true
	}
	return g.insertRow(rowIn{res: to, cols: cols, nested: nested, depth: depth + 1, kind: "p", path: p})
}

// connectParent emits `__p_n AS (SELECT * FROM t WHERE key = $n)`; the keys
// must be exactly the referenced columns, the primary key or one unique
// constraint of the related resource. A PERIOD relation names the
// referenced entity instead (the equality columns of the referenced key, or
// a row key), and its CTE selects those columns DISTINCT so one row is
// inserted however many periods the entity has.
func (g *chain) connectParent(n nestedIn, connect any, p string) (string, error) {
	to := n.rel.To
	obj, ok := connect.(map[string]any)
	if !ok || len(obj) == 0 {
		return "", errAt(CodeInvalidBody, p, `"connect" must be a JSON object naming the key columns of %q`, to.Path)
	}
	byCol := map[string]any{}
	var colNames []string
	for key, v := range obj {
		f := to.Field(key)
		if f == nil {
			return "", errAt(CodeInvalidBody, p, `unknown key %q in "connect" for %q`, key, to.Path)
		}
		if v == nil {
			return "", errAt(CodeInvalidBody, p, `"connect" key %q must not be null`, key)
		}
		byCol[f.Column.Name] = v
		colNames = append(colNames, f.Column.Name)
	}
	refCols := n.rel.FK.RefColumns
	if n.rel.FK.Period {
		refCols = fillFrom("", n.rel).refCols
	}
	if !keySetOK(to, refCols, colNames) {
		return "", errAt(CodeInvalidBody, p, `"connect" keys must be the primary key or one unique constraint of %q`, to.Path)
	}
	var conds []string
	for _, c := range to.Table.Columns {
		v, ok := byCol[c.Name]
		if !ok {
			continue
		}
		cv, err := parse.CoerceValue(g.built, c, v)
		if err != nil {
			return "", wrapErr(err, p)
		}
		expr, err := g.c.InputValue(c, cv, g.ps)
		if err != nil {
			return "", wrapErr(err, p)
		}
		conds = append(conds, compile.QuoteIdent(c.Name)+" = "+expr)
	}
	list := "*"
	if n.rel.FK.Period {
		quoted := make([]string, len(refCols))
		for i, c := range refCols {
			quoted[i] = compile.QuoteIdent(c)
		}
		list = "DISTINCT " + strings.Join(quoted, ", ")
	}
	name := g.name("p")
	g.ctes = append(g.ctes, name+" AS (\n"+indent("SELECT "+list+" FROM "+compile.TableRefSQL(to.Table.Schema, to.Table.Name)+"\nWHERE "+strings.Join(conds, " AND "))+"\n)")
	return name, nil
}

func keySetOK(r *api.Resource, refCols, cols []string) bool {
	if sameSet(refCols, cols) {
		return true
	}
	if pk := r.PrimaryKey(); pk != nil && sameSet(pk.Columns, cols) {
		return true
	}
	return r.UniqueFor(cols) != nil
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

// insertChildren handles a backward nested key: {"create": [{…}, …]} inserts
// one __c_n CTE per row, FK columns read from the parent CTE.
func (g *chain) insertChildren(n nestedIn, parentCTE string, depth int, path string) error {
	p := childPath(path, n.key)
	obj, ok := n.raw.(map[string]any)
	if !ok {
		return errAt(CodeInvalidBody, p, `expected an object with a "create" list`)
	}
	for _, k := range sortedKeys(obj) {
		switch k {
		case "create":
		case "connect":
			return errAt(CodeInvalidBody, p, `"connect" is not supported on a backward relation (use "create")`)
		default:
			return errAt(CodeInvalidBody, p, `unknown key %q (expected "create")`, k)
		}
	}
	create, ok := obj["create"]
	if !ok {
		return errAt(CodeInvalidBody, p, `"create" is required`)
	}
	list, ok := create.([]any)
	if !ok || len(list) == 0 {
		return errAt(CodeInvalidBody, p, `"create" must be a non-empty JSON array of objects`)
	}
	if depth+1 > g.p.maxDepth {
		return errAt(CodeEmbedDepthExceeded, p, "nesting depth %d exceeds max_depth %d", depth+1, g.p.maxDepth)
	}
	child := n.rel.To
	if !child.Methods[api.MethodPost] {
		return errAt(CodeUnknownRelation, p, `%q does not accept "create" (no INSERT on the related table)`, child.Path)
	}
	for i, item := range list {
		ep := p + "[" + strconv.Itoa(i) + "]"
		row, ok := item.(map[string]any)
		if !ok {
			return errAt(CodeInvalidBody, ep, "expected a JSON object")
		}
		cols, nested, err := g.splitRow(child, row, ep)
		if err != nil {
			return err
		}
		fill := fillFrom(parentCTE, n.rel)
		if _, err := g.insertRow(rowIn{res: child, cols: cols, nested: nested, depth: depth + 1, kind: "c", path: ep, parent: &fill}); err != nil {
			return err
		}
	}
	return nil
}

// splitRow separates a nested row into its column values (coerced by the
// core body rules) and its nested keys, mirroring the parser's precedence:
// a column key always wins over a relation key of the same name.
func (g *chain) splitRow(res *api.Resource, row map[string]any, path string) (map[string]any, []nestedIn, error) {
	keys := NestedKeys(res)
	plain := make(map[string]any, len(row))
	var nested []nestedIn
	for k, v := range row {
		if _, ok := res.InputColumns[k]; ok {
			plain[k] = v
			continue
		}
		if f := res.Field(k); f != nil && f.ReadOnly {
			plain[k] = v
			continue
		}
		if rel := keys[k]; rel != nil {
			nested = append(nested, nestedIn{key: k, rel: rel, raw: v})
			continue
		}
		plain[k] = v
	}
	cols, err := parse.CoerceObject(g.built, res, plain)
	if err != nil {
		return nil, nil, wrapErr(err, path)
	}
	sort.Slice(nested, func(i, j int) bool { return nested[i].key < nested[j].key })
	return cols, nested, nil
}

func sortedKeys(m map[string]any) []string {
	keys := make([]string, 0, len(m))
	for k := range m {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	return keys
}

func keyFor(res *api.Resource, col string) string {
	if f := res.FieldByColumn(col); f != nil {
		return f.Key
	}
	return col
}

func indent(sql string) string {
	return "  " + strings.ReplaceAll(sql, "\n", "\n  ")
}
