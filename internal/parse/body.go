package parse

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"sort"
	"strings"

	"github.com/suprbdev/pdbcore/introspect"
	"github.com/suprbdev/pdbcore/postgis"

	"github.com/suprbdev/pdbr/internal/api"
	"github.com/suprbdev/pdbr/internal/compile"
	"github.com/suprbdev/pdbr/internal/plan"
)

// CodeColumnReadOnly is the code for generated / identity columns in a body.
const CodeColumnReadOnly = "column_readonly"

// parseBody reads and validates the JSON body of a resource write
// (docs/mutations.md "Body validation") into plan.Body. Values are coerced
// per column type and keyed by column name; Body.Columns is the union of
// provided columns in catalog order (bulk bodies insert DEFAULT for the
// gaps). DELETE ignores any body.
//
// Body keys that are not columns are rejected (invalid_body) unless
// Options.NestedKeys claims them for the nested-mutations plugin: those
// travel as Body.Nested[key] = {Relation, Raw: <decoded JSON>} for the
// plugin's CompileHook to resolve, and the FK columns of a forward relation
// stop being required. Nested keys are accepted on single-object POST bodies
// only.
func (p *parser) parseBody(r *http.Request) error {
	if !p.isMutation() || p.method == plan.DELETE {
		return nil
	}
	res := p.match.Resource
	raw, err := readBody(r)
	if err != nil {
		return err
	}
	if len(bytes.TrimSpace(raw)) == 0 {
		return errf(CodeInvalidBody, "request body is required (a JSON object%s)", orArray(p.method))
	}
	dec := json.NewDecoder(bytes.NewReader(raw))
	dec.UseNumber()
	var val any
	if err := dec.Decode(&val); err != nil {
		return errf(CodeInvalidBody, "request body is not valid JSON")
	}
	if dec.More() {
		return errf(CodeInvalidBody, "request body has trailing data after the JSON value")
	}
	body := &plan.Body{}
	switch v := val.(type) {
	case map[string]any:
		obj, nested, err := p.bodyObject(res, v, true)
		if err != nil {
			return err
		}
		body.Object, body.Nested = obj, nested
		body.Columns = unionColumns(res.Table, obj)
	case []any:
		if p.method == plan.PATCH {
			return errf(CodeInvalidBody, "PATCH body must be a JSON object")
		}
		if p.req.Key != nil {
			return errf(CodeInvalidBody, "%s on a single-row path takes one JSON object", p.method)
		}
		if len(v) == 0 {
			return errf(CodeInvalidBody, "request body is an empty array; provide at least one object")
		}
		body.Objects = make([]map[string]any, len(v))
		for i, item := range v {
			m, ok := item.(map[string]any)
			if !ok {
				return errf(CodeInvalidBody, "request body element %d is not a JSON object", i)
			}
			obj, _, err := p.bodyObject(res, m, false)
			if err != nil {
				return elementErr(i, err)
			}
			body.Objects[i] = obj
		}
		body.Columns = unionColumns(res.Table, body.Objects...)
	default:
		return errf(CodeInvalidBody, "request body must be a JSON object%s", orArray(p.method))
	}
	if p.method == plan.PUT && p.req.Key != nil {
		if err := p.keyIntoBody(res, body); err != nil {
			return err
		}
	}
	if err := p.checkBody(res, body); err != nil {
		return err
	}
	p.req.Body = body
	return nil
}

func orArray(m plan.Method) string {
	if m == plan.PATCH {
		return ""
	}
	return " or a non-empty array of objects"
}

// keyIntoBody completes the body of PUT {table}/{pk} (or /by/{cols}/{vals}):
// key columns the body omits take the path values, key columns it sets must
// equal them, and the path's constraint becomes the conflict target.
func (p *parser) keyIntoBody(res *api.Resource, body *plan.Body) error {
	key := p.req.Key
	for i, c := range key.Constraint.Columns {
		if v, ok := body.Object[c]; ok {
			if fmt.Sprint(v) != fmt.Sprint(key.Values[i]) {
				return errf(CodeInvalidBody, "body %q = %v does not match the path key %v", keyFor(res, c), v, key.Values[i])
			}
			continue
		}
		body.Object[c] = key.Values[i]
	}
	body.Columns = unionColumns(res.Table, body.Object)
	p.req.OnConflict = key.Constraint
	return nil
}

