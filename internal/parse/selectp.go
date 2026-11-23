package parse

import (
	"math"
	"strconv"
	"strings"

	"github.com/suprbdev/pdbcore/contrib"
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
	spread   bool   // ...rel(...)
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
	if rest, ok := strings.CutPrefix(s, "..."); ok {
		it.spread, s = true, rest
	}
	if j := lastIndexOutsideQuotes(s, ")"); j >= 0 && strings.HasPrefix(s[j+1:], "::") {
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
	if it.spread {
		switch {
		case it.alias != "":
			return errf(CodeInvalidSelect, "a spread embed takes no alias (%s:...%s); alias its items instead", it.alias, it.name)
		case it.outCast != "":
			return errf(CodeInvalidSelect, "a spread embed cannot be cast")
		case len(res.Relations[it.name]) == 0:
			return errf(CodeUnknownRelation, "unknown relation %q on %q (spread ... applies to embeds)", it.name, res.Path)
		}
		return p.resolveEmbed(node, it, depth)
	}
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
	reserved := api.IsReserved(it.name)
	if reserved && res.Pseudo[it.name] == nil {
		return errf(CodeUnknownColumn, "unknown column %q on %q", it.name, res.Path)
	}
	// Embed: a relation key followed by (...), or carrying hints.
	if cands := res.Relations[it.name]; len(cands) > 0 && !reserved && (it.hasInner || len(it.hints) > 0) {
		return p.resolveEmbed(node, it, depth)
	}
	if len(it.hints) > 0 && (res.Field(it.name) != nil || res.Computed[it.name] != nil || res.Pseudo[it.name] != nil) {
		return errf(CodeInvalidSelect, "hints (!%s) are only valid on embeds", strings.Join(it.hints, "!"))
	}
	fs := plan.FieldSel{Alias: it.alias}
	switch {
	case reserved:
		fs.Pseudo = res.Pseudo[it.name]
	case res.Field(it.name) != nil:
		fs.Column = res.Field(it.name).Column
	case res.Computed[it.name] != nil:
		fs.Computed = res.Computed[it.name]
	case res.Raster(it.name) != nil && res.Raster(it.name).Column.IsArray:
		return errf(CodeInvalidSelect, "raster array column %q of %q is not readable; expose it through a computed column", it.name, res.Path)
	case res.Raster(it.name) != nil:
		return errf(CodeInvalidSelect, "raster column %q of %q is not readable: select its derived columns (~%s_width, ~%s_num_bands, ~%s_envelope, ~%s_value(band=<n>) with ~near=, ~%s_summary_stats(band=<n>), ...) or expose the bytes through a computed column", it.name, res.Path, it.name, it.name, it.name, it.name, it.name)
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
	if fs.Pseudo != nil && fs.Pseudo.Score != nil {
		p.scores = append(p.scores, fs.Pseudo)
	}
	if fs.Pseudo != nil && fs.Pseudo.Headline != nil {
		h, err := parseHeadlineArgs(fs.Pseudo, it.inner, it.hasInner)
		if err != nil {
			return err
		}
		h.MaxDocument = p.opts.MaxHeadlineDocument
		fs.Headline = h
		p.headlines = append(p.headlines, headlineUse{node: node, pc: fs.Pseudo, h: h})
	}
	if it.hasInner && (fs.Computed == nil || fs.Computed.SetOf != nil && inner != "") {
		switch {
		case fs.Pseudo != nil && fs.Pseudo.Raster != nil:
			args, err := parseRasterArgs(fs.Pseudo, it.inner)
			if err != nil {
				return err
			}
			fs.Args = args
		case fs.Pseudo != nil && fs.Pseudo.Headline != nil:
		case fs.Pseudo != nil && fs.Pseudo.Score != nil:
			args, err := parseScoreArgs(fs.Pseudo, it.inner)
			if err != nil {
				return err
			}
			fs.Args = args
		case fs.Pseudo != nil && fs.Pseudo.Contrib != nil:
			args, err := parseContribArgs(fs.Pseudo, it.inner)
			if err != nil {
				return err
			}
			fs.Args = args
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
			return errf(CodeInvalidSelect, "arguments are not allowed on %q (only spatial columns and their pseudo-columns take (transform=,simplify=,buffer=,precision=))", it.name)
		}
	}
	if fs.Pseudo != nil && fs.Pseudo.Contrib != nil && fs.Pseudo.Contrib.Expr != nil && !it.hasInner {
		return errf(CodeInvalidSelect, "%q needs arguments: %s%s", it.name, it.name, fs.Pseudo.Contrib.ContribArgSyntax())
	}
	node.Fields = append(node.Fields, fs)
	return nil
}

// parseContribArgs parses the k=v arguments of a derived contrib
// pseudo-column (path_lca(path=a.b), path_subpath(offset=1,length=2)) and
// checks them with the expression's own operand rules; an accessor takes
// none.
func parseContribArgs(pc *api.PseudoColumn, inner string) (map[string]any, error) {
	cv := pc.Contrib
	if cv.Expr == nil {
		return nil, errf(CodeInvalidSelect, "%q takes no arguments", pc.Key)
	}
	items, ok := splitTop(inner)
	if !ok {
		return nil, errf(CodeInvalidSelect, "unbalanced parentheses or quotes in select")
	}
	args := map[string]any{}
	for _, raw := range items {
		name, val, isArg := cutArg(raw)
		if !isArg {
			return nil, errf(CodeInvalidSelect, "%q takes arguments %s, got %q", pc.Key, cv.ContribArgSyntax(), strings.TrimSpace(raw))
		}
		var spec *api.ContribArg
		for i := range cv.Args {
			if cv.Args[i].Name == name {
				spec = &cv.Args[i]
			}
		}
		if spec == nil {
			return nil, errf(CodeUnknownArgument, "%q has no argument %q (takes %s)", pc.Key, name, cv.ContribArgSyntax())
		}
		if _, dup := args[name]; dup {
			return nil, errf(CodeInvalidSelect, "duplicate argument %q", name)
		}
		if spec.Type == "integer" {
			n, err := strconv.ParseInt(strings.TrimSpace(val), 10, 32)
			if err != nil {
				return nil, errf(CodeInvalidSelect, "argument %q of %q must be an integer, got %q", name, pc.Key, val)
			}
			args[name] = n
			continue
		}
		args[name] = val
	}
	for _, a := range cv.Args {
		if _, ok := args[a.Name]; !ok && !a.Optional {
			return nil, errf(CodeInvalidSelect, "%q needs argument %q: %s%s", pc.Key, a.Name, pc.Key, cv.ContribArgSyntax())
		}
	}
	if _, err := cv.Expr.Params(cv.Operand(args)); err != nil {
		return nil, errf(CodeInvalidSelect, "%s: %s", pc.Key, err.Error())
	}
	return args, nil
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
			return nil, "", errf(CodeInvalidArgument, "argument %q: %s", name, uerr.Detail)
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
		Spread:   it.spread,
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
		return nil, errf(CodeUnknownRelation, "hint %q selects no relation %q on %q; candidates: %s", hint, key, res.Path, strings.Join(hs, ", "))
	}
	return nil, errf(CodeAmbiguousRelation, "relation %q on %q is ambiguous; use a hint: %s", key, res.Path, strings.Join(hs, ", "))
}

// parseHeadlineArgs parses the arguments of a headline pseudo-column:
// document=<col> (required on a tsvector column, refused on a document
// column, which highlights itself) and the ts_headline options, validated by
// contrib.ParseHeadlineOptions.
func parseHeadlineArgs(pc *api.PseudoColumn, inner string, hasInner bool) (*plan.Headline, error) {
	h := &plan.Headline{Document: pc.Column}
	opts := map[string]any{}
	doc, hasDoc := "", false
	if hasInner {
		items, ok := splitTop(inner)
		if !ok {
			return nil, errf(CodeInvalidSelect, "unbalanced parentheses or quotes in select")
		}
		if strings.TrimSpace(inner) == "" {
			return nil, errf(CodeInvalidSelect, "empty argument list")
		}
		seen := map[string]bool{}
		for _, raw := range items {
			name, val, isArg := cutArg(raw)
			if !isArg {
				return nil, errf(CodeInvalidSelect, "%q takes arguments %s%s, got %q", pc.Key, pc.Key, pc.SelectArgSyntax(), strings.TrimSpace(raw))
			}
			if seen[name] {
				return nil, errf(CodeInvalidSelect, "duplicate argument %q", name)
			}
			seen[name] = true
			if name == "document" {
				if pc.Headline.Documents == nil {
					return nil, errf(CodeInvalidSelect, "%q highlights %s itself and takes no document=", pc.Key, pc.Column.Name)
				}
				doc, hasDoc = val, true
				continue
			}
			key := api.HeadlineOption(name)
			if key == "" {
				return nil, errf(CodeUnknownArgument, "%q has no argument %q (takes %s)", pc.Key, name, pc.SelectArgSyntax())
			}
			opts[key] = val
		}
	}
	if pc.Headline.Documents != nil {
		if !hasDoc {
			return nil, errf(CodeInvalidSelect, "%q needs a document to highlight: %s%s", pc.Key, pc.Key, pc.SelectArgSyntax())
		}
		col := pc.Headline.Documents[doc]
		if col == nil {
			return nil, errf(CodeInvalidSelect, "%q: document %q is not a readable text, json or jsonb column of this resource", pc.Key, doc)
		}
		h.Document = col
	}
	o, err := contrib.ParseHeadlineOptions(opts)
	if err != nil {
		msg := err.Error()
		for _, k := range contrib.HeadlineOptionKeys() {
			msg = strings.ReplaceAll(msg, k, api.HeadlineArg(k))
		}
		return nil, errf(CodeInvalidSelect, "%s: %s", pc.Key, msg)
	}
	h.Options = o.String()
	return h, nil
}

// parseSpatialArgs parses "transform=<srid>,simplify=<tolerance>,
// buffer=<distance>,precision=<digits>".
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
		case "buffer":
			f, err := strconv.ParseFloat(v, 64)
			if err == nil {
				err = postgis.CheckBuffer(f)
			}
			if err != nil {
				return nil, errf(CodeInvalidSelect, "buffer must be a finite distance, got %q", v)
			}
			out[k] = f
		case "precision":
			n, err := strconv.ParseInt(v, 10, 64)
			if err == nil {
				err = postgis.CheckPrecision(n)
			}
			if err != nil {
				return nil, errf(CodeInvalidSelect, "precision must be an integer between 0 and %d, got %q", postgis.MaxPrecision, v)
			}
			out[k] = n
		default:
			return nil, errf(CodeInvalidSelect, "unknown argument %q (want transform=, simplify=, buffer= or precision=)", k)
		}
	}
	return out, nil
}

