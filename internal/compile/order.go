package compile

import (
	"fmt"
	"strings"

	"github.com/suprbdev/pdbcore/introspect"
	"github.com/suprbdev/pdbcore/postgis"

	"github.com/suprbdev/pdbr/internal/plan"
)

// orderTerm is one resolved ORDER BY term.
type orderTerm struct {
	col string // plain column (also the PK tiebreaker)
	// fn set: order by a computed column called on the current row.
	fn      *introspect.Function
	fnTable *introspect.Table
	// rel set: order by a related table's column (forward: correlated
	// scalar subquery; backward: MIN/MAX aggregate).
	rel *plan.RelationOrder
	// dist set: order by KNN distance from near to this spatial column.
	dist       *introspect.Column
	desc       bool
	nullsFirst bool
	// nullable reports whether the sort expression can yield NULL:
	// computed functions and relational subqueries always can; plain
	// columns only when not declared NOT NULL.
	nullable bool
}

// orderTerms resolves the plan's order terms and appends the primary key
// columns not already present, so keyset paging is stable.
func (s *stmt) orderTerms(t *introspect.Table, terms []plan.OrderTerm) []orderTerm {
	out := make([]orderTerm, 0, len(terms)+2)
	used := map[string]bool{}
	for i := range terms {
		term := &terms[i]
		o := orderTerm{desc: term.Desc, nullsFirst: term.NullsFirst}
		switch {
		case term.Distance != nil:
			// Distance ordering ignores the nulls modifier.
			o.dist = term.Distance
			o.nullsFirst = false
			o.nullable = !term.Distance.NotNull
		case term.Relation != nil:
			o.rel = term.Relation
			o.nullable = true
		case term.Computed != nil:
			o.fn = term.Computed.Function
			o.fnTable = term.Computed.Resource.Table
			o.nullable = true
		case term.Column != nil:
			o.col = term.Column.Name
			o.nullable = !term.Column.NotNull
			used[o.col] = true
		default:
			continue
		}
		out = append(out, o)
	}
	if t.PrimaryKey != nil {
		for _, name := range t.PrimaryKey.Columns {
			if used[name] {
				continue
			}
			c := t.Column(name)
			out = append(out, orderTerm{col: name, nullable: c != nil && !c.NotNull})
		}
	}
	return out
}

// renderOrder renders terms as an ORDER BY clause over alias; reversed
// flips every direction (before= paging) so the reversed scan is the exact
// mirror of the forward order. NULLs sort last in both directions unless a
// nulls modifier was given; the modifier is only emitted when it deviates
// from PostgreSQL's default for the direction and the term can be NULL (an
// index in default order can still answer the sort).
func (s *stmt) renderOrder(terms []orderTerm, alias string, reversed bool) string {
	if len(terms) == 0 {
		return ""
	}
	parts := make([]string, len(terms))
	for i, term := range terms {
		desc, nullsFirst := term.desc, term.nullsFirst
		if reversed {
			desc, nullsFirst = !desc, !nullsFirst
		}
		dir := " ASC"
		if desc {
			dir = " DESC"
		}
		if term.nullable {
			switch {
			case desc && !nullsFirst:
				dir += " NULLS LAST"
			case !desc && nullsFirst:
				dir += " NULLS FIRST"
			}
		}
		parts[i] = s.orderExprSQL(term, alias) + dir
	}
	return "\nORDER BY " + strings.Join(parts, ", ")
}

// orderExprSQL renders the sort expression for one term.
func (s *stmt) orderExprSQL(term orderTerm, alias string) string {
	switch {
	case term.dist != nil:
		// <-> is the KNN operator: a GiST index answers it directly, which is
		// what makes "nearest N" cheap.
		return postgis.KNNSQL(colRef(alias, term.dist.Name), s.nearExpr(term.dist))
	case term.rel != nil:
		return s.relationOrderSQL(term, alias)
	case term.fn != nil:
		return s.computedCallSQL(term.fn, term.fnTable, alias)
	}
	return colRef(alias, term.col)
}

// nearExpr renders the near= geometry for distance ordering over col.
func (s *stmt) nearExpr(col *introspect.Column) string {
	if s.plan.Near == nil {
		s.fail(errUser(CodeInvalidFilter, "order by distance requires near="))
		return "NULL"
	}
	expr, err := s.spatialOperand(postgis.FlavourOf(col.PGType), spatialSRID(col), s.plan.Near)
	if err != nil {
		s.fail(err)
		return "NULL"
	}
	return expr
}

// relationOrderSQL renders the correlated subquery for a related order
// term. To-one relations select the sort column off the single referenced
// row; to-many relations (backward, or forward over a PERIOD key) aggregate
// over the related rows (MIN for asc, MAX for desc), yielding NULL when no
// related row exists. The inner alias is fixed: relational subqueries never
// nest, so it cannot collide.
func (s *stmt) relationOrderSQL(term orderTerm, alias string) string {
	rel := term.rel
	const sub = "__ro"
	expr := colRef(sub, rel.Column.Name)
	if rel.Relation.ToMany() {
		agg := "MIN"
		if term.desc {
			agg = "MAX"
		}
		expr = agg + "(" + expr + ")"
	}
	return fmt.Sprintf("(SELECT %s FROM %s %s WHERE %s)", expr, s.sourceRef(rel.Relation.To.Table), sub, joinConds(rel.Relation, sub, alias))
}

// reorderForDistinct puts the DISTINCT ON columns at the head of the order
// terms (PostgreSQL requires them leftmost in ORDER BY), keeping a matching
// direction when one was given and dropping the duplicates.
func reorderForDistinct(terms []orderTerm, dcols []*introspect.Column) []orderTerm {
	if len(dcols) == 0 {
		return terms
	}
	used := map[string]bool{}
	out := make([]orderTerm, 0, len(terms)+len(dcols))
	for _, dc := range dcols {
		if used[dc.Name] {
			continue
		}
		used[dc.Name] = true
		o := orderTerm{col: dc.Name, nullable: !dc.NotNull}
		for _, term := range terms {
			if term.fn == nil && term.rel == nil && term.dist == nil && term.col == dc.Name {
				o.desc, o.nullsFirst = term.desc, term.nullsFirst
				break
			}
		}
		out = append(out, o)
	}
	for _, term := range terms {
		if term.fn == nil && term.rel == nil && term.dist == nil && used[term.col] {
			continue
		}
		out = append(out, term)
	}
	return out
}
