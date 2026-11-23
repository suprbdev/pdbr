package parse

import (
	"strings"

	"github.com/suprbdev/pdbcore/introspect"
	"github.com/suprbdev/pdbcore/postgis"

	"github.com/suprbdev/pdbr/internal/api"
	"github.com/suprbdev/pdbr/internal/plan"
)

// parseOrder parses "term,term" where
// term := path [.asc|.desc] [.nullsfirst|.nullslast] and
// path := column | computed | relkey[!hint](column) | spatialcol.dist.
func (p *parser) parseOrder(s string, res *api.Resource) ([]plan.OrderTerm, error) {
	if strings.TrimSpace(s) == "" {
		return nil, errf(CodeInvalidFilter, "order must not be empty")
	}
	raws, ok := splitTop(s, ',')
	if !ok {
		return nil, errf(CodeInvalidFilter, "unbalanced parentheses in order %q", s)
	}
	out := make([]plan.OrderTerm, 0, len(raws))
	for _, raw := range raws {
		t, err := p.parseOrderTerm(strings.TrimSpace(raw), res)
		if err != nil {
			return nil, err
		}
		out = append(out, t)
	}
	return out, nil
}

func (p *parser) parseOrderTerm(raw string, res *api.Resource) (plan.OrderTerm, error) {
	var t plan.OrderTerm
	if raw == "" {
		return t, errf(CodeInvalidFilter, "empty order term")
	}
	// Modifiers hang off the end; the relation form has its own dots inside
	// parentheses so we split the tail only after the closing paren.
	path := raw
	tail := ""
	if i := strings.IndexByte(raw, ')'); i >= 0 {
		path, tail = raw[:i+1], raw[i+1:]
	} else if i := strings.IndexByte(raw, '.'); i >= 0 {
		path, tail = raw[:i], raw[i:]
	}
	mods := []string{}
	if tail != "" {
		if !strings.HasPrefix(tail, ".") {
			return t, errf(CodeInvalidFilter, "malformed order term %q", raw)
		}
		mods = strings.Split(tail[1:], ".")
	}
	// A leading ".dist" belongs to the path.
	if len(mods) > 0 && mods[0] == "dist" && !strings.Contains(path, "(") {
		path += ".dist"
		mods = mods[1:]
	}
	dirSet, nullsSet := false, false
	for _, m := range mods {
		switch m {
		case "asc", "desc":
			if dirSet || nullsSet {
				return t, errf(CodeInvalidFilter, "malformed order term %q", raw)
			}
			dirSet = true
			t.Desc = m == "desc"
		case "nullsfirst", "nullslast":
			if nullsSet {
				return t, errf(CodeInvalidFilter, "malformed order term %q", raw)
			}
			nullsSet = true
			t.NullsFirst = m == "nullsfirst"
		default:
			return t, errf(CodeInvalidFilter, "unknown order modifier %q in %q (want asc|desc, nullsfirst|nullslast)", m, raw)
		}
	}
	if err := p.resolveOrderPath(&t, path, res); err != nil {
		return t, err
	}
	return t, nil
}

