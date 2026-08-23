package parse

import (
	"strconv"
	"strings"

	"github.com/suprbdev/pdbr/internal/api"
	"github.com/suprbdev/pdbr/internal/compile"
	"github.com/suprbdev/pdbr/internal/plan"
)

// isLogicKey reports whether a parameter name is a logic tree
// (and / or / not.and / not.or). Nested logic items use the call form
// (and(...), or(...)) instead.
func isLogicKey(key string) bool {
	switch key {
	case "and", "or", "not.and", "not.or":
		return true
	}
	return false
}

// opSpec is one operator's shape.
type opSpec struct {
	op plan.Op
	// list: value is a parenthesised list (in); structural: is; spatial: postgis.
	list, structural, spatial bool
}

var ops = map[string]opSpec{
	"eq": {op: plan.OpEq}, "neq": {op: plan.OpNeq},
	"gt": {op: plan.OpGt}, "gte": {op: plan.OpGte}, "lt": {op: plan.OpLt}, "lte": {op: plan.OpLte},
	"in":   {op: plan.OpIn, list: true},
	"is":   {op: plan.OpIs, structural: true},
	"like": {op: plan.OpLike}, "ilike": {op: plan.OpILike},
	"sw": {op: plan.OpSw}, "ew": {op: plan.OpEw},
	"cs": {op: plan.OpCs}, "cd": {op: plan.OpCd}, "ov": {op: plan.OpOv},
	"hk": {op: plan.OpHk}, "jpe": {op: plan.OpJpe}, "jpm": {op: plan.OpJpm},
	"st_intersects":       {op: plan.OpStIntersects, spatial: true},
	"st_disjoint":         {op: plan.OpStDisjoint, spatial: true},
	"st_contains":         {op: plan.OpStContains, spatial: true},
	"st_containsproperly": {op: plan.OpStContainsProperly, spatial: true},
	"st_within":           {op: plan.OpStWithin, spatial: true},
	"st_covers":           {op: plan.OpStCovers, spatial: true},
	"st_coveredby":        {op: plan.OpStCoveredBy, spatial: true},
	"st_crosses":          {op: plan.OpStCrosses, spatial: true},
	"st_overlaps":         {op: plan.OpStOverlaps, spatial: true},
	"st_touches":          {op: plan.OpStTouches, spatial: true},
	"st_equals":           {op: plan.OpStEquals, spatial: true},
	"st_dwithin":          {op: plan.OpStDWithin, spatial: true, list: true},
	"st_beyond":           {op: plan.OpStBeyond, spatial: true, list: true},
	"bbox_intersects":     {op: plan.OpBBoxIntersects, spatial: true, list: true},
	"bbox_contains":       {op: plan.OpBBoxContains, spatial: true, list: true},
	"bbox_containedby":    {op: plan.OpBBoxContainedBy, spatial: true, list: true},
}

// operand is a parsed "[not.]op.value".
type operand struct {
	negate bool
	op     string
	spec   opSpec
	value  string
}

// splitOperand parses "[not.]op.value" (value runs to the end).
func splitOperand(raw string) (operand, *compile.UserError) {
	var o operand
	s := raw
	if strings.HasPrefix(s, "not.") {
		o.negate = true
		s = s[len("not."):]
	}
	op, val, ok := strings.Cut(s, ".")
	if !ok {
		if s == "" {
			return o, errf(CodeInvalidFilter, "missing operator in %q", raw)
		}
		return o, errf(CodeInvalidFilter, "missing value after operator %q", s)
	}
	spec, known := ops[op]
	if !known && op != "exists" {
		return o, errf(CodeInvalidFilter, "unknown operator %q", op)
	}
	o.op, o.spec, o.value = op, spec, val
	return o, nil
}

// parseColumnFilter parses "<key>=<operand>" for a real or computed column
// of res.
func (p *parser) parseColumnFilter(res *api.Resource, key, raw string) (plan.Expr, error) {
	o, err := splitOperand(raw)
	if err != nil {
		return nil, err
	}
	return p.columnCond(res, key, o)
}

