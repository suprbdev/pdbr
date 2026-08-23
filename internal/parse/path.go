package parse

import (
	"net/url"
	"strings"

	"github.com/suprbdev/pdbr/internal/api"
	"github.com/suprbdev/pdbr/internal/plan"
)

// parseKey turns the raw key segments of a row / unique route into
// plan.Key. Composite values are comma-separated in the raw (still
// percent-encoded) segment so a literal comma travels as %2C; a
// single-column key takes the whole segment verbatim.
func (p *parser) parseKey() error {
	m := p.match
	res := m.Resource
	switch m.Route.Kind {
	case api.RouteRow:
		pk := res.Table.PrimaryKey
		if pk == nil {
			return errf(CodeNotFound, "resource %q has no primary key", res.Path)
		}
		raw, err := splitKeySegment(m.Key, len(pk.Columns))
		if err != nil {
			return err
		}
		vals, err := p.coerceKey(res, pk.Columns, raw)
		if err != nil {
			return err
		}
		p.req.Key = &plan.Key{Constraint: pk, Values: vals}
	case api.RouteUnique:
		u := m.Route.Unique
		if u == nil {
			return errf(CodeNotFound, "no such route")
		}
		cols, err := splitKeySegment(m.Cols, len(u.Columns))
		if err != nil {
			return err
		}
		raw, err := splitKeySegment(m.Vals, len(u.Columns))
		if err != nil {
			return err
		}
		byCol := map[string]string{}
		for i, k := range cols {
			f := res.Field(k)
			if f == nil {
				return errf(CodeNotFound, "no such route")
			}
			byCol[f.Column.Name] = raw[i]
		}
		ordered := make([]string, len(u.Columns))
		for i, c := range u.Columns {
			v, ok := byCol[c]
			if !ok {
				return errf(CodeNotFound, "no such route")
			}
			ordered[i] = v
		}
		vals, err := p.coerceKey(res, u.Columns, ordered)
		if err != nil {
			return err
		}
		p.req.Key = &plan.Key{Constraint: u, Values: vals}
	}
	return nil
}

// splitKeySegment splits a raw path segment into n percent-decoded values.
// With n == 1 the whole segment is one value (commas need no escaping).
func splitKeySegment(seg string, n int) ([]string, error) {
	var parts []string
	if n == 1 {
		parts = []string{seg}
	} else {
		parts = strings.Split(seg, ",")
	}
	if len(parts) != n {
		return nil, errf(CodeNotFound, "key has %d value(s), want %d", len(parts), n)
	}
	out := make([]string, n)
	for i, s := range parts {
		v, err := url.PathUnescape(s)
		if err != nil {
			return nil, errf(CodeNotFound, "malformed key segment %q", s)
		}
		out[i] = v
	}
	return out, nil
}

func (p *parser) coerceKey(res *api.Resource, cols []string, raw []string) ([]any, error) {
	out := make([]any, len(cols))
	for i, name := range cols {
		col := res.Table.Column(name)
		if col == nil {
			return nil, errf(CodeNotFound, "key column %q is not exposed", name)
		}
		v, err := p.coerceScalar(col, raw[i])
		if err != nil {
			return nil, errf(CodeInvalidFilter, "invalid key value for %q: %s", name, err.Detail)
		}
		out[i] = v
	}
	return out, nil
}
