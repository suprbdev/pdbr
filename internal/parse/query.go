package parse

import (
	"net/url"
	"strconv"
	"strings"

	"github.com/suprbdev/pdbr/internal/api"
	"github.com/suprbdev/pdbr/internal/plan"
)

// param is one decoded query parameter, in query-string order.
type param struct {
	key string
	val string
}

// splitQuery decodes a raw query string preserving order and duplicates
// (url.Values loses both). Malformed percent-escapes are a 400.
func splitQuery(raw string) ([]param, error) {
	var out []param
	for raw != "" {
		var part string
		part, raw, _ = strings.Cut(raw, "&")
		if part == "" {
			continue
		}
		k, v, _ := strings.Cut(part, "=")
		key, err := url.QueryUnescape(k)
		if err != nil {
			return nil, errf(CodeInvalidParameter, "malformed query parameter %q", part)
		}
		val, err := url.QueryUnescape(v)
		if err != nil {
			return nil, errf(CodeInvalidParameter, "malformed query parameter %q", part)
		}
		out = append(out, param{key: key, val: val})
	}
	return out, nil
}

// reserved are the query parameters that are never filters (bare form).
// The reference parameters carry api.ReservedPrefix, so no column, computed
// column or function argument can take their place.
var reserved = map[string]bool{
	"select": true, "order": true, "limit": true, "offset": true, "after": true,
	"before": true, "count": true, "distinct": true, "on_conflict": true, "columns": true,
	api.ParamNear: true, api.ParamTrgm: true, api.ParamVec: true, api.ParamLevenshtein: true,
	api.ParamLatLon: true, api.ParamGeometry: true,
}

// isReserved reports whether key is a reserved parameter on this route.
func (p *parser) isReserved(key string, _ *api.Resource) bool {
	if key == "columns" {
		return p.isMutation() && p.method != plan.DELETE
	}
	return reserved[key]
}

// listOnly are the reserved params that only make sense on list requests.
var listOnly = map[string]bool{
	"order": true, "limit": true, "offset": true, "after": true, "before": true,
	"count": true, "distinct": true,
}

// isMutation reports whether the request is a write on a resource (bodies
// and keys, no read parameters).
func (p *parser) isMutation() bool {
	if p.match.RPC != nil {
		return false
	}
	switch p.method {
	case plan.POST, plan.PUT, plan.PATCH, plan.DELETE:
		return true
	}
	return false
}

// isBulkWrite reports a PATCH or DELETE on a list route: the root filters
// select the rows to write.
func (p *parser) isBulkWrite() bool {
	return p.match.RPC == nil && p.match.Route.Kind == api.RouteList && (p.method == plan.PATCH || p.method == plan.DELETE)
}

// parseQuery processes the query string against res (the list/row resource
// or the SETOF <table> RPC's resource). extra, when non-nil, is consulted
// first for non-reserved keys (RPC GET arguments); it returns true when it
// consumed the parameter.
func (p *parser) parseQuery(raw string, res *api.Resource) error {
	return p.parseQueryWith(raw, res, nil)
}

