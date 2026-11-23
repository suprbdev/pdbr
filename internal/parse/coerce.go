package parse

import (
	"encoding/json"
	"fmt"
	"regexp"
	"strconv"
	"strings"

	"github.com/suprbdev/pdbcore/contrib"
	"github.com/suprbdev/pdbcore/introspect"
	"github.com/suprbdev/pdbcore/postgis"

	"github.com/suprbdev/pdbr/internal/api"
	"github.com/suprbdev/pdbr/internal/compile"
)

type typeKind int

const (
	kindOther typeKind = iota // passed through as text; PG casts
	kindText
	kindInt
	kindFloat
	kindNumeric
	kindBool
	kindUUID
	kindJSON
	kindEnum
	kindSpatial
	kindComposite
)

// colType is the parser's view of a column (or function argument) type.
type colType struct {
	base    string // pg type name without the array prefix
	isArray bool
	kind    typeKind
	enum    *api.Enum
	flavour postgis.Flavour
	srid    int
	// owner names the extension vocabulary owning a scalar type (citext,
	// hstore, ltree); "" otherwise.
	owner string
	// codec converts the JSON object / list form of an hstore, range or
	// multirange (element) type; nil otherwise.
	codec *contrib.Codec
}

func (t colType) elem() colType {
	e := t
	e.isArray = false
	return e
}

func (t colType) name() string {
	if t.isArray {
		return t.base + "[]"
	}
	return t.base
}

func (p *parser) typeOf(col *introspect.Column) colType {
	t := p.typeFor(col.PGType, col.TypeSchema, col.IsArray, col.EnumRef)
	if t.kind == kindSpatial && col.Spatial != nil {
		t.srid = col.Spatial.SRID
	}
	return t
}

func (p *parser) typeFor(pgType, typeSchema string, isArray bool, enumRef string) colType {
	base := strings.TrimPrefix(pgType, "_")
	t := colType{base: base, isArray: isArray || strings.HasPrefix(pgType, "_")}
	if enumRef != "" {
		if e := p.built.Enums[enumRef]; e != nil {
			t.kind, t.enum = kindEnum, e
			return t
		}
	}
	if e := p.built.Enums[typeSchema+"."+base]; e != nil && !e.Enum.TableBacked {
		t.kind, t.enum = kindEnum, e
		return t
	}
	if p.built.Composites[typeSchema+"."+base] != nil {
		t.kind = kindComposite
		return t
	}
	if f := postgis.FlavourOf(base); f.Spatial() {
		t.kind, t.flavour = kindSpatial, f
		return t
	}
	t.kind = scalarKind(base)
	if v, ok := api.OwnerVocabulary(p.built.Catalog, &introspect.Column{PGType: pgType, TypeSchema: typeSchema, IsArray: t.isArray}); ok {
		t.owner = v.Name
	}
	if c, ok := api.CodecFor(p.built.Catalog, &introspect.Column{PGType: base, TypeSchema: typeSchema}); ok {
		t.codec = c
	}
	return t
}

func scalarKind(base string) typeKind {
	switch base {
	case "text", "varchar", "bpchar", "char", "name", "citext":
		return kindText
	case "int2", "int4", "int8", "oid":
		return kindInt
	case "float4", "float8":
		return kindFloat
	case "numeric":
		return kindNumeric
	case "bool":
		return kindBool
	case "uuid":
		return kindUUID
	case "json", "jsonb":
		return kindJSON
	}
	return kindOther
}

var (
	numericRe = regexp.MustCompile(`^[+-]?(\d+\.?\d*|\.\d+)([eE][+-]?\d+)?$`)
	uuidRe    = regexp.MustCompile(`^(\{)?[0-9a-fA-F]{8}-?[0-9a-fA-F]{4}-?[0-9a-fA-F]{4}-?[0-9a-fA-F]{4}-?[0-9a-fA-F]{12}(\})?$`)
)

// coerceScalar coerces a text value to col's type. Array columns take a
// PostgreSQL array literal ({a,b}) and yield []any.
func (p *parser) coerceScalar(col *introspect.Column, text string) (any, *compile.UserError) {
	return p.coerceText(p.typeOf(col), text)
}

func (p *parser) coerceText(t colType, text string) (any, *compile.UserError) {
	if t.isArray {
		items, err := parseArrayLiteral(text)
		if err != nil {
			return nil, err
		}
		out := make([]any, len(items))
		for i, it := range items {
			if it == nil {
				continue
			}
			v, err := p.coerceElem(t.elem(), *it)
			if err != nil {
				return nil, err
			}
			out[i] = v
		}
		return out, nil
	}
	return p.coerceElem(t, text)
}

