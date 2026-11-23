package parse

import (
	"strings"

	"github.com/suprbdev/pdbcore/introspect"
	"github.com/suprbdev/pdbcore/postgis"

	"github.com/suprbdev/pdbr/internal/api"
	"github.com/suprbdev/pdbr/internal/plan"
)

// aggFuncs are the aggregate function names of the select= grammar.
var aggFuncs = map[string]plan.AggFunc{
	"count": plan.AggCount, "sum": plan.AggSum, "avg": plan.AggAvg, "max": plan.AggMax, "min": plan.AggMin,
	"extent": plan.AggExtent, "union": plan.AggUnion, "collect": plan.AggCollect, "centroid": plan.AggCentroid,
}

// castTypes are the built-in cast targets of the aggregate grammar
// (pg_catalog type names); castAliases maps their SQL spellings onto them.
// Anything else — the reg* name lookups, pseudo-types, composites, types
// with user-defined casts — is not castable.
var (
	castTypes = typeSet("int2", "int4", "int8", "numeric", "float4", "float8", "text", "varchar", "bpchar", "bool",
		"date", "time", "timetz", "timestamp", "timestamptz", "interval", "uuid", "json", "jsonb", "money")
	castAliases = map[string]string{
		"smallint": "int2", "int": "int4", "integer": "int4", "bigint": "int8",
		"real": "float4", "float": "float8", "decimal": "numeric", "boolean": "bool", "char": "bpchar",
	}
)

var (
	sumTypes = typeSet("int2", "int4", "int8", "float4", "float8", "numeric", "money", "interval")
	avgTypes = typeSet("int2", "int4", "int8", "float4", "float8", "numeric", "interval")
	// unorderedTypes have no max/min aggregate.
	unorderedTypes = typeSet("json", "jsonb", "bool", "bytea", "xml", "uuid", "point", "line", "lseg", "box", "path", "polygon", "circle", "vector", "halfvec", "sparsevec")
	// ungroupableTypes have no equality or B-tree ordering, so they cannot
	// be grouping keys (json keys are grouped as jsonb, except inside a
	// composite).
	ungroupableTypes = typeSet("xml", "point", "line", "lseg", "box", "path", "polygon", "circle", "xid", "cid",
		"aclitem", "txid_snapshot", "pg_snapshot", "int2vector", "oidvector")
)

func typeSet(names ...string) map[string]bool {
	out := make(map[string]bool, len(names))
	for _, n := range names {
		out[n] = true
	}
	return out
}

// aggregateItem recognises an aggregate select item: col.fn() (the column
// part may carry an input cast), bare count(), and the legacy bare count
// that PostgREST still accepts when no column, computed column, pseudo
// column or relation of the resource is named count. It returns the
// function and the column part ("" for count over whole rows).
func aggregateItem(it selectItem, res *api.Resource) (plan.AggFunc, string, bool) {
	if !it.hasInner {
		if it.name == "count" && it.outCast == "" && len(it.hints) == 0 &&
			res.Field("count") == nil && res.Computed["count"] == nil && res.Pseudo["count"] == nil && len(res.Relations["count"]) == 0 {
			return plan.AggCount, "", true
		}
		return "", "", false
	}
	if strings.TrimSpace(it.inner) != "" {
		return "", "", false
	}
	if it.name == "count" && len(res.Relations["count"]) == 0 {
		return plan.AggCount, "", true
	}
	i := lastIndexOutsideQuotes(it.name, ".")
	if i <= 0 {
		return "", "", false
	}
	fn, ok := aggFuncs[it.name[i+1:]]
	if !ok {
		return "", "", false
	}
	return fn, it.name[:i], true
}

// takesArgs reports col.fn(...) with a non-empty argument list for an
// aggregate function name.
func takesArgs(it selectItem) bool {
	i := lastIndexOutsideQuotes(it.name, ".")
	if !it.hasInner || i <= 0 {
		return false
	}
	_, ok := aggFuncs[it.name[i+1:]]
	return ok
}

