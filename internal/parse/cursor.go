package parse

import (
	"bytes"
	"encoding/base64"
	"encoding/json"
	"strings"

	"github.com/suprbdev/pdbr/internal/api"
)

// parseCursor decodes an after=/before= cursor: base64url (no padding) of
// the JSON array ["schema.table", pk...]. Values come back coerced to the
// PK column types (same Go types as plan.Key.Values).
func (p *parser) parseCursor(param, cur string, res *api.Resource) ([]any, error) {
	if !res.Keyset || res.Table.PrimaryKey == nil {
		return nil, errf(CodeKeysetUnsupported, "resource %q has no primary key; use offset paging", res.Path)
	}
	raw, ok := decodeBase64(cur)
	if !ok {
		return nil, errf(CodeInvalidCursor, "%s: malformed cursor", param)
	}
	dec := json.NewDecoder(bytes.NewReader(raw))
	dec.UseNumber()
	var arr []any
	if err := dec.Decode(&arr); err != nil || len(arr) < 2 {
		return nil, errf(CodeInvalidCursor, "%s: malformed cursor", param)
	}
	name, ok := arr[0].(string)
	want := res.Table.Schema + "." + res.Table.Name
	if !ok || name != want {
		return nil, errf(CodeInvalidCursor, "%s: cursor was issued for a different resource", param)
	}
	pk := res.Table.PrimaryKey
	keys := arr[1:]
	if len(keys) != len(pk.Columns) {
		return nil, errf(CodeInvalidCursor, "%s: cursor has %d key value(s), want %d", param, len(keys), len(pk.Columns))
	}
	out := make([]any, len(keys))
	for i, k := range keys {
		col := res.Table.Column(pk.Columns[i])
		if col == nil {
			return nil, errf(CodeInvalidCursor, "%s: cursor key column %q is not exposed", param, pk.Columns[i])
		}
		switch k.(type) {
		case json.Number, string, bool:
		default:
			return nil, errf(CodeInvalidCursor, "%s: cursor key values must be scalars", param)
		}
		v, err := p.coerceJSON(p.typeOf(col), k)
		if err != nil {
			return nil, errf(CodeInvalidCursor, "%s: %s", param, err.Detail)
		}
		out[i] = v
	}
	return out, nil
}

// decodeBase64 accepts base64url without padding (the documented form) and
// tolerates padded / standard alphabets so hand-built cursors still work.
func decodeBase64(s string) ([]byte, bool) {
	s = strings.TrimRight(s, "=")
	if s == "" {
		return nil, false
	}
	if b, err := base64.RawURLEncoding.DecodeString(s); err == nil {
		return b, true
	}
	if b, err := base64.RawStdEncoding.DecodeString(s); err == nil {
		return b, true
	}
	return nil, false
}