func (p *parser) coerceElem(t colType, text string) (any, *compile.UserError) {
	switch t.kind {
	case kindInt:
		n, err := strconv.ParseInt(strings.TrimSpace(text), 10, 64)
		if err != nil {
			return nil, errf(CodeInvalidFilter, "%q is not a valid %s", text, t.base)
		}
		return n, nil
	case kindFloat:
		f, err := strconv.ParseFloat(strings.TrimSpace(text), 64)
		if err != nil {
			return nil, errf(CodeInvalidFilter, "%q is not a valid %s", text, t.base)
		}
		return f, nil
	case kindNumeric:
		s := strings.TrimSpace(text)
		if !numericRe.MatchString(s) && !strings.EqualFold(s, "nan") && !strings.EqualFold(s, "infinity") && !strings.EqualFold(s, "-infinity") {
			return nil, errf(CodeInvalidFilter, "%q is not a valid numeric", text)
		}
		return s, nil
	case kindBool:
		b, ok := parseBool(text)
		if !ok {
			return nil, errf(CodeInvalidFilter, "%q is not a valid boolean", text)
		}
		return b, nil
	case kindUUID:
		s := strings.TrimSpace(text)
		if !uuidRe.MatchString(s) {
			return nil, errf(CodeInvalidFilter, "%q is not a valid uuid", text)
		}
		return s, nil
	case kindJSON, kindComposite:
		// Composite values travel as JSON text (RPC GET arguments); filters
		// never reach here because composite columns are not filterable.
		if !json.Valid([]byte(text)) {
			return nil, errf(CodeInvalidFilter, "%q is not valid JSON", text)
		}
		return text, nil
	case kindEnum:
		return p.enumLabel(t.enum, text)
	case kindSpatial:
		return p.coerceSpatial(t, text)
	}
	return text, nil
}

func parseBool(text string) (bool, bool) {
	switch strings.ToLower(strings.TrimSpace(text)) {
	case "t", "true", "y", "yes", "on", "1":
		return true, true
	case "f", "false", "n", "no", "off", "0":
		return false, true
	}
	return false, false
}

// enumLabel maps a client-facing enum value to its pg label.
func (p *parser) enumLabel(e *api.Enum, text string) (any, *compile.UserError) {
	for i, v := range e.Values {
		if v == text {
			return e.Enum.Values[i], nil
		}
	}
	return nil, errf(CodeInvalidFilter, "%q is not a value of enum %q", text, e.Name)
}

// spatialInput decodes GeoJSON text into an object and leaves WKT/EWKT as
// the string it is: the two input forms postgis.ParseFor accepts.
func spatialInput(text string) (any, *compile.UserError) {
	if !strings.HasPrefix(strings.TrimSpace(text), "{") {
		return text, nil
	}
	var m map[string]any
	if err := json.Unmarshal([]byte(text), &m); err != nil {
		return nil, errf(CodeInvalidFilter, "spatial value is not valid GeoJSON")
	}
	return m, nil
}

// coerceSpatial parses GeoJSON text or WKT/EWKT into a postgis.Value bound
// for a column of type t (its flavour and typmod SRID).
func (p *parser) coerceSpatial(t colType, text string) (any, *compile.UserError) {
	in, uerr := spatialInput(text)
	if uerr != nil {
		return nil, uerr
	}
	v, err := postgis.ParseFor(in, t.flavour, t.srid)
	if err != nil {
		return nil, errf(CodeInvalidFilter, "%s", err.Error())
	}
	return v, nil
}

// coerceJSON coerces a decoded JSON value (json.Decoder with UseNumber) to
// t: cursor elements and RPC POST arguments.
func (p *parser) coerceJSON(t colType, v any) (any, *compile.UserError) {
	if v == nil {
		return nil, nil
	}
	if t.isArray {
		list, ok := v.([]any)
		if !ok {
			if s, ok := v.(string); ok {
				return p.coerceText(t, s)
			}
			return nil, errf(CodeInvalidFilter, "expected a JSON array for %s", t.name())
		}
		out := make([]any, len(list))
		for i, item := range list {
			x, err := p.coerceJSON(t.elem(), item)
			if err != nil {
				return nil, err
			}
			out[i] = x
		}
		return out, nil
	}
	if t.codec != nil {
		switch v.(type) {
		case map[string]any, []any:
			lit, err := contribLiteral(t.codec, v)
			if err != nil {
				return nil, errf(CodeInvalidFilter, "%s", err.Error())
			}
			return lit, nil
		}
	}
	switch t.kind {
	case kindJSON, kindComposite:
		b, err := json.Marshal(v)
		if err != nil {
			return nil, errf(CodeInvalidFilter, "value is not encodable as JSON")
		}
		return string(b), nil
	case kindSpatial:
		val, err := postgis.ParseFor(v, t.flavour, t.srid)
		if err != nil {
			return nil, errf(CodeInvalidFilter, "%s", err.Error())
		}
		return val, nil
	}
	switch x := v.(type) {
	case json.Number:
		return p.coerceElem(t, x.String())
	case string:
		return p.coerceElem(t, x)
	case bool:
		if t.kind == kindBool {
			return x, nil
		}
		return p.coerceElem(t, strconv.FormatBool(x))
	}
	return nil, errf(CodeInvalidFilter, "expected a JSON scalar for %s", t.name())
}