func (p *parser) parseQueryWith(raw string, res *api.Resource, extra func(key, val string) (bool, error)) error {
	params, err := splitQuery(raw)
	if err != nil {
		return err
	}
	seen := map[string]bool{}
	for _, q := range params {
		if p.isReserved(q.key, res) {
			if seen[q.key] {
				return errf(CodeInvalidParameter, "duplicate parameter %q", q.key)
			}
			seen[q.key] = true
		}
	}
	// select first: dotted parameters address embeds by response key.
	root := &plan.Node{Resource: res}
	if res != nil {
		for _, q := range params {
			if q.key == "select" {
				n, err := p.parseSelect(q.val, res, 0)
				if err != nil {
					return err
				}
				root = n
				p.req.Select = n
			}
		}
	}
	p.root = root
	var near, trgm, vec, lev, latlon string
	hasNear, hasTrgm, hasVec, hasLev, hasLatLon := false, false, false, false, false
	for _, q := range params {
		switch {
		case q.key == "select":
			if res == nil {
				return errf(CodeInvalidSelect, "select is not valid on this route")
			}
			continue
		case q.key == api.ParamNear:
			near, hasNear = q.val, true
			continue
		case q.key == api.ParamTrgm:
			trgm, hasTrgm = q.val, true
			continue
		case q.key == api.ParamVec:
			vec, hasVec = q.val, true
			continue
		case q.key == api.ParamLevenshtein:
			lev, hasLev = q.val, true
			continue
		case q.key == api.ParamLatLon:
			latlon, hasLatLon = q.val, true
			continue
		case q.key == api.ParamGeometry:
			if q.val == "" {
				return errf(CodeInvalidParameter, "~geometry= needs the key of a spatial select item")
			}
			p.req.Geometry = q.val
			continue
		case q.key == "columns" && p.isReserved(q.key, res):
			if err := p.parseColumnsParam(q.val); err != nil {
				return err
			}
			continue
		case q.key == "on_conflict":
			if err := p.parseOnConflict(q.val, res); err != nil {
				return err
			}
			continue
		case p.isReserved(q.key, res):
			if err := p.parseListParam(q.key, q.val, res); err != nil {
				return err
			}
			continue
		}
		if extra != nil {
			ok, err := extra(q.key, q.val)
			if err != nil {
				return err
			}
			if ok {
				continue
			}
		}
		if err := p.parseDotted(root, q.key, q.val, 0); err != nil {
			return err
		}
	}
	if err := p.resolveNear(near, hasNear); err != nil {
		return err
	}
	if err := p.resolveContribOrders(refParams{trgm, hasTrgm, vec, hasVec, lev, hasLev, latlon, hasLatLon}); err != nil {
		return err
	}
	if err := p.resolveHeadlines(); err != nil {
		return err
	}
	if err := p.applyRange(); err != nil {
		return err
	}
	if !p.req.Page.LimitSet {
		p.req.Page.Limit = p.opts.maxPageSize()
	}
	if err := p.checkPaging(res); err != nil {
		return err
	}
	return p.checkAggregates()
}

// parseListParam handles order/limit/offset/after/before/count/distinct.
func (p *parser) parseListParam(key, val string, res *api.Resource) error {
	if listOnly[key] {
		if p.isMutation() {
			return errf(CodeInvalidParameter, "%s is not valid on a mutation route", key)
		}
		if !p.isList() {
			return errf(CodeInvalidParameter, "%s is not valid on a single-row path", key)
		}
	}
	switch key {
	case "order":
		if res == nil {
			return errf(CodeInvalidParameter, "order is not valid on this route")
		}
		terms, err := p.parseOrder(val, res)
		if err != nil {
			return err
		}
		p.req.Order = terms
	case "limit":
		n, err := p.parseLimit(val)
		if err != nil {
			return err
		}
		p.req.Page.Limit, p.req.Page.LimitSet = n, true
	case "offset":
		n, err := parseOffset(val)
		if err != nil {
			return err
		}
		p.req.Page.Offset = n
	case "after", "before":
		if res == nil {
			return errf(CodeKeysetUnsupported, "%s is not valid on this route", key)
		}
		vals, err := p.parseCursor(key, val, res)
		if err != nil {
			return err
		}
		if key == "after" {
			p.req.Page.After = vals
		} else {
			p.req.Page.Before = vals
		}
	case "count":
		switch val {
		case "true":
			p.req.Count = true
		case "false":
		default:
			return errf(CodeInvalidParameter, "count must be true or false, got %q", val)
		}
	case "distinct":
		if res == nil {
			return errf(CodeInvalidParameter, "distinct is not valid on this route")
		}
		cols, err := p.parseDistinct(val, res)
		if err != nil {
			return err
		}
		p.req.Distinct = cols
	}
	return nil
}

func (p *parser) parseLimit(val string) (int, error) {
	n, err := strconv.Atoi(val)
	if err != nil {
		return 0, errf(CodeInvalidParameter, "limit must be an integer, got %q", val)
	}
	if n < 0 {
		return 0, errf(CodeInvalidParameter, "limit must not be negative, got %d", n)
	}
	if n > p.opts.maxPageSize() {
		return 0, errf(CodePageSizeExceeded, "limit %d exceeds the maximum page size %d", n, p.opts.maxPageSize())
	}
	return n, nil
}

