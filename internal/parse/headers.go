package parse

import (
	"mime"
	"net/http"
	"sort"
	"strconv"
	"strings"

	"github.com/suprbdev/pdbr/internal/plan"
)

// checkContentType enforces application/json on requests that carry a
// body (POST/PUT/PATCH with a non-empty body); DELETE and body-less RPC
// POSTs skip the check.
func (p *parser) checkContentType(r *http.Request) error {
	switch p.method {
	case plan.POST, plan.PUT, plan.PATCH:
	default:
		return nil
	}
	if r.ContentLength == 0 || r.Body == nil {
		return nil
	}
	ct := r.Header.Get("Content-Type")
	if ct == "" {
		return errf(CodeUnsupportedMediaType, "request body must be application/json (Content-Type missing)")
	}
	mt, _, err := mime.ParseMediaType(ct)
	if err != nil || mt != "application/json" {
		return errf(CodeUnsupportedMediaType, "request body must be application/json, got %q", ct)
	}
	return nil
}

// parseHeaders reads Prefer, If-Match and If-None-Match.
func (p *parser) parseHeaders(r *http.Request) {
	p.req.Prefer = plan.Prefer{Return: "representation"}
	for _, h := range r.Header.Values("Prefer") {
		for _, tok := range strings.Split(h, ",") {
			k, v, _ := strings.Cut(strings.TrimSpace(tok), "=")
			if strings.EqualFold(strings.TrimSpace(k), "return") {
				switch strings.ToLower(strings.TrimSpace(v)) {
				case "minimal":
					p.req.Prefer.Return = "minimal"
				case "representation":
					p.req.Prefer.Return = "representation"
				}
			}
		}
	}
	if p.opts.Etags {
		p.req.IfMatch = r.Header.Get("If-Match")
		p.req.IfNoneMatch = r.Header.Get("If-None-Match")
	}
}

type mediaRange struct {
	typ, sub string
	q        float64
	pos      int
}

// negotiateAccept picks JSON or CSV. CSV needs server.csv, a GET/HEAD list
// (list route or set-returning RPC) and no embeds in select=; anything
// else that is not JSON-compatible is 406.
func (p *parser) negotiateAccept(r *http.Request) error {
	p.req.Accept = plan.AcceptJSON
	accept := strings.TrimSpace(strings.Join(r.Header.Values("Accept"), ","))
	if accept == "" {
		return nil
	}
	var ranges []mediaRange
	for i, part := range strings.Split(accept, ",") {
		part = strings.TrimSpace(part)
		if part == "" {
			continue
		}
		mt, params, err := mime.ParseMediaType(part)
		if err != nil {
			continue
		}
		q := 1.0
		if qs, ok := params["q"]; ok {
			if f, err := strconv.ParseFloat(qs, 64); err == nil {
				q = f
			}
		}
		typ, sub, _ := strings.Cut(mt, "/")
		ranges = append(ranges, mediaRange{typ: typ, sub: sub, q: q, pos: i})
	}
	if len(ranges) == 0 {
		return errf(CodeNotAcceptable, "Accept header %q is not satisfiable", accept)
	}
	// Most specific and highest q first; ties keep header order.
	sort.SliceStable(ranges, func(i, j int) bool {
		if ranges[i].q != ranges[j].q {
			return ranges[i].q > ranges[j].q
		}
		return specificity(ranges[i]) > specificity(ranges[j])
	})
	csvOK := p.csvAllowed()
	csvWanted := false
	for _, mr := range ranges {
		if mr.q <= 0 {
			continue
		}
		switch {
		case mr.typ == "*" && mr.sub == "*",
			mr.typ == "application" && (mr.sub == "*" || mr.sub == "json" || mr.sub == "problem+json"):
			p.req.Accept = plan.AcceptJSON
			return nil
		case mr.typ == "text" && (mr.sub == "csv" || mr.sub == "*"):
			if csvOK {
				p.req.Accept = plan.AcceptCSV
				return nil
			}
			csvWanted = csvWanted || mr.sub == "csv"
		}
	}
	if csvWanted {
		return p.csvRejection(accept)
	}
	return errf(CodeNotAcceptable, "Accept header %q is not satisfiable; supported: application/json, text/csv", accept)
}

func specificity(m mediaRange) int {
	switch {
	case m.typ == "*":
		return 0
	case m.sub == "*":
		return 1
	}
	return 2
}

func (p *parser) csvAllowed() bool {
	if !p.opts.CSV {
		return false
	}
	if p.method != plan.GET && p.method != plan.HEAD {
		return false
	}
	if !p.isList() {
		return false
	}
	return !hasEmbeds(p.req.Select)
}

func (p *parser) csvRejection(accept string) error {
	switch {
	case !p.opts.CSV:
		return errf(CodeNotAcceptable, "text/csv is disabled (server.csv)")
	case p.method != plan.GET && p.method != plan.HEAD, !p.isList():
		return errf(CodeNotAcceptable, "text/csv is only available on list GET requests")
	case hasEmbeds(p.req.Select):
		return errf(CodeNotAcceptable, "text/csv cannot represent embedded relations; remove embeds from select=")
	}
	return errf(CodeNotAcceptable, "Accept header %q is not satisfiable", accept)
}