// elementErr prefixes a bulk element's error with its index, keeping the code.
func elementErr(i int, err error) error {
	var ue *compile.UserError
	if errors.As(err, &ue) {
		return errf(ue.Code, "element %d: %s", i, ue.Detail)
	}
	return errf(CodeInvalidBody, "element %d: %s", i, err.Error())
}

// readBody drains the request body; the server's MaxBytesReader surfaces
// as 413.
func readBody(r *http.Request) ([]byte, error) {
	if r.Body == nil {
		return nil, nil
	}
	raw, err := io.ReadAll(r.Body)
	if err != nil {
		var mbe *http.MaxBytesError
		if errors.As(err, &mbe) {
			return nil, errf(CodeBodyTooLarge, "request body exceeds %d bytes", mbe.Limit)
		}
		return nil, errf(CodeInvalidBody, "could not read request body")
	}
	return raw, nil
}

// bodyObject validates and coerces one body object into a column-name keyed
// map. allowNested admits nested-mutation keys (single-object POST only).
func (p *parser) bodyObject(res *api.Resource, obj map[string]any, allowNested bool) (map[string]any, map[string]*plan.Nested, error) {
	var nestedKeys map[string]*api.Relation
	if allowNested && p.method == plan.POST && p.opts.NestedKeys != nil {
		nestedKeys = p.opts.NestedKeys(res)
	}
	out := make(map[string]any, len(obj))
	var nested map[string]*plan.Nested
	inputCols := res.InputColumnsFor(api.Method(p.method))
	for _, key := range sortedKeys(obj) {
		v := obj[key]
		if col, ok := inputCols[key]; ok {
			cv, err := p.coerceBody(col, v)
			if err != nil {
				return nil, nil, errf(CodeInvalidBody, "%q: %s", key, err.Detail)
			}
			out[col.Name] = cv
			continue
		}
		if _, ok := res.InputColumns[key]; ok {
			return nil, nil, errf(CodeInvalidBody, "column %q of %q cannot be written by %s", key, resName(res), p.method)
		}
		if f := res.Field(key); f != nil && f.ReadOnly {
			return nil, nil, errf(CodeColumnReadOnly, "column %q of %q is generated and cannot be written", key, resName(res))
		}
		if rel := nestedKeys[key]; rel != nil {
			if nested == nil {
				nested = map[string]*plan.Nested{}
			}
			nested[key] = &plan.Nested{Relation: rel, Raw: v}
			continue
		}
		return nil, nil, errf(CodeInvalidBody, "unknown key %q in body for %q", key, resName(res))
	}
	for key, n := range nested {
		if !n.Relation.Forward {
			continue
		}
		for _, c := range n.Relation.FK.Columns {
			if _, ok := out[c]; ok {
				return nil, nil, errf(CodeInvalidBody, "body sets both column %q and nested relation %q", c, key)
			}
		}
	}
	return out, nested, nil
}

