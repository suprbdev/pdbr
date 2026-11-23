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
func (p *parser) parseHeaders(r *http.Request) error {
	if err := p.parsePrefer(r.Header.Values("Prefer")); err != nil {
		return err
	}
	if p.opts.Etags {
		p.req.IfMatch = r.Header.Get("If-Match")
		p.req.IfNoneMatch = r.Header.Get("If-None-Match")
	}
	return nil
}

type preference struct {
	key, val, raw string
}

// parsePrefer reads the Prefer header(s) per docs/mutations.md "Prefer". Tokens
// are case-insensitive and may repeat (the last wins, handling= included). Under the default
// handling=lenient anything unknown or malformed is ignored; handling=strict
// turns it into 400 invalid_preference and enables max-affected.
func (p *parser) parsePrefer(headers []string) error {
	p.req.Prefer = plan.Prefer{Return: "representation"}
	var prefs []preference
	for _, h := range headers {
		for _, tok := range strings.Split(h, ",") {
			tok = strings.TrimSpace(tok)
			if tok == "" {
				continue
			}
			kv, _, _ := strings.Cut(tok, ";")
			k, v, _ := strings.Cut(kv, "=")
			prefs = append(prefs, preference{
				key: strings.ToLower(strings.TrimSpace(k)),
				val: strings.ToLower(strings.Trim(strings.TrimSpace(v), `"`)),
				raw: tok,
			})
		}
	}
	for _, pr := range prefs {
		if pr.key == "handling" && (pr.val == "strict" || pr.val == "lenient") {
			p.req.Prefer.Strict = pr.val == "strict"
		}
	}
	pf := &p.req.Prefer
	var applied []string
	apply := func(key, val string) {
		for i, a := range applied {
			if strings.HasPrefix(a, key+"=") {
				applied = append(applied[:i], applied[i+1:]...)
				break
			}
		}
		applied = append(applied, key+"="+val)
	}
	write := p.isMutation()
	for _, pr := range prefs {
		ok := true
		switch pr.key {
		case "return":
			switch pr.val {
			case "representation", "minimal", "headers-only":
				pf.Return = pr.val
				if write {
					apply("return", pr.val)
				}
			default:
				ok = false
			}
		case "handling":
			switch pr.val {
			case "strict", "lenient":
				apply("handling", pr.val)
			default:
				ok = false
			}
		case "max-affected":
			n, err := strconv.ParseInt(pr.val, 10, 64)
			if err != nil || n < 0 {
				ok = false
				break
			}
			if pf.Strict && write && (p.method == plan.PATCH || p.method == plan.DELETE) {
				pf.MaxAffected = &n
				apply("max-affected", pr.val)
			}
		default:
			ok = false
		}
		if !ok && pf.Strict {
			return errf(CodeInvalidPreference, "invalid preference %q (Prefer: handling=strict rejects unknown or malformed preferences)", pr.raw)
		}
	}
	pf.Applied = applied
	return nil
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
