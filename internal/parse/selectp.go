package parse

import (
	"strconv"
	"strings"

	"github.com/suprbdev/pdbcore/introspect"
	"github.com/suprbdev/pdbcore/postgis"

	"github.com/suprbdev/pdbr/internal/api"
	"github.com/suprbdev/pdbr/internal/plan"
)

// selectItem is one parsed (unresolved) select= item.
type selectItem struct {
	star     bool
	alias    string
	name     string
	hints    []string
	inner    string // contents of the trailing (...)
	hasInner bool
	outCast  string // type after a closing parenthesis: fn()::type
}

// splitTop splits s on commas at nesting depth 0 (parentheses, braces and
// double quotes are respected); ok is false on unbalanced input.
func splitTop(s string) (parts []string, ok bool) {
	var out []string
	depth := 0
	inQuote := false
	start := 0
	for i := 0; i < len(s); i++ {
		c := s[i]
		switch {
		case inQuote:
			if c == '\\' && i+1 < len(s) {
				i++
			} else if c == '"' {
				inQuote = false
			}
		case c == '"':
			inQuote = true
		case c == '(' || c == '{':
			depth++
		case c == ')' || c == '}':
			depth--
			if depth < 0 {
				return nil, false
			}
		case c == ',' && depth == 0:
			out = append(out, s[start:i])
			start = i + 1
		}
	}
	if depth != 0 || inQuote {
		return nil, false
	}
	out = append(out, s[start:])
	return out, true
}

// scanSelectItem parses "[alias:]name[!hint]*[(inner)[::type]]" or "*".
func scanSelectItem(raw string) (selectItem, bool) {
	var it selectItem
	s := strings.TrimSpace(raw)
	if s == "*" {
		it.star = true
		return it, true
	}
	if s == "" {
		return it, false
	}
	// alias
	if i := indexOutsideQuotes(s, ":"); i >= 0 && !strings.HasPrefix(s[i:], "::") {
		paren := indexOutsideQuotes(s, "(")
		bang := indexOutsideQuotes(s, "!")
		if (paren < 0 || i < paren) && (bang < 0 || i < bang) {
			it.alias = s[:i]
			s = s[i+1:]
			if it.alias == "" || s == "" {
				return it, false
			}
		}
	}
	if j := strings.LastIndexByte(s, ')'); j >= 0 && strings.HasPrefix(s[j+1:], "::") {
		it.outCast = s[j+3:]
		s = s[:j+1]
		if it.outCast == "" {
			return it, false
		}
	}
	if i := indexOutsideQuotes(s, "("); i >= 0 {
		if !strings.HasSuffix(s, ")") {
			return it, false
		}
		it.inner = s[i+1 : len(s)-1]
		it.hasInner = true
		s = s[:i]
	}
	if hasJSONPath(s) {
		it.name = s
		return it, true
	}
	parts := strings.Split(s, "!")
	it.name = parts[0]
	for _, h := range parts[1:] {
		if h == "" {
			return it, false
		}
		it.hints = append(it.hints, h)
	}
	if it.name == "" || strings.ContainsAny(it.name, "()\"") {
		return it, false
	}
	return it, true
}

// parseSelect parses a select= list against res into a Node.
func (p *parser) parseSelect(s string, res *api.Resource, depth int) (*plan.Node, error) {
	node := &plan.Node{Resource: res}
	if strings.TrimSpace(s) == "" {
		node.Star = true
		return node, nil
	}
	items, ok := splitTop(s)
	if !ok {
		return nil, errf(CodeInvalidSelect, "unbalanced parentheses or quotes in select")
	}
	for _, raw := range items {
		it, ok := scanSelectItem(raw)
		if !ok {
			return nil, errf(CodeInvalidSelect, "malformed select item %q", strings.TrimSpace(raw))
		}
		if it.star {
			node.Star = true
			continue
		}
		if err := p.resolveSelectItem(node, it, depth); err != nil {
			return nil, err
		}
	}
	return node, nil
}

