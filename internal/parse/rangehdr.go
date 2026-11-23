package parse

import (
	"net/http"
	"regexp"
	"strconv"
	"strings"

	"github.com/suprbdev/pdbr/internal/plan"
)

var rangeHeaderRE = regexp.MustCompile(`^([0-9]+)-([0-9]*)$`)

// readRange records the Range request header of a GET or HEAD (every other
// method ignores it, RFC 9110 section 14.2). A Range-Unit other than items
// disables it.
func (p *parser) readRange(r *http.Request) {
	if p.method != plan.GET && p.method != plan.HEAD {
		return
	}
	if u := strings.TrimSpace(r.Header.Get("Range-Unit")); u != "" && !strings.EqualFold(u, "items") {
		return
	}
	p.rangeHdr = strings.TrimSpace(r.Header.Get("Range"))
}

// applyRange intersects the Range header (inclusive first-last, last
// optional) with the limit/offset window of the query and writes the result
// back into Page. A header that does not match first-last is ignored; an
// empty intersection is 400 invalid_range. The page size is clamped to
// limits.max_page_size without an error, unlike an explicit limit. Cursors
// cannot be combined with a Range.
func (p *parser) applyRange() error {
	if p.rangeHdr == "" || !p.isList() {
		return nil
	}
	m := rangeHeaderRE.FindStringSubmatch(p.rangeHdr)
	if m == nil {
		return nil
	}
	lo, err := strconv.ParseInt(m[1], 10, 64)
	if err != nil {
		return errf(CodeInvalidRange, "Range %q is out of bounds", p.rangeHdr)
	}
	hi := int64(-1)
	if m[2] != "" {
		if hi, err = strconv.ParseInt(m[2], 10, 64); err != nil {
			hi = -1
		}
	}
	pg := &p.req.Page
	if pg.After != nil || pg.Before != nil {
		return errf(CodeKeysetUnsupported, "Range cannot be combined with after/before; use limit with a cursor")
	}
	if hi >= 0 && hi < lo {
		return errf(CodeInvalidRange, "Range %q: the last item precedes the first", p.rangeHdr)
	}
	qlo := int64(pg.Offset)
	qhi := int64(-1)
	if pg.LimitSet {
		qhi = qlo + int64(pg.Limit) - 1
	}
	lo = max(lo, qlo)
	switch {
	case hi < 0:
		hi = qhi
	case qhi >= 0:
		hi = min(hi, qhi)
	}
	if hi >= 0 && hi < lo {
		return errf(CodeInvalidRange, "Range %q does not overlap the offset/limit window [%d, %d]", p.rangeHdr, qlo, qhi)
	}
	pg.Offset = int(lo)
	if hi >= 0 {
		pg.Limit = int(min(hi-lo+1, int64(p.opts.maxPageSize())))
		pg.LimitSet = true
	}
	pg.FromRange = true
	return nil
}