// castType resolves a cast target: an allowlisted built-in type
// (pg_catalog) or a native enum of an exposed schema (schema.schemas order),
// matched case-insensitively for the built-ins and exactly or lower-cased
// for enums.
func (p *parser) castType(raw string) (*plan.TypeRef, error) {
	name := strings.TrimSpace(raw)
	t := strings.ToLower(name)
	if c, ok := castAliases[t]; ok {
		t = c
	}
	if castTypes[t] {
		return &plan.TypeRef{Schema: "pg_catalog", Name: t}, nil
	}
	if cat := p.built.Catalog; cat != nil && name != "" {
		for _, schema := range cat.Schemas {
			for _, e := range cat.Enums {
				if e.Schema == schema && !e.TableBacked && (e.Name == name || e.Name == t) {
					return &plan.TypeRef{Schema: e.Schema, Name: e.Name}, nil
				}
			}
		}
	}
	return nil, errf(CodeInvalidSelect, "cast type %q is not allowed; use a built-in scalar type (int, bigint, numeric, float8, text, date, timestamptz, interval, …) or an enum of the API", raw)
}

// resolveAggregate resolves col.fn() / count() into an aggregate field.
func (p *parser) resolveAggregate(node *plan.Node, it selectItem, fn plan.AggFunc, colPart string) error {
	res := node.Resource
	if !p.opts.Aggregates {
		return errf(CodeAggregatesDisabled, "aggregate functions are disabled; enable server.aggregates to use %s()", fn)
	}
	if len(it.hints) > 0 {
		return errf(CodeInvalidSelect, "hints (!%s) are only valid on embeds", strings.Join(it.hints, "!"))
	}
	agg := &plan.Aggregate{Func: fn}
	if it.outCast != "" {
		t, err := p.castType(it.outCast)
		if err != nil {
			return err
		}
		agg.OutputCast = t
	}
	fs := plan.FieldSel{Alias: it.alias, Agg: agg}
	if colPart == "" {
		node.Fields = append(node.Fields, fs)
		return nil
	}
	name := colPart
	if i := lastIndexOutsideQuotes(colPart, "::"); i >= 0 {
		t, err := p.castType(colPart[i+2:])
		if err != nil {
			return err
		}
		name, agg.InputCast = colPart[:i], t
	}
	col, path, has, uerr := splitJSONPath(name)
	if uerr != nil {
		return withCode(uerr, CodeInvalidSelect)
	}
	if has {
		f, err := p.resolvePathField(res, col, path, CodeInvalidSelect)
		if err != nil {
			return err
		}
		fs.Column, fs.Path = f.Column, path
	} else {
		f := res.Field(name)
		if f == nil {
			if res.Computed[name] != nil || res.Pseudo[name] != nil {
				return errf(CodeInvalidSelect, "%s() needs a column; %q is a computed or derived field", fn, name)
			}
			return errf(CodeUnknownColumn, "unknown column %q on %q", name, res.Path)
		}
		fs.Column = f.Column
	}
	if err := p.checkAggregateType(&fs); err != nil {
		return err
	}
	node.Fields = append(node.Fields, fs)
	return nil
}

// checkAggregateType rejects aggregate/input-type combinations PostgreSQL
// has no aggregate for, by the catalog type or the input cast.
func (p *parser) checkAggregateType(fs *plan.FieldSel) error {
	c, agg := fs.Column, fs.Agg
	name := c.Name + fs.Path.String()
	if len(fs.Path) > 0 {
		pt := p.pathType(c, fs.Path)
		c = &introspect.Column{Name: name, PGType: pt.base, TypeSchema: "pg_catalog"}
	}
	spatial := postgis.IsSpatial(c.PGType) && agg.InputCast == nil
	if agg.Func.Spatial() {
		switch {
		case !p.opts.SpatialAggregates:
			return errf(CodeInvalidSelect, "%s() is a spatial aggregate; it needs the postgis plugin", agg.Func)
		case !spatial || c.IsArray:
			return errf(CodeInvalidSelect, "%s() needs a geometry or geography column; %q is not one", agg.Func, c.Name)
		}
		return nil
	}
	if agg.Func == plan.AggCount {
		return nil
	}
	if spatial {
		return errf(CodeInvalidSelect, "%s() is not defined on spatial column %q; use extent(), union(), collect() or centroid()", agg.Func, c.Name)
	}
	typ := strings.TrimPrefix(c.PGType, "_")
	array := c.IsArray
	if agg.InputCast != nil {
		typ, array = agg.InputCast.Name, false
	}
	switch agg.Func {
	case plan.AggSum, plan.AggAvg:
		allowed := sumTypes
		if agg.Func == plan.AggAvg {
			allowed = avgTypes
		}
		if array || !allowed[typ] {
			return errf(CodeInvalidSelect, "%s() needs a numeric column; %q is %s (cast it: %s::numeric.%s())", agg.Func, c.Name, typ, c.Name, agg.Func)
		}
	case plan.AggMax, plan.AggMin:
		composite := agg.InputCast == nil && len(fs.Path) == 0 && p.built.Catalog != nil && p.built.Catalog.Composite(c.TypeSchema, typ) != nil
		if !array && (unorderedTypes[typ] || composite) {
			return errf(CodeInvalidSelect, "%s() is not defined on %s column %q", agg.Func, typ, c.Name)
		}
	}
	return nil
}

