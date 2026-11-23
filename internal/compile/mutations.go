package compile

import (
	"fmt"
	"strings"

	"github.com/suprbdev/pdbcore/contrib"
	"github.com/suprbdev/pdbcore/introspect"
	"github.com/suprbdev/pdbcore/postgis"

	"github.com/suprbdev/pdbr/internal/api"
	"github.com/suprbdev/pdbr/internal/plan"
)

// returningPlaceholder marks where the DML's RETURNING list goes; the
// response compiler replaces it with exactly the columns it references
// through the __mut row source (RETURNING needs SELECT privilege per
// projected column, so `RETURNING *` would break mutations for roles with
// column-level grants).
const returningPlaceholder = "%%RETURNING%%"

// mutation compiles POST/PUT/PATCH/DELETE: the DML runs in a
// `WITH __mut AS (... RETURNING ...)` CTE and the response is compiled
// against __mut like a regular row source (embeds are laterals correlated
// on __mut columns). If-Match adds a `cur` CTE hashing the current row so
// 404 and 412 resolve in the same statement.
//
// Every table scan of the statement shares its snapshot, so an embed
// leading back to the mutated table would read it as it stood before the
// DML. Such embeds read a visibility CTE instead (see visSource.cte).
func (s *stmt) mutation() (*Statement, error) {
	p := s.plan
	if p.Body == nil && p.Method != plan.DELETE {
		return nil, fmt.Errorf("compile: %s without a body", p.Method)
	}
	var ctes []string
	var dml string
	var rep replaced
	var err error
	switch p.Method {
	case plan.POST:
		var keys map[string]string
		dml, keys = s.insertDML()
		dml += s.resolutionSQL()
		if p.Prefer.Resolution == plan.MergeDuplicates && p.OnConflict != nil {
			rep = s.conflictReplaced(p.OnConflict, keys)
		}
	case plan.PUT:
		dml, rep, err = s.upsertDML()
	case plan.PATCH, plan.DELETE:
		if p.Key == nil {
			dml, rep, err = s.bulkDML()
			break
		}
		var cur string
		cur, dml, rep, err = s.keyedDML()
		if cur != "" {
			ctes = append(ctes, cur)
		}
	default:
		return nil, fmt.Errorf("compile: unsupported mutation %q", p.Method)
	}
	if err != nil {
		return nil, err
	}
	ctes = append(ctes, "__mut AS (\n"+indent(dml+"\nRETURNING "+returningPlaceholder)+"\n)")
	return s.mutationResponse(strings.Join(ctes, ",\n"), rep, nil)
}

// visCTE names the visibility CTE of the mutated table; those of other
// tables a caller's chain inserted into are numbered after it.
const visCTE = "__vis"

// visSource is one visibility CTE of a mutation response, standing in for
// table wherever the response reads it.
type visSource struct {
	table *introspect.Table
	// root marks the mutated table: the CTE merges in __mut, minus the
	// pre-statement versions of the rows the DML replaced.
	root bool
	// rows lists further CTEs of the chain holding rows that are in table
	// once the statement completes (nested_mutations), each projecting
	// every column; keep restricts the table half to the rows the chain
	// left alone.
	rows []string
	keep []string
	// name is allocated on first read; aliases records every alias the
	// CTE is read under.
	name    string
	aliases []string
}

// replaced identifies, for the visibility CTE of the mutated table, the
// rows the DML replaced as they stood before it ran.
type replaced struct {
	// from is the FROM item of the CTE's table half: the bare table, or the
	// table under the alias cond refers to.
	from string
	// cond keeps the table rows the DML left alone ("" keeps all of them).
	cond string
	// removes marks a DELETE: __mut's rows are not merged back.
	removes bool
}