// checkBody applies the per-method rules: required columns on inserts, a
// non-empty PATCH, and the upsert conflict target.
func (p *parser) checkBody(res *api.Resource, body *plan.Body) error {
	switch p.method {
	case plan.PATCH:
		if len(body.Object) == 0 {
			return errf(CodeInvalidBody, "PATCH body must set at least one column")
		}
		return nil
	case plan.PUT:
		if p.req.OnConflict == nil {
			p.req.OnConflict = res.Table.PrimaryKey
		}
		if p.req.OnConflict == nil {
			return errf(CodeInvalidBody, "resource %q has no primary key; PUT needs ?on_conflict= naming a unique constraint", resName(res))
		}
	case plan.POST:
		if p.req.Prefer.Resolution != "" && len(body.Nested) > 0 {
			return errf(CodeInvalidBody, "nested mutations cannot be combined with Prefer: resolution=")
		}
		switch p.req.Prefer.Resolution {
		case plan.MergeDuplicates:
			if !res.Methods[api.MethodPut] {
				return errf(CodeInvalidPreference, "resolution=merge-duplicates needs UPDATE on %q and a primary key or unique constraint (the PUT method)", resName(res))
			}
			fallthrough
		case plan.IgnoreDuplicates:
			if p.req.OnConflict == nil {
				p.req.OnConflict = res.Table.PrimaryKey
			}
			if p.req.OnConflict == nil && p.req.Prefer.Resolution == plan.MergeDuplicates {
				return errf(CodeInvalidBody, "resource %q has no primary key; resolution=merge-duplicates needs ?on_conflict= naming a unique constraint", resName(res))
			}
		}
	}
	each := func(check func(obj map[string]any) error) error {
		if body.Objects == nil {
			return check(body.Object)
		}
		for i, obj := range body.Objects {
			if err := check(obj); err != nil {
				return elementErr(i, err)
			}
		}
		return nil
	}
	if p.method == plan.PUT {
		if err := each(func(obj map[string]any) error {
			var missing []string
			for _, c := range p.req.OnConflict.Columns {
				if _, ok := obj[c]; !ok {
					missing = append(missing, keyFor(res, c))
				}
			}
			if len(missing) > 0 {
				return errf(CodeInvalidBody, "upsert body must include the conflict target column(s): %s", strings.Join(missing, ", "))
			}
			return nil
		}); err != nil {
			return err
		}
	}
	return each(func(obj map[string]any) error {
		return requiredColumns(res, obj, body.Nested)
	})
}

// requiredColumns rejects an insert row missing a NOT NULL column without a
// default (generated columns and FK columns covered by a nested forward
// relation excepted).
func requiredColumns(res *api.Resource, obj map[string]any, nested map[string]*plan.Nested) error {
	covered := map[string]bool{}
	for _, n := range nested {
		if n.Relation != nil && n.Relation.Forward {
			for _, c := range n.Relation.FK.Columns {
				covered[c] = true
			}
		}
	}
	var missing []string
	for _, c := range res.Table.Columns {
		if !c.NotNull || c.HasDefault || c.Generated || covered[c.Name] {
			continue
		}
		if _, ok := obj[c.Name]; !ok {
			missing = append(missing, keyFor(res, c.Name))
		}
	}
	if len(missing) > 0 {
		return errf(CodeInvalidBody, "missing required column(s): %s", strings.Join(missing, ", "))
	}
	return nil
}

func keyFor(res *api.Resource, col string) string {
	if f := res.FieldByColumn(col); f != nil {
		return f.Key
	}
	return col
}

// unionColumns lists the columns provided by any of the objects, in catalog
// order.
func unionColumns(t *introspect.Table, objs ...map[string]any) []*introspect.Column {
	var out []*introspect.Column
	for _, c := range t.Columns {
		for _, obj := range objs {
			if _, ok := obj[c.Name]; ok {
				out = append(out, c)
				break
			}
		}
	}
	return out
}

