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
//	cost(column) = 1; `*` costs the number of columns it expands to
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
		if mult <= 0 || mult > maxPageSize {
			mult = maxPageSize
		}
	}
	return 1 + mult*nodeCost(node, maxPageSize)
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
		if e.Relation != nil && !e.Relation.Forward {
			mult = defaultEmbedFanout
			if e.Page.LimitSet && e.Page.Limit > 0 {
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
