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
// path := column | computed | relkey[!hint](column) | spatialcol.dist |
// column.rank | column.rank_cd | column.trgm_dist | column.trgm_wdist |
// column.trgm_swdist | column.vec_l2 | column.vec_ip | column.vec_cos |
// column.vec_l1.
func (p *parser) parseOrder(s string, res *api.Resource) ([]plan.OrderTerm, error) {
	if strings.TrimSpace(s) == "" {
		return nil, errf(CodeInvalidFilter, "order must not be empty")
	}
	raws, ok := splitTop(s)
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
	jsonPath := hasJSONPath(raw) && indexOutsideQuotes(raw, "(") < 0
	switch i := strings.IndexByte(raw, ')'); {
	case jsonPath:
		if j := indexOutsideQuotes(raw, "."); j >= 0 {
			path, tail = raw[:j], raw[j:]
		}
	case i >= 0:
		path, tail = raw[:i+1], raw[i+1:]
	default:
		if i := strings.IndexByte(raw, '.'); i >= 0 {
			path, tail = raw[:i], raw[i:]
		}
	}
	mods := []string{}
	if tail != "" {
		if !strings.HasPrefix(tail, ".") {
			return t, errf(CodeInvalidFilter, "malformed order term %q", raw)
		}
		mods = strings.Split(tail[1:], ".")
	}
	// A leading ".dist" (or a contrib modifier such as ".rank") belongs to
	// the path.
	if len(mods) > 0 && (mods[0] == "dist" || api.IsContribOrder(mods[0])) && !strings.Contains(path, "(") && !jsonPath {
		path += "." + mods[0]
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
	if hasJSONPath(path) && indexOutsideQuotes(path, "(") < 0 {
		name, jp, _, uerr := splitJSONPath(path)
		if uerr != nil {
			return uerr
		}
		f, err := p.resolvePathField(res, name, jp, CodeInvalidFilter)
		if err != nil {
			return err
		}
		if !res.OrderColumns[f.Key] {
			return errf(CodeColumnNotOrderable, "column %q on %q is not orderable", f.Key, res.Path)
		}
		t.Column, t.Path = f.Column, jp
		return nil
	}
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
		if p.typeOf(f.Column).kind == kindVector {
			return errf(CodeColumnNotOrderable, "vector column %q on %q is orderable by distance only", col, rel.To.Path)
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
		if p.typeOf(f.Column).kind == kindVector {
			return errf(CodeColumnNotOrderable, "vector column %q is orderable by distance only (%s.vec_l2|vec_ip|vec_cos|vec_l1 with vec=)", path, path)
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
	if i := strings.LastIndexByte(path, '.'); i > 0 && api.IsContribOrder(path[i+1:]) {
		return p.resolveContribOrder(t, path[:i], path[i+1:], res)
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
		if !p.isReserved("near", p.rootResource()) {
			return errf(CodeColumnNotOrderable, "%s.dist needs near=, which is a column or argument filter on this route", col)
		}
		t.Distance = f.Column
		return nil
	}
	return errf(CodeUnknownColumn, "unknown column %q on %q", path, res.Path)
}

// resolveContribOrder resolves order=<key>.<mod> for a contrib modifier;
// the full-text filter a rank term needs and the trgm= text or vec= vector
// a distance term needs are bound once the whole query is parsed
// (resolveContribOrders).
func (p *parser) resolveContribOrder(t *plan.OrderTerm, key, mod string, res *api.Resource) error {
	f := res.Field(key)
	if f == nil {
		return errf(CodeUnknownColumn, "unknown column %q on %q", key, res.Path)
	}
	if !res.OrderColumns[key] {
		return errf(CodeColumnNotOrderable, "column %q on %q is not orderable", key, res.Path)
	}
	for _, o := range p.built.ContribOrders(res, f.Column) {
		if o.Param != "" && !p.isReserved(o.Param, p.rootResource()) {
			continue
		}
		if o.Name != mod || (o.Rank != "" && !res.FilterColumns[key]) {
			continue
		}
		t.Contrib = &plan.ContribOrder{Column: f.Column, Name: mod, Rank: string(o.Rank), Expr: o.Expr.Name}
		return nil
	}
	if strings.HasPrefix(mod, "rank") {
		return errf(CodeColumnNotOrderable, "%s.%s needs a column that takes full-text filters", key, mod)
	}
	param := api.DistanceParam(mod)
	if res.NameTaken(param) || !p.isReserved(param, p.rootResource()) {
		return errf(CodeColumnNotOrderable, "%s.%s needs %s=, which is a column or argument filter on this route", key, mod, param)
	}
	if param == "vec" {
		return errf(CodeColumnNotOrderable, "%s.%s needs an index serving it on the column under filters.indexed_only (HNSW; IVFFlat also serves vec_l2, vec_ip and vec_cos on vector and halfvec), or the column in filters.allow_columns", key, mod)
	}
	return errf(CodeColumnNotOrderable, "%s.%s needs a pg_trgm text column with an ordering (GiST) index under filters.indexed_only, or the column in filters.allow_columns", key, mod)
}

// rootResource is the resource the query string addresses (nil before the
// select pass).
func (p *parser) rootResource() *api.Resource {
	if p.root == nil {
		return nil
	}
	return p.root.Resource
}

// resolveContribOrders binds the contrib order terms of the root and of
// every embed: a rank term to the one full-text filter on its column in
// the same scope, a trigram distance term to trgm= and a vector distance
// term to vec= (missing, or the parameter without such a term → 400, like
// near= for .dist). vec= is checked against every vector term's column
// type.
func (p *parser) resolveContribOrders(trgm string, hasTrgm bool, vec string, hasVec bool) error {
	needTrgm := ""
	var vecTerms []*plan.ContribOrder
	bind := func(terms []plan.OrderTerm, filter plan.Expr) error {
		for i := range terms {
			co := terms[i].Contrib
			if co == nil {
				continue
			}
			if api.DistanceParam(co.Name) == "vec" {
				vecTerms = append(vecTerms, co)
				continue
			}
			if co.Rank == "" {
				if needTrgm == "" {
					needTrgm = co.Column.Name + "." + co.Name
				}
				continue
			}
			fc, err := fullTextFilter(filter, co.Column)
			if err != nil {
				return err
			}
			co.Filter = fc
		}
		return nil
	}
	if err := bind(p.req.Order, p.req.Filter); err != nil {
		return err
	}
	var walk func(n *plan.Node) error
	walk = func(n *plan.Node) error {
		if n == nil {
			return nil
		}
		for i := range n.Embeds {
			if err := bind(n.Embeds[i].Order, n.Embeds[i].Filter); err != nil {
				return err
			}
			if err := walk(n.Embeds[i].Node); err != nil {
				return err
			}
		}
		return nil
	}
	if err := walk(p.req.Select); err != nil {
		return err
	}
	if needTrgm != "" {
		needTrgm = "order by " + needTrgm
	} else {
		score, err := p.scoreUse("trgm")
		if err != nil {
			return err
		}
		if score != nil {
			needTrgm = score.Key
		}
	}
	if needTrgm != "" && !hasTrgm {
		return errf(CodeInvalidFilter, "%s requires trgm=<text>", needTrgm)
	}
	if hasTrgm && needTrgm == "" {
		return errf(CodeInvalidFilter, "trgm= applies to order=<col>.trgm_dist|trgm_wdist|trgm_swdist and the trigram score columns only, and none is present")
	}
	if hasTrgm {
		p.req.Trgm = trgm
	}
	return p.bindVec(vecTerms, vec, hasVec)
}

// bindVec parses vec= for the vector and bit distance terms collected over
// the root and every embed, the score pseudo-columns measured against it
// and the distance-threshold filters, checking it against each one's
// column type.
func (p *parser) bindVec(terms []*plan.ContribOrder, vec string, hasVec bool) error {
	score, err := p.scoreUse("vec")
	if err != nil {
		return err
	}
	if len(terms) == 0 && score == nil && len(p.vecFilters) == 0 {
		if hasVec {
			return errf(CodeInvalidFilter, "vec= applies to order=<col>.vec_*, the vector score columns and vec_*_lt filters only, and none is present")
		}
		return nil
	}
	if !hasVec {
		switch {
		case len(terms) > 0:
			return errf(CodeInvalidFilter, "order by %s.%s requires vec=<vector>", terms[0].Column.Name, terms[0].Name)
		case score != nil:
			return errf(CodeInvalidFilter, "%s requires vec=<vector>", score.Key)
		}
		return errf(CodeInvalidFilter, "filter %s on %s requires vec=<vector>", p.vecFilters[0].Name, p.vecFilters[0].Column.Name)
	}
	v, err := api.VectorOperand(vec)
	if err != nil {
		return errf(CodeInvalidFilter, "vec: %s", err.Error())
	}
	for _, co := range terms {
		voc, _ := api.DistanceVocabulary(p.built.Catalog, co.Column)
		e, ok := voc.Expr(co.Expr)
		if !ok {
			return errf(CodeColumnNotOrderable, "%s.%s is not available on this column", co.Column.Name, co.Name)
		}
		if _, err := e.Params(v); err != nil {
			return errf(CodeInvalidFilter, "vec: %s", err.Error())
		}
	}
	for _, pc := range p.scores {
		if pc.Score.Param != "vec" {
			continue
		}
		if _, err := pc.Score.Expr.Params(v); err != nil {
			return errf(CodeInvalidFilter, "vec: %s", err.Error())
		}
	}
	for _, f := range p.vecFilters {
		if _, err := f.Op.Params(map[string]any{"vector": v, "distance": 0}); err != nil {
			return errf(CodeInvalidFilter, "vec: %s", err.Error())
		}
	}
	p.req.Vec = v
	return nil
}

// scoreUse returns the first selected score pseudo-column measured against
// param (nil when none is), failing when param is not a reserved
// parameter of the route (a root column or argument takes its name).
func (p *parser) scoreUse(param string) (*api.PseudoColumn, error) {
	for _, pc := range p.scores {
		if pc.Score.Param != param {
			continue
		}
		if !p.isReserved(param, p.rootResource()) {
			return nil, errf(CodeInvalidSelect, "%s needs %s=, which is a column or argument filter on this route", pc.Key, param)
		}
		return pc, nil
	}
	return nil, nil
}

// fullTextFilter finds the single non-negated full-text condition on col
// among the conjuncts of filter: a Cond, or the terms of And nodes at any
// depth (and=(...) nests one inside the top-level And); Or and Not
// subtrees do not count.
func fullTextFilter(filter plan.Expr, col *introspect.Column) (*plan.Cond, error) {
	var found *plan.Cond
	ambiguous := false
	var walk func(e plan.Expr)
	walk = func(e plan.Expr) {
		switch f := e.(type) {
		case plan.And:
			for _, t := range f.Terms {
				walk(t)
			}
		case *plan.And:
			if f != nil {
				for _, t := range f.Terms {
					walk(t)
				}
			}
		case plan.Cond:
			if f.Column != col || f.Negate || f.Relation != nil || !api.IsFullTextOp(string(f.Op)) {
				return
			}
			if found != nil {
				ambiguous = true
				return
			}
			found = &f
		}
	}
	walk(filter)
	if ambiguous {
		return nil, errf(CodeInvalidFilter, "order by %s.rank is ambiguous: more than one full-text filter on %s", col.Name, col.Name)
	}
	if found == nil {
		return nil, errf(CodeInvalidFilter, "order by %s.rank needs a non-negated fts, plfts, phfts or wfts filter on %s outside or=/not.", col.Name, col.Name)
	}
	return found, nil
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
		if t := p.typeOf(f.Column); t.kind == kindSpatial || t.kind == kindComposite || t.kind == kindVector {
			return nil, errf(CodeInvalidFilter, "distinct is not supported on %s column %q", t.name(), k)
		}
		out = append(out, f.Column)
	}
	return out, nil
}

// resolveNear parses near= for the .dist order terms collected so far
// (root order and every embed order); missing near with a .dist term, or
// near without one, → 400.
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
	score, err := p.scoreUse("near")
	if err != nil {
		return err
	}
	if dist == nil && score == nil {
		if hasNear {
			return errf(CodeInvalidFilter, "near= applies to order=<col>.dist and <col>_distance only, and neither is present")
		}
		return nil
	}
	if !hasNear {
		if dist == nil {
			return errf(CodeInvalidFilter, "%s requires near=<GeoJSON or WKT>", score.Key)
		}
		return errf(CodeInvalidFilter, "order by %s.dist requires near=<GeoJSON or WKT>", dist.Name)
	}
	if dist == nil {
		dist = score.Column
	}
	if _, err := p.coerceSpatial(p.typeOf(dist), near); err != nil {
		return errf(CodeInvalidFilter, "near: %s", err.Detail)
	}
	v, _ := spatialInput(near)
	p.req.Near = v
	return nil
}
