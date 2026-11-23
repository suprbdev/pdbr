package compile

import (
	"github.com/suprbdev/pdbr/internal/api"
	"github.com/suprbdev/pdbr/internal/plan"
)

// defaultEmbedFanout is the estimated row count of a backward embed without
// an explicit limit (pdbq parity for nested lists).
const defaultEmbedFanout = 10

// Cost estimates a request's row fan-out (docs/pagination.md):
//
//	cost         = 1 + Σ mult(child) × cost(child)
//	mult(list)   = explicit limit clamped to maxPageSize, else 10 (backward embed)
//	               / the effective limit (root list)
//	mult(other)  = 1 (columns, forward embeds, single rows)
//	cost(column) = 1; `*` costs the number of columns it expands to; an
//	               aggregate function costs 1 like a column
//
// Spread embeds cost like embeds (a to-one spread joins one row, a to-many
// spread aggregates its related rows). An aggregated list (a node selecting
// an aggregate function, directly or through a to-one spread) is costed as
// if it had no limit: limit bounds the groups it returns, not the rows the
// aggregate reads (root: maxPageSize, embed: 10). A total on an aggregated
// root scans again: it adds maxPageSize × its grouping keys.
//
// @costMultiplier on an FK replaces a backward embed's multiplier when lower.
// exec compares the result to server.max_cost before compiling.
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
	cost := len(node.Fields)
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
			mult = defaultEmbedFanout
			if e.Page.LimitSet && e.Page.Limit > 0 && !child.Aggregated() {
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
