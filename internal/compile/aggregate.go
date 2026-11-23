package compile

import (
	"fmt"
	"strconv"
	"strings"

	"github.com/suprbdev/pdbcore/introspect"
	"github.com/suprbdev/pdbcore/postgis"

	"github.com/suprbdev/pdbr/internal/plan"
)

// groupedQuery is the grouped scan of an aggregated node:
//
//	SELECT <key> AS __k0, …, <aggregate> AS __a0, …
//	FROM <table> t <laterals> WHERE … GROUP BY 1, …
//
// Plain grouping columns are selected raw (so they sort by their own type
// and the outer ORDER BY can address them), computed and derived fields as
// their response JSON; obj renders the response object over alias, the name
// the caller gives the grouped subquery. Embeds are never grouping keys.
type groupedQuery struct {
	sql   string
	alias string
	obj   string
	// cols maps a plain grouping column name to its key term.
	cols map[string]orderTerm
	// keys are every grouping key in select order (the ORDER BY tiebreak).
	keys []orderTerm
}

// grouped compiles node (which selects an aggregate) over its resource
// with filter and, for an embed, the correlation corr renders against the
// scan alias.
func (s *stmt) grouped(node *plan.Node, filter plan.Expr, corr func(childAlias string) string, root bool, depth int) (*groupedQuery, error) {
	if depth > s.maxDepth {
		return nil, errUser(CodeEmbedDepthExceeded, "embed depth exceeds limit (%d)", s.maxDepth)
	}
	if len(node.Embeds) > 0 {
		return nil, errUser(CodeInvalidSelect, "embeds cannot be grouping keys of an aggregate select")
	}
	r := node.Resource
	t := r.Table
	alias := s.alias("t")
	g := &groupedQuery{alias: s.alias("g"), cols: map[string]orderTerm{}}
	var cols, groupBy, pairs []string
	key := func(expr string, term orderTerm) string {
		name := "__k" + strconv.Itoa(len(g.keys))
		cols = append(cols, expr+" AS "+name)
		groupBy = append(groupBy, strconv.Itoa(len(cols)))
		term.col = name
		g.keys = append(g.keys, term)
		return name
	}
	columnKey := func(c *introspect.Column) string {
		if term, ok := g.cols[c.Name]; ok {
			return term.col
		}
		name := key(groupKeySQL(alias, c), orderTerm{nullable: !c.NotNull, owner: s.owner(c)})
		g.cols[c.Name] = g.keys[len(g.keys)-1]
		return name
	}
	columnJSON := func(c *introspect.Column, args map[string]any) (string, error) {
		kc := *c
		kc.Name = columnKey(c)
		return s.columnJSON(g.alias, &kc, args, depth)
	}
	seen := map[string]bool{}
	add := func(k, expr string) {
		seen[k] = true
		pairs = append(pairs, quoteLiteral(k), expr)
	}
	if node.Star {
		for _, f := range r.Columns {
			expr, err := columnJSON(f.Column, nil)
			if err != nil {
				return nil, err
			}
			add(f.Key, expr)
		}
	}
	nAgg := 0
	for i := range node.Fields {
		f := &node.Fields[i]
		k := f.Key(r)
		var expr string
		var err error
		switch {
		case f.Agg != nil:
			name := "__a" + strconv.Itoa(nAgg)
			nAgg++
			cols = append(cols, s.aggregateSQL(f, alias)+" AS "+name)
			expr = aggregateJSON(colRef(g.alias, name), f.Agg)
		case f.Column != nil:
			if node.Star && f.Alias == "" && seen[k] {
				continue
			}
			expr, err = columnJSON(f.Column, f.Args)
		default:
			_, value, ferr := s.fieldJSON(r, f, alias, depth)
			if ferr != nil {
				return nil, ferr
			}
			expr = colRef(g.alias, key("to_jsonb("+value+")", orderTerm{nullable: true}))
		}
		if err != nil {
			return nil, err
		}
		add(k, expr)
	}
	var extra []string
	if corr != nil {
		extra = append(extra, corr(alias))
	}
	where, err := s.whereClause(node, filter, alias, extra)
	if err != nil {
		return nil, err
	}
	from := s.sourceRef(t)
	if root {
		from = s.rootRef(t)
	}
	group := ""
	if len(groupBy) > 0 {
		group = "\nGROUP BY " + strings.Join(groupBy, ", ")
	}
	g.sql = fmt.Sprintf("SELECT %s\nFROM %s %s%s%s", strings.Join(cols, ", "), from, alias, where, group)
	g.obj = buildJSONObject(pairs)
	return g, nil
}

