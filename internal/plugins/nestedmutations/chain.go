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
	"github.com/suprbdev/pdbr/internal/plan"
)

// Error codes the resolver emits (docs/errors.md).
const (
	CodeInvalidBody        = compile.CodeInvalidBody
	CodeUnknownRelation    = parse.CodeUnknownRelation
	CodeEmbedDepthExceeded = compile.CodeEmbedDepthExceeded
)

// The operations of a nested key.
const (
	opConnect    = "connect"
	opCreate     = "create"
	opDelete     = "delete"
	opDisconnect = "disconnect"
	opUpdate     = "update"
)

// allOps lists the operations in emission order.
var allOps = []string{opConnect, opCreate, opDelete, opDisconnect, opUpdate}

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

// locator renders a predicate over a reference to a table (a table
// reference, an alias, or "" for unqualified columns) that holds for the
// row a nested operation targets, as it stood before the statement.
type locator func(ref string) string

// scope narrows an UPDATE, DELETE or lock of a related row to the rows
// referencing a parent row: from names the parent's CTE, cond renders the
// join over a reference to the row's table.
type scope struct {
	from string
	cond func(ref string) string
}

// updIn is one existing row to update (or lock, when cols is empty).
type updIn struct {
	res    *api.Resource
	cols   map[string]any // coerced patch by column name
	nested []nestedIn     // sorted by key
	depth  int
	cte    string // fixed CTE name (root: __mut)
	path   string
	loc    locator
	scope  *scope
}

// chain accumulates the WITH chain of one nested mutation.
type chain struct {
	p      *Plugin
	c      *compile.Compiler
	built  *api.Built
	ps     *compile.ParamSet
	ctes   []string
	n      int
	aliasN int
	ops    int
	guards []string
	// writes lists, per "schema.table", the CTEs whose rows the response's
	// embeds must read along with the table and the conditions excluding
	// the table rows the chain updated or deleted.
	writes map[string]compile.TableWrites
}

func (g *chain) name(kind string) string {
	g.n++
	return "__" + kind + "_" + strconv.Itoa(g.n)
}

func (g *chain) alias(prefix string) string {
	g.aliasN++
	return "__" + prefix + strconv.Itoa(g.aliasN)
}

func tableKey(t *introspect.Table) string { return t.Schema + "." + t.Name }

func (g *chain) addRows(t *introspect.Table, cte string) {
	w := g.writes[tableKey(t)]
	w.Rows = append(w.Rows, cte)
	g.writes[tableKey(t)] = w
}

func (g *chain) addKeep(t *introspect.Table, cond string) {
	w := g.writes[tableKey(t)]
	w.Keep = append(w.Keep, cond)
	g.writes[tableKey(t)] = w
}

// recordChange records an UPDATE (kept) or DELETE CTE over t that targeted
// the row loc identifies: the table half of t's visibility CTE drops the
// row by a NOT NULL key the CTE left alone, matched against its returned
// rows, or by loc when it patched every such key.
func (g *chain) recordChange(t *introspect.Table, cte string, loc locator, patched map[string]bool, keep bool) {
	ref := compile.TableRefSQL(t.Schema, t.Name)
	if key := stableKey(t, patched); key != nil {
		conds := make([]string, len(key))
		for i, c := range key {
			q := compile.QuoteIdent(c)
			conds[i] = g.c.KeyEqualSQL(t.Column(c), "__m."+q, ref+"."+q)
		}
		g.addKeep(t, "NOT EXISTS (SELECT 1 FROM "+cte+" __m WHERE "+strings.Join(conds, " AND ")+")")
	} else {
		g.addKeep(t, "("+loc(ref)+") IS NOT TRUE")
	}
	if keep {
		g.addRows(t, cte)
	}
}