// contribLiteral renders the JSON form of an hstore, range or multirange
// value (validated by its contrib codec) as the type's text literal, for
// places that bind one text parameter per value: RPC arguments and array
// elements.
func contribLiteral(c *contrib.Codec, v any) (string, error) {
	params, err := c.Params(v)
	if err != nil {
		return "", err
	}
	quote := func(s *string) string {
		if s == nil {
			return ""
		}
		return `"` + strings.ReplaceAll(strings.ReplaceAll(*s, `\`, `\\`), `"`, `\"`) + `"`
	}
	str := func(x any) *string {
		if s, ok := x.(string); ok {
			return &s
		}
		return nil
	}
	rangeLit := func(lo, hi *string, bounds string) string {
		return bounds[:1] + quote(lo) + "," + quote(hi) + bounds[1:]
	}
	switch c.Arg {
	case contrib.ArgHstore:
		kv := params[0].([]*string)
		pairs := make([]string, 0, len(kv)/2)
		for i := 0; i+1 < len(kv); i += 2 {
			val := "NULL"
			if kv[i+1] != nil {
				val = quote(kv[i+1])
			}
			pairs = append(pairs, quote(kv[i])+"=>"+val)
		}
		return strings.Join(pairs, ", "), nil
	case contrib.ArgRange:
		return rangeLit(str(params[0]), str(params[1]), params[2].(string)), nil
	case contrib.ArgMultirange:
		lo, hi, bounds := params[0].([]*string), params[1].([]*string), params[2].([]string)
		parts := make([]string, len(bounds))
		for i := range bounds {
			parts[i] = rangeLit(lo[i], hi[i], bounds[i])
		}
		return "{" + strings.Join(parts, ",") + "}", nil
	}
	return "", fmt.Errorf("no literal form for %s values", c.Arg)
}

// parseArrayLiteral parses a one-dimensional PostgreSQL array literal
// ({a,b,"c,d",NULL}); nil entries are SQL NULLs.
func parseArrayLiteral(s string) ([]*string, *compile.UserError) {
	s = strings.TrimSpace(s)
	if len(s) < 2 || s[0] != '{' || s[len(s)-1] != '}' {
		return nil, errf(CodeInvalidFilter, "%q is not an array literal ({a,b})", s)
	}
	body := s[1 : len(s)-1]
	var out []*string
	if strings.TrimSpace(body) == "" {
		return out, nil
	}
	i := 0
	for {
		for i < len(body) && (body[i] == ' ' || body[i] == '\t') {
			i++
		}
		if i < len(body) && body[i] == '"' {
			var b strings.Builder
			i++
			closed := false
			for i < len(body) {
				c := body[i]
				if c == '\\' && i+1 < len(body) {
					b.WriteByte(body[i+1])
					i += 2
					continue
				}
				if c == '"' {
					closed = true
					i++
					break
				}
				b.WriteByte(c)
				i++
			}
			if !closed {
				return nil, errf(CodeInvalidFilter, "unterminated quote in array literal %q", s)
			}
			v := b.String()
			out = append(out, &v)
		} else {
			start := i
			for i < len(body) && body[i] != ',' {
				if body[i] == '{' || body[i] == '}' {
					return nil, errf(CodeInvalidFilter, "nested arrays are not supported in %q", s)
				}
				i++
			}
			raw := strings.TrimSpace(body[start:i])
			if strings.EqualFold(raw, "null") {
				out = append(out, nil)
			} else {
				v := raw
				out = append(out, &v)
			}
		}
		for i < len(body) && (body[i] == ' ' || body[i] == '\t') {
			i++
		}
		if i >= len(body) {
			return out, nil
		}
		if body[i] != ',' {
			return nil, errf(CodeInvalidFilter, "malformed array literal %q", s)
		}
		i++
		if i >= len(body) {
			return nil, errf(CodeInvalidFilter, "malformed array literal %q", s)
		}
	}
}
