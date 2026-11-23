package api

import (
	"regexp"
	"strconv"
	"strings"

	"github.com/suprbdev/pdbcore/contrib"
	"github.com/suprbdev/pdbcore/introspect"
	"github.com/suprbdev/pdbcore/postgis"
)

// addDomains claims a component schema name for every domain over a
// built-in type when Options.Domains is set. Domains over PostGIS, range
// and extension types keep their base type's schema, whose input and
// output shapes differ or come from a plugin.
func (b *Builder) addDomains() {
	if !b.Options.Domains {
		return
	}
	for _, d := range b.Catalog.Domains {
		if !domainDocumented(d) {
			continue
		}
		name := b.claimOpenAPIName(b.typeSchemaName(d.Schema, d.Name), "domain "+d.Ref())
		b.Domains[d.Ref()] = &Domain{Name: name, Domain: d}
	}
}

func domainDocumented(d *introspect.Domain) bool {
	if d.BaseTypeSchema != "pg_catalog" {
		return false
	}
	elem := strings.TrimPrefix(d.BaseType, "_")
	_, isRange := contrib.Range(elem)
	_, isMulti := contrib.Multirange(elem)
	return !isRange && !isMulti && !postgis.IsSpatial(elem)
}

// domainRef returns the schema of a value whose type involves the domain
// ref: a $ref to the domain's component, or an array of it when pgType
// (declared or resolved) is an array of the domain. nil when the domain
// has no component.
func (b *Builder) domainRef(ref, pgType string) *Schema {
	d := b.Domains[ref]
	if d == nil {
		return nil
	}
	if strings.HasPrefix(pgType, "_") && !d.Domain.IsArrayBase {
		return &Schema{Type: "array", Items: SchemaRef(d.Name)}
	}
	return SchemaRef(d.Name)
}

// colTypeSchema is typeSchema for a column-shaped value, referencing its
// domain's component when it has one.
func (b *Builder) colTypeSchema(c *introspect.Column) *Schema {
	if s := b.domainRef(c.Domain, c.PGType); s != nil {
		return s
	}
	return b.typeSchema(c.PGType, c.TypeSchema, c.EnumRef)
}

// colInputTypeSchema is inputTypeSchema for a column-shaped value,
// referencing its domain's component when it has one.
func (b *Builder) colInputTypeSchema(c *introspect.Column) *Schema {
	if s := b.domainRef(c.Domain, c.PGType); s != nil {
		return s
	}
	return b.inputTypeSchema(c.PGType, c.TypeSchema, c.BaseTypeSchema, c.EnumRef)
}

// returnTypeSchema is the schema of one value f returns.
func (b *Builder) returnTypeSchema(f *introspect.Function) *Schema {
	return b.colTypeSchema(ReturnColumn(b.Catalog, f))
}

// domainSchema renders d's component: its base type's schema, the
// pattern and bounds its (and its parents') CHECK constraints spell
// trivially, and a description of the domain with every constraint.
func (b *Builder) domainSchema(d *introspect.Domain) *Schema {
	base := b.typeSchema(d.BaseType, d.BaseTypeSchema, "")
	s := *base
	chain := b.Catalog.DomainChain(d)
	var cons []string
	notNull := false
	for _, cd := range chain {
		notNull = notNull || cd.NotNull
		cons = append(cons, cd.Checks...)
	}
	if !d.IsArrayBase {
		applyChecks(&s, d.BaseType, cons)
	}
	var desc []string
	if c := description(d.Comment); c != "" {
		desc = append(desc, c)
	}
	line := "Domain " + d.Ref() + " over " + d.BaseType + "."
	if base.Description != "" {
		line += " " + base.Description
	}
	desc = append(desc, line)
	if notNull {
		cons = append([]string{"NOT NULL"}, cons...)
	}
	if len(cons) > 0 {
		desc = append(desc, "Constraints: `"+strings.Join(cons, "`, `")+"`.")
	}
	s.Description = strings.Join(desc, "\n\n")
	return &s
}

var (
	checkCompare = regexp.MustCompile(`^\(?VALUE\)?(?:::[a-z ]+)? (>=|<=|>|<) (.+)$`)
	checkRegex   = regexp.MustCompile(`^\(?VALUE\)?(?:::[a-z ]+)? ~ '((?:[^']|'')*)'(?:::[a-z ]+)?$`)
	checkInt     = regexp.MustCompile(`^(?:\((-?\d+)\)::[a-z ]+|'(-?\d+)'::[a-z ]+|(-?\d+))$`)
)

var numericBases = map[string]bool{"int2": true, "int4": true, "float4": true, "float8": true}
var textBases = map[string]bool{"text": true, "varchar": true, "bpchar": true, "name": true}

