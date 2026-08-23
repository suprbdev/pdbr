package exec

import (
	"bytes"
	"encoding/json"
	"net/http"
	"slices"
	"strconv"
	"strings"

	"github.com/suprbdev/pdbr/internal/compile"
	"github.com/suprbdev/pdbr/internal/plan"
)

type pageWrapper struct {
	Rows    json.RawMessage `json:"rows"`
	HasNext bool            `json:"has_next"`
	HasPrev bool            `json:"has_prev"`
	First   []any           `json:"first"`
	Last    []any           `json:"last"`
	Total   *int64          `json:"total"`
}

type etagWrapper struct {
	Etag string          `json:"etag"`
	Row  json.RawMessage `json:"row"`
}

// Unwrap maps the statement's data column (raw; found reports whether the
// statement returned a row) to the client response per stmt.Shape: the 404
// for missed single-row lookups, ETag / 304, the page headers
// (X-Total-Count, X-Next-Cursor, X-Prev-Cursor, Link), the RPC shapes
// (unwrap_rpc.go) and the mutation statuses (201/200/204/404/412, Location;
// unwrap_mutation.go). basePath prefixes Link and Location URLs. Pure: no
// database, no HTTP.
func Unwrap(op *Operation, stmt *compile.Statement, raw []byte, found bool, basePath string) (*Result, error) {
	res := &Result{Status: http.StatusOK, Headers: http.Header{}}
	switch stmt.Shape {
	case compile.ShapeRow:
		if !found || raw == nil {
			return nil, notFound(op)
		}
		res.Data = raw
	case compile.ShapeRowEtag:
		if !found || raw == nil {
			return nil, notFound(op)
		}
		var w etagWrapper
		if err := json.Unmarshal(raw, &w); err != nil {
			return nil, &Error{Status: http.StatusInternalServerError, Code: CodeInternal, Detail: "malformed etag wrapper"}
		}
		etag := `W/"` + w.Etag + `"`
		res.Headers.Set("ETag", etag)
		if op.Plan != nil && etagMatches(op.Plan.IfNoneMatch, w.Etag) {
			res.Status = http.StatusNotModified
			return res, nil
		}
		res.Data = w.Row
	case compile.ShapePage:
		if !found || raw == nil {
			res.Data = json.RawMessage("[]")
			return res, nil
		}
		dec := json.NewDecoder(bytes.NewReader(raw))
		dec.UseNumber()
		var w pageWrapper
		if err := dec.Decode(&w); err != nil {
			return nil, &Error{Status: http.StatusInternalServerError, Code: CodeInternal, Detail: "malformed page wrapper"}
		}
		res.Data = w.Rows
		if res.Data == nil {
			res.Data = json.RawMessage("[]")
		}
		if stmt.Total && w.Total != nil {
			res.Headers.Set("X-Total-Count", strconv.FormatInt(*w.Total, 10))
		}
		pageHeaders(res.Headers, op, stmt, &w, basePath)
	case compile.ShapeVoid, compile.ShapeScalar, compile.ShapeJWT, compile.ShapeJWTSet:
		unwrapRPC(res, stmt, raw, found)
	case compile.ShapeMutRow, compile.ShapeMutList, compile.ShapeMutWrap:
		return unwrapMutation(op, stmt, raw, found, basePath)
	default:
		if !found || raw == nil {
			res.Data = json.RawMessage("null")
		} else {
			res.Data = raw
		}
	}
	return res, nil
}

func notFound(op *Operation) *Error {
	what := "row"
	if op != nil && op.Resource != nil {
		what = "row of " + op.Resource.Path
	}
	return &Error{Status: http.StatusNotFound, Code: CodeNotFound, Detail: "no such " + what}
}

// etagMatches implements the weak comparison of If-None-Match against the
// row hash: `*`, or any listed tag whose opaque part equals the hash.
func etagMatches(header, etag string) bool {
	header = strings.TrimSpace(header)
	if header == "" {
		return false
	}
	if header == "*" {
		return true
	}
	for _, tag := range strings.Split(header, ",") {
		tag = strings.TrimSpace(tag)
		tag = strings.TrimPrefix(tag, "W/")
		tag = strings.Trim(tag, `"`)
		if tag == etag {
			return true
		}
	}
	return false
}

// pageHeaders sets the cursor and Link headers for a page. Keyset-capable
// pages carry cursors and after=/before= links (any offset is dropped:
// the cursor is an exact continuation); offset-only pages carry offset=
// links and no cursors.
func pageHeaders(h http.Header, op *Operation, stmt *compile.Statement, w *pageWrapper, basePath string) {
	var pg plan.Page
	if op != nil && op.Plan != nil {
		pg = op.Plan.Page
	}
	base := basePath + op.Path
	var links []string
	if stmt.Cursors && op.Resource != nil {
		if w.HasNext && w.Last != nil {
			next := plan.EncodeCursor(op.Resource, w.Last)
			h.Set("X-Next-Cursor", next)
			links = append(links, `<`+base+withParam(op.RawQuery, "after", next, "before", "offset")+`>; rel="next"`)
		}
		if w.HasPrev && w.First != nil {
			prev := plan.EncodeCursor(op.Resource, w.First)
			h.Set("X-Prev-Cursor", prev)
			links = append(links, `<`+base+withParam(op.RawQuery, "before", prev, "after", "offset")+`>; rel="prev"`)
		}
	} else {
		limit := pg.Limit
		if limit <= 0 {
			limit = 100
		}
		if w.HasNext {
			links = append(links, `<`+base+withParam(op.RawQuery, "offset", strconv.Itoa(pg.Offset+limit))+`>; rel="next"`)
		}
		if w.HasPrev && pg.Offset > 0 {
			links = append(links, `<`+base+withParam(op.RawQuery, "offset", strconv.Itoa(max(pg.Offset-limit, 0)))+`>; rel="prev"`)
		}
	}
	if len(links) > 0 {
		h.Set("Link", strings.Join(links, ", "))
	}
}

// withParam rewrites a raw query string for a Link URL: the first key=…
// pair is replaced in place (appended when absent), later duplicates and
// every drop key (the opposite cursor, offset) are removed, and all other
// pairs are kept verbatim and in order — so the client's own parameters
// (select, filters, order, encoding) survive untouched. The result starts
// with "?".
func withParam(rawQuery, key, value string, drop ...string) string {
	var kept []string
	replaced := false
	for _, pair := range strings.Split(rawQuery, "&") {
		if pair == "" {
			continue
		}
		name, _, _ := strings.Cut(pair, "=")
		if name == key {
			if !replaced {
				kept = append(kept, key+"="+value)
				replaced = true
			}
			continue
		}
		if slices.Contains(drop, name) {
			continue
		}
		kept = append(kept, pair)
	}
	if !replaced {
		kept = append(kept, key+"="+value)
	}
	return "?" + strings.Join(kept, "&")
}