// groupKeySQL renders a column as a grouping key: the raw value, json as
// jsonb (json has no equality operator).
func groupKeySQL(alias string, c *introspect.Column) string {
	expr := colRef(alias, c.Name)
	if strings.TrimPrefix(c.PGType, "_") == "json" {
		expr += "::jsonb"
		if c.IsArray {
			expr += "[]"
		}
	}
	return expr
}

// groupCountSQL renders the number of groups of the aggregated root: a
// key-only grouped scan with the same filter (no aggregates, no JSON
// rendering of column keys); 1 when there is no grouping key.
func (s *stmt) groupCountSQL(node *plan.Node) (string, error) {
	r := node.Resource
	alias := s.alias("c")
	var keys []string
	seen := map[string]bool{}
	column := func(c *introspect.Column) {
		if !seen[c.Name] {
			seen[c.Name] = true
			keys = append(keys, groupKeySQL(alias, c))
		}
	}
	if node.Star {
		for _, f := range r.Columns {
			column(f.Column)
		}
	}
	for i := range node.Fields {
		f := &node.Fields[i]
		switch {
		case f.Agg != nil:
		case f.Column != nil:
			column(f.Column)
		default:
			_, value, err := s.fieldJSON(r, f, alias, 0)
			if err != nil {
				return "", err
			}
			keys = append(keys, "to_jsonb("+value+")")
		}
	}
	if len(keys) == 0 {
		return "1", nil
	}
	where, err := s.whereClause(node, s.plan.Filter, alias, nil)
	if err != nil {
		return "", err
	}
	where = strings.Replace(where, "\nWHERE ", " WHERE ", 1)
	return fmt.Sprintf("(SELECT count(*) FROM (SELECT 1 FROM %s %s%s GROUP BY %s) %s)",
		s.rootRef(r.Table), alias, where, strings.Join(keys, ", "), s.alias("g")), nil
}

// orderTerms maps a node's order terms onto its grouping keys (the parser
// admits only selected plain columns) and appends the remaining keys, so
// the order of groups — and with it limit/offset paging — is total.
func (g *groupedQuery) orderTerms(order []plan.OrderTerm) ([]orderTerm, error) {
	out := make([]orderTerm, 0, len(g.keys))
	used := map[string]bool{}
	for _, o := range order {
		if o.Column == nil {
			return nil, errUser(CodeColumnNotOrderable, "an aggregate select orders only by its grouping columns")
		}
		term, ok := g.cols[o.Column.Name]
		if !ok {
			return nil, errUser(CodeColumnNotOrderable, "order by %q needs %q selected as a grouping column", o.Column.Name, o.Column.Name)
		}
		if used[term.col] {
			continue
		}
		used[term.col] = true
		term.desc, term.nullsFirst = o.Desc, o.NullsFirst
		out = append(out, term)
	}
	for _, k := range g.keys {
		if !used[k.col] {
			out = append(out, k)
		}
	}
	return out, nil
}

