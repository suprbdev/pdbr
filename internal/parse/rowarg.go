package parse

import (
	"bytes"
	"encoding/json"
	"sort"

	"github.com/suprbdev/pdbcore/introspect"
	"github.com/suprbdev/pdbcore/postgis"

	"github.com/suprbdev/pdbr/internal/api"
	"github.com/suprbdev/pdbr/internal/compile"
)

// rowArg validates and coerces the value of a function argument typed as
// res's row type: a JSON object keyed by res's readable columns, all
// optional (an omitted key is a NULL attribute), or for an array argument a
// JSON array of such objects and nulls. Unknown and hidden keys are
// rejected with code. The result is JSON-encodable and keyed by column
// name, ready for jsonb_populate_record.
func (p *parser) rowArg(a introspect.FuncArg, res *api.Resource, v any, array bool, code string) (any, *compile.UserError) {
	if v == nil {
		return nil, nil
	}
	if !array {
		return p.rowObject(a, res, v, code)
	}
	list, ok := v.([]any)
	if !ok {
		return nil, errf(code, "argument %q: expected a JSON array of %s objects", a.Name, res.Path)
	}
	out := make([]any, len(list))
	for i, item := range list {
		row, err := p.rowObject(a, res, item, code)
		if err != nil {
			return nil, errf(code, "element %d: %s", i, err.Detail)
		}
		out[i] = row
	}
	return out, nil
}

func (p *parser) rowObject(a introspect.FuncArg, res *api.Resource, v any, code string) (any, *compile.UserError) {
	if v == nil {
		return nil, nil
	}
	obj, ok := v.(map[string]any)
	if !ok {
		return nil, errf(code, "argument %q: expected a JSON object of %s columns", a.Name, res.Path)
	}
	keys := make([]string, 0, len(obj))
	for k := range obj {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	out := make(map[string]any, len(obj))
	for _, k := range keys {
		f := res.Field(k)
		if f == nil {
			return nil, errf(code, "argument %q: unknown column %q of %s", a.Name, k, res.Path)
		}
		val, err := p.rowAttr(f.Column, obj[k])
		if err != nil {
			return nil, errf(code, "argument %q: column %q: %s", a.Name, k, err.Detail)
		}
		out[f.Column.Name] = val
	}
	return out, nil
}

// rowAttr coerces one attribute with the request-body rules and returns it
// in the form jsonb_populate_record parses: JSON values and composites as
// embedded JSON, spatial values as their GeoJSON or EWKT text, extension
// types (hstore, ranges) only in their literal string form.
func (p *parser) rowAttr(col *introspect.Column, v any) (any, *compile.UserError) {
	if v == nil {
		return nil, nil
	}
	if _, ok := api.CodecFor(p.built.Catalog, col); ok {
		if _, isText := v.(string); !isText {
			return nil, errf(CodeInvalidBody, "pass the %s literal string inside a row argument", p.typeOf(col).name())
		}
	}
	out, err := p.coerceBody(col, v)
	if err != nil {
		return nil, err
	}
	return populateForm(p.typeOf(col), out), nil
}

func populateForm(t colType, v any) any {
	switch x := v.(type) {
	case postgis.Value:
		return x.Param
	case []any:
		out := make([]any, len(x))
		for i, e := range x {
			out[i] = populateForm(t.elem(), e)
		}
		return out
	case string:
		if t.kind == kindJSON || t.kind == kindComposite {
			return json.RawMessage(x)
		}
	}
	return v
}

// decodeJSONArg decodes the JSON text of a GET argument (UseNumber).
func decodeJSONArg(text string) (any, bool) {
	dec := json.NewDecoder(bytes.NewReader([]byte(text)))
	dec.UseNumber()
	var v any
	if err := dec.Decode(&v); err != nil || dec.More() {
		return nil, false
	}
	return v, true
}