func (p *parser) resolveSelectItem(node *plan.Node, it selectItem, depth int) error {
	res := node.Resource
	if fn, col, ok := aggregateItem(it, res); ok {
		return p.resolveAggregate(node, it, fn, col)
	}
	if takesArgs(it) {
		return errf(CodeInvalidSelect, "aggregate functions take no arguments: %s()", it.name)
	}
	if it.outCast == "" && !it.hasInner && len(it.hints) == 0 && (hasJSONPath(it.name) || strings.Contains(it.name, "::")) {
		return p.resolveValueItem(node, it)
	}
	if it.outCast != "" || strings.Contains(it.name, "::") || hasJSONPath(it.name) {
		return errf(CodeInvalidSelect, "casts and JSON paths apply to columns (col::type, col->key, col->>key::type) and aggregate functions, not to %q", strings.TrimSpace(it.name))
	}
	// Embed: a relation key followed by (...), or carrying hints.
	if cands := res.Relations[it.name]; len(cands) > 0 && (it.hasInner || len(it.hints) > 0) {
		return p.resolveEmbed(node, it, depth)
	}
	if len(it.hints) > 0 && (res.Field(it.name) != nil || res.Computed[it.name] != nil || res.Pseudo[it.name] != nil) {
		return errf(CodeInvalidSelect, "hints (!%s) are only valid on embeds", strings.Join(it.hints, "!"))
	}
	fs := plan.FieldSel{Alias: it.alias}
	switch {
	case res.Field(it.name) != nil:
		fs.Column = res.Field(it.name).Column
	case res.Computed[it.name] != nil:
		fs.Computed = res.Computed[it.name]
	case res.Pseudo[it.name] != nil:
		fs.Pseudo = res.Pseudo[it.name]
	default:
		if len(res.Relations[it.name]) > 0 {
			return errf(CodeInvalidSelect, "embed %q needs a selection: %s(...)", it.name, it.name)
		}
		if len(it.hints) > 0 || (it.hasInner && !strings.Contains(it.inner, "=")) {
			return errf(CodeUnknownRelation, "unknown relation %q on %q", it.name, res.Path)
		}
		return errf(CodeUnknownColumn, "unknown column %q on %q", it.name, res.Path)
	}
	inner := it.inner
	if fs.Computed != nil && it.hasInner {
		args, rest, err := p.computedArgs(fs.Computed, it.inner)
		if err != nil {
			return err
		}
		fs.CallArgs = args
		inner = rest
		if fs.Computed.SetOf == nil && rest != "" {
			return errf(CodeInvalidSelect, "computed column %q takes only arguments (k=v), got %q", it.name, strings.TrimSpace(rest))
		}
	}
	if it.hasInner && (fs.Computed == nil || fs.Computed.SetOf != nil && inner != "") {
		switch {
		case fs.Computed != nil && fs.Computed.SetOf != nil:
			if depth+1 > p.opts.maxEmbedDepth() {
				return errf(CodeEmbedDepthExceeded, "select nesting exceeds max_embed_depth %d", p.opts.maxEmbedDepth())
			}
			sub, err := p.parseSelect(inner, fs.Computed.SetOf, depth+1)
			if err != nil {
				return err
			}
			sub.Alias = it.name
			if it.alias != "" {
				sub.Alias = it.alias
			}
			fs.Node = sub
		case (fs.Pseudo != nil || fs.Column != nil && postgis.IsSpatial(fs.Column.PGType)) && p.opts.spatialProjection():
			args, err := parseSpatialArgs(it.inner)
			if err != nil {
				return err
			}
			fs.Args = args
		default:
			return errf(CodeInvalidSelect, "arguments are not allowed on %q (only spatial columns and their pseudo-columns take (transform=,simplify=))", it.name)
		}
	}
	node.Fields = append(node.Fields, fs)
	return nil
}