// aggregateSQL renders the aggregate call over the scan alias. The spatial
// extensions aggregate geometry (geography input is cast): extent is the
// bounding box as a polygon carrying the column's SRID when the catalog
// knows it, centroid the centroid of the collected geometries.
func (s *stmt) aggregateSQL(f *plan.FieldSel, alias string) string {
	a := f.Agg
	if f.Column == nil {
		return "count(*)"
	}
	in := colRef(alias, f.Column.Name)
	if a.InputCast != nil {
		in = "(" + in + ")::" + typeRefSQL(a.InputCast)
	}
	if !a.Func.Spatial() {
		return string(a.Func) + "(" + in + ")"
	}
	flavour := postgis.FlavourOf(f.Column.PGType)
	if flavour == postgis.Geography {
		in += "::geometry"
	}
	switch a.Func {
	case plan.AggExtent:
		srid := spatialSRID(f.Column)
		if srid == 0 && flavour == postgis.Geography {
			srid = 4326
		}
		if srid > 0 {
			return "ST_SetSRID(ST_Extent(" + in + ")::geometry, " + strconv.Itoa(srid) + ")"
		}
		return "ST_Extent(" + in + ")::geometry"
	case plan.AggUnion:
		return "ST_Union(" + in + ")"
	case plan.AggCollect:
		return "ST_Collect(" + in + ")"
	}
	return "ST_Centroid(ST_Collect(" + in + "))"
}

// aggregateJSON renders an aggregate result: through the output cast when
// one is given, GeoJSON for the spatial extensions, and to_jsonb otherwise
// — numbers (bigint and numeric included) are JSON numbers, as PostgREST
// returns them.
func aggregateJSON(ref string, a *plan.Aggregate) string {
	switch {
	case a.OutputCast != nil:
		return "to_jsonb((" + ref + ")::" + typeRefSQL(a.OutputCast) + ")"
	case a.Func.Spatial():
		return postgis.OutputSQL(ref)
	}
	return "to_jsonb(" + ref + ")"
}

// typeRefSQL renders a cast target schema-qualified and quoted, so it never
// depends on the search_path.
func typeRefSQL(t *plan.TypeRef) string {
	return quoteIdent(t.Schema) + "." + quoteIdent(t.Name)
}

// aggList compiles the list GET of an aggregated root: one row per group,
// paged by limit/offset (groups have no keyset) and ordered by the
// requested grouping columns, then by every key. The total counts groups
// (groupCountSQL).
func (s *stmt) aggList(node *plan.Node) (*Statement, error) {
	pp, err := s.planPage(s.plan.Resource, s.plan.Page)
	if err != nil {
		return nil, err
	}
	if pp.hasAfter || pp.hasBefore {
		return nil, errUser(CodeKeysetUnsupported, "aggregate functions support only limit/offset paging")
	}
	pp.keyset = false
	g, err := s.grouped(node, s.plan.Filter, nil, true, 0)
	if err != nil {
		return nil, err
	}
	terms, err := g.orderTerms(s.plan.Order)
	if err != nil {
		return nil, err
	}
	orderSQL := s.renderOrder(terms, g.alias, false)
	limit := "\nLIMIT " + s.param(pp.limit+1)
	if pp.offset > 0 {
		limit += "\nOFFSET " + s.param(pp.offset)
	}
	inner := fmt.Sprintf("SELECT %s AS data, row_number() OVER (%s) AS __rn\nFROM (\n%s\n) %s%s%s",
		g.obj, strings.TrimPrefix(orderSQL, "\n"), indent(g.sql), g.alias, orderSQL, limit)
	count := func() (string, error) { return s.groupCountSQL(node) }
	return s.pageEnvelope(inner, pp, count)
}

// aggListInner is listInner for an aggregated to-many embed: the groups of
// the related rows of one parent, ordered and offset-paged like any
// embedded list.
func (s *stmt) aggListInner(node *plan.Node, filter plan.Expr, order []plan.OrderTerm, page plan.Page, corr func(childAlias string) string, depth int) (string, error) {
	g, err := s.grouped(node, filter, corr, false, depth)
	if err != nil {
		return "", err
	}
	terms, err := g.orderTerms(order)
	if err != nil {
		return "", err
	}
	limit, err := s.embedLimit(page)
	if err != nil {
		return "", err
	}
	pageSQL := "\nLIMIT " + s.param(limit)
	if page.Offset > 0 {
		pageSQL += "\nOFFSET " + s.param(page.Offset)
	}
	return fmt.Sprintf("SELECT %s AS data\nFROM (\n%s\n) %s%s%s", g.obj, indent(g.sql), g.alias, s.renderOrder(terms, g.alias, false), pageSQL), nil
}
