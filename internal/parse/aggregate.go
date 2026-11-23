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

// castAliases maps the SQL spellings of the built-in types to their
// pg_type names, which is what the column types and the type checks use.
var castAliases = map[string]string{
	"smallint": "int2", "int": "int4", "integer": "int4", "bigint": "int8",
	"real": "float4", "float": "float8", "decimal": "numeric", "boolean": "bool",
}

var (
	sumTypes = typeSet("int2", "int4", "int8", "float4", "float8", "numeric", "money", "interval")
	avgTypes = typeSet("int2", "int4", "int8", "float4", "float8", "numeric", "interval")
	// unorderedTypes have no max/min aggregate.
	unorderedTypes = typeSet("json", "jsonb", "bool", "bytea", "xml", "uuid", "point", "line", "lseg", "box", "path", "polygon", "circle")
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
	i := strings.LastIndexByte(it.name, '.')
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
	i := strings.LastIndexByte(it.name, '.')
	if !it.hasInner || i <= 0 {
		return false
	}
	_, ok := aggFuncs[it.name[i+1:]]
	return ok
}

// castType validates a cast target: one SQL type name (letters, digits and
// underscores, not starting with a digit), lower-cased. It is rendered
// unquoted, so the built-in spellings (int, bigint, text) resolve as in SQL.
func castType(raw string) (string, bool) {
	t := strings.ToLower(strings.TrimSpace(raw))
	if t == "" || t[0] >= '0' && t[0] <= '9' {
		return "", false
	}
	for i := 0; i < len(t); i++ {
		c := t[i]
		if c != '_' && (c < 'a' || c > 'z') && (c < '0' || c > '9') {
			return "", false
		}
	}
	return t, true
}

// canonicalType returns the pg_type name of a cast target.
func canonicalType(t string) string {
	if c, ok := castAliases[t]; ok {
		return c
	}
	return t
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
		t, ok := castType(it.outCast)
		if !ok {
			return errf(CodeInvalidSelect, "invalid cast type %q", it.outCast)
		}
		agg.OutputCast = t
	}
	fs := plan.FieldSel{Alias: it.alias, Agg: agg}
	if colPart == "" {
		node.Fields = append(node.Fields, fs)
		return nil
	}
	name, cast, hasCast := strings.Cut(colPart, "::")
	if hasCast {
		t, ok := castType(cast)
		if !ok {
			return errf(CodeInvalidSelect, "invalid cast type %q", cast)
		}
		agg.InputCast = t
	}
	f := res.Field(name)
	if f == nil {
		if res.Computed[name] != nil || res.Pseudo[name] != nil {
			return errf(CodeInvalidSelect, "%s() needs a column; %q is a computed or derived field", fn, name)
		}
		return errf(CodeUnknownColumn, "unknown column %q on %q", name, res.Path)
	}
	fs.Column = f.Column
	if err := p.checkAggregateType(fs.Column, agg); err != nil {
		return err
	}
	node.Fields = append(node.Fields, fs)
	return nil
}

// checkAggregateType rejects aggregate/input-type combinations PostgreSQL
// has no aggregate for, as far as the catalog type (or the input cast)
// tells; a cast to a type outside the built-in tables is left to the
// database.
func (p *parser) checkAggregateType(c *introspect.Column, agg *plan.Aggregate) error {
	spatial := postgis.IsSpatial(c.PGType) && agg.InputCast == ""
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
	if agg.InputCast != "" {
		typ, array = canonicalType(agg.InputCast), false
	}
	known := agg.InputCast == "" || sumTypes[typ] || unorderedTypes[typ] || typ == "text"
	switch agg.Func {
	case plan.AggSum, plan.AggAvg:
		allowed := sumTypes
		if agg.Func == plan.AggAvg {
			allowed = avgTypes
		}
		if known && (array || !allowed[typ]) {
			return errf(CodeInvalidSelect, "%s() needs a numeric column; %q is %s (cast it: %s::numeric.%s())", agg.Func, c.Name, typ, c.Name, agg.Func)
		}
	case plan.AggMax, plan.AggMin:
		composite := agg.InputCast == "" && p.built.Catalog != nil && p.built.Catalog.Composite(c.TypeSchema, typ) != nil
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
	return checkAggNode(root, p.req.Order)
}

func checkAggNode(n *plan.Node, order []plan.OrderTerm) error {
	for i := range n.Fields {
		if f := &n.Fields[i]; f.Node.Aggregates() {
			return errf(CodeInvalidSelect, "aggregate functions are not supported inside the set-returning computed column %q", f.Computed.Key)
		}
	}
	for i := range n.Embeds {
		e := &n.Embeds[i]
		if e.Node.Aggregated() && !e.Relation.ToMany() {
			return errf(CodeInvalidSelect, "aggregate functions are only supported in to-many embeds; %q embeds one row", embedKey(e))
		}
		if err := checkAggNode(e.Node, e.Order); err != nil {
			return err
		}
	}
	if !n.Aggregated() {
		return nil
	}
	if err := checkAggKeys(n); err != nil {
		return err
	}
	for _, t := range order {
		if t.Column == nil || t.Distance != nil || t.Contrib != nil {
			return errf(CodeColumnNotOrderable, "an aggregate select orders only by its grouping columns (selected, non-aggregated columns)")
		}
		if !groupsBy(n, t.Column) {
			return errf(CodeColumnNotOrderable, "order by %q needs %q selected as a grouping column (a non-aggregated select item)", t.Column.Name, t.Column.Name)
		}
	}
	return nil
}

// groupsBy reports whether col is a grouping key of the aggregated node.
func groupsBy(n *plan.Node, col *introspect.Column) bool {
	if n.Star {
		return true
	}
	for i := range n.Fields {
		if f := &n.Fields[i]; f.Agg == nil && f.Column == col {
			return true
		}
	}
	return false
}

// checkAggKeys rejects two items of an aggregated node answering under the
// same response key (amount.sum(),price.sum() are both "sum").
func checkAggKeys(n *plan.Node) error {
	res := n.Resource
	seen := map[string]bool{}
	if n.Star {
		for _, f := range res.Columns {
			seen[f.Key] = true
		}
	}
	add := func(key string) error {
		if seen[key] {
			return errf(CodeInvalidSelect, "two select items answer under %q; alias one of them (total:amount.sum())", key)
		}
		seen[key] = true
		return nil
	}
	for i := range n.Fields {
		f := &n.Fields[i]
		key := f.Key(res)
		if n.Star && f.Alias == "" && f.Column != nil && f.Agg == nil && seen[key] {
			continue
		}
		if err := add(key); err != nil {
			return err
		}
	}
	for i := range n.Embeds {
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