// parseRasterArgs parses the arguments of a raster pseudo-column: the
// pixel value and the summary statistics take band=<n> (a 1-based band
// number), the envelope precision=<digits> (GeoJSON decimal digits), the
// other metadata accessors none.
func parseRasterArgs(pc *api.PseudoColumn, s string) (map[string]any, error) {
	rv := pc.Raster
	name, limit := "band", int64(math.MaxInt32)
	switch {
	case rv.Accessor != nil && rv.Accessor.GeoJSON:
		name, limit = "precision", postgis.MaxPrecision
	case !rv.TakesBand():
		return nil, errf(CodeInvalidSelect, "%q takes no arguments", pc.Key)
	}
	k, v, ok := strings.Cut(s, "=")
	k, v = strings.TrimSpace(k), strings.TrimSpace(v)
	if !ok || k != name || strings.Contains(v, ",") {
		return nil, errf(CodeInvalidSelect, "%q takes one argument, %s=<n>", pc.Key, name)
	}
	n, err := strconv.ParseInt(v, 10, 64)
	switch {
	case name == "band" && (err != nil || n < 1 || n > limit):
		return nil, errf(CodeInvalidSelect, "band must be a positive integer (1-based), got %q", v)
	case name == "precision" && (err != nil || n < 0 || n > limit):
		return nil, errf(CodeInvalidSelect, "precision must be an integer between 0 and %d, got %q", postgis.MaxPrecision, v)
	}
	return map[string]any{name: n}, nil
}