func parseOffset(val string) (int, error) {
	n, err := strconv.Atoi(val)
	if err != nil {
		return 0, errf(CodeInvalidParameter, "offset must be an integer, got %q", val)
	}
	if n < 0 {
		return 0, errf(CodeInvalidParameter, "offset must not be negative, got %d", n)
	}
	return n, nil
}

// checkPaging enforces the keyset combination rules once every param is in.
func (p *parser) checkPaging(res *api.Resource) error {
	pg := &p.req.Page
	if pg.After == nil && pg.Before == nil {
		return nil
	}
	if p.req.Distinct != nil {
		return errf(CodeKeysetUnsupported, "after/before cannot be combined with distinct")
	}
	if pg.Before != nil && pg.Offset > 0 {
		return errf(CodeKeysetUnsupported, "offset cannot be combined with before")
	}
	if p.match.RPC != nil {
		return errf(CodeKeysetUnsupported, "after/before are not supported on RPC results; use offset")
	}
	if res == nil || !res.Keyset {
		return errf(CodeKeysetUnsupported, "resource %q has no primary key; use offset paging", resName(res))
	}
	return nil
}

func (p *parser) parseOnConflict(val string, res *api.Resource) error {
	switch {
	case res == nil, p.match.Route.Kind != api.RouteList:
		return errf(CodeInvalidParameter, "on_conflict is only valid on the collection path (PUT, or POST with Prefer: resolution=)")
	case p.method == plan.POST && p.req.Prefer.Resolution == "":
		return errf(CodeInvalidParameter, "on_conflict on POST needs Prefer: resolution=merge-duplicates or resolution=ignore-duplicates")
	case p.method != plan.PUT && p.method != plan.POST:
		return errf(CodeInvalidParameter, "on_conflict is only valid on PUT, or POST with Prefer: resolution=")
	}
	var cols []string
	for _, k := range strings.Split(val, ",") {
		k = strings.TrimSpace(k)
		f := res.Field(k)
		if f == nil {
			return errf(CodeUnknownColumn, "unknown column %q on %q", k, resName(res))
		}
		cols = append(cols, f.Column.Name)
	}
	if pk := res.PrimaryKey(); pk != nil && sameSet(pk.Columns, cols) {
		p.req.OnConflict = pk
		return nil
	}
	u := res.UniqueFor(cols)
	if u == nil {
		if tk := res.TemporalKeyFor(cols); tk != nil {
			if p.method == plan.POST && p.req.Prefer.Resolution == plan.IgnoreDuplicates {
				p.req.OnConflict = tk
				return nil
			}
			return errf(CodeTemporalConflict, "on_conflict columns (%s) are a temporal key (WITHOUT OVERLAPS) of %q, which only Prefer: resolution=ignore-duplicates accepts as a conflict target", val, resName(res))
		}
		return errf(CodeInvalidBody, "on_conflict columns (%s) match no unique constraint of %q", val, resName(res))
	}
	p.req.OnConflict = u
	return nil
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

func resName(r *api.Resource) string {
	if r == nil {
		return ""
	}
	return r.Path
}

// parseDotted routes a non-reserved parameter: a filter on node's resource,
// a per-embed parameter (posts.order, posts.limit, posts.offset,
// posts.<filter>) addressed by the embed's response key, a logic tree
// (and=, or=, not.and=, not.or=), or a relation filter.
func (p *parser) parseDotted(node *plan.Node, key, val string, depth int) error {
	res := node.Resource
	if isLogicKey(key) {
		expr, err := p.parseLogic(res, key, val)
		if err != nil {
			return err
		}
		return p.addFilter(node, expr)
	}
	// A real field wins over any dotted interpretation.
	if filterKey(res, key) {
		expr, err := p.parseColumnFilter(res, key, val)
		if err != nil {
			return err
		}
		return p.addFilter(node, expr)
	}
	head, rest, dotted := strings.Cut(key, ".")
	if dotted && !strings.Contains(head, "!") {
		if emb := findEmbed(node, head); emb != nil {
			return p.parseEmbedParam(emb, rest, val, depth+1)
		}
	}
	expr, err := p.parseRelationFilter(res, key, val)
	if err != nil {
		return err
	}
	return p.addFilter(node, expr)
}

// parseEmbedParam handles the part after "<embed>." for a selected embed.
func (p *parser) parseEmbedParam(emb *plan.Embed, rest, val string, depth int) error {
	res := emb.Node.Resource
	switch rest {
	case "order":
		terms, err := p.parseOrder(val, res)
		if err != nil {
			return err
		}
		emb.Order = terms
		return nil
	case "limit":
		n, err := p.parseLimit(val)
		if err != nil {
			return err
		}
		emb.Page.Limit, emb.Page.LimitSet = n, true
		return nil
	case "offset":
		n, err := parseOffset(val)
		if err != nil {
			return err
		}
		emb.Page.Offset = n
		return nil
	case "select", "after", "before", "count", "distinct":
		return errf(CodeInvalidParameter, "%s is not supported on embeds", rest)
	}
	return p.parseDotted(emb.Node, rest, val, depth)
}

// findEmbed returns the embed of node addressed by name: its alias when
// aliased, else its relation key.
func findEmbed(node *plan.Node, name string) *plan.Embed {
	for i := range node.Embeds {
		e := &node.Embeds[i]
		if e.Alias != "" {
			if e.Alias == name {
				return e
			}
			continue
		}
		if e.Key == name {
			return e
		}
	}
	return nil
}

// addFilter ANDs expr into the filter of the request (root node) or of the
// embed owning node.
func (p *parser) addFilter(node *plan.Node, expr plan.Expr) error {
	if node == p.root {
		if p.isMutation() && !p.isBulkWrite() {
			return errf(CodeInvalidFilter, "filters are not valid on a mutation route")
		}
		p.req.Filter = andExpr(p.req.Filter, expr)
		return nil
	}
	emb := embedFor(p.root, node)
	if emb == nil {
		return errf(CodeInvalidFilter, "internal: filter target is not a selected embed")
	}
	emb.Filter = andExpr(emb.Filter, expr)
	return nil
}

// mergeRelation folds a bare relation filter into an earlier one on the
// same relation and quantifier (docs/filtering.md: several conditions on
// the same key are ANDed inside one EXISTS). It reports whether expr was
// absorbed.
func mergeRelation(cur plan.Expr, expr plan.Expr) bool {
	c, ok := expr.(plan.Cond)
	if !ok || c.Relation == nil || c.Relation.Leaf == nil {
		return false
	}
	terms := []plan.Expr{cur}
	if a, ok := cur.(plan.And); ok {
		terms = a.Terms
	}
	for _, t := range terms {
		prev, ok := t.(plan.Cond)
		if !ok || prev.Relation == nil || prev.Relation.Exists != nil || prev.Negate || c.Negate {
			continue
		}
		rp := prev.Relation
		if len(rp.Steps) != 1 || rp.Steps[0] != c.Relation.Steps[0] || rp.Quantifier != c.Relation.Quantifier {
			continue
		}
		var leaves []plan.Expr
		switch {
		case rp.Filter != nil:
			if a, ok := rp.Filter.(plan.And); ok {
				leaves = append(leaves, a.Terms...)
			} else {
				leaves = append(leaves, rp.Filter)
			}
		case rp.Leaf != nil:
			leaves = append(leaves, *rp.Leaf)
		}
		leaves = append(leaves, *c.Relation.Leaf)
		rp.Filter = plan.And{Terms: leaves}
		return true
	}
	return false
}

// embedFor finds the embed whose Node is node anywhere under root.
func embedFor(root, node *plan.Node) *plan.Embed {
	if root == nil {
		return nil
	}
	for i := range root.Embeds {
		e := &root.Embeds[i]
		if e.Node == node {
			return e
		}
		if found := embedFor(e.Node, node); found != nil {
			return found
		}
	}
	return nil
}

// andExpr appends expr to an existing filter, flattening top-level Ands so
// the plan is a single conjunction in query-string order.
func andExpr(cur, expr plan.Expr) plan.Expr {
	if cur == nil {
		return expr
	}
	if mergeRelation(cur, expr) {
		return cur
	}
	if a, ok := cur.(plan.And); ok {
		return plan.And{Terms: append(a.Terms, expr)}
	}
	return plan.And{Terms: []plan.Expr{cur, expr}}
}