// mutationResponse compiles the response half of a mutation over a CTE
// chain (which must define __mut) and assembles the statement. rep says
// which rows of the mutated table __mut replaced; writes maps
// "schema.table" to the chain's other writes to that table. Both feed the
// visibility CTEs, which are emitted only for the tables the response
// actually reads.
func (s *stmt) mutationResponse(ctes string, rep replaced, writes map[string]TableWrites) (*Statement, error) {
	p := s.plan
	r := p.Resource
	t := r.Table
	s.vis = map[string]*visSource{}
	for name, w := range writes {
		if tbl := s.tableByName(name); tbl != nil {
			s.vis[name] = &visSource{table: tbl, rows: w.Rows, keep: w.Keep}
		}
	}
	root := t.Schema + "." + t.Name
	if v := s.vis[root]; v != nil {
		v.root = true
	} else {
		s.vis[root] = &visSource{table: t, root: true}
	}
	bulk := p.Body != nil && p.Body.Objects != nil
	minimal := p.Prefer.Minimal()
	ifMatch := s.ifMatchTags() != nil
	alias := s.alias("t")

	var keyExpr string
	if pk := t.PrimaryKey; pk != nil && len(pk.Columns) == 1 && !bulk && (p.Method == plan.POST || p.Method == plan.PUT) {
		keyExpr = "jsonb_build_array(" + colRef(alias, pk.Columns[0]) + ")"
	}
	insertedExpr := ""
	if p.Method == plan.PUT && !bulk {
		insertedExpr = alias + ".__inserted"
	}

	var body string
	shape := ShapeMutWrap
	switch {
	case p.Key == nil && (p.Method == plan.PATCH || p.Method == plan.DELETE):
		shape = ShapeMutBulk
		if minimal {
			body = "SELECT json_build_object('n', (SELECT count(*) FROM __mut)) AS data"
			break
		}
		jsonExpr, laterals, err := s.rowJSON(s.rootNode(), alias, 0)
		if err != nil {
			return nil, err
		}
		sub := s.alias("s")
		body = fmt.Sprintf("SELECT json_build_object('n', count(*), 'rows', coalesce(json_agg(%s.data), '[]'::json)) AS data\nFROM (\n%s\n) %s",
			sub, indent(fmt.Sprintf("SELECT %s AS data\nFROM __mut %s%s", jsonExpr, alias, joinLaterals(laterals))), sub)
	case minimal && ifMatch:
		body = fmt.Sprintf("SELECT %s AS data", buildJSONObject([]string{
			"'found'", "EXISTS (SELECT 1 FROM cur)",
			"'row'", "(SELECT to_json(true) FROM __mut " + alias + ")",
		}))
	case minimal:
		pairs := []string{"'n'", "(SELECT count(*) FROM __mut)"}
		if keyExpr != "" {
			pairs = append(pairs, "'key'", "(SELECT "+keyExpr+" FROM __mut "+alias+")")
		}
		if insertedExpr != "" {
			pairs = append(pairs, "'inserted'", "(SELECT "+insertedExpr+" FROM __mut "+alias+")")
		}
		body = fmt.Sprintf("SELECT %s AS data", buildJSONObject(pairs))
	default:
		jsonExpr, laterals, err := s.rowJSON(s.rootNode(), alias, 0)
		if err != nil {
			return nil, err
		}
		from := "FROM __mut " + alias + joinLaterals(laterals)
		switch {
		case bulk:
			shape = ShapeMutList
			sub := s.alias("s")
			body = fmt.Sprintf("SELECT coalesce(json_agg(%s.data), '[]'::json) AS data\nFROM (\n%s\n) %s",
				sub, indent(fmt.Sprintf("SELECT %s AS data\n%s", jsonExpr, from)), sub)
		case ifMatch:
			body = fmt.Sprintf("SELECT %s AS data", buildJSONObject([]string{
				"'found'", "EXISTS (SELECT 1 FROM cur)",
				"'row'", fmt.Sprintf("(\n%s\n)", indent(fmt.Sprintf("SELECT %s\n%s", jsonExpr, from))),
			}))
		case keyExpr == "" && insertedExpr == "":
			shape = ShapeMutRow
			body = fmt.Sprintf("SELECT %s AS data\n%s", jsonExpr, from)
		default:
			var pairs []string
			if insertedExpr != "" {
				pairs = append(pairs, "'inserted'", insertedExpr)
			}
			if keyExpr != "" {
				pairs = append(pairs, "'key'", keyExpr)
			}
			pairs = append(pairs, "'row'", jsonExpr)
			body = fmt.Sprintf("SELECT %s AS data\n%s", buildJSONObject(pairs), from)
		}
	}
	sql := stripLiterals(ctes + "\n" + body)
	for _, v := range s.visOrder {
		ctes += ",\n" + v.cte(referencedColumns(v.table, sql, v.aliases), rep)
	}
	ctes = strings.ReplaceAll(ctes, returningPlaceholder, returningList(t, ctes+"\n"+body, alias, p.Method == plan.PUT))
	if len(s.guards) > 0 {
		ctes += ",\n__guard AS MATERIALIZED (\n  SELECT " + strings.Join(s.guards, "\n    AND ") + " AS ok\n)"
		body = "SELECT __guarded.data\nFROM __guard\nLEFT JOIN LATERAL (\n" + indent(body) + "\n) __guarded ON true\nWHERE __guard.ok"
	}
	return &Statement{SQL: "WITH " + ctes + "\n" + body, Args: s.ps.Args(), Mutation: true, Shape: shape}, nil
}

