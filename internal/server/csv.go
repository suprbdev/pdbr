package server

import (
	"bytes"
	"encoding/csv"
	"encoding/json"
	"errors"
	"io"

	"github.com/suprbdev/pdbr/internal/api"
	"github.com/suprbdev/pdbr/internal/exec"
	"github.com/suprbdev/pdbr/internal/plan"
)

// ContentTypeCSV is the media type of text/csv list responses.
const ContentTypeCSV = "text/csv; charset=utf-8"

// csvColumns returns the CSV header of a list response in select= order:
// `*` expands to the resource's columns in catalog order (explicit items
// follow, unaliased duplicates dropped, exactly like the compiled projection),
// composite sets take the type's attributes, scalar sets a single "value"
// column. It never returns nil.
func csvColumns(op *exec.Operation) []string {
	var res *api.Resource
	var node *plan.Node
	if op.Plan != nil {
		node = op.Plan.Select
	}
	switch {
	case op.Resource != nil:
		res = op.Resource
	case op.RPC != nil:
		switch op.RPC.Returns {
		case api.ReturnSetOfRows:
			res = op.RPC.Resource
		case api.ReturnSetOfComposite:
			out := make([]string, 0, len(op.RPC.Composite.Composite.Fields))
			for _, f := range op.RPC.Composite.Composite.Fields {
				out = append(out, f.Name)
			}
			return out
		default:
			return []string{"value"}
		}
	}
	if res == nil {
		return []string{"value"}
	}
	var cols []string
	seen := map[string]bool{}
	add := func(k string) {
		seen[k] = true
		cols = append(cols, k)
	}
	if node == nil || node.Star {
		for _, f := range res.Columns {
			add(f.Key)
		}
	}
	if node == nil {
		return cols
	}
	for i := range node.Fields {
		f := &node.Fields[i]
		key := f.Alias
		if key == "" {
			switch {
			case f.Column != nil:
				key = f.Column.Name
				if fld := res.FieldByColumn(f.Column.Name); fld != nil {
					key = fld.Key
				}
			case f.Computed != nil:
				key = f.Computed.Key
			case f.Pseudo != nil:
				key = f.Pseudo.Key
			}
		}
		if node.Star && f.Alias == "" && f.Column != nil && seen[key] {
			continue
		}
		add(key)
	}
	return cols
}

// writeCSV streams a JSON array (of objects, or of bare values when columns
// is the single "value" column) as RFC 4180 CSV: header row, one record per
// element, values in column order; NULL and missing keys are empty fields,
// strings are written verbatim (quoted by the writer when needed), numbers
// and booleans as their JSON literals, arrays/objects as compact JSON text.
func writeCSV(w io.Writer, columns []string, data []byte) error {
	cw := csv.NewWriter(w)
	cw.UseCRLF = true
	if err := cw.Write(columns); err != nil {
		return err
	}
	dec := json.NewDecoder(bytes.NewReader(data))
	dec.UseNumber()
	tok, err := dec.Token()
	if err != nil {
		return err
	}
	if d, ok := tok.(json.Delim); !ok || d != '[' {
		return errors.New("csv: response is not a JSON array")
	}
	scalar := len(columns) == 1 && columns[0] == "value"
	record := make([]string, len(columns))
	for dec.More() {
		var raw json.RawMessage
		if err := dec.Decode(&raw); err != nil {
			return err
		}
		if scalar && (len(raw) == 0 || raw[0] != '{') {
			record[0] = csvField(raw)
		} else {
			var obj map[string]json.RawMessage
			if err := json.Unmarshal(raw, &obj); err != nil {
				return err
			}
			for i, c := range columns {
				record[i] = csvField(obj[c])
			}
		}
		if err := cw.Write(record); err != nil {
			return err
		}
	}
	cw.Flush()
	return cw.Error()
}

func csvField(raw json.RawMessage) string {
	if len(raw) == 0 || bytes.Equal(raw, []byte("null")) {
		return ""
	}
	if raw[0] == '"' {
		var s string
		if err := json.Unmarshal(raw, &s); err == nil {
			return s
		}
		return string(raw)
	}
	if raw[0] == '{' || raw[0] == '[' {
		var buf bytes.Buffer
		if err := json.Compact(&buf, raw); err == nil {
			return buf.String()
		}
	}
	return string(raw)
}