// columnCond resolves key on res and builds the Cond for o.
func (p *parser) columnCond(res *api.Resource, key string, o operand) (plan.Expr, error) {
	if o.op == "exists" {
		return nil, errf(CodeInvalidFilter, "exists is only valid on a relation key (%s=exists.true)", key)
	}
	if f := res.Field(key); f != nil {
		if !res.FilterColumns[key] {
			return nil, errf(CodeColumnNotFilterable, "column %q on %q is not filterable", key, res.Path)
		}
		v, err := p.coerceOperand(p.typeOf(f.Column), key, o)
		if err != nil {
			return nil, err
		}
		return plan.Cond{Column: f.Column, Op: o.spec.op, Value: v, Negate: o.negate}, nil
	}
	if c := res.Computed[key]; c != nil {
		if !p.opts.ComputedFilters || c.Function.ReturnsSet || !c.Filterable {
			return nil, errf(CodeColumnNotFilterable, "computed column %q on %q is not filterable", key, res.Path)
		}
		t := p.typeFor(c.Function.ReturnType, c.Function.ReturnTypeSchema, false, "")
		v, err := p.coerceOperand(t, key, o)
		if err != nil {
			return nil, err
		}
		return plan.Cond{Computed: c, Op: o.spec.op, Value: v, Negate: o.negate}, nil
	}
	return nil, errf(CodeUnknownColumn, "unknown column %q on %q", key, res.Path)
}

// coerceOperand checks op/type compatibility and coerces the operand.
func (p *parser) coerceOperand(t colType, key string, o operand) (any, error) {
	spec := o.spec
	if t.kind == kindComposite {
		return nil, errf(CodeColumnNotFilterable, "composite column %q is not filterable", key)
	}
	if spec.spatial {
		if !p.opts.spatialFilters() {
			return nil, errf(CodeInvalidFilter, "spatial operator %q is not enabled (postgis plugin)", o.op)
		}
		if t.kind != kindSpatial || t.isArray {
			return nil, errf(CodeInvalidFilter, "operator %q requires a spatial column, %q is %s", o.op, key, t.name())
		}
		return p.spatialOperand(t, o)
	}
	switch spec.op {
	case plan.OpIs:
		switch strings.ToLower(o.value) {
		case "null":
			return nil, nil
		case "true", "false":
			if t.kind != kindBool || t.isArray {
				return nil, errf(CodeInvalidFilter, "is.%s requires a boolean column, %q is %s", o.value, key, t.name())
			}
			return strings.EqualFold(o.value, "true"), nil
		}
		return nil, errf(CodeInvalidFilter, "is takes null, true or false, got %q", o.value)
	case plan.OpEq, plan.OpNeq, plan.OpGt, plan.OpGte, plan.OpLt, plan.OpLte:
		if t.kind == kindSpatial || (t.kind == kindJSON && t.base == "json") {
			return nil, errf(CodeInvalidFilter, "operator %q is not supported on %s column %q", o.op, t.name(), key)
		}
		v, err := p.coerceText(t, o.value)
		if err != nil {
			return nil, err
		}
		return v, nil
	case plan.OpIn:
		if t.isArray || t.kind == kindJSON || t.kind == kindSpatial {
			return nil, errf(CodeInvalidFilter, "operator in is not supported on %s column %q", t.name(), key)
		}
		items, err := parseList(o.value)
		if err != nil {
			return nil, err
		}
		out := make([]any, len(items))
		for i, it := range items {
			v, err := p.coerceElem(t, it)
			if err != nil {
				return nil, err
			}
			out[i] = v
		}
		return out, nil
	case plan.OpLike, plan.OpILike, plan.OpSw, plan.OpEw:
		if t.kind != kindText || t.isArray {
			return nil, errf(CodeInvalidFilter, "operator %q requires a text column, %q is %s", o.op, key, t.name())
		}
		return o.value, nil
	case plan.OpCs, plan.OpCd:
		if t.kind == kindJSON && !t.isArray {
			v, err := p.coerceElem(t, o.value)
			if err != nil {
				return nil, err
			}
			return v, nil
		}
		fallthrough
	case plan.OpOv:
		if !t.isArray {
			return nil, errf(CodeInvalidFilter, "operator %q requires an array column, %q is %s", o.op, key, t.name())
		}
		v, err := p.coerceText(t, o.value)
		if err != nil {
			return nil, err
		}
		return v, nil
	case plan.OpHk, plan.OpJpe, plan.OpJpm:
		if t.kind != kindJSON || t.isArray {
			return nil, errf(CodeInvalidFilter, "operator %q requires a json/jsonb column, %q is %s", o.op, key, t.name())
		}
		return o.value, nil
	}
	return nil, errf(CodeInvalidFilter, "unknown operator %q", o.op)
}