// tableByName returns the table of the resource keyed "schema.table", or nil.
func (s *stmt) tableByName(name string) *introspect.Table {
	if s.c.Built == nil {
		return nil
	}
	if res := s.c.Built.Resources[name]; res != nil {
		return res.Table
	}
	return nil
}

// cte renders the visibility CTE: the table as it stands once the statement
// completes, projected to cols. That is the table itself (still subject to
// the role's RLS policies) minus, for the mutated table, the rows rep says
// the DML replaced, minus the rows a chain's keep conditions exclude, UNION
// ALL the RETURNING rows of __mut unless the DML deleted them, UNION ALL the
// rows of the chain's other CTEs for the table. RETURNING rows already
// passed the table's SELECT policies: INSERT and UPDATE ... RETURNING raise
// an error for a new row they reject.
//
// The __mut half reads its columns as __mut."col", so the RETURNING list
// widens to them (as it does to the key columns a bulk exclusion reads).
// NOT MATERIALIZED inlines the CTE at every reference, so the correlated
// embed predicates reach the table's indexes instead of a materialized copy
// of the whole table.
func (v *visSource) cte(cols []string, rep replaced) string {
	bare := make([]string, len(cols))
	fromMut := make([]string, len(cols))
	for i, c := range cols {
		bare[i] = quoteIdent(c)
		fromMut[i] = colRef("__mut", c)
	}
	list := func(cols []string) string {
		if len(cols) == 0 {
			return "1"
		}
		return strings.Join(cols, ", ")
	}
	from := tableRef(v.table)
	var conds []string
	if v.root {
		if rep.from != "" {
			from = rep.from
		}
		if rep.cond != "" {
			conds = append(conds, rep.cond)
		}
	}
	conds = append(conds, v.keep...)
	where := ""
	if len(conds) > 0 {
		where = " WHERE " + strings.Join(conds, " AND ")
	}
	parts := []string{"SELECT " + list(bare) + " FROM " + from + where}
	if v.root && !rep.removes {
		parts = append(parts, "SELECT "+list(fromMut)+" FROM __mut")
	}
	for _, rows := range v.rows {
		parts = append(parts, "SELECT "+list(bare)+" FROM "+rows)
	}
	return fmt.Sprintf("%s AS NOT MATERIALIZED (\n  %s\n)", v.name, strings.Join(parts, "\n  UNION ALL "))
}

// referencedColumns returns, in table order, the columns of t that sql
// references through one of aliases. Every column reference is rendered as
// `<alias>."col"` (column projections, embed correlations, filters,
// computed-column ROW(...) casts), so a substring scan is exact.
func referencedColumns(t *introspect.Table, sql string, aliases []string) []string {
	var cols []string
	for _, c := range t.Columns {
		q := quoteIdent(c.Name)
		for _, a := range aliases {
			if strings.Contains(sql, a+"."+q) {
				cols = append(cols, c.Name)
				break
			}
		}
	}
	return cols
}

// conflictReplaced identifies the rows an upsert on target updated: the
// table rows whose conflict key equals the input's (keys holds each
// target column's input expression of a single-row body), or, for a bulk
// body or one leaving a target column to its default, the rows whose
// conflict key __mut returned.
func (s *stmt) conflictReplaced(target *introspect.Constraint, keys map[string]string) replaced {
	t := s.plan.Resource.Table
	conds := make([]string, len(target.Columns))
	for i, name := range target.Columns {
		expr, ok := keys[name]
		if !ok {
			return replaced{cond: s.notInMut(t, target.Columns, tableRef(t))}
		}
		conds[i] = s.keyEqSQL(t.Column(name), colRef(tableRef(t), name), expr)
	}
	return replaced{cond: "(" + strings.Join(conds, " AND ") + ") IS NOT TRUE"}
}

