package compile

import (
	"fmt"
	"strings"

	"github.com/suprbdev/pdbcore/introspect"
	"github.com/suprbdev/pdbcore/postgis"

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
// against __mut like a regular row source (embeds are laterals against the
// real tables correlated on __mut columns). If-Match adds a `cur` CTE
// hashing the current row so 404 and 412 resolve in the same statement.
func (s *stmt) mutation() (*Statement, error) {
	p := s.plan
	if p.Body == nil && p.Method != plan.DELETE {
		return nil, fmt.Errorf("compile: %s without a body", p.Method)
	}
	var ctes []string
	var dml string
	var err error
	switch p.Method {
	case plan.POST:
		dml = s.insertDML() + s.resolutionSQL()
	case plan.PUT:
		dml, err = s.upsertDML()
	case plan.PATCH, plan.DELETE:
		if p.Key == nil {
			dml, err = s.bulkDML()
			break
		}
		var cur string
		cur, dml, err = s.keyedDML()
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
	return s.mutationResponse(strings.Join(ctes, ",\n"))
}

// mutationResponse compiles the response half of a mutation over a CTE
// chain (which must define __mut) and assembles the statement.
func (s *stmt) mutationResponse(ctes string) (*Statement, error) {
	p := s.plan
	r := p.Resource
	t := r.Table
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
			body = "SELECT jsonb_build_object('n', (SELECT count(*) FROM __mut)) AS data"
			break
		}
		jsonExpr, laterals, err := s.rowJSON(s.rootNode(), alias, 0)
		if err != nil {
			return nil, err
		}
		sub := s.alias("s")
		body = fmt.Sprintf("SELECT jsonb_build_object('n', count(*), 'rows', coalesce(jsonb_agg(%s.data), '[]'::jsonb)) AS data\nFROM (\n%s\n) %s",
			sub, indent(fmt.Sprintf("SELECT %s AS data\nFROM __mut %s%s", jsonExpr, alias, joinLaterals(laterals))), sub)
	case minimal && ifMatch:
		body = fmt.Sprintf("SELECT %s AS data", buildJSONObject([]string{
			"'found'", "EXISTS (SELECT 1 FROM cur)",
			"'row'", "(SELECT to_jsonb(true) FROM __mut " + alias + ")",
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
			body = fmt.Sprintf("SELECT coalesce(jsonb_agg(%s.data), '[]'::jsonb) AS data\nFROM (\n%s\n) %s",
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
			pairs := []string{"'row'", jsonExpr}
			if insertedExpr != "" {
				pairs = append(pairs, "'inserted'", insertedExpr)
			}
			if keyExpr != "" {
				pairs = append(pairs, "'key'", keyExpr)
			}
			body = fmt.Sprintf("SELECT %s AS data\n%s", buildJSONObject(pairs), from)
		}
	}
	ctes = strings.ReplaceAll(ctes, returningPlaceholder, returningList(t, ctes+"\n"+body, alias, p.Method == plan.PUT))
	return &Statement{SQL: "WITH " + ctes + "\n" + body, Args: s.ps.Args(), Mutation: true, Shape: shape}, nil
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
func (s *stmt) insertDML() string {
	t := s.plan.Resource.Table
	body := s.plan.Body
	cols := body.Columns
	if body.Objects == nil {
		if len(cols) == 0 {
			return fmt.Sprintf("INSERT INTO %s DEFAULT VALUES", tableRef(t))
		}
		return fmt.Sprintf("INSERT INTO %s (%s)\nVALUES %s", tableRef(t), columnList(cols), s.tupleSQL(cols, body.Object))
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
	return fmt.Sprintf("INSERT INTO %s (%s)\nVALUES %s", tableRef(t), columnList(cols), strings.Join(tuples, ", "))
}

// resolutionSQL renders the ON CONFLICT clause of a POST under Prefer:
// resolution=: merge-duplicates updates like PUT, ignore-duplicates skips
// conflicting rows (on any unique constraint when the table has no primary
// key and no ?on_conflict= names one).
func (s *stmt) resolutionSQL() string {
	p := s.plan
	switch p.Prefer.Resolution {
	case plan.MergeDuplicates:
		if p.OnConflict == nil {
			s.fail(errUser(CodeInvalidBody, "resolution=merge-duplicates needs a primary key or ?on_conflict="))
			return ""
		}
		return "\n" + conflictUpdate(p.OnConflict, p.Body.Columns)
	case plan.IgnoreDuplicates:
		if p.OnConflict == nil {
			return "\nON CONFLICT DO NOTHING"
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
func (s *stmt) upsertDML() (string, error) {
	t := s.plan.Resource.Table
	body := s.plan.Body
	target := s.plan.OnConflict
	if target == nil {
		target = t.PrimaryKey
	}
	if target == nil {
		return "", errUser(CodeInvalidBody, "upsert needs a primary key or ?on_conflict=")
	}
	if target.WithoutOverlaps {
		return "", errUser(CodeTemporalConflict, "a temporal key (WITHOUT OVERLAPS) cannot be a conflict target")
	}
	cols := body.Columns
	if len(cols) == 0 {
		return "", errUser(CodeInvalidBody, "upsert body must set at least the conflict target columns")
	}
	var tuples []string
	if body.Objects == nil {
		tuples = []string{s.tupleSQL(cols, body.Object)}
	} else {
		tuples = make([]string, len(body.Objects))
		for i, obj := range body.Objects {
			tuples[i] = s.tupleSQL(cols, obj)
		}
	}
	return fmt.Sprintf("INSERT INTO %s (%s)\nVALUES %s\n%s",
		tableRef(t), columnList(cols), strings.Join(tuples, ", "), conflictUpdate(target, cols)), nil
}

// keyedDML renders the UPDATE / DELETE of a keyed path plus, under
// If-Match, the `cur` CTE hashing the current row (all columns, like the
// GET ETag) and the hash predicate on the DML.
func (s *stmt) keyedDML() (cur, dml string, err error) {
	p := s.plan
	t := p.Resource.Table
	key := p.Key
	if key == nil || key.Constraint == nil {
		return "", "", fmt.Errorf("compile: %s without a key", p.Method)
	}
	if len(key.Values) != len(key.Constraint.Columns) {
		return "", "", errUser(CodeInvalidFilter, "key has %d values, want %d", len(key.Values), len(key.Constraint.Columns))
	}
	var sets []string
	if p.Method == plan.PATCH {
		if len(p.Body.Columns) == 0 {
			return "", "", errUser(CodeInvalidBody, "PATCH body must set at least one column")
		}
		for _, c := range p.Body.Columns {
			sets = append(sets, quoteIdent(c.Name)+" = "+s.inputValue(c, p.Body.Object[c.Name]))
		}
	}
	placeholders := make([]string, len(key.Values))
	for i, name := range key.Constraint.Columns {
		col := t.Column(name)
		if col == nil {
			return "", "", fmt.Errorf("compile: key column %q not found on %s", name, t.Name)
		}
		placeholders[i] = s.param(coerceInput(col, key.Values[i]))
	}
	keyConds := func(alias string) string {
		conds := make([]string, len(placeholders))
		for i, name := range key.Constraint.Columns {
			conds[i] = colRef(alias, name) + " = " + placeholders[i]
		}
		return strings.Join(conds, " AND ")
	}
	where := keyConds(tableRef(t))
	if tags := s.ifMatchTags(); tags != nil {
		cur = fmt.Sprintf("cur AS (\n  SELECT md5(to_jsonb(__cur)::text) AS h\n  FROM %s __cur\n  WHERE %s\n)", tableRef(t), keyConds("__cur"))
		switch {
		case tags.star:
		case len(tags.tags) == 1:
			where += "\n  AND (SELECT h FROM cur) = " + s.param(tags.tags[0])
		default:
			where += "\n  AND (SELECT h FROM cur) = ANY(" + s.param(tags.tags) + ")"
		}
	}
	if p.Method == plan.DELETE {
		return cur, fmt.Sprintf("DELETE FROM %s\nWHERE %s", tableRef(t), where), nil
	}
	return cur, fmt.Sprintf("UPDATE %s\nSET %s\nWHERE %s", tableRef(t), strings.Join(sets, ", "), where), nil
}

// bulkDML renders the UPDATE / DELETE of a list route over exactly the rows
// the same GET would list: the root filter plus one EXISTS per !inner embed,
// compiled against an aliased target (no WHERE at all when
// server.allow_unfiltered_mutations let an unrestricted request through).
func (s *stmt) bulkDML() (string, error) {
	p := s.plan
	t := p.Resource.Table
	alias := s.alias("m")
	var conds []string
	if p.Filter != nil {
		cond, err := s.filterSQL(p.Filter, alias)
		if err != nil {
			return "", err
		}
		if cond == "" {
			return "", fmt.Errorf("compile: %s filter on %s rendered no condition", p.Method, t.Name)
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
			return "", err
		}
		conds = append(conds, cond)
	}
	where := ""
	if len(conds) > 0 {
		where = "\nWHERE " + strings.Join(conds, " AND ")
	}
	if p.Method == plan.DELETE {
		return fmt.Sprintf("DELETE FROM %s %s%s", tableRef(t), alias, where), nil
	}
	if p.Body == nil || len(p.Body.Columns) == 0 {
		return "", errUser(CodeInvalidBody, "PATCH body must set at least one column")
	}
	sets := make([]string, len(p.Body.Columns))
	for i, c := range p.Body.Columns {
		sets[i] = quoteIdent(c.Name) + " = " + s.inputValue(c, p.Body.Object[c.Name])
	}
	return fmt.Sprintf("UPDATE %s %s\nSET %s%s", tableRef(t), alias, strings.Join(sets, ", "), where), nil
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
	exprs := make([]string, len(cols))
	for i, c := range cols {
		v, ok := obj[c.Name]
		if !ok {
			exprs[i] = "DEFAULT"
			continue
		}
		exprs[i] = s.inputValue(c, v)
	}
	return "(" + strings.Join(exprs, ", ") + ")"
}

// inputValue renders a body value as a parameter expression for col:
// composites are populated from a jsonb parameter, spatial values go
// through their PostGIS constructor, arrays and json/jsonb carry an explicit
// cast, everything else is a bare parameter typed by the target column. The
// expression depends only on the column type (and, for spatial values, the
// input encoding), never on the value.
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
	switch base := strings.TrimPrefix(col.PGType, "_"); base {
	case "json", "jsonb":
		return s.param(coerceInput(col, v)) + "::" + base
	}
	return s.param(coerceInput(col, v))
}

// compositeInput renders a composite (or composite-array) value from one
// jsonb parameter holding the object (or array of objects) as JSON text.
func (s *stmt) compositeInput(comp *introspect.Composite, col *introspect.Column, v any) string {
	typeRef := quoteIdent(comp.Schema) + "." + quoteIdent(comp.Name)
	ph := s.param(compositeText(v))
	if col.IsArray {
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
func (s *stmt) ifMatchTags() *etagTags {
	p := s.plan
	if p.IfMatch == "" || p.Key == nil || !s.etagWanted() {
		return nil
	}
	if p.Method != plan.PATCH && p.Method != plan.DELETE {
		return nil
	}
	return parseETagList(p.IfMatch)
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
