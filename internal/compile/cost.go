package compile

import (
	"github.com/suprbdev/pdbcore/contrib"
	"github.com/suprbdev/pdbcore/postgis"

	"github.com/suprbdev/pdbr/internal/api"
	"github.com/suprbdev/pdbr/internal/plan"
)

// Cost estimates a request's row fan-out (docs/pagination.md):
//
//	cost         = 1 + Σ mult(child) × cost(child)
//	mult(list)   = the limit the compiled SQL applies: an explicit limit
//	               clamped to maxPageSize, else maxPageSize (root list,
//	               to-many embed or spread)
//	mult(other)  = 1 (columns, forward embeds, single rows)
//	cost(column) = 1; `*` costs the number of columns it expands to; an
//	               aggregate function costs 1 like a column; a headline
//	               costs contrib.HeadlineDocumentCost of its document
//	               bound (plan.Headline.MaxDocument), a buffer argument adds
//	               postgis.BufferCost; a raster pixel value costs
//	               postgis.RasterValueCost and raster summary statistics
//	               postgis.RasterSummaryStatsCost (raster metadata
//	               accessors and every other pseudo-column cost 1)
//
// Spread embeds cost like embeds (a to-one spread joins one row, a to-many
// spread aggregates its related rows). An aggregated list (a node selecting
// an aggregate function, directly or through a to-one spread) is costed as
// if it had no limit: limit bounds the groups it returns, not the rows the
// aggregate reads (maxPageSize). A total on an aggregated root scans again:
// it adds maxPageSize × its grouping keys. A set-returning computed column
// has no limit either and costs 1 + maxPageSize × the cost of its select.
//
// @costMultiplier on an FK replaces a backward embed's multiplier when lower.
// Filters are not priced (the estimate is the fan-out of what is returned),
// so neither is the raster st_intersects filter. exec compares the result to
// limits.max_cost before compiling.
func Cost(req *plan.Request, maxPageSize int) int {
	if req == nil {
		return 0
	}
	if maxPageSize <= 0 {
		maxPageSize = defaultMaxPageSize
	}
	node := req.Select
	res := req.Resource
	if res == nil && req.RPC != nil {
		res = req.RPC.Resource
	}
	if node == nil {
		node = &plan.Node{Resource: res, Star: true}
	} else if node.Resource == nil {
		n := *node
		n.Resource = res
		node = &n
	}
	mult := 1
	if req.Key == nil && (req.RPC == nil || req.RPC.Returns == api.ReturnSetOfRows) {
		mult = req.Page.Limit
		if mult <= 0 || mult > maxPageSize || node.Aggregated() {
			mult = maxPageSize
		}
	}
	cost := 1 + mult*nodeCost(node, maxPageSize)
	if req.Count && node.Aggregated() {
		cost += maxPageSize * groupKeys(node)
	}
	return cost
}

// groupKeys counts the grouping keys of an aggregated node, those of its
// to-one spreads included.
func groupKeys(node *plan.Node) int {
	n := 0
	if node.Star && node.Resource != nil {
		n = len(node.Resource.Columns)
	}
	for i := range node.Fields {
		if node.Fields[i].Agg == nil {
			n++
		}
	}
	for i := range node.Embeds {
		if e := &node.Embeds[i]; e.SpreadOne() && e.Node != nil {
			n += groupKeys(e.Node)
		}
	}
	return n
}

func nodeCost(node *plan.Node, maxPageSize int) int {
	if node == nil {
		return 0
	}
	cost := 0
	for i := range node.Fields {
		cost += fieldCost(&node.Fields[i], maxPageSize)
	}
	if node.Star && node.Resource != nil {
		cost += len(node.Resource.Columns)
	}
	for i := range node.Embeds {
		e := &node.Embeds[i]
		child := e.Node
		if child == nil || child.Resource == nil {
			c := plan.Node{Star: true}
			if child != nil {
				c = *child
			}
			if e.Relation != nil {
				c.Resource = e.Relation.To
			}
			child = &c
		}
		mult := 1
		if e.Relation != nil && e.Relation.ToMany() {
			mult = maxPageSize
			if e.Page.Limit > 0 && !child.Aggregated() {
				mult = min(e.Page.Limit, maxPageSize)
			}
			if cm := e.Relation.CostMultiplier; cm > 0 && cm < mult {
				mult = cm
			}
		}
		cost += 1 + mult*nodeCost(child, maxPageSize)
	}
	return cost
}

// fieldCost is the cost of one selected field: 1;
// contrib.HeadlineDocumentCost of its document bound for a headline; the raster weight of a raster pixel value or summary
// statistics pseudo-column; 1 + postgis.BufferCost for a spatial value
// with a buffer argument; or for a set-returning computed column
// 1 + maxPageSize × the cost of its rows (one value per row for a set of
// scalars).
func fieldCost(f *plan.FieldSel, maxPageSize int) int {
	if f.Pseudo != nil && f.Pseudo.Headline != nil {
		return contrib.HeadlineDocumentCost(headlineDocument(f.Headline))
	}
	if rv := rasterOf(f); rv != nil {
		switch {
		case rv.Value:
			return postgis.RasterValueCost
		case rv.SummaryStats:
			return postgis.RasterSummaryStatsCost
		}
		return postgis.RasterAccessorCost
	}
	if _, ok := f.Args["buffer"]; ok {
		return 1 + postgis.BufferCost
	}
	c := f.Computed
	if c == nil || c.Function == nil || !c.Function.ReturnsSet {
		return 1
	}
	if c.SetOf == nil {
		return 1 + maxPageSize
	}
	child := f.Node
	if child == nil {
		child = &plan.Node{Resource: c.SetOf, Star: true}
	} else if child.Resource == nil {
		n := *child
		n.Resource = c.SetOf
		child = &n
	}
	return 1 + maxPageSize*nodeCost(child, maxPageSize)
}

// rasterOf returns the raster surface a selected field reads, or nil.
func rasterOf(f *plan.FieldSel) *api.RasterValue {
	if f.Pseudo == nil {
		return nil
	}
	return f.Pseudo.Raster
}

// headlineDocument returns the characters of its document headline h
// reads: h.MaxDocument, or contrib.MaxHeadlineDocument when unset.
func headlineDocument(h *plan.Headline) int {
	if h == nil || h.MaxDocument <= 0 {
		return contrib.MaxHeadlineDocument
	}
	return h.MaxDocument
}