// notInMut renders NOT EXISTS over the __mut rows whose key columns equal
// those of the table row read under ref.
func (s *stmt) notInMut(t *introspect.Table, key []string, ref string) string {
	conds := make([]string, len(key))
	for i, name := range key {
		conds[i] = s.keyEqSQL(t.Column(name), colRef("__mut", name), colRef(ref, name))
	}
	return "NOT EXISTS (SELECT 1 FROM __mut WHERE " + strings.Join(conds, " AND ") + ")"
}

// stableKey returns the columns of the first row key of t (primary key,
// then unique constraints) that are all NOT NULL and untouched by patched,
// or nil.
func stableKey(t *introspect.Table, patched []*introspect.Column) []string {
	touched := make(map[string]bool, len(patched))
	for _, c := range patched {
		touched[c.Name] = true
	}
	keys := append([]*introspect.Constraint{t.PrimaryKey}, t.Uniques...)
	for _, con := range keys {
		if con == nil || len(con.Columns) == 0 {
			continue
		}
		ok := true
		for _, name := range con.Columns {
			c := t.Column(name)
			if c == nil || touched[name] || (!c.NotNull && con != t.PrimaryKey) {
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

// returningList renders the RETURNING list: every table column referenced
// as `<alias>."col"` or `__mut."col"` anywhere in the statement text (the
// response projection, lateral correlations, computed-column ROW(...) casts,
// caller CTEs reading __mut), plus `(xmax = 0) AS __inserted` when an
// upsert response reads it. Nothing referenced yields `1` so the DML still
// reports its row count.
func returningList(t *introspect.Table, sql, alias string, upsert bool) string {
	// User-controlled text (response JSON keys) only ever enters SQL inside
	// single-quoted literals; strip them so an alias like `t0."balance"` cannot
	// widen RETURNING.
	sql = stripLiterals(sql)
	var cols []string
	for _, c := range t.Columns {
		q := quoteIdent(c.Name)
		if strings.Contains(sql, alias+"."+q) || strings.Contains(sql, "__mut."+q) {
			cols = append(cols, q)
		}
	}
	if upsert && strings.Contains(sql, "__inserted") {
		cols = append(cols, "(xmax = 0) AS __inserted")
	}
	if len(cols) == 0 {
		return "1"
	}
	return strings.Join(cols, ", ")
}

// insertDML renders the INSERT for a single or bulk POST body: the column
// list is the union of provided columns in catalog order, missing values
// in a bulk tuple insert DEFAULT, and an empty object inserts DEFAULT VALUES.
// For a single-object body it also returns the input expression of every
// provided column by name.
func (s *stmt) insertDML() (string, map[string]string) {
	t := s.plan.Resource.Table
	body := s.plan.Body
	cols := body.Columns
	if body.Objects == nil {
		if len(cols) == 0 {
			return fmt.Sprintf("INSERT INTO %s DEFAULT VALUES", tableRef(t)), nil
		}
		tuple, exprs := s.tuple(cols, body.Object)
		return fmt.Sprintf("INSERT INTO %s (%s)\nVALUES %s", tableRef(t), columnList(cols), tuple), exprs
	}
	if len(cols) == 0 && len(t.Columns) > 0 {
		// A bulk body of empty objects: DEFAULT VALUES inserts one row, so
		// name one column and default it per tuple instead.
		cols = t.Columns[:1]
	}
	tuples := make([]string, len(body.Objects))
	for i, obj := range body.Objects {
		tuples[i] = s.tupleSQL(cols, obj)
	}
	return fmt.Sprintf("INSERT INTO %s (%s)\nVALUES %s", tableRef(t), columnList(cols), strings.Join(tuples, ", ")), nil
}

// resolutionSQL renders the ON CONFLICT clause of a POST under Prefer:
// resolution=: merge-duplicates updates like PUT, ignore-duplicates skips
// conflicting rows (on any unique constraint when the table has no primary
// key and no ?on_conflict= names one). A temporal (WITHOUT OVERLAPS) target
// is an exclusion constraint: ignore-duplicates names it with ON CONSTRAINT,
// the only form PostgreSQL accepts, and merge-duplicates is refused.
func (s *stmt) resolutionSQL() string {
	p := s.plan
	switch p.Prefer.Resolution {
	case plan.MergeDuplicates:
		if p.OnConflict == nil {
			s.fail(errUser(CodeInvalidBody, "resolution=merge-duplicates needs a primary key or ?on_conflict="))
			return ""
		}
		if p.OnConflict.WithoutOverlaps {
			s.fail(errUser(CodeTemporalConflict, "a temporal key (WITHOUT OVERLAPS) cannot be updated on conflict"))
			return ""
		}
		return "\n" + conflictUpdate(p.OnConflict, p.Body.Columns)
	case plan.IgnoreDuplicates:
		if p.OnConflict == nil {
			return "\nON CONFLICT DO NOTHING"
		}
		if p.OnConflict.WithoutOverlaps {
			return "\nON CONFLICT ON CONSTRAINT " + quoteIdent(p.OnConflict.Name) + " DO NOTHING"
		}
		return "\nON CONFLICT (" + strings.Join(quotedNames(p.OnConflict.Columns), ", ") + ") DO NOTHING"
	}
	return ""
}

// conflictUpdate renders ON CONFLICT (target) DO UPDATE SET c = EXCLUDED.c
// for every provided non-target column, or the target onto itself when the
// body carries nothing else (DO NOTHING would return zero rows).
func conflictUpdate(target *introspect.Constraint, cols []*introspect.Column) string {
	isTarget := map[string]bool{}
	for _, c := range target.Columns {
		isTarget[c] = true
	}
	var sets []string
	for _, c := range cols {
		if !isTarget[c.Name] {
			sets = append(sets, quoteIdent(c.Name)+" = EXCLUDED."+quoteIdent(c.Name))
		}
	}
	if len(sets) == 0 {
		for _, c := range target.Columns {
			sets = append(sets, quoteIdent(c)+" = EXCLUDED."+quoteIdent(c))
		}
	}
	return "ON CONFLICT (" + strings.Join(quotedNames(target.Columns), ", ") + ") DO UPDATE\nSET " + strings.Join(sets, ", ")
}

func quotedNames(names []string) []string {
	out := make([]string, len(names))
	for i, n := range names {
		out[i] = quoteIdent(n)
	}
	return out
}

// upsertDML renders INSERT ... ON CONFLICT (target) DO UPDATE SET c =
// EXCLUDED.c for every provided non-target column; a body carrying only the
// target updates the target to itself so the conflict branch still returns
// the existing row (DO NOTHING would yield zero rows).
func (s *stmt) upsertDML() (string, replaced, error) {
	t := s.plan.Resource.Table
	body := s.plan.Body
	target := s.plan.OnConflict
	if target == nil {
		target = t.PrimaryKey
	}
	if target == nil {
		return "", replaced{}, errUser(CodeInvalidBody, "upsert needs a primary key or ?on_conflict=")
	}
	if target.WithoutOverlaps {
		return "", replaced{}, errUser(CodeTemporalConflict, "a temporal key (WITHOUT OVERLAPS) cannot be a conflict target")
	}
	cols := body.Columns
	if len(cols) == 0 {
		return "", replaced{}, errUser(CodeInvalidBody, "upsert body must set at least the conflict target columns")
	}
	var tuples []string
	var keys map[string]string
	if body.Objects == nil {
		var tuple string
		tuple, keys = s.tuple(cols, body.Object)
		tuples = []string{tuple}
	} else {
		tuples = make([]string, len(body.Objects))
		for i, obj := range body.Objects {
			tuples[i] = s.tupleSQL(cols, obj)
		}
	}
	return fmt.Sprintf("INSERT INTO %s (%s)\nVALUES %s\n%s",
		tableRef(t), columnList(cols), strings.Join(tuples, ", "), conflictUpdate(target, cols)), s.conflictReplaced(target, keys), nil
}

// keyedDML renders the UPDATE / DELETE of a keyed path plus, under
// If-Match, the `cur` CTE hashing the current row (all columns, like the
// GET ETag) and the hash predicate on the DML. The rows it replaced are
// those matching its key lookup (placeholders reused), which still matches
// the old version when the patch changes the key.
func (s *stmt) keyedDML() (cur, dml string, rep replaced, err error) {
	p := s.plan
	t := p.Resource.Table
	key := p.Key
	if key == nil || key.Constraint == nil {
		return "", "", rep, fmt.Errorf("compile: %s without a key", p.Method)
	}
	if len(key.Values) != len(key.Constraint.Columns) {
		return "", "", rep, errUser(CodeInvalidParameter, "key has %d values, want %d", len(key.Values), len(key.Constraint.Columns))
	}
	var sets []string
	if p.Method == plan.PATCH {
		if len(p.Body.Columns) == 0 {
			return "", "", rep, errUser(CodeInvalidBody, "PATCH body must set at least one column")
		}
		for _, c := range p.Body.Columns {
			sets = append(sets, quoteIdent(c.Name)+" = "+s.inputValue(c, p.Body.Object[c.Name]))
		}
	}
	placeholders := make([]string, len(key.Values))
	for i, name := range key.Constraint.Columns {
		col := t.Column(name)
		if col == nil {
			return "", "", rep, fmt.Errorf("compile: key column %q not found on %s", name, t.Name)
		}
		placeholders[i] = s.param(coerceInput(col, key.Values[i]))
	}
	keyConds := func(alias string) string {
		conds := make([]string, len(placeholders))
		for i, name := range key.Constraint.Columns {
			conds[i] = s.keyEqSQL(t.Column(name), colRef(alias, name), placeholders[i])
		}
		return strings.Join(conds, " AND ")
	}
	where := keyConds(tableRef(t))
	rep = replaced{cond: "(" + where + ") IS NOT TRUE", removes: p.Method == plan.DELETE}
	if tags := s.ifMatchTags(); tags != nil {
		hash, err := s.rowHashSQL(p.Resource, "__cur")
		if err != nil {
			return "", "", rep, err
		}
		cur = fmt.Sprintf("cur AS (\n  SELECT %s AS h\n  FROM %s __cur\n  WHERE %s\n)", hash, tableRef(t), keyConds("__cur"))
		switch {
		case tags.star:
		case len(tags.tags) == 1:
			where += "\n  AND (SELECT h FROM cur) = " + s.param(tags.tags[0])
		default:
			where += "\n  AND (SELECT h FROM cur) = ANY(" + s.param(tags.tags) + ")"
		}
	}
	if p.Method == plan.DELETE {
		return cur, fmt.Sprintf("DELETE FROM %s\nWHERE %s", tableRef(t), where), rep, nil
	}
	return cur, fmt.Sprintf("UPDATE %s\nSET %s\nWHERE %s", tableRef(t), strings.Join(sets, ", "), where), rep, nil
}

// bulkDML renders the UPDATE / DELETE of a list route over exactly the rows
// the same GET would list: the root filter plus one EXISTS per !inner embed,
// compiled against an aliased target (no WHERE at all when
// mutations.allow_unfiltered let an unrestricted request through).
//
// An UPDATE or DELETE policy narrower than the SELECT one may skip rows the
// filter matched, so the rows it replaced are those whose key __mut
// returned, by a NOT NULL row key the patch leaves alone. Only when every
// such key is patched (or the table has none) do they fall back to the
// rows the filter matches.
func (s *stmt) bulkDML() (string, replaced, error) {
	p := s.plan
	t := p.Resource.Table
	alias := s.alias("m")
	var conds []string
	if p.Filter != nil {
		cond, err := s.filterSQL(p.Filter, alias)
		if err != nil {
			return "", replaced{}, err
		}
		if cond == "" {
			return "", replaced{}, fmt.Errorf("compile: %s filter on %s rendered no condition", p.Method, t.Name)
		}
		conds = append(conds, cond)
	}
	node := s.rootNode()
	for i := range node.Embeds {
		e := &node.Embeds[i]
		if !e.Inner || e.Relation == nil {
			continue
		}
		cond, err := s.innerExists(e, alias)
		if err != nil {
			return "", replaced{}, err
		}
		conds = append(conds, cond)
	}
	where := ""
	if len(conds) > 0 {
		where = "\nWHERE " + strings.Join(conds, " AND ")
	}
	var patched []*introspect.Column
	if p.Body != nil {
		patched = p.Body.Columns
	}
	rep := replaced{removes: p.Method == plan.DELETE}
	switch key := stableKey(t, patched); {
	case key != nil:
		rep.cond = s.notInMut(t, key, tableRef(t))
	case len(conds) > 0:
		rep.from = tableRef(t) + " " + alias
		rep.cond = "(" + strings.Join(conds, " AND ") + ") IS NOT TRUE"
	default:
		rep.cond = "false"
	}
	if p.Method == plan.DELETE {
		return fmt.Sprintf("DELETE FROM %s %s%s", tableRef(t), alias, where), rep, nil
	}
	if p.Body == nil || len(p.Body.Columns) == 0 {
		return "", replaced{}, errUser(CodeInvalidBody, "PATCH body must set at least one column")
	}
	sets := make([]string, len(p.Body.Columns))
	for i, c := range p.Body.Columns {
		sets[i] = quoteIdent(c.Name) + " = " + s.inputValue(c, p.Body.Object[c.Name])
	}
	return fmt.Sprintf("UPDATE %s %s\nSET %s%s", tableRef(t), alias, strings.Join(sets, ", "), where), rep, nil
}

func columnList(cols []*introspect.Column) string {
	quoted := make([]string, len(cols))
	for i, c := range cols {
		quoted[i] = quoteIdent(c.Name)
	}
	return strings.Join(quoted, ", ")
}

// tupleSQL renders one VALUES tuple over cols; columns the object does not
// provide insert DEFAULT.
func (s *stmt) tupleSQL(cols []*introspect.Column, obj map[string]any) string {
	tuple, _ := s.tuple(cols, obj)
	return tuple
}

// tuple is tupleSQL also returning the input expression of every provided
// column by name.
func (s *stmt) tuple(cols []*introspect.Column, obj map[string]any) (string, map[string]string) {
	exprs := make([]string, len(cols))
	byName := make(map[string]string, len(cols))
	for i, c := range cols {
		v, ok := obj[c.Name]
		if !ok {
			exprs[i] = "DEFAULT"
			continue
		}
		exprs[i] = s.inputValue(c, v)
		byName[c.Name] = exprs[i]
	}
	return "(" + strings.Join(exprs, ", ") + ")", byName
}

// inputValue renders a body value as a parameter expression for col:
// composites are populated from a jsonb parameter, spatial values go
// through their PostGIS constructor, hstore objects and structured ranges
// through their pdbcore/contrib codec, vectors (lists of numbers, text
// forms and nulls alike) as one text parameter cast to the vector type,
// arrays and json/jsonb carry an explicit cast, everything else is a bare
// parameter typed by the target column. The expression depends only on the
// column type (and, for spatial, hstore and range values, the input
// encoding), never on the value.
func (s *stmt) inputValue(col *introspect.Column, v any) string {
	if comp := s.compositeFor(col); comp != nil {
		return s.compositeInput(comp, col, v)
	}
	if f := postgis.FlavourOf(col.PGType); f.Spatial() {
		expr, err := s.spatialInput(f, spatialSRID(col), col.IsArray, v)
		if err != nil {
			s.fail(err)
			return "NULL"
		}
		return expr
	}
	if col.IsArray {
		return s.param(coerceInput(col, v)) + arrayCast(s.c.Built, col)
	}
	if codec, ok := api.CodecFor(s.catalog(), col); ok {
		if codec.Arg == contrib.ArgVector {
			if v == nil {
				return codec.SQL(s.param(nil))
			}
			params, err := codec.Params(v)
			if err != nil {
				s.fail(errUser(CodeInvalidBody, "%s: %s", col.Name, err.Error()))
				return "NULL"
			}
			return codec.SQL(s.params(params)...)
		}
		switch v.(type) {
		case map[string]any, []any:
			params, err := codec.Params(v)
			if err != nil {
				s.fail(errUser(CodeInvalidBody, "%s: %s", col.Name, err.Error()))
				return "NULL"
			}
			return codec.SQL(s.params(params)...)
		}
	}
	switch base := strings.TrimPrefix(col.PGType, "_"); base {
	case "json", "jsonb":
		return s.param(coerceInput(col, v)) + "::" + base
	}
	return s.param(coerceInput(col, v))
}

// compositeInput renders a composite (or composite-array) value from one
// jsonb parameter holding the object (or array of objects) as JSON text.
func (s *stmt) compositeInput(comp *introspect.Composite, col *introspect.Column, v any) string {
	return s.populateSQL(quoteIdent(comp.Schema)+"."+quoteIdent(comp.Name), col.IsArray, compositeText(v))
}

// populateSQL renders a value of the composite or row type typeRef (an
// array of it when isArray) from one jsonb parameter holding text, the
// object or array of objects as JSON.
func (s *stmt) populateSQL(typeRef string, isArray bool, text any) string {
	ph := s.param(text)
	if isArray {
		// Element-wise population keeps null elements (populate_recordset
		// rejects them) and the array order explicit.
		elem := s.alias("e")
		return fmt.Sprintf("CASE WHEN %s::jsonb IS NULL THEN NULL ELSE coalesce((SELECT array_agg(jsonb_populate_record(NULL::%s, NULLIF(%s.v, 'null'::jsonb)) ORDER BY %s.o) FROM jsonb_array_elements(%s::jsonb) WITH ORDINALITY %s(v, o)), ARRAY[]::%s[]) END",
			ph, typeRef, elem, elem, ph, elem, typeRef)
	}
	return fmt.Sprintf("jsonb_populate_record(NULL::%s, %s::jsonb)", typeRef, ph)
}

// compositeText normalises a composite input to JSON text (parse hands
// bodies over as text already; decoded values are marshalled).
func compositeText(v any) any {
	switch x := v.(type) {
	case nil:
		return nil
	case string:
		return x
	case []byte:
		return string(x)
	}
	return toJSONParam(v)
}

// spatialInput renders a geometry/geography value (postgis.Value from the
// parser, or raw GeoJSON / WKT) through its constructor; arrays become an
// ARRAY[...] of constructors. NULL is a typed null parameter.
func (s *stmt) spatialInput(f postgis.Flavour, srid int, isArray bool, v any) (string, error) {
	one := func(item any) (string, error) {
		if item == nil {
			return s.param(nil) + "::" + f.String(), nil
		}
		val, ok := item.(postgis.Value)
		if !ok {
			var err error
			val, err = postgis.ParseFor(item, f, srid)
			if err != nil {
				return "", errUser(CodeInvalidBody, "%s", err.Error())
			}
		}
		return val.SQL(s.param(val.Param)), nil
	}
	if !isArray {
		return one(v)
	}
	if v == nil {
		return s.param(nil) + "::" + f.String() + "[]", nil
	}
	list, ok := v.([]any)
	if !ok {
		return "", errUser(CodeInvalidBody, "spatial array value must be a JSON array")
	}
	parts := make([]string, len(list))
	for i, item := range list {
		expr, err := one(item)
		if err != nil {
			return "", err
		}
		parts[i] = expr
	}
	return "ARRAY[" + strings.Join(parts, ", ") + "]::" + f.String() + "[]", nil
}

// etagTags is a parsed If-Match header.
type etagTags struct {
	star bool
	tags []string
}

// ifMatchTags returns the If-Match tags for a keyed PATCH/DELETE when ETags
// are enabled for the resource, or nil when the header is absent/ignored.
// Each tag is cut to its row part (before the "."), so a tag read from a
// response with any select=, embeds or Accept guards the same row state.
func (s *stmt) ifMatchTags() *etagTags {
	p := s.plan
	if p.IfMatch == "" || p.Key == nil || !s.etagWanted() {
		return nil
	}
	if p.Method != plan.PATCH && p.Method != plan.DELETE {
		return nil
	}
	tags := parseETagList(p.IfMatch)
	for i, tag := range tags.tags {
		tags.tags[i], _, _ = strings.Cut(tag, ".")
	}
	return tags
}

// parseETagList reads an If-Match value: `*`, or a comma-separated list of
// (optionally weak) quoted entity tags; the opaque part of each tag is kept.
func parseETagList(h string) *etagTags {
	out := &etagTags{}
	h = strings.TrimSpace(h)
	if h == "*" {
		out.star = true
		return out
	}
	i := 0
	for i < len(h) {
		for i < len(h) && (h[i] == ' ' || h[i] == '\t' || h[i] == ',') {
			i++
		}
		if i >= len(h) {
			break
		}
		if strings.HasPrefix(h[i:], "W/") || strings.HasPrefix(h[i:], "w/") {
			i += 2
		}
		if i < len(h) && h[i] == '"' {
			end := strings.IndexByte(h[i+1:], '"')
			if end < 0 {
				out.tags = append(out.tags, h[i+1:])
				break
			}
			out.tags = append(out.tags, h[i+1:i+1+end])
			i += end + 2
			continue
		}
		end := strings.IndexByte(h[i:], ',')
		if end < 0 {
			end = len(h) - i
		}
		tag := strings.TrimSpace(h[i : i+end])
		if tag == "*" {
			out.star = true
		} else if tag != "" {
			out.tags = append(out.tags, tag)
		}
		i += end
	}
	if !out.star && len(out.tags) == 0 {
		out.tags = []string{""}
	}
	return out
}

// stripLiterals removes single-quoted string literals (” escapes included)
// from SQL text.
func stripLiterals(sql string) string {
	var b strings.Builder
	b.Grow(len(sql))
	in := false
	for i := 0; i < len(sql); i++ {
		c := sql[i]
		if c == '\'' {
			if in && i+1 < len(sql) && sql[i+1] == '\'' {
				i++
				continue
			}
			in = !in
			continue
		}
		if !in {
			b.WriteByte(c)
		}
	}
	return b.String()
}