// parseScoreArgs parses the arguments of a score pseudo-column: only the
// spatial distance of a geometry column takes one, spheroid=true|false.
func parseScoreArgs(pc *api.PseudoColumn, s string) (map[string]any, error) {
	if pc.Score.Param != api.ParamNear || postgis.FlavourOf(pc.Column.PGType) != postgis.Geometry {
		return nil, errf(CodeInvalidSelect, "%q takes no arguments", pc.Key)
	}
	k, v, ok := strings.Cut(s, "=")
	k, v = strings.TrimSpace(k), strings.TrimSpace(v)
	if !ok || k != "spheroid" || strings.Contains(v, ",") {
		return nil, errf(CodeInvalidSelect, "%q takes one argument, spheroid=true|false", pc.Key)
	}
	b, err := strconv.ParseBool(v)
	if err != nil {
		return nil, errf(CodeInvalidSelect, "spheroid must be true or false, got %q", v)
	}
	return map[string]any{"spheroid": b}, nil
}

// hasEmbeds reports whether the select tree nests any object (CSV
// negotiation): an embed or a set-returning computed selection. Spread
// embeds hoist their items into the row, so only what they nest counts.
func hasEmbeds(n *plan.Node) bool {
	if n == nil {
		return false
	}
	for i := range n.Embeds {
		if !n.Embeds[i].Spread || hasEmbeds(n.Embeds[i].Node) {
			return true
		}
	}
	for _, f := range n.Fields {
		if f.Node != nil {
			return true
		}
	}
	return false
}