// spatialOperand parses the operand of a spatial op.
func (p *parser) spatialOperand(t colType, o operand) (any, error) {
	switch o.spec.op {
	case plan.OpStDWithin, plan.OpStBeyond:
		items, err := parseList(o.value)
		if err != nil {
			return nil, err
		}
		if len(items) < 2 || len(items) > 3 {
			return nil, errf(CodeInvalidFilter, "%s takes (geometry,distance[,spheroid])", o.op)
		}
		geom, err := p.coerceSpatial(t.flavour, items[0])
		if err != nil {
			return nil, err
		}
		dist, perr := strconv.ParseFloat(strings.TrimSpace(items[1]), 64)
		if perr != nil || dist < 0 {
			return nil, errf(CodeInvalidFilter, "%s distance must be a non-negative number, got %q", o.op, items[1])
		}
		d := plan.DWithin{Geom: geom, Distance: dist}
		if len(items) == 3 {
			b, ok := parseBool(items[2])
			if !ok {
				return nil, errf(CodeInvalidFilter, "%s spheroid must be a boolean, got %q", o.op, items[2])
			}
			d.Spheroid = &b
		}
		return d, nil
	case plan.OpBBoxIntersects, plan.OpBBoxContains, plan.OpBBoxContainedBy:
		items, err := parseList(o.value)
		if err != nil {
			return nil, err
		}
		if len(items) < 4 || len(items) > 5 {
			return nil, errf(CodeInvalidFilter, "%s takes (minx,miny,maxx,maxy[,srid])", o.op)
		}
		var b plan.BBox
		nums := []*float64{&b.MinX, &b.MinY, &b.MaxX, &b.MaxY}
		for i, dst := range nums {
			f, perr := strconv.ParseFloat(strings.TrimSpace(items[i]), 64)
			if perr != nil {
				return nil, errf(CodeInvalidFilter, "%s coordinate %q is not a number", o.op, items[i])
			}
			*dst = f
		}
		if len(items) == 5 {
			n, perr := strconv.Atoi(strings.TrimSpace(items[4]))
			if perr != nil || n <= 0 {
				return nil, errf(CodeInvalidFilter, "%s srid must be a positive integer, got %q", o.op, items[4])
			}
			b.SRID = n
		}
		return b, nil
	}
	v, err := p.coerceSpatial(t.flavour, o.value)
	if err != nil {
		return nil, err
	}
	return v, nil
}

// parseList parses "(a,b,"c,d")": items comma-separated, double-quoted
// items may contain , ( ) " with \" and \\ escapes.
func parseList(s string) ([]string, *compile.UserError) {
	s = strings.TrimSpace(s)
	if len(s) < 2 || s[0] != '(' || s[len(s)-1] != ')' {
		return nil, errf(CodeInvalidFilter, "expected a parenthesised list, got %q", s)
	}
	body := s[1 : len(s)-1]
	if strings.TrimSpace(body) == "" {
		return []string{}, nil
	}
	parts, ok := splitTop(body, ',')
	if !ok {
		return nil, errf(CodeInvalidFilter, "unbalanced list %q", s)
	}
	out := make([]string, len(parts))
	for i, part := range parts {
		v, err := unquote(strings.TrimSpace(part))
		if err != nil {
			return nil, err
		}
		out[i] = v
	}
	return out, nil
}