// checkAggregates enforces where aggregate functions may appear once the
// whole query string is parsed: list reads only (list GET/HEAD and
// set-returning functions), to-many embeds only, never inside a SETOF
// computed column; an aggregated node orders only by its grouping columns
// and the aggregated root takes neither cursors nor distinct=.
func (p *parser) checkAggregates() error {
	root := p.req.Select
	if !root.Aggregates() {
		return nil
	}
	if p.isMutation() {
		return errf(CodeInvalidSelect, "aggregate functions are only valid on reads")
	}
	if !p.isList() {
		return errf(CodeInvalidSelect, "aggregate functions need a collection: a single-row path returns one row")
	}
	if root.Aggregated() {
		if p.req.Distinct != nil {
			return errf(CodeInvalidSelect, "distinct cannot be combined with aggregate functions (the non-aggregated items already group the rows)")
		}
		if p.req.Page.After != nil || p.req.Page.Before != nil {
			return errf(CodeKeysetUnsupported, "after/before cannot be combined with aggregate functions; page grouped results with limit/offset")
		}
	}
	return p.checkAggNode(root, p.req.Order, false)
}

// checkAggNode checks n and the nodes below it; hoisted marks the node of a
// to-one spread, whose items are checked with the node it is spread into.
func (p *parser) checkAggNode(n *plan.Node, order []plan.OrderTerm, hoisted bool) error {
	for i := range n.Fields {
		if f := &n.Fields[i]; f.Node.Aggregates() {
			return errf(CodeInvalidSelect, "aggregate functions are not supported inside the set-returning computed column %q", f.Computed.Key)
		}
	}
	for i := range n.Embeds {
		e := &n.Embeds[i]
		switch {
		case e.Spread && e.Relation.ToMany() && e.Node.Aggregates():
			return errf(CodeInvalidSelect, "aggregate functions are not supported inside to-many spreads (...%s)", e.Key)
		case e.Node.Aggregated() && !e.Relation.ToMany() && !e.Spread:
			return errf(CodeInvalidSelect, "aggregate functions are only supported in to-many embeds and to-one spreads; %q embeds one row (spread it: ...%s(...))", embedKey(e), e.Key)
		}
		if err := p.checkAggNode(e.Node, e.Order, e.SpreadOne()); err != nil {
			return err
		}
	}
	if hoisted || !n.Aggregated() {
		return nil
	}
	if err := p.checkGroupKeys(n); err != nil {
		return err
	}
	if err := checkAggKeys(n); err != nil {
		return err
	}
	for _, t := range order {
		if t.Column == nil || t.Distance != nil || t.Contrib != nil {
			return errf(CodeColumnNotOrderable, "an aggregate select orders only by its grouping columns (selected, non-aggregated columns)")
		}
		if len(t.Path) > 0 {
			if !groupsByPath(n, t.Column, t.Path) {
				return errf(CodeColumnNotOrderable, "order by %s%s needs the same JSON path selected as a grouping key (without a cast)", t.Column.Name, t.Path.String())
			}
			continue
		}
		if !groupsBy(n, t.Column) {
			return errf(CodeColumnNotOrderable, "order by %q needs %q selected as a grouping column (a non-aggregated select item)", t.Column.Name, t.Column.Name)
		}
	}
	return nil
}