// computedArgs splits the parenthesised part of a computed-column item into
// its k=v arguments (validated and coerced against the function's extra
// arguments) and the remaining items, the nested select of a SETOF <table>
// column. A value is bare text or a double-quoted string.
func (p *parser) computedArgs(c *api.Computed, inner string) (map[string]any, string, error) {
	items, ok := splitTop(inner)
	if !ok {
		return nil, "", errf(CodeInvalidSelect, "unbalanced parentheses or quotes in select")
	}
	byName := make(map[string]introspect.FuncArg, len(c.Args))
	for _, a := range c.Args {
		byName[a.Name] = a
	}
	var args map[string]any
	var rest []string
	for _, raw := range items {
		name, val, isArg := cutArg(raw)
		if !isArg {
			if strings.TrimSpace(raw) != "" {
				rest = append(rest, raw)
			}
			continue
		}
		a, ok := byName[name]
		if !ok {
			return nil, "", errf(CodeUnknownArgument, "computed column %q has no argument %q", c.Key, name)
		}
		if _, dup := args[name]; dup {
			return nil, "", errf(CodeInvalidSelect, "duplicate argument %q", name)
		}
		v, uerr := p.coerceText(p.typeOf(api.ArgColumn(p.built.Catalog, a)), val)
		if uerr != nil {
			return nil, "", errf(CodeInvalidFilter, "argument %q: %s", name, uerr.Detail)
		}
		if args == nil {
			args = map[string]any{}
		}
		args[name] = v
	}
	return args, strings.Join(rest, ","), nil
}

// cutArg splits "name=value" with an identifier name; a double-quoted value
// is unquoted (\" and \\ escape). Anything else is not an argument.
func cutArg(raw string) (name, val string, ok bool) {
	k, v, found := strings.Cut(strings.TrimSpace(raw), "=")
	if !found || k == "" || strings.ContainsAny(k, "():\"!. ") {
		return "", "", false
	}
	if len(v) >= 2 && v[0] == '"' && v[len(v)-1] == '"' {
		var b strings.Builder
		q := v[1 : len(v)-1]
		for i := 0; i < len(q); i++ {
			if q[i] == '\\' && i+1 < len(q) {
				i++
			}
			b.WriteByte(q[i])
		}
		v = b.String()
	}
	return k, v, true
}

// resolveValueItem resolves "column[->path][::type]": a column, optionally
// followed by a JSON path, optionally cast to an allowlisted type.
func (p *parser) resolveValueItem(node *plan.Node, it selectItem) error {
	res := node.Resource
	name, cast := it.name, ""
	if i := lastIndexOutsideQuotes(name, "::"); i >= 0 {
		name, cast = name[:i], name[i+2:]
		if cast == "" {
			return errf(CodeInvalidSelect, "missing cast type in %q", it.name)
		}
	}
	col, path, has, uerr := splitJSONPath(name)
	if uerr != nil {
		return withCode(uerr, CodeInvalidSelect)
	}
	fs := plan.FieldSel{Alias: it.alias}
	if has {
		f, err := p.resolvePathField(res, col, path, CodeInvalidSelect)
		if err != nil {
			return err
		}
		fs.Column, fs.Path = f.Column, path
	} else {
		f := res.Field(col)
		if f == nil {
			if res.Computed[col] != nil || res.Pseudo[col] != nil || len(res.Relations[col]) > 0 {
				return errf(CodeInvalidSelect, "casts apply to columns and JSON paths; %q is not a column", col)
			}
			return errf(CodeUnknownColumn, "unknown column %q on %q", col, res.Path)
		}
		fs.Column = f.Column
	}
	if cast != "" {
		t, err := p.castType(cast)
		if err != nil {
			return err
		}
		fs.Cast = t
	}
	node.Fields = append(node.Fields, fs)
	return nil
}

