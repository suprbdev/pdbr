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
	// cols maps a plain grouping column name to its key term, paths a
	// grouping JSON path (pathSignature) to its key term.
	cols  map[string]orderTerm
	paths map[string]orderTerm
	// keys are every grouping key in select order (the ORDER BY tiebreak).
	keys []orderTerm
}

// grouped compiles node (which selects an aggregate) over its resource
// with filter and, for an embed, the correlation corr renders against the
// scan alias. The items of to-one spreads are the node's own: their tables
// are LEFT JOINed to the scan (spread grouping), so their columns group and
// their aggregates aggregate over the joined rows.
func (s *stmt) grouped(node *plan.Node, filter plan.Expr, corr func(childAlias string) string, root bool, depth int) (*groupedQuery, error) {
	if depth > s.maxDepth {
		return nil, errUser(CodeDepthExceeded, "embed depth exceeds limit (%d)", s.maxDepth)
	}
	t := node.Resource.Table
	alias := s.alias("t")
	g := &groupedQuery{alias: s.alias("g"), cols: map[string]orderTerm{}, paths: map[string]orderTerm{}}
	var cols, groupBy, joins []string
	pl := newPairList()
	key := func(expr string, term orderTerm) string {
		name := "__k" + strconv.Itoa(len(g.keys))
		cols = append(cols, expr+" AS "+name)
		groupBy = append(groupBy, strconv.Itoa(len(cols)))
		term.col = name
		g.keys = append(g.keys, term)
		return name
	}
	nAgg := 0
	var addNode func(n *plan.Node, alias string, top bool, depth int) error
	addNode = func(n *plan.Node, alias string, top bool, depth int) error {
		r := n.Resource
		colKeys := map[string]string{}
		columnJSON := func(c *introspect.Column, args map[string]any) (string, error) {
			name, ok := colKeys[c.Name]
			if !ok {
				name = key(groupKeySQL(alias, c), orderTerm{nullable: !c.NotNull, owner: s.owner(c)})
				colKeys[c.Name] = name
				if top {
					g.cols[c.Name] = g.keys[len(g.keys)-1]
				}
			}
			kc := *c
			kc.Name = name
			return s.columnJSON(g.alias, &kc, args, depth)
		}
		if n.Star {
			for _, f := range r.Columns {
				expr, err := columnJSON(f.Column, nil)
				if err != nil {
					return err
				}
				if err := pl.add(f.Key, quoteLiteral(f.Key), expr); err != nil {
					return err
				}
			}
		}
		for i := range n.Fields {
			f := &n.Fields[i]
			k := f.Key(r)
			var expr string
			var err error
			switch {
			case f.Agg != nil:
				name := "__a" + strconv.Itoa(nAgg)
				nAgg++
				agg := s.aggregateSQL(f, alias)
				if !top && f.Column == nil {
					agg = "count(" + alias + ".*)"
				}
				cols = append(cols, agg+" AS "+name)
				expr = aggregateJSON(colRef(g.alias, name), f.Agg)
			case f.Column != nil && (len(f.Path) > 0 || f.Cast != nil):
				name := key(s.valueSQL(alias, f, false), orderTerm{nullable: true})
				if top && len(f.Path) > 0 && f.Cast == nil {
					g.paths[pathSignature(f.Column, f.Path)] = g.keys[len(g.keys)-1]
				}
				expr = colRef(g.alias, name)
				if f.Cast != nil {
					expr = "to_json(" + expr + ")"
				}
			case f.Column != nil:
				expr, err = columnJSON(f.Column, f.Args)
			default:
				_, value, ferr := s.fieldJSON(r, f, alias, depth)
				if ferr != nil {
					return ferr
				}
				expr = colRef(g.alias, key("to_jsonb("+value+")", orderTerm{nullable: true}))
			}
			if err != nil {
				return err
			}
			if err := pl.add(k, s.pairKey(f, k), expr); err != nil {
				return err
			}
		}
		for i := range n.Embeds {
			e := &n.Embeds[i]
			if !e.SpreadOne() {
				return errUser(CodeInvalidSelect, "embeds other than to-one spreads cannot be grouping keys of an aggregate select")
			}
			if !e.Relation.UniqueTarget() {
				return errUser(CodeInvalidSelect, "spread grouping over %q needs its referenced columns to be a primary or unique key of the target", e.Key)
			}
			if depth+1 > s.maxDepth {
				return errUser(CodeDepthExceeded, "embed depth exceeds limit (%d)", s.maxDepth)
			}
			child := embedNode(e)
			childAlias := s.alias("t")
			on := []string{s.joinConds(e.Relation, childAlias, alias)}
			more, err := s.parentConds(child, e.Filter, childAlias)
			if err != nil {
				return err
			}
			on = append(on, more...)
			joins = append(joins, fmt.Sprintf("LEFT JOIN %s %s ON %s", s.sourceRef(child.Resource.Table, childAlias), childAlias, strings.Join(on, " AND ")))
			if err := addNode(child, childAlias, false, depth+1); err != nil {
				return err
			}
		}
		return nil
	}
	if err := addNode(node, alias, true, depth); err != nil {
		return nil, err
	}
	var extra []string
	if corr != nil {
		extra = append(extra, corr(alias))
	}
	where, err := s.whereClause(node, filter, alias, extra)
	if err != nil {
		return nil, err
	}
	from := s.sourceRef(t, alias)
	if root {
		from = s.rootRef(t, alias)
	}
	group := ""
	if len(groupBy) > 0 {
		group = "\nGROUP BY " + strings.Join(groupBy, ", ")
	}
	g.sql = fmt.Sprintf("SELECT %s\nFROM %s %s%s%s%s", strings.Join(cols, ", "), from, alias, joinLaterals(joins), where, group)
	g.obj = pl.object()
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
// key-only grouped scan with the same filter and spread joins (no
// aggregates, no JSON rendering of column keys); 1 when there is no
// grouping key.
func (s *stmt) groupCountSQL(node *plan.Node) (string, error) {
	r := node.Resource
	alias := s.alias("c")
	var keys, joins []string
	var walk func(n *plan.Node, alias string) error
	walk = func(n *plan.Node, alias string) error {
		seen := map[string]bool{}
		column := func(c *introspect.Column) {
			if !seen[c.Name] {
				seen[c.Name] = true
				keys = append(keys, groupKeySQL(alias, c))
			}
		}
		if n.Star {
			for _, f := range n.Resource.Columns {
				column(f.Column)
			}
		}
		for i := range n.Fields {
			f := &n.Fields[i]
			switch {
			case f.Agg != nil:
			case f.Column != nil && (len(f.Path) > 0 || f.Cast != nil):
				keys = append(keys, s.valueSQL(alias, f, false))
			case f.Column != nil:
				column(f.Column)
			default:
				_, value, err := s.fieldJSON(n.Resource, f, alias, 0)
				if err != nil {
					return err
				}
				keys = append(keys, "to_jsonb("+value+")")
			}
		}
		for i := range n.Embeds {
			e := &n.Embeds[i]
			if !e.SpreadOne() {
				continue
			}
			child := embedNode(e)
			childAlias := s.alias("c")
			on := []string{s.joinConds(e.Relation, childAlias, alias)}
			more, err := s.parentConds(child, e.Filter, childAlias)
			if err != nil {
				return err
			}
			on = append(on, more...)
			joins = append(joins, fmt.Sprintf(" LEFT JOIN %s %s ON %s", s.sourceRef(child.Resource.Table, childAlias), childAlias, strings.Join(on, " AND ")))
			if err := walk(child, childAlias); err != nil {
				return err
			}
		}
		return nil
	}
	if err := walk(node, alias); err != nil {
		return "", err
	}
	if len(keys) == 0 {
		return "1", nil
	}
	where, err := s.whereClause(node, s.plan.Filter, alias, nil)
	if err != nil {
		return "", err
	}
	where = strings.Replace(where, "\nWHERE ", " WHERE ", 1)
	return fmt.Sprintf("(SELECT count(*) FROM (SELECT 1 FROM %s %s%s%s GROUP BY %s) %s)",
		s.rootRef(r.Table, alias), alias, strings.Join(joins, ""), where, strings.Join(keys, ", "), s.alias("g")), nil
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
		if len(o.Path) > 0 {
			term, ok = g.paths[pathSignature(o.Column, o.Path)]
		}
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
	if len(f.Path) > 0 {
		in = s.jsonPathSQL(alias, f.Column, f.Path, false)
	} else if a.InputCast != nil {
		in = s.renderedRef(alias, f.Column)
	}
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
// one is given, GeoJSON for the spatial extensions, and to_json otherwise
// — numbers (bigint and numeric included) are JSON numbers, as PostgREST
// returns them.
func aggregateJSON(ref string, a *plan.Aggregate) string {
	switch {
	case a.OutputCast != nil:
		return "to_json((" + ref + ")::" + typeRefSQL(a.OutputCast) + ")"
	case a.Func.Spatial():
		return postgis.OutputJSONSQL(ref)
	}
	return "to_json(" + ref + ")"
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
	limit := "\nLIMIT " + strconv.Itoa(pp.limit+1)
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
	pageSQL := "\nLIMIT " + strconv.Itoa(limit)
	if page.Offset > 0 {
		pageSQL += "\nOFFSET " + s.param(page.Offset)
	}
	return fmt.Sprintf("SELECT %s AS data\nFROM (\n%s\n) %s%s%s", g.obj, indent(g.sql), g.alias, s.renderOrder(terms, g.alias, false), pageSQL), nil
}