func (p *parser) resolveOrderPath(t *plan.OrderTerm, path string, res *api.Resource) error {
	if i := strings.IndexByte(path, '('); i >= 0 {
		if !strings.HasSuffix(path, ")") {
			return errf(CodeInvalidFilter, "malformed order path %q", path)
		}
		head, col := path[:i], path[i+1:len(path)-1]
		parts := strings.Split(head, "!")
		if len(parts) > 2 || parts[0] == "" || col == "" {
			return errf(CodeInvalidFilter, "malformed order path %q (want rel[!hint](column))", path)
		}
		hint := ""
		if len(parts) == 2 {
			hint = parts[1]
		}
		if len(res.Relations[parts[0]]) == 0 {
			return errf(CodeUnknownRelation, "unknown relation %q on %q", parts[0], res.Path)
		}
		if !p.opts.RelationOrder {
			return errf(CodeInvalidFilter, "related ordering is not enabled (advanced-filters plugin)")
		}
		rel, err := p.resolveRelation(res, parts[0], hint)
		if err != nil {
			return err
		}
		if !rel.Orderable {
			return errf(CodeColumnNotOrderable, "relation %q on %q is not orderable", parts[0], res.Path)
		}
		f := rel.To.Field(col)
		if f == nil {
			return errf(CodeUnknownColumn, "unknown column %q on %q", col, rel.To.Path)
		}
		if !rel.To.OrderColumns[col] {
			return errf(CodeColumnNotOrderable, "column %q on %q is not orderable", col, rel.To.Path)
		}
		t.Relation = &plan.RelationOrder{Relation: rel, Column: f.Column}
		return nil
	}
	if f := res.Field(path); f != nil {
		if !res.OrderColumns[path] {
			return errf(CodeColumnNotOrderable, "column %q on %q is not orderable", path, res.Path)
		}
		if postgis.IsSpatial(f.Column.PGType) {
			return errf(CodeColumnNotOrderable, "spatial column %q is orderable by distance only (%s.dist with near=)", path, path)
		}
		t.Column = f.Column
		return nil
	}
	if c := res.Computed[path]; c != nil {
		if !p.opts.ComputedOrder || c.Function.ReturnsSet || !c.Orderable {
			return errf(CodeColumnNotOrderable, "computed column %q on %q is not orderable", path, res.Path)
		}
		t.Computed = c
		return nil
	}
	if col, ok := strings.CutSuffix(path, ".dist"); ok {
		f := res.Field(col)
		if f == nil {
			return errf(CodeUnknownColumn, "unknown column %q on %q", col, res.Path)
		}
		if !p.opts.spatialOrder() || !postgis.IsSpatial(f.Column.PGType) {
			return errf(CodeInvalidFilter, ".dist ordering requires a spatial column (postgis plugin); %q is %s", col, f.Column.PGType)
		}
		if !res.OrderColumns[col] {
			return errf(CodeColumnNotOrderable, "column %q on %q is not orderable", col, res.Path)
		}
		t.Distance = f.Column
		return nil
	}
	return errf(CodeUnknownColumn, "unknown column %q on %q", path, res.Path)
}

// parseDistinct parses distinct=col1,col2.
func (p *parser) parseDistinct(s string, res *api.Resource) ([]*introspect.Column, error) {
	if strings.TrimSpace(s) == "" {
		return nil, errf(CodeInvalidFilter, "distinct must not be empty")
	}
	var out []*introspect.Column
	for _, k := range strings.Split(s, ",") {
		k = strings.TrimSpace(k)
		f := res.Field(k)
		if f == nil {
			return nil, errf(CodeUnknownColumn, "unknown column %q on %q", k, res.Path)
		}
		if !res.OrderColumns[k] {
			return nil, errf(CodeColumnNotOrderable, "column %q on %q is not orderable", k, res.Path)
		}
		if t := p.typeOf(f.Column); t.kind == kindSpatial || t.kind == kindComposite {
			return nil, errf(CodeInvalidFilter, "distinct is not supported on %s column %q", t.name(), k)
		}
		out = append(out, f.Column)
	}
	return out, nil
}

// resolveNear parses near= for the .dist order terms collected so far
// (root order and every embed order); missing near with a .dist term → 400.
func (p *parser) resolveNear(near string, hasNear bool) error {
	var dist *introspect.Column
	walk := func(terms []plan.OrderTerm) {
		for _, t := range terms {
			if t.Distance != nil && dist == nil {
				dist = t.Distance
			}
		}
	}
	walk(p.req.Order)
	var walkNode func(n *plan.Node)
	walkNode = func(n *plan.Node) {
		if n == nil {
			return
		}
		for i := range n.Embeds {
			walk(n.Embeds[i].Order)
			walkNode(n.Embeds[i].Node)
		}
	}
	walkNode(p.req.Select)
	if dist == nil {
		return nil
	}
	if !hasNear {
		return errf(CodeInvalidFilter, "order by %s.dist requires near=<GeoJSON or WKT>", dist.Name)
	}
	if _, err := p.coerceSpatial(p.typeOf(dist), near); err != nil {
		return errf(CodeInvalidFilter, "near: %s", err.Detail)
	}
	v, _ := spatialInput(near)
	p.req.Near = v
	return nil
}
