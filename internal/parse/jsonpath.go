package parse

import (
	"strconv"
	"strings"

	"github.com/suprbdev/pdbcore/contrib"
	"github.com/suprbdev/pdbcore/introspect"
	"github.com/suprbdev/pdbcore/postgis"

	"github.com/suprbdev/pdbr/internal/api"
	"github.com/suprbdev/pdbr/internal/compile"
	"github.com/suprbdev/pdbr/internal/plan"
)

// maxJSONPathSteps bounds the arrows of one JSON path.
const maxJSONPathSteps = 32

// indexOutsideQuotes returns the index of the first sub in s outside double
// quotes, or -1.
func indexOutsideQuotes(s, sub string) int {
	inQuote := false
	for i := 0; i < len(s); i++ {
		switch {
		case s[i] == '"':
			inQuote = !inQuote
		case !inQuote && strings.HasPrefix(s[i:], sub):
			return i
		}
	}
	return -1
}

// lastIndexOutsideQuotes returns the index of the last sub in s outside
// double quotes, or -1.
func lastIndexOutsideQuotes(s, sub string) int {
	last := -1
	inQuote := false
	for i := 0; i < len(s); i++ {
		switch {
		case s[i] == '"':
			inQuote = !inQuote
		case !inQuote && strings.HasPrefix(s[i:], sub):
			last = i
		}
	}
	return last
}

// cutOutsideQuotes is strings.Cut on the first sep outside double quotes.
func cutOutsideQuotes(s, sep string) (before, after string, found bool) {
	if i := indexOutsideQuotes(s, sep); i >= 0 {
		return s[:i], s[i+len(sep):], true
	}
	return s, "", false
}

// hasJSONPath reports whether s names a JSON path (col->key …).
func hasJSONPath(s string) bool { return indexOutsideQuotes(s, "->") > 0 }

// splitJSONPath splits "col->a->0->>b" into the column part and its path
// (PostgREST grammar): each step is -> or ->> followed by an array index
// (an optionally negative integer), a double-quoted key (any characters but
// a double quote) or a bare key (no . , : ( ) ! " or whitespace); only the
// last step may be ->>. has is false when s has no arrow.
func splitJSONPath(s string) (col string, path plan.JSONPath, has bool, uerr *compile.UserError) {
	i := indexOutsideQuotes(s, "->")
	if i < 0 {
		return s, nil, false, nil
	}
	col, rest := s[:i], s[i:]
	if col == "" {
		return "", nil, true, errf(CodeInvalidFilter, "JSON path %q needs a column before the first arrow", s)
	}
	for rest != "" {
		if len(path) == maxJSONPathSteps {
			return "", nil, true, errf(CodeInvalidFilter, "JSON path %q has more than %d steps", s, maxJSONPathSteps)
		}
		if len(path) > 0 && path[len(path)-1].Text {
			return "", nil, true, errf(CodeInvalidFilter, "JSON path %q: ->> must be the last step (it yields text)", s)
		}
		var st plan.JSONStep
		switch {
		case strings.HasPrefix(rest, "->>"):
			st.Text, rest = true, rest[3:]
		case strings.HasPrefix(rest, "->"):
			rest = rest[2:]
		default:
			return "", nil, true, errf(CodeInvalidFilter, "malformed JSON path %q", s)
		}
		var tok string
		if strings.HasPrefix(rest, `"`) {
			end := strings.IndexByte(rest[1:], '"')
			if end < 0 {
				return "", nil, true, errf(CodeInvalidFilter, "unterminated quoted key in JSON path %q", s)
			}
			st.Key, rest = rest[1:end+1], rest[end+2:]
			if rest != "" && !strings.HasPrefix(rest, "->") {
				return "", nil, true, errf(CodeInvalidFilter, "malformed JSON path %q", s)
			}
			path = append(path, st)
			continue
		}
		if j := strings.Index(rest, "->"); j >= 0 {
			tok, rest = rest[:j], rest[j:]
		} else {
			tok, rest = rest, ""
		}
		if tok == "" {
			return "", nil, true, errf(CodeInvalidFilter, "empty key in JSON path %q (quote it: ->\"\")", s)
		}
		if isIndex(tok) {
			n, err := strconv.ParseInt(tok, 10, 32)
			if err != nil {
				return "", nil, true, errf(CodeInvalidFilter, "array index %s in JSON path %q is out of range", tok, s)
			}
			st.Array, st.Index = true, int(n)
		} else {
			if strings.ContainsAny(tok, ".,:()!\" \t") {
				return "", nil, true, errf(CodeInvalidFilter, "key %q in JSON path %q must be double-quoted", tok, s)
			}
			st.Key = tok
		}
		path = append(path, st)
	}
	return col, path, true, nil
}

func isDigit(c byte) bool { return c >= '0' && c <= '9' }

// isIndex reports an optionally negative decimal integer.
func isIndex(s string) bool {
	s = strings.TrimPrefix(s, "-")
	if s == "" {
		return false
	}
	for i := 0; i < len(s); i++ {
		if !isDigit(s[i]) {
			return false
		}
	}
	return true
}

// jsonPathTarget reports whether a JSON path can be applied to c: json and
// jsonb columns directly, arrays and composites through to_jsonb (as
// PostgREST does), hstore columns as a key lookup; never spatial columns.
func (p *parser) jsonPathTarget(c *introspect.Column) bool {
	t := p.typeOf(c)
	switch {
	case t.isArray:
		return !postgis.IsSpatial(c.PGType)
	case t.kind == kindJSON, t.kind == kindComposite, t.owner == contrib.VocabHstore:
		return true
	}
	return false
}

// resolvePathField resolves the column a JSON path starts from; code is the
// error code of the calling context (select, filter or order).
func (p *parser) resolvePathField(res *api.Resource, name string, path plan.JSONPath, code string) (*api.Field, error) {
	f := res.Field(name)
	if f == nil {
		if res.Computed[name] != nil || res.Pseudo[name] != nil {
			return nil, errf(code, "JSON paths apply to columns; %q is a computed or derived field", name)
		}
		return nil, errf(CodeUnknownColumn, "unknown column %q on %q", name, res.Path)
	}
	if !p.jsonPathTarget(f.Column) {
		return nil, errf(code, "JSON path %s%s needs a json, jsonb, array, composite or hstore column; %q is %s", name, path.String(), name, p.typeOf(f.Column).name())
	}
	if p.typeOf(f.Column).owner == contrib.VocabHstore && (len(path) != 1 || path[0].Array) {
		return nil, errf(code, "hstore column %q takes the value of one key: %s->key or %s->>key, not %s%s", name, name, name, name, path.String())
	}
	return f, nil
}

// pathType is the parser's view of a JSON path's result on col: text for
// ->> and for an hstore key lookup, jsonb for -> (json columns are
// compared as jsonb).
func (p *parser) pathType(col *introspect.Column, path plan.JSONPath) colType {
	if path.Text() || p.typeOf(col).owner == contrib.VocabHstore {
		return colType{base: "text", kind: kindText}
	}
	return colType{base: "jsonb", kind: kindJSON}
}

// withCode re-labels a UserError raised by the shared path grammar.
func withCode(err *compile.UserError, code string) error {
	if err == nil {
		return nil
	}
	return errf(code, "%s", err.Detail)
}
