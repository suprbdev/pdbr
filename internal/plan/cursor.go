package plan

import (
	"bytes"
	"encoding/base64"
	"encoding/json"
	"fmt"

	"github.com/suprbdev/pdbr/internal/api"
)

// EncodeCursor mints a keyset cursor for a row of r: base64url (no padding)
// of the JSON array ["schema.table", pk…] with one element per primary key
// column (json.Number values marshal as numbers).
func EncodeCursor(r *api.Resource, pk []any) string {
	payload := make([]any, 0, len(pk)+1)
	payload = append(payload, r.Table.Schema+"."+r.Table.Name)
	payload = append(payload, pk...)
	b, err := json.Marshal(payload)
	if err != nil {
		return ""
	}
	return base64.RawURLEncoding.EncodeToString(b)
}

// DecodeCursor decodes a cursor structurally: the "schema.table" it was
// minted for and the raw primary key values (numbers as json.Number). The
// caller checks the resource matches and coerces each value to its PK
// column type; anything malformed is an error (400 invalid_cursor).
func DecodeCursor(cursor string) (resource string, pk []any, err error) {
	raw, err := base64.RawURLEncoding.DecodeString(cursor)
	if err != nil {
		// Tolerate padded / standard-alphabet cursors from hand-written clients.
		if raw, err = base64.URLEncoding.DecodeString(cursor); err != nil {
			if raw, err = base64.StdEncoding.DecodeString(cursor); err != nil {
				return "", nil, fmt.Errorf("cursor is not base64url")
			}
		}
	}
	dec := json.NewDecoder(bytes.NewReader(raw))
	dec.UseNumber()
	var arr []any
	if err := dec.Decode(&arr); err != nil || len(arr) < 2 {
		return "", nil, fmt.Errorf("cursor is not a JSON array of at least two elements")
	}
	name, ok := arr[0].(string)
	if !ok || name == "" {
		return "", nil, fmt.Errorf("cursor does not name a resource")
	}
	return name, arr[1:], nil
}
