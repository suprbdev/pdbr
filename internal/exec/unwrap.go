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
	N       *int            `json:"n"`
}

type etagWrapper struct {
	Etag string          `json:"etag"`
	Row  json.RawMessage `json:"row"`
}

// Unwrap maps the statement's data column (raw; found reports whether the
// statement returned a row) to the client response per stmt.Shape: the 404
// for missed single-row lookups, ETag / 304, the page headers
// (X-Total-Count, Content-Range, X-Next-Cursor, X-Prev-Cursor, Link; 206 when a
// counted page is a strict subset of the total, 416 past it), the RPC shapes
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
		if err := decodeEnvelope(raw, "row", &w, &w.Row); err != nil {
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
		var w pageWrapper
		if err := decodeEnvelope(raw, "rows", &w, &w.Rows); err != nil {
			return nil, &Error{Status: http.StatusInternalServerError, Code: CodeInternal, Detail: "malformed page wrapper"}
		}
		res.Data = w.Rows
		if res.Data == nil {
			res.Data = json.RawMessage("[]")
		}
		var total *int64
		if stmt.Total && w.Total != nil {
			total = w.Total
			res.Headers.Set("X-Total-Count", strconv.FormatInt(*w.Total, 10))
		}
		if op.Plan == nil || op.Plan.Page.After == nil && op.Plan.Page.Before == nil {
			n := 0
			if w.N != nil {
				n = *w.N
			} else {
				var rows []json.RawMessage
				if err := json.Unmarshal(res.Data, &rows); err != nil {
					return nil, &Error{Status: http.StatusInternalServerError, Code: CodeInternal, Detail: "malformed page wrapper"}
				}
				n = len(rows)
			}
			offset := 0
			if op.Plan != nil {
				offset = op.Plan.Page.Offset
			}
			res.Headers.Set("Content-Range", contentRange(offset, n, total))
			if total != nil {
				switch t := *total; {
				case int64(offset) > t:
					return nil, &Error{
						Status:  http.StatusRequestedRangeNotSatisfiable,
						Code:    CodeRangeNotSatisfiable,
						Detail:  "an offset of " + strconv.Itoa(offset) + " was requested, but there are only " + strconv.FormatInt(t, 10) + " rows",
						Headers: http.Header{"Content-Range": {"*/" + strconv.FormatInt(t, 10)}},
					}
				case int64(n) < t:
					res.Status = http.StatusPartialContent
				}
			}
		}
		pageHeaders(res.Headers, op, stmt, &w, basePath)
	case compile.ShapeVoid, compile.ShapeScalar, compile.ShapeJWT, compile.ShapeJWTSet:
		unwrapRPC(res, stmt, raw, found)
	case compile.ShapeMutRow, compile.ShapeMutList, compile.ShapeMutWrap, compile.ShapeMutBulk:
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

// contentRange renders the PostgREST-style Content-Range of n rows starting
// at offset: "<first>-<last>/<total>", "*" for an empty range or an unknown
// total.
func contentRange(offset, n int, total *int64) string {
	r := "*"
	if n > 0 {
		r = strconv.Itoa(offset) + "-" + strconv.Itoa(offset+n-1)
	}
	t := "*"
	if total != nil {
		t = strconv.FormatInt(*total, 10)
	}
	return r + "/" + t
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
		offsetLink := func(offset int) string {
			q := withParam(op.RawQuery, "offset", strconv.Itoa(offset))
			if pg.FromRange {
				q = withParam(strings.TrimPrefix(q, "?"), "limit", strconv.Itoa(limit))
			}
			return `<` + base + q + `>`
		}
		if w.HasNext {
			links = append(links, offsetLink(pg.Offset+limit)+`; rel="next"`)
		}
		if w.HasPrev && pg.Offset > 0 {
			links = append(links, offsetLink(max(pg.Offset-limit, 0))+`; rel="prev"`)
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

// decodeEnvelope decodes a wrapper object into w (numbers as json.Number).
// The member key, which the compiler emits last, is split off unread and
// stored in *payload; a wrapper that lacks it or starts with it is decoded
// in full.
func decodeEnvelope(raw []byte, key string, w any, payload *json.RawMessage) error {
	meta, value, split := splitEnvelope(raw, key)
	if !split {
		meta = raw
	}
	dec := json.NewDecoder(bytes.NewReader(meta))
	dec.UseNumber()
	if err := dec.Decode(w); err != nil {
		return err
	}
	if split {
		*payload = value
	}
	return nil
}