// unquote strips one level of double quotes with \" and \\ escapes; unquoted
// input is returned verbatim.
func unquote(s string) (string, *compile.UserError) {
	if len(s) < 2 || s[0] != '"' {
		return s, nil
	}
	if s[len(s)-1] != '"' {
		return "", errf(CodeInvalidFilter, "unterminated quote in %q", s)
	}
	var b strings.Builder
	body := s[1 : len(s)-1]
	for i := 0; i < len(body); i++ {
		c := body[i]
		if c == '\\' && i+1 < len(body) && (body[i+1] == '"' || body[i+1] == '\\') {
			b.WriteByte(body[i+1])
			i++
			continue
		}
		if c == '"' {
			return "", errf(CodeInvalidFilter, "unescaped quote in %q", s)
		}
		b.WriteByte(c)
	}
	return b.String(), nil
}

// parseLogic parses and=(...) / or=(...) / not.and=(...) / not.or=(...).
func (p *parser) parseLogic(res *api.Resource, key, val string) (plan.Expr, error) {
	negate := strings.HasPrefix(key, "not.")
	kind := strings.TrimPrefix(key, "not.")
	items, err := parseList(val)
	if err != nil {
		return nil, err
	}
	if len(items) == 0 {
		return nil, errf(CodeInvalidFilter, "%s needs at least one condition", key)
	}
	terms := make([]plan.Expr, 0, len(items))
	for _, it := range items {
		e, err := p.parseLogicItem(res, it, 0)
		if err != nil {
			return nil, err
		}
		terms = append(terms, e)
	}
	var expr plan.Expr
	if kind == "and" {
		expr = plan.And{Terms: terms}
	} else {
		expr = plan.Or{Terms: terms}
	}
	if negate {
		return plan.Not{Term: expr}, nil
	}
	return expr, nil
}

const maxLogicDepth = 32

// parseLogicItem parses one item inside a logic list:
// "col.[not.]op.value" or "[not.]and(...)" / "[not.]or(...)".
func (p *parser) parseLogicItem(res *api.Resource, item string, depth int) (plan.Expr, error) {
	if depth > maxLogicDepth {
		return nil, errf(CodeInvalidFilter, "logic nesting too deep")
	}
	item = strings.TrimSpace(item)
	for _, prefix := range []string{"and(", "or(", "not.and(", "not.or("} {
		if !strings.HasPrefix(item, prefix) {
			continue
		}
		if !strings.HasSuffix(item, ")") {
			return nil, errf(CodeInvalidFilter, "unbalanced logic item %q", item)
		}
		key := strings.TrimSuffix(prefix, "(")
		inner := item[len(prefix)-1:]
		negate := strings.HasPrefix(key, "not.")
		kind := strings.TrimPrefix(key, "not.")
		items, err := parseList(inner)
		if err != nil {
			return nil, err
		}
		if len(items) == 0 {
			return nil, errf(CodeInvalidFilter, "%s needs at least one condition", key)
		}
		terms := make([]plan.Expr, 0, len(items))
		for _, it := range items {
			e, err := p.parseLogicItem(res, it, depth+1)
			if err != nil {
				return nil, err
			}
			terms = append(terms, e)
		}
		var expr plan.Expr
		if kind == "and" {
			expr = plan.And{Terms: terms}
		} else {
			expr = plan.Or{Terms: terms}
		}
		if negate {
			return plan.Not{Term: expr}, nil
		}
		return expr, nil
	}
	// Both "not.col.op.value" (docs example) and "col.not.op.value"
	// (grammar) negate the condition.
	negate := false
	if strings.HasPrefix(item, "not.") {
		negate = true
		item = item[len("not."):]
	}
	col, rest, ok := strings.Cut(item, ".")
	if !ok || col == "" {
		return nil, errf(CodeInvalidFilter, "malformed condition %q (want column.op.value)", item)
	}
	if res.Field(col) == nil && res.Computed[col] == nil {
		// Relation filter inside a logic list: rel[!hint].col.op.value or
		// rel=exists.true spelled rel.exists.true.
		relKey, _, _ := strings.Cut(col, "!")
		if len(res.Relations[relKey]) > 0 {
			next, rest2, ok := strings.Cut(rest, ".")
			if ok && next != "exists" && next != "not" {
				col, rest = col+"."+next, rest2
			}
		}
	}
	o, err := splitOperand(rest)
	if err != nil {
		return nil, err
	}
	if negate {
		o.negate = !o.negate
	}
	// Quoted scalar values are unquoted here; list operands keep their
	// parentheses and are unquoted item by item.
	if !o.spec.list {
		v, err := unquote(o.value)
		if err != nil {
			return nil, err
		}
		o.value = v
	}
	if res.Field(col) != nil || res.Computed[col] != nil {
		return p.columnCond(res, col, o)
	}
	return p.relationCond(res, col, o)
}

