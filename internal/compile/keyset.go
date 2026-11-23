package compile

import (
	"fmt"
	"slices"
	"strings"

	"github.com/suprbdev/pdbcore/introspect"

	"github.com/suprbdev/pdbr/internal/api"
	"github.com/suprbdev/pdbr/internal/plan"
)

// pagePlan is the Go-side pagination decision for one list.
type pagePlan struct {
	// keyset: the resource has a primary key, so cursors are emitted and
	// after/before compile to keyset predicates.
	keyset bool
	// reversed: before= paging — scan flipped, re-reversed in the outer
	// aggregate.
	reversed   bool
	limit      int
	offset     int
	hasAfter   bool
	hasBefore  bool
	afterKeys  []any
	beforeKeys []any
}

// planPage resolves limit/offset/after/before into a pagePlan. Absent limit
// defaults to the cap rather than an unbounded scan; larger is rejected.
func (s *stmt) planPage(r *api.Resource, page plan.Page) (pagePlan, error) {
	t := r.Table
	p := pagePlan{keyset: t.PrimaryKey != nil, limit: page.Limit, offset: page.Offset}
	if p.limit <= 0 {
		p.limit = s.maxPageSize
	}
	if p.limit > s.maxPageSize {
		return p, errPageSize(p.limit, s.maxPageSize)
	}
	if p.offset < 0 {
		return p, errUser(CodeInvalidFilter, "offset must be >= 0")
	}
	if page.After != nil {
		p.hasAfter, p.afterKeys = true, page.After
	}
	if page.Before != nil {
		p.hasBefore, p.beforeKeys, p.reversed = true, page.Before, true
	}
	if p.hasAfter || p.hasBefore {
		if !p.keyset {
			return p, errUser(CodeKeysetUnsupported, "keyset paging requires a primary key")
		}
		pk := len(t.PrimaryKey.Columns)
		if p.hasAfter && len(p.afterKeys) != pk {
			return p, errUser(CodeInvalidCursor, "cursor has %d key values, want %d", len(p.afterKeys), pk)
		}
		if p.hasBefore && len(p.beforeKeys) != pk {
			return p, errUser(CodeInvalidCursor, "cursor has %d key values, want %d", len(p.beforeKeys), pk)
		}
	}
	if p.hasBefore && p.offset > 0 {
		return p, errUser(CodeKeysetUnsupported, "offset cannot be combined with before")
	}
	return p, nil
}

// anchorVals renders one SQL value expression per order term for the cursor
// row identified by keys. Terms on primary-key columns take the decoded
// cursor value as a plain parameter — the planner can use it as a btree
// start condition, and a deleted anchor still pages on. Other terms become
// uncorrelated scalar subqueries fetching the anchor row's value; a deleted
// anchor makes them yield NULL, so the predicate matches nothing and the
// page comes back empty.
func (s *stmt) anchorVals(t *introspect.Table, terms []orderTerm, keys []any) ([]string, error) {
	pk := t.PrimaryKey.Columns
	if len(pk) != len(keys) {
		return nil, errUser(CodeInvalidCursor, "cursor has %d key values, want %d", len(keys), len(pk))
	}
	vals := make([]string, len(terms))
	subWhere := "" // shared across subqueries: the params are allocated once
	for i, term := range terms {
		if term.plain() {
			if j := slices.Index(pk, term.col); j >= 0 {
				vals[i] = s.param(coerceInput(t.Column(term.col), keys[j]))
				continue
			}
		}
		if subWhere == "" {
			conds, err := s.keyConds(t, pk, keys, "__a")
			if err != nil {
				return nil, err
			}
			subWhere = strings.Join(conds, " AND ")
		}
		vals[i] = fmt.Sprintf("(SELECT %s FROM %s __a WHERE %s)", s.orderExprSQL(term, "__a"), s.sourceRef(t), subWhere)
	}
	return vals, nil
}

// keysetPredicate renders the "strictly after the anchor row" comparison
// against the anchor value expressions from anchorVals (parallel to terms);
// flip selects the "strictly before" form. A uniform direction over
// non-nullable terms compiles to a row comparison the planner turns into an
// index range scan; mixed directions or nullable terms nest as
// s1 OR (e1 AND (s2 OR (e2 AND ...))). NULL ordering matches renderOrder:
// NULLs sort last in both directions unless nullsfirst was given.
func (s *stmt) keysetPredicate(alias string, vals []string, terms []orderTerm, flip bool) string {
	uniform := len(terms) > 0
	for _, term := range terms {
		if term.nullable || term.desc != terms[0].desc || term.owner != nil {
			uniform = false
			break
		}
	}
	if uniform {
		cols := make([]string, len(terms))
		for i, term := range terms {
			cols[i] = s.orderExprSQL(term, alias)
		}
		op := ">"
		if terms[0].desc != flip {
			op = "<"
		}
		if len(cols) == 1 {
			return cols[0] + " " + op + " " + vals[0]
		}
		return fmt.Sprintf("(%s) %s (%s)", strings.Join(cols, ", "), op, strings.Join(vals, ", "))
	}
	pred := ""
	for i := len(terms) - 1; i >= 0; i-- {
		term := terms[i]
		desc := term.desc != flip
		col := s.orderExprSQL(term, alias)
		k := vals[i]
		op := term.cmp(">")
		if desc {
			op = term.cmp("<")
		}
		var strict string
		switch {
		case !term.nullable:
			strict = fmt.Sprintf("%s %s %s", col, op, k)
		case term.nullsFirst != flip:
			// NULLs sort first in the scan direction: NULL is strictly
			// before any value.
			strict = fmt.Sprintf("(%s %s %s OR (%s IS NOT NULL AND %s IS NULL))", col, op, k, col, k)
		default:
			// NULLs sort last: NULL is strictly after any value.
			strict = fmt.Sprintf("(%s %s %s OR (%s IS NULL AND %s IS NOT NULL))", col, op, k, col, k)
		}
		if pred == "" {
			pred = strict
			continue
		}
		eq := fmt.Sprintf("%s IS NOT DISTINCT FROM %s", col, k)
		switch {
		case !term.nullable:
			eq = fmt.Sprintf("%s %s %s", col, term.cmp("="), k)
		case term.owner != nil:
			eq = fmt.Sprintf("(%s %s %s OR (%s IS NULL AND %s IS NULL))", col, term.cmp("="), k, col, k)
		}
		pred = fmt.Sprintf("(%s OR (%s AND %s))", strict, eq, pred)
	}
	return pred
}