// applyChecks sets pattern (text bases) and integer bounds (int2, int4,
// float4 and float8 bases) from CHECK constraints of the simple forms
// pg_get_constraintdef renders for VALUE ~ 'regex', VALUE >= n, VALUE > n,
// VALUE <= n and VALUE < n, also inside a top-level AND. NOT VALID
// constraints are skipped: existing values may violate them. Only one
// pattern can be expressed, so two or more leave pattern unset.
func applyChecks(s *Schema, baseType string, checks []string) {
	var patterns []string
	for _, c := range checks {
		if strings.HasSuffix(c, " NOT VALID") {
			continue
		}
		expr, ok := strings.CutPrefix(c, "CHECK ")
		if !ok {
			continue
		}
		for _, term := range conjuncts(expr) {
			if m := checkRegex.FindStringSubmatch(term); m != nil {
				patterns = append(patterns, strings.ReplaceAll(m[1], "''", "'"))
				continue
			}
			m := checkCompare.FindStringSubmatch(term)
			if m == nil || !numericBases[baseType] {
				continue
			}
			n, ok := checkIntLiteral(m[2])
			if !ok {
				continue
			}
			switch m[1] {
			case ">=":
				s.Minimum = tighter(s.Minimum, n, true)
			case ">":
				s.ExclusiveMinimum = tighter(s.ExclusiveMinimum, n, true)
			case "<=":
				s.Maximum = tighter(s.Maximum, n, false)
			case "<":
				s.ExclusiveMaximum = tighter(s.ExclusiveMaximum, n, false)
			}
		}
	}
	if len(patterns) == 1 && textBases[baseType] {
		s.Pattern = patterns[0]
	}
}

func checkIntLiteral(lit string) (int, bool) {
	m := checkInt.FindStringSubmatch(lit)
	if m == nil {
		return 0, false
	}
	n, err := strconv.Atoi(m[1] + m[2] + m[3])
	return n, err == nil
}

func tighter(cur *int, n int, lower bool) *int {
	if cur == nil || (lower && n > *cur) || (!lower && n < *cur) {
		return &n
	}
	return cur
}

// conjuncts strips the parentheses around a constraint expression and
// splits it at top-level ANDs, each term stripped likewise. Quoted
// literals are skipped when matching parentheses and AND.
func conjuncts(expr string) []string {
	expr = stripParens(expr)
	var out []string
	depth, start, quoted := 0, 0, false
	for i := 0; i < len(expr); i++ {
		switch c := expr[i]; {
		case c == '\'':
			quoted = !quoted
		case quoted:
		case c == '(':
			depth++
		case c == ')':
			depth--
		case depth == 0 && strings.HasPrefix(expr[i:], " AND "):
			out = append(out, stripParens(expr[start:i]))
			start = i + len(" AND ")
			i = start - 1
		}
	}
	return append(out, stripParens(expr[start:]))
}

// stripParens removes parentheses enclosing the whole of s.
func stripParens(s string) string {
	for len(s) >= 2 && s[0] == '(' && s[len(s)-1] == ')' && closes(s) == len(s)-1 {
		s = s[1 : len(s)-1]
	}
	return s
}

// closes returns the index of the parenthesis closing s[0], or -1.
func closes(s string) int {
	depth, quoted := 0, false
	for i := 0; i < len(s); i++ {
		switch c := s[i]; {
		case c == '\'':
			quoted = !quoted
		case quoted:
		case c == '(':
			depth++
		case c == ')':
			depth--
			if depth == 0 {
				return i
			}
		}
	}
	return -1
}

// ArgColumn describes a function parameter as a column of its resolved
// type: a parameter declared as a domain (or an array of one) takes the
// domain's base type and keeps the domain in Column.Domain, as a domain
// column does. Calls still cast to the declared type.
func ArgColumn(cat *introspect.Catalog, a introspect.FuncArg) *introspect.Column {
	c := resolvedColumn(cat, a.PGType, a.TypeSchema, a.Domain)
	c.Name = a.Name
	return c
}

// ReturnColumn describes the type f returns as a column, with a domain
// resolved as in ArgColumn.
func ReturnColumn(cat *introspect.Catalog, f *introspect.Function) *introspect.Column {
	c := resolvedColumn(cat, f.ReturnType, f.ReturnTypeSchema, f.ReturnDomain)
	c.Name = f.Name
	return c
}

func resolvedColumn(cat *introspect.Catalog, pgType, typeSchema, domain string) *introspect.Column {
	isArray := strings.HasPrefix(pgType, "_")
	var d *introspect.Domain
	if cat != nil {
		d = cat.DomainByRef(domain)
	}
	col := func(t string, array bool) *introspect.Column {
		return &introspect.Column{PGType: t, TypeSchema: d.BaseTypeSchema, BaseTypeSchema: d.BaseTypeSchema, IsArray: array, Domain: domain}
	}
	switch {
	case d == nil:
	case d.IsArrayBase && !isArray:
		return col(d.BaseType, true)
	case !d.IsArrayBase && isArray:
		return col("_"+d.BaseType, true)
	case !d.IsArrayBase:
		return col(d.BaseType, false)
	}
	return &introspect.Column{PGType: pgType, TypeSchema: typeSchema, IsArray: isArray}
}