func sortedKeys(m map[string]any) []string {
	keys := make([]string, 0, len(m))
	for k := range m {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	return keys
}

// coerceBody coerces one decoded JSON body value for col (the coercion
// table in docs/mutations.md): scalars per type, json/jsonb and composites
// as JSON text (the compiler casts / populates them), arrays element-wise
// (composite arrays stay one JSON text), spatial values as postgis.Value,
// null as nil.
func (p *parser) coerceBody(col *introspect.Column, v any) (any, *compile.UserError) {
	t := p.typeOf(col)
	if v == nil {
		return nil, nil
	}
	switch t.kind {
	case kindComposite:
		comp := p.built.Composites[col.TypeSchema+"."+t.base]
		if err := p.checkComposite(comp, v, t.isArray); err != nil {
			return nil, err
		}
		b, err := json.Marshal(v)
		if err != nil {
			return nil, errf(CodeInvalidBody, "value is not encodable as JSON")
		}
		return string(b), nil
	case kindSpatial:
		if !t.isArray {
			return p.spatialBody(t, v)
		}
		list, ok := v.([]any)
		if !ok {
			return nil, errf(CodeInvalidBody, "expected a JSON array of geometries")
		}
		out := make([]any, len(list))
		for i, item := range list {
			if item == nil {
				continue
			}
			g, err := p.spatialBody(t, item)
			if err != nil {
				return nil, err
			}
			out[i] = g
		}
		return out, nil
	}
	out, err := p.coerceJSON(t, v)
	if err != nil {
		return nil, errf(CodeInvalidBody, "%s", err.Detail)
	}
	return out, nil
}

func (p *parser) spatialBody(t colType, v any) (any, *compile.UserError) {
	val, err := postgis.ParseFor(v, t.flavour, t.srid)
	if err != nil {
		return nil, errf(CodeInvalidBody, "%s", err.Error())
	}
	return val, nil
}

// checkComposite requires an object (or array of objects) whose keys are
// attributes of the composite type; attribute values are left to
// jsonb_populate_record.
func (p *parser) checkComposite(comp *api.Composite, v any, isArray bool) *compile.UserError {
	if isArray {
		list, ok := v.([]any)
		if !ok {
			return errf(CodeInvalidBody, "expected a JSON array of objects for %s[]", comp.Name)
		}
		for i, item := range list {
			if item == nil {
				continue
			}
			if err := p.checkComposite(comp, item, false); err != nil {
				return errf(CodeInvalidBody, "element %d: %s", i, err.Detail)
			}
		}
		return nil
	}
	m, ok := v.(map[string]any)
	if !ok {
		return errf(CodeInvalidBody, "expected a JSON object for composite type %s", comp.Name)
	}
	for key, val := range m {
		var attr *introspect.Column
		for _, f := range comp.Composite.Fields {
			if f.Name == key {
				attr = f
				break
			}
		}
		if attr == nil {
			return errf(CodeInvalidBody, "unknown attribute %q of composite type %s", key, comp.Name)
		}
		if val == nil {
			continue
		}
		at := p.typeOf(attr)
		if at.kind == kindComposite {
			nested := p.built.Composites[attr.TypeSchema+"."+at.base]
			if nested != nil {
				if err := p.checkComposite(nested, val, at.isArray); err != nil {
					return errf(CodeInvalidBody, "attribute %q: %s", key, err.Detail)
				}
			}
		}
	}
	return nil
}

// CoerceValue coerces one decoded JSON value (UseNumber) for col with the
// body coercion rules, regardless of whether the column is writable — the
// nested-mutations plugin uses it for connect keys, which are usually
// identity columns.
func CoerceValue(built *api.Built, col *introspect.Column, v any) (any, error) {
	if built == nil || col == nil {
		return nil, fmt.Errorf("parse: CoerceValue needs a built API and a column")
	}
	p := &parser{built: built, method: plan.POST}
	out, err := p.coerceBody(col, v)
	if err != nil {
		return nil, err
	}
	return out, nil
}

// CoerceObject validates and coerces a JSON object (decoded with UseNumber)
// against res's input columns exactly as a request body object: unknown keys
// are invalid_body, generated columns column_readonly, values coerced per
// the body coercion table. The result is keyed by column name. Plugins that
// build their own DML from nested body rows (nested-mutations) use this so
// their coercion matches the core's.
func CoerceObject(built *api.Built, res *api.Resource, obj map[string]any) (map[string]any, error) {
	if built == nil || res == nil {
		return nil, fmt.Errorf("parse: CoerceObject needs a built API and a resource")
	}
	p := &parser{built: built, method: plan.POST}
	out, _, err := p.bodyObject(res, obj, false)
	if err != nil {
		return nil, err
	}
	return out, nil
}