// parseRelationFilter parses "<relkey[!hint]*>[.<column>]=<operand>" for a
// key that is neither a column nor a selected embed.
func (p *parser) parseRelationFilter(res *api.Resource, key, raw string) (plan.Expr, error) {
	o, err := splitOperand(raw)
	if err != nil {
		return nil, err
	}
	return p.relationCond(res, key, o)
}

func (p *parser) relationCond(res *api.Resource, key string, o operand) (plan.Expr, error) {
	head, rest, dotted := strings.Cut(key, ".")
	parts := strings.Split(head, "!")
	relKey := parts[0]
	if len(res.Relations[relKey]) == 0 {
		if dotted {
			return nil, errf(CodeUnknownRelation, "unknown relation %q on %q", relKey, res.Path)
		}
		return nil, errf(CodeUnknownColumn, "unknown column %q on %q", key, res.Path)
	}
	if !p.opts.RelationFilters {
		return nil, errf(CodeInvalidFilter, "relation filters are not enabled (advanced-filters plugin)")
	}
	quant := plan.Some
	hint := ""
	for _, h := range parts[1:] {
		switch h {
		case "none", "every":
			if quant != plan.Some {
				return nil, errf(CodeInvalidFilter, "relation %q has more than one quantifier", head)
			}
			quant = plan.Quantifier(h)
		case "":
			return nil, errf(CodeInvalidFilter, "empty hint in %q", head)
		case "inner":
			return nil, errf(CodeInvalidFilter, "!inner is a select= hint, not a filter quantifier")
		default:
			if hint != "" {
				return nil, errf(CodeInvalidFilter, "relation %q has more than one hint", head)
			}
			hint = h
		}
	}
	rel, err := p.resolveRelation(res, relKey, hint)
	if err != nil {
		return nil, err
	}
	if !rel.Filterable {
		return nil, errf(CodeColumnNotFilterable, "relation %q on %q is not filterable", relKey, res.Path)
	}
	if rel.Forward && quant != plan.Some {
		return nil, errf(CodeInvalidFilter, "quantifier !%s is not valid on forward relation %q", quant, relKey)
	}
	if !dotted {
		if o.op != "exists" {
			return nil, errf(CodeInvalidFilter, "relation %q takes exists.true|false or a column path (%s.<column>=...)", relKey, relKey)
		}
		if quant != plan.Some {
			return nil, errf(CodeInvalidFilter, "exists cannot be combined with a quantifier")
		}
		b, ok := parseBool(o.value)
		if !ok || (o.value != "true" && o.value != "false") {
			return nil, errf(CodeInvalidFilter, "exists takes true or false, got %q", o.value)
		}
		if o.negate {
			b = !b
		}
		return plan.Cond{Relation: &plan.RelationPath{Steps: []*api.Relation{rel}, Quantifier: plan.Exists, Exists: &b}, Op: plan.OpExists}, nil
	}
	if o.op == "exists" {
		return nil, errf(CodeInvalidFilter, "exists applies to the relation key itself (%s=exists.true)", relKey)
	}
	if rel.To.Field(rest) == nil && rel.To.Computed[rest] == nil {
		if strings.Contains(rest, ".") {
			return nil, errf(CodeInvalidFilter, "relation filter paths are one level deep (%s.<column>)", relKey)
		}
		return nil, errf(CodeUnknownColumn, "unknown column %q on %q", rest, rel.To.Path)
	}
	leafExpr, err := p.columnCond(rel.To, rest, o)
	if err != nil {
		return nil, err
	}
	leaf := leafExpr.(plan.Cond)
	return plan.Cond{Relation: &plan.RelationPath{Steps: []*api.Relation{rel}, Quantifier: quant, Leaf: &leaf}, Op: leaf.Op}, nil
}