// checkGroupKeys rejects grouping keys whose cost the limit cannot bound or
// that PostgreSQL cannot group: embeds other than to-one spreads (evaluated
// per scanned row; a to-one spread is one join), set-returning computed
// columns, and columns of types without equality and ordering. The items of
// to-one spreads are checked as keys of n.
func (p *parser) checkGroupKeys(n *plan.Node) error {
	for i := range n.Embeds {
		e := &n.Embeds[i]
		switch {
		case e.SpreadOne() && !e.Relation.UniqueTarget():
			return errf(CodeInvalidSelect, "spread grouping over %q needs its referenced columns (%s) to be the primary key or a unique key of %q; this relation may match several rows (a @foreignKey onto non-unique columns, or a view without @primaryKey)", e.Key, strings.Join(e.Relation.FK.RefColumns, ", "), e.Relation.To.Path)
		case e.SpreadOne():
			if err := p.checkGroupKeys(e.Node); err != nil {
				return err
			}
		case e.Spread:
			return errf(CodeInvalidSelect, "a to-many spread cannot be a grouping key of an aggregate select (...%s): it yields arrays", e.Key)
		default:
			return errf(CodeInvalidSelect, "embeds cannot be grouping keys of an aggregate select (%q); spread a to-one relation (...%s(col)), aggregate inside the embed (rel(count())) or filter on the relation", embedKey(e), e.Key)
		}
	}
	check := func(c *introspect.Column) error {
		if !p.groupable(c, false, 0) {
			return errf(CodeInvalidSelect, "column %q (%s) cannot be a grouping key: its type has no equality or ordering", c.Name, strings.TrimPrefix(c.PGType, "_"))
		}
		return nil
	}
	if n.Star {
		for _, f := range n.Resource.Columns {
			if err := check(f.Column); err != nil {
				return err
			}
		}
	}
	for i := range n.Fields {
		f := &n.Fields[i]
		switch {
		case f.Agg != nil:
		case f.Cast != nil && f.Cast.Schema == "pg_catalog" && f.Cast.Name == "json":
			return errf(CodeInvalidSelect, "a value cast to json cannot be a grouping key (json has no equality); cast to jsonb")
		case f.Cast != nil, len(f.Path) > 0:
		case f.Column != nil:
			if err := check(f.Column); err != nil {
				return err
			}
		case f.Computed != nil && f.Computed.Function.ReturnsSet:
			return errf(CodeInvalidSelect, "the set-returning computed column %q cannot be a grouping key", f.Computed.Key)
		}
	}
	return nil
}

// groupable reports whether values of c's type can be grouped: not one of
// ungroupableTypes, and for a composite every attribute (json included,
// which is only converted at the top level).
func (p *parser) groupable(c *introspect.Column, nested bool, depth int) bool {
	typ := strings.TrimPrefix(c.PGType, "_")
	if ungroupableTypes[typ] || nested && typ == "json" {
		return false
	}
	if p.built.Catalog == nil || depth > 8 {
		return true
	}
	comp := p.built.Catalog.Composite(c.TypeSchema, typ)
	if comp == nil {
		return true
	}
	for _, f := range comp.Fields {
		if !p.groupable(f, true, depth+1) {
			return false
		}
	}
	return true
}

// groupsBy reports whether col is a grouping key of the aggregated node.
func groupsBy(n *plan.Node, col *introspect.Column) bool {
	if n.Star {
		return true
	}
	for i := range n.Fields {
		if f := &n.Fields[i]; f.Agg == nil && f.Column == col && len(f.Path) == 0 && f.Cast == nil {
			return true
		}
	}
	return false
}

// groupsByPath reports whether col's JSON path is an uncast grouping key of
// the aggregated node.
func groupsByPath(n *plan.Node, col *introspect.Column, path plan.JSONPath) bool {
	for i := range n.Fields {
		if f := &n.Fields[i]; f.Agg == nil && f.Cast == nil && f.Column == col && f.Path.Equal(path) {
			return true
		}
	}
	return false
}

// checkAggKeys rejects two items of an aggregated node answering under the
// same response key (amount.sum(),price.sum() are both "sum"), the items of
// its to-one spreads included.
func checkAggKeys(n *plan.Node) error {
	return aggKeys(n, map[string]bool{})
}

func aggKeys(n *plan.Node, seen map[string]bool) error {
	res := n.Resource
	add := func(key string) error {
		if seen[key] {
			return errf(CodeInvalidSelect, "two select items answer under %q; alias one of them (total:amount.sum())", key)
		}
		seen[key] = true
		return nil
	}
	star := map[string]bool{}
	if n.Star {
		for _, f := range res.Columns {
			if err := add(f.Key); err != nil {
				return err
			}
			star[f.Key] = true
		}
	}
	for i := range n.Fields {
		f := &n.Fields[i]
		key := f.Key(res)
		if star[key] && f.Alias == "" && f.Column != nil && f.Agg == nil && len(f.Path) == 0 && f.Cast == nil {
			continue
		}
		if err := add(key); err != nil {
			return err
		}
	}
	for i := range n.Embeds {
		if e := &n.Embeds[i]; e.SpreadOne() {
			if err := aggKeys(e.Node, seen); err != nil {
				return err
			}
			continue
		}
		if err := add(embedKey(&n.Embeds[i])); err != nil {
			return err
		}
	}
	return nil
}

func embedKey(e *plan.Embed) string {
	if e.Alias != "" {
		return e.Alias
	}
	return e.Key
}