func (p *parser) resolveEmbed(node *plan.Node, it selectItem, depth int) error {
	res := node.Resource
	inner := false
	hint := ""
	for _, h := range it.hints {
		if h == "inner" {
			inner = true
			continue
		}
		if hint != "" {
			return errf(CodeInvalidSelect, "embed %q has more than one relation hint", it.name)
		}
		hint = h
	}
	rel, err := p.resolveRelation(res, it.name, hint)
	if err != nil {
		return err
	}
	if !rel.Embeddable {
		return errf(CodeUnknownRelation, "relation %q of %q cannot be embedded", it.name, res.Path)
	}
	if depth+1 > p.opts.maxEmbedDepth() {
		return errf(CodeEmbedDepthExceeded, "select nesting exceeds max_embed_depth %d", p.opts.maxEmbedDepth())
	}
	if !it.hasInner {
		return errf(CodeInvalidSelect, "embed %q needs a selection: %s(...)", it.name, it.name)
	}
	sub, err := p.parseSelect(it.inner, rel.To, depth+1)
	if err != nil {
		return err
	}
	sub.Alias = it.name
	if it.alias != "" {
		sub.Alias = it.alias
	}
	node.Embeds = append(node.Embeds, plan.Embed{
		Key:      it.name,
		Alias:    it.alias,
		Relation: rel,
		Inner:    inner,
		Node:     sub,
		Page:     plan.Page{Limit: p.opts.maxPageSize()},
	})
	return nil
}

// resolveRelation applies Resource.Resolve with the documented errors.
func (p *parser) resolveRelation(res *api.Resource, key, hint string) (*api.Relation, error) {
	rel, cands := res.Resolve(key, hint)
	if rel != nil {
		return rel, nil
	}
	if len(cands) == 0 {
		return nil, errf(CodeUnknownRelation, "unknown relation %q on %q", key, res.Path)
	}
	var hs []string
	for _, c := range cands {
		for _, h := range c.Hints {
			hs = append(hs, key+"!"+h)
		}
	}
	if hint != "" {
		return nil, errf(CodeAmbiguousRelation, "hint %q selects no relation %q on %q; candidates: %s", hint, key, res.Path, strings.Join(hs, ", "))
	}
	return nil, errf(CodeAmbiguousRelation, "relation %q on %q is ambiguous; use a hint: %s", key, res.Path, strings.Join(hs, ", "))
}

// parseSpatialArgs parses "transform=<srid>,simplify=<tolerance>".
func parseSpatialArgs(s string) (map[string]any, error) {
	out := map[string]any{}
	if strings.TrimSpace(s) == "" {
		return nil, errf(CodeInvalidSelect, "empty argument list")
	}
	for _, kv := range strings.Split(s, ",") {
		k, v, ok := strings.Cut(kv, "=")
		k = strings.TrimSpace(k)
		v = strings.TrimSpace(v)
		if !ok || v == "" {
			return nil, errf(CodeInvalidSelect, "malformed argument %q (want k=v)", kv)
		}
		if _, dup := out[k]; dup {
			return nil, errf(CodeInvalidSelect, "duplicate argument %q", k)
		}
		switch k {
		case "transform":
			n, err := strconv.Atoi(v)
			if err != nil || n <= 0 {
				return nil, errf(CodeInvalidSelect, "transform must be a positive SRID, got %q", v)
			}
			out[k] = n
		case "simplify":
			f, err := strconv.ParseFloat(v, 64)
			if err != nil || f < 0 {
				return nil, errf(CodeInvalidSelect, "simplify must be a non-negative tolerance, got %q", v)
			}
			out[k] = f
		default:
			return nil, errf(CodeInvalidSelect, "unknown argument %q (want transform= or simplify=)", k)
		}
	}
	return out, nil
}

// hasEmbeds reports whether the select tree embeds any relation (CSV
// negotiation).
func hasEmbeds(n *plan.Node) bool {
	if n == nil {
		return false
	}
	if len(n.Embeds) > 0 {
		return true
	}
	for _, f := range n.Fields {
		if f.Node != nil {
			return true
		}
	}
	return false
}
