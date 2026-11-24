package server

import (
	"bufio"
	"bytes"
	"encoding/json"
	"errors"
	"io"
	"strings"
	"unicode"
	"unicode/utf8"

	"github.com/suprbdev/pdbr/internal/api"
	"github.com/suprbdev/pdbr/internal/exec"
	"github.com/suprbdev/pdbr/internal/plan"
)

// ContentTypeCSV is the media type of text/csv list responses.
const ContentTypeCSV = "text/csv; charset=utf-8"

// csvColumns returns the CSV header of a list response in select= order:
// `*` expands to the resource's columns in catalog order (explicit items
// follow, unaliased duplicates dropped, exactly like the compiled projection;
// spread embeds add their items after the node's own, a key once),
// composite sets take the type's attributes (rasters excepted), scalar
// sets a single "value" column, aggregate functions their alias or
// function name. It never returns nil.
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
			fields := api.RenderedFields(op.RPC.Composite.Composite)
			out := make([]string, 0, len(fields))
			for _, f := range fields {
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
	if node == nil {
		for _, f := range res.Columns {
			add(f.Key)
		}
		return cols
	}
	var walk func(n *plan.Node, res *api.Resource)
	walk = func(n *plan.Node, res *api.Resource) {
		if n.Star {
			for _, f := range res.Columns {
				if !seen[f.Key] {
					add(f.Key)
				}
			}
		}
		for i := range n.Fields {
			f := &n.Fields[i]
			key := f.Key(res)
			if n.Star && f.Alias == "" && f.Column != nil && f.Agg == nil && len(f.Path) == 0 && f.Cast == nil && seen[key] {
				continue
			}
			if !seen[key] {
				add(key)
			}
		}
		for i := range n.Embeds {
			e := &n.Embeds[i]
			if e.Spread && e.Node != nil && e.Relation != nil {
				walk(e.Node, e.Relation.To)
			}
		}
	}
	walk(node, res)
	return cols
}

// writeCSV streams a JSON array (of objects, or of bare values when columns
// is the single "value" column) as RFC 4180 CSV: header row, one record per
// element, values in column order; NULL and missing keys are empty fields,
// strings are written byte for byte (quoted when needed, see writeRecord),
// numbers and booleans as their JSON literals, arrays/objects as compact
// JSON text.
// With escapeFormulas, a header or string cell that a spreadsheet would
// read as a formula gets a leading single quote (escapeFormula).
func writeCSV(w io.Writer, columns []string, data []byte, escapeFormulas bool) error {
	bw := bufio.NewWriter(w)
	header := columns
	if escapeFormulas {
		header = make([]string, len(columns))
		for i, c := range columns {
			header[i] = escapeFormula(c)
		}
	}
	if err := writeRecord(bw, header); err != nil {
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
			record[0] = csvField(raw, escapeFormulas)
		} else {
			var obj map[string]json.RawMessage
			if err := json.Unmarshal(raw, &obj); err != nil {
				return err
			}
			for i, c := range columns {
				record[i] = csvField(obj[c], escapeFormulas)
			}
		}
		if err := writeRecord(bw, record); err != nil {
			return err
		}
	}
	return bw.Flush()
}

// writeRecord writes one RFC 4180 record terminated by CRLF. A field holding
// a comma, a double quote, CR or LF, or starting with white space, is quoted
// with inner quotes doubled; every other byte, line breaks inside a quoted
// field included, is copied unchanged.
func writeRecord(w *bufio.Writer, fields []string) error {
	for i, f := range fields {
		if i > 0 {
			if err := w.WriteByte(','); err != nil {
				return err
			}
		}
		if !csvNeedsQuotes(f) {
			if _, err := w.WriteString(f); err != nil {
				return err
			}
			continue
		}
		if err := w.WriteByte('"'); err != nil {
			return err
		}
		if _, err := w.WriteString(strings.ReplaceAll(f, `"`, `""`)); err != nil {
			return err
		}
		if err := w.WriteByte('"'); err != nil {
			return err
		}
	}
	_, err := w.WriteString("\r\n")
	return err
}

func csvNeedsQuotes(f string) bool {
	if f == "" {
		return false
	}
	if strings.ContainsAny(f, ",\"\r\n") {
		return true
	}
	r, _ := utf8.DecodeRuneInString(f)
	return unicode.IsSpace(r)
}

func csvField(raw json.RawMessage, escapeFormulas bool) string {
	if len(raw) == 0 || bytes.Equal(raw, []byte("null")) {
		return ""
	}
	if raw[0] == '"' {
		var s string
		if err := json.Unmarshal(raw, &s); err != nil {
			return string(raw)
		}
		if escapeFormulas {
			return escapeFormula(s)
		}
		return s
	}
	if raw[0] == '{' || raw[0] == '[' {
		var buf bytes.Buffer
		if err := json.Compact(&buf, raw); err == nil {
			return buf.String()
		}
	}
	return string(raw)
}

// escapeFormula prefixes s with a single quote when it starts with a
// character that makes Excel, LibreOffice or Google Sheets evaluate the
// cell as a formula (=, +, -, @, tab, carriage return; OWASP CSV
// injection). JSON numbers never pass through it, so negative numbers stay
// numeric.
func escapeFormula(s string) string {
	if s == "" {
		return s
	}
	switch s[0] {
	case '=', '+', '-', '@', '\t', '\r':
		return "'" + s
	}
	return s
}