// stableKey returns the columns of the first row key of t (primary key,
// then unique constraints) that are all NOT NULL and untouched by patched,
// or nil.
func stableKey(t *introspect.Table, patched map[string]bool) []string {
	for _, con := range append([]*introspect.Constraint{t.PrimaryKey}, t.Uniques...) {
		if con == nil || len(con.Columns) == 0 || !con.RowKey() {
			continue
		}
		ok := true
		for _, cn := range con.Columns {
			c := t.Column(cn)
			if c == nil || patched[cn] || (!c.NotNull && con != t.PrimaryKey) {
				ok = false
				break
			}
		}
		if ok {
			return con.Columns
		}
	}
	return nil
}

// guard requires cte to yield a row; label names the operation.
func (g *chain) guard(cte, label string) {
	g.guards = append(g.guards, compile.GuardSQL(cte, label))
}

// countOp charges one nested operation against max_operations.
func (g *chain) countOp(path string) error {
	g.ops++
	if g.ops > g.p.maxOps {
		return errAt(CodeInvalidBody, path, "more than %d nested operations (max_operations)", g.p.maxOps)
	}
	return nil
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

func guardLabel(path string) string { return fmt.Sprintf("nested %q:", path) }

// wrapErr prefixes a parse error's detail with the body path, keeping its
// code and status.
func wrapErr(err error, path string) error {
	var ue *compile.UserError
	if errors.As(err, &ue) {
		return &compile.UserError{Code: ue.Code, Status: ue.Status, Detail: fmt.Sprintf("nested %q: %s", path, ue.Detail)}
	}
	return err
}

// checkOp rejects an operation rel does not offer on a row being inserted
// (update false) or updated.
func checkOp(rel *api.Relation, op string, update bool, path string) error {
	if !update && (op == opUpdate || op == opDelete || op == opDisconnect) {
		return errAt(CodeInvalidBody, path, "%q applies only to a row being updated (PATCH)", op)
	}
	if why := unavailable(rel, op, update); why != "" {
		return errAt(CodeUnknownRelation, path, "%q does not accept %q (%s)", rel.To.Path, op, why)
	}
	return nil
}

// opsObject reads the {"<op>": …} object of a nested key, rejecting keys
// that are not operations.
func opsObject(raw any, path, expected string) (map[string]any, error) {
	obj, ok := raw.(map[string]any)
	if !ok {
		return nil, errAt(CodeInvalidBody, path, "expected an object with %s", expected)
	}
	for _, k := range sortedKeys(obj) {
		if !slices.Contains(allOps, k) {
			return nil, errAt(CodeInvalidBody, path, "unknown key %q (expected %s)", k, expected)
		}
	}
	return obj, nil
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
	if err := g.checkForwardColumns(in.res, in.cols, in.nested, in.parent, in.path); err != nil {
		return "", err
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
		link, _, err := g.forward(n, nil, in.depth, in.path)
		if err != nil {
			return "", err
		}
		fills = append(fills, fillFrom(link, n.rel))
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
		g.addRows(t, name)
	}
	for _, n := range children {
		if err := g.backward(n, name, in.depth, in.path, false); err != nil {
			return "", err
		}
	}
	return name, nil
}

// checkForwardColumns rejects a row setting an FK column that one of its
// forward nested keys sets too, or that its parent row fills.
func (g *chain) checkForwardColumns(res *api.Resource, cols map[string]any, nested []nestedIn, parent *fkFill, path string) error {
	for _, n := range nested {
		if !n.rel.Forward {
			continue
		}
		for _, c := range n.rel.EqualityColumns() {
			if _, ok := cols[c]; ok {
				return errAt(CodeInvalidBody, path, "body sets both column %q and nested relation %q", keyFor(res, c), n.key)
			}
			if parent != nil && slices.Contains(parent.cols, c) {
				return errAt(CodeInvalidBody, path, "nested relation %q sets column %q, which is filled from the parent row", n.key, keyFor(res, c))
			}
		}
	}
	return nil
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
		if c.OmittableOnInsert() || covered[c.Name] {
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

// updateRow emits the CTEs for one existing row: its forward operations
// first, then its own UPDATE (or, when the patch sets no column, a
// SELECT … FOR UPDATE lock, which still needs the UPDATE privilege and
// passes the UPDATE policies), then the operations on its children.
func (g *chain) updateRow(in updIn) error {
	t := in.res.Table
	ref := compile.TableRefSQL(t.Schema, t.Name)
	if err := g.checkForwardColumns(in.res, in.cols, in.nested, nil, in.path); err != nil {
		return err
	}
	for _, n := range in.nested {
		if n.rel.Forward {
			continue
		}
		for _, pair := range n.rel.FK.JoinPairs() {
			if _, ok := in.cols[pair.RefColumn]; ok {
				return errAt(CodeInvalidBody, in.path, "the patch changes column %q, which nested relation %q matches its rows on", keyFor(in.res, pair.RefColumn), n.key)
			}
		}
	}

	patched := map[string]bool{}
	var sets, links []string
	for _, c := range t.Columns {
		v, ok := in.cols[c.Name]
		if !ok {
			continue
		}
		expr, err := g.c.InputValue(c, v, g.ps)
		if err != nil {
			return wrapErr(err, in.path)
		}
		sets = append(sets, compile.QuoteIdent(c.Name)+" = "+expr)
		patched[c.Name] = true
	}
	var children []nestedIn
	for _, n := range in.nested {
		if !n.rel.Forward {
			children = append(children, n)
			continue
		}
		link, clear, err := g.forward(n, &owner{res: in.res, loc: in.loc}, in.depth, in.path)
		if err != nil {
			return err
		}
		if link == "" && !clear {
			continue
		}
		for _, p := range n.rel.EqualityPairs() {
			if patched[p.Column] {
				return errAt(CodeInvalidBody, in.path, "column %q is set by more than one nested relation", keyFor(in.res, p.Column))
			}
			patched[p.Column] = true
			val := "NULL"
			if link != "" {
				val = "(SELECT " + compile.QuoteIdent(p.RefColumn) + " FROM " + link + ")"
			}
			sets = append(sets, compile.QuoteIdent(p.Column)+" = "+val)
		}
		if link != "" {
			links = append(links, "EXISTS (SELECT 1 FROM "+link+")")
		}
	}

	root := in.cte == "__mut"
	if len(sets) == 0 {
		var lock string
		if root {
			lock = "SELECT %%RETURNING%% FROM " + ref + "\nWHERE " + in.loc(ref) + "\nFOR UPDATE"
		} else {
			alias := g.alias("s")
			from := []string{ref + " " + alias}
			conds := []string{in.loc(alias)}
			if in.scope != nil {
				from = append(from, in.scope.from)
				conds = append(conds, in.scope.cond(alias))
			}
			lock = "SELECT " + alias + ".* FROM " + strings.Join(from, ", ") + "\nWHERE " + strings.Join(conds, " AND ") + "\nFOR UPDATE OF " + alias
		}
		g.ctes = append(g.ctes, in.cte+" AS (\n"+indent(lock)+"\n)")
	} else {
		conds := []string{in.loc(ref)}
		from := ""
		if in.scope != nil {
			from = "\nFROM " + in.scope.from
			conds = append(conds, in.scope.cond(ref))
		}
		conds = append(conds, links...)
		returning := ref + ".*"
		if root {
			returning = "%%RETURNING%%"
		}
		sql := "UPDATE " + ref + "\nSET " + strings.Join(sets, ", ") + from + "\nWHERE " + strings.Join(conds, " AND ") + "\nRETURNING " + returning
		g.ctes = append(g.ctes, in.cte+" AS (\n"+indent(sql)+"\n)")
		if !root {
			g.recordChange(t, in.cte, in.loc, patched, true)
		}
	}
	for _, n := range children {
		if err := g.backward(n, in.cte, in.depth, in.path, true); err != nil {
			return err
		}
	}
	return nil
}

// owner is the existing row whose forward nested key is resolved.
type owner struct {
	res *api.Resource
	loc locator
}

// forward resolves a forward nested key. create and connect produce the
// CTE holding the row the owner's FK then points at (link); disconnect
// clears the FK (clear); update and delete act on the row the owner
// references before the statement. own is nil for a row being inserted,
// which takes exactly one of create and connect.
func (g *chain) forward(n nestedIn, own *owner, depth int, path string) (link string, clear bool, err error) {
	p := childPath(path, n.key)
	update := own != nil
	expected := `exactly one of "create" or "connect"`
	if update {
		expected = `at most one of "create", "connect" or "disconnect", plus "update" or "delete"`
	}
	obj, err := opsObject(n.raw, p, expected)
	if err != nil {
		return "", false, err
	}
	setters := 0
	for _, op := range []string{opCreate, opConnect, opDisconnect} {
		if _, ok := obj[op]; ok {
			setters++
		}
	}
	switch {
	case !update && setters != 1:
		return "", false, errAt(CodeInvalidBody, p, `exactly one of "create" or "connect" is required`)
	case len(obj) == 0:
		return "", false, errAt(CodeInvalidBody, p, "expected %s", expected)
	case setters > 1:
		return "", false, errAt(CodeInvalidBody, p, `at most one of "create", "connect" or "disconnect" is allowed`)
	}
	if _, u := obj[opUpdate]; u {
		if _, d := obj[opDelete]; d {
			return "", false, errAt(CodeInvalidBody, p, `"update" and "delete" target the same row; send one of them`)
		}
	}
	to := n.rel.To
	for _, op := range sortedKeys(obj) {
		if err := checkOp(n.rel, op, update, p); err != nil {
			return "", false, err
		}
		if err := g.countOp(p); err != nil {
			return "", false, err
		}
		v := obj[op]
		switch op {
		case opCreate:
			if depth+1 > g.p.maxDepth {
				return "", false, errAt(CodeEmbedDepthExceeded, p, "nesting depth %d exceeds max_depth %d", depth+1, g.p.maxDepth)
			}
			row, ok := v.(map[string]any)
			if !ok {
				return "", false, errAt(CodeInvalidBody, p, `"create" must be a JSON object`)
			}
			cols, nested, err := g.splitRow(to, row, p, plan.POST)
			if err != nil {
				return "", false, err
			}
			link, err = g.insertRow(rowIn{res: to, cols: cols, nested: nested, depth: depth + 1, kind: "p", path: p})
			if err != nil {
				return "", false, err
			}
		case opConnect:
			link, err = g.connectParent(n, v, p)
			if err != nil {
				return "", false, err
			}
		case opDisconnect:
			if v != true {
				return "", false, errAt(CodeInvalidBody, p, `"disconnect" must be true`)
			}
			clear = true
		case opUpdate:
			if depth+1 > g.p.maxDepth {
				return "", false, errAt(CodeEmbedDepthExceeded, p, "nesting depth %d exceeds max_depth %d", depth+1, g.p.maxDepth)
			}
			row, ok := v.(map[string]any)
			if !ok || len(row) == 0 {
				return "", false, errAt(CodeInvalidBody, p, `"update" must be a non-empty JSON object (the patch of the referenced row)`)
			}
			up := p + ".update"
			cols, nested, err := g.splitRow(to, row, up, plan.PATCH)
			if err != nil {
				return "", false, err
			}
			cte := g.name("p")
			if err := g.updateRow(updIn{res: to, cols: cols, nested: nested, depth: depth + 1, cte: cte, path: up, loc: g.referenced(n.rel, own)}); err != nil {
				return "", false, err
			}
			g.guard(cte, guardLabel(up))
		case opDelete:
			if v != true {
				return "", false, errAt(CodeInvalidBody, p, `"delete" must be true`)
			}
			t := to.Table
			ref := compile.TableRefSQL(t.Schema, t.Name)
			loc := g.referenced(n.rel, own)
			cte := g.name("d")
			g.ctes = append(g.ctes, cte+" AS (\n"+indent("DELETE FROM "+ref+"\nWHERE "+loc(ref)+"\nRETURNING "+ref+".*")+"\n)")
			g.recordChange(t, cte, loc, nil, false)
			g.guard(cte, guardLabel(p+".delete"))
		}
	}
	return link, clear, nil
}

// referenced locates the row of rel's referenced table that the owner row
// references before the statement.
func (g *chain) referenced(rel *api.Relation, own *owner) locator {
	t := own.res.Table
	ownerRef := compile.TableRefSQL(t.Schema, t.Name)
	alias := g.alias("o")
	return func(ref string) string {
		return "EXISTS (SELECT 1 FROM " + ownerRef + " " + alias + " WHERE " + own.loc(alias) + " AND " + g.match(rel.FK, t, alias, ref) + ")"
	}
}

// match renders the join of fk between the referencing row at ref (of
// table t) and the referenced row at parent: equality on the plain pairs,
// overlap on the period pair of a PERIOD key.
func (g *chain) match(fk *introspect.ForeignKey, t *introspect.Table, ref, parent string) string {
	var conds []string
	for _, pair := range fk.JoinPairs() {
		l := ref + "." + compile.QuoteIdent(pair.Column)
		r := parent + "." + compile.QuoteIdent(pair.RefColumn)
		if pair.Op == introspect.JoinEqual {
			conds = append(conds, g.c.KeyEqualSQL(t.Column(pair.Column), l, r))
		} else {
			conds = append(conds, l+" && "+r)
		}
	}
	return strings.Join(conds, " AND ")
}

// connectParent emits `__p_n AS (SELECT * FROM t WHERE key = $n)`; the keys
// must be exactly the referenced columns, the primary key or one unique
// constraint of the related resource. A PERIOD relation names the
// referenced entity instead (the equality columns of the referenced key, or
// a row key), and its CTE selects those columns DISTINCT so one row is
// inserted however many periods the entity has. A guard requires a match.
func (g *chain) connectParent(n nestedIn, connect any, p string) (string, error) {
	to := n.rel.To
	refCols := n.rel.FK.RefColumns
	if n.rel.FK.Period {
		refCols = fillFrom("", n.rel).refCols
	}
	loc, err := g.keyLookup(to, connect, p, opConnect, refCols)
	if err != nil {
		return "", err
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
	g.ctes = append(g.ctes, name+" AS (\n"+indent("SELECT "+list+" FROM "+compile.TableRefSQL(to.Table.Schema, to.Table.Name)+"\nWHERE "+loc(""))+"\n)")
	g.guard(name, guardLabel(p+".connect"))
	return name, nil
}

// keyLookup reads the key object of an existing row of res (a connect
// target, or the where of a backward operation): exactly the primary key,
// one unique constraint, or one of extra, with non-null values. It returns
// the locator of that row, its values bound as parameters once.
func (g *chain) keyLookup(res *api.Resource, raw any, p, what string, extra ...[]string) (locator, error) {
	obj, ok := raw.(map[string]any)
	if !ok || len(obj) == 0 {
		return nil, errAt(CodeInvalidBody, p, "%q must be a JSON object naming the key columns of %q", what, res.Path)
	}
	byCol := map[string]any{}
	var colNames []string
	for _, key := range sortedKeys(obj) {
		f := res.Field(key)
		if f == nil {
			return nil, errAt(CodeInvalidBody, p, "unknown key %q in %q for %q", key, what, res.Path)
		}
		if obj[key] == nil {
			return nil, errAt(CodeInvalidBody, p, "%q key %q must not be null", what, key)
		}
		byCol[f.Column.Name] = obj[key]
		colNames = append(colNames, f.Column.Name)
	}
	if !keySetOK(res, colNames, extra...) {
		return nil, errAt(CodeInvalidBody, p, "%q keys must be the primary key or one unique constraint of %q", what, res.Path)
	}
	type term struct {
		col  *introspect.Column
		expr string
	}
	var terms []term
	for _, c := range res.Table.Columns {
		v, ok := byCol[c.Name]
		if !ok {
			continue
		}
		cv, err := parse.CoerceValue(g.built, c, v)
		if err != nil {
			return nil, wrapErr(err, p)
		}
		expr, err := g.c.InputValue(c, cv, g.ps)
		if err != nil {
			return nil, wrapErr(err, p)
		}
		terms = append(terms, term{c, expr})
	}
	return func(ref string) string {
		conds := make([]string, len(terms))
		for i, tm := range terms {
			col := compile.QuoteIdent(tm.col.Name)
			if ref != "" {
				col = ref + "." + col
			}
			conds[i] = g.c.KeyEqualSQL(tm.col, col, tm.expr)
		}
		return strings.Join(conds, " AND ")
	}, nil
}

// pathKey is the locator of the row a keyed request path addresses, bound
// as the core keyed DML binds it.
func (g *chain) pathKey(res *api.Resource, key *plan.Key) (locator, error) {
	if key == nil || key.Constraint == nil || len(key.Values) != len(key.Constraint.Columns) {
		return nil, fmt.Errorf("nested_mutations: PATCH without a complete key")
	}
	t := res.Table
	cols := make([]*introspect.Column, len(key.Values))
	exprs := make([]string, len(key.Values))
	for i, name := range key.Constraint.Columns {
		cols[i] = t.Column(name)
		if cols[i] == nil {
			return nil, fmt.Errorf("nested_mutations: key column %q not found on %s", name, t.Name)
		}
		exprs[i] = g.ps.Add(compile.CoerceInput(g.built, cols[i], key.Values[i]))
	}
	return func(ref string) string {
		conds := make([]string, len(cols))
		for i, c := range cols {
			conds[i] = g.c.KeyEqualSQL(c, ref+"."+compile.QuoteIdent(c.Name), exprs[i])
		}
		return strings.Join(conds, " AND ")
	}, nil
}

func keySetOK(r *api.Resource, cols []string, extra ...[]string) bool {
	for _, e := range extra {
		if sameSet(e, cols) {
			return true
		}
	}
	if pk := r.PrimaryKey(); pk != nil && !r.HiddenKey && sameSet(pk.Columns, cols) {
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

// backward resolves a backward nested key on the row in parentCTE: create
// inserts children referencing it, connect points existing rows at it, and
// on a row being updated, update, delete and disconnect act on the rows
// referencing it before the statement. Every operation takes a non-empty
// list.
func (g *chain) backward(n nestedIn, parentCTE string, depth int, path string, update bool) error {
	p := childPath(path, n.key)
	expected := `"create" and/or "connect" lists`
	if update {
		expected = `"connect", "create", "delete", "disconnect" and/or "update" lists`
	}
	obj, err := opsObject(n.raw, p, expected)
	if err != nil {
		return err
	}
	if len(obj) == 0 {
		return errAt(CodeInvalidBody, p, "expected %s", expected)
	}
	child := n.rel.To
	t := child.Table
	ref := compile.TableRefSQL(t.Schema, t.Name)
	for _, op := range sortedKeys(obj) {
		if err := checkOp(n.rel, op, update, p); err != nil {
			return err
		}
		list, ok := obj[op].([]any)
		if !ok || len(list) == 0 {
			return errAt(CodeInvalidBody, p, "%q must be a non-empty JSON array of objects", op)
		}
		if (op == opCreate || op == opUpdate) && depth+1 > g.p.maxDepth {
			return errAt(CodeEmbedDepthExceeded, p, "nesting depth %d exceeds max_depth %d", depth+1, g.p.maxDepth)
		}
		for i, item := range list {
			ep := p + "." + op + "[" + strconv.Itoa(i) + "]"
			if op == opCreate {
				ep = p + "[" + strconv.Itoa(i) + "]"
			}
			if err := g.countOp(ep); err != nil {
				return err
			}
			if op == opCreate {
				row, ok := item.(map[string]any)
				if !ok {
					return errAt(CodeInvalidBody, ep, "expected a JSON object")
				}
				cols, nested, err := g.splitRow(child, row, ep, plan.POST)
				if err != nil {
					return err
				}
				fill := fillFrom(parentCTE, n.rel)
				if _, err := g.insertRow(rowIn{res: child, cols: cols, nested: nested, depth: depth + 1, kind: "c", path: ep, parent: &fill}); err != nil {
					return err
				}
				continue
			}
			if op == opUpdate {
				if err := g.updateChild(n, item, parentCTE, depth, ep); err != nil {
					return err
				}
				continue
			}
			loc, err := g.keyLookup(child, item, ep, op)
			if err != nil {
				return err
			}
			cte := g.name("c")
			match := g.match(n.rel.FK, t, ref, parentCTE)
			patched := map[string]bool{}
			var sets []string
			for _, pair := range n.rel.EqualityPairs() {
				patched[pair.Column] = true
				val := "NULL"
				if op == opConnect {
					val = parentCTE + "." + compile.QuoteIdent(pair.RefColumn)
				}
				sets = append(sets, compile.QuoteIdent(pair.Column)+" = "+val)
			}
			var sql string
			switch op {
			case opConnect:
				sql = "UPDATE " + ref + "\nSET " + strings.Join(sets, ", ") + "\nFROM " + parentCTE + "\nWHERE " + loc(ref)
			case opDisconnect:
				sql = "UPDATE " + ref + "\nSET " + strings.Join(sets, ", ") + "\nFROM " + parentCTE + "\nWHERE " + loc(ref) + " AND " + match
			case opDelete:
				sql = "DELETE FROM " + ref + "\nUSING " + parentCTE + "\nWHERE " + loc(ref) + " AND " + match
				patched = nil
			}
			g.ctes = append(g.ctes, cte+" AS (\n"+indent(sql+"\nRETURNING "+ref+".*")+"\n)")
			g.recordChange(t, cte, loc, patched, op != opDelete)
			if op != opDisconnect {
				g.guard(cte, guardLabel(ep))
			}
		}
	}
	return nil
}

// updateChild handles one {"where": {key}, "data": {patch}} item of a
// backward update: the child the key names, among the rows referencing the
// parent, is patched (and its own nested keys resolved).
func (g *chain) updateChild(n nestedIn, item any, parentCTE string, depth int, ep string) error {
	obj, ok := item.(map[string]any)
	if !ok {
		return errAt(CodeInvalidBody, ep, `expected a JSON object with "where" and "data"`)
	}
	for _, k := range sortedKeys(obj) {
		if k != "where" && k != "data" {
			return errAt(CodeInvalidBody, ep, `unknown key %q (expected "where" and "data")`, k)
		}
	}
	data, ok := obj["data"].(map[string]any)
	if !ok || len(data) == 0 {
		return errAt(CodeInvalidBody, ep, `"data" must be a non-empty JSON object (the patch of the row)`)
	}
	if _, ok := obj["where"]; !ok {
		return errAt(CodeInvalidBody, ep, `"where" is required (the key of the row)`)
	}
	child := n.rel.To
	loc, err := g.keyLookup(child, obj["where"], ep, "where")
	if err != nil {
		return err
	}
	cols, nested, err := g.splitRow(child, data, ep, plan.PATCH)
	if err != nil {
		return err
	}
	for _, pair := range n.rel.EqualityPairs() {
		if _, ok := cols[pair.Column]; ok {
			return errAt(CodeInvalidBody, ep, "column %q links the row to its parent and cannot be set here (use connect or disconnect)", keyFor(child, pair.Column))
		}
	}
	cte := g.name("c")
	sc := &scope{from: parentCTE, cond: func(ref string) string { return g.match(n.rel.FK, child.Table, ref, parentCTE) }}
	if err := g.updateRow(updIn{res: child, cols: cols, nested: nested, depth: depth + 1, cte: cte, path: ep, loc: loc, scope: sc}); err != nil {
		return err
	}
	g.guard(cte, guardLabel(ep))
	return nil
}

// splitRow separates a nested row into its column values (coerced by the
// core body rules of method) and its nested keys, mirroring the parser's
// precedence: a column key always wins over a relation key of the same
// name.
func (g *chain) splitRow(res *api.Resource, row map[string]any, path string, method plan.Method) (map[string]any, []nestedIn, error) {
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
	cols, err := parse.CoerceObjectFor(g.built, res, plain, method)
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
