package parse

import (
	"bytes"
	"encoding/json"
	"errors"
	"io"
	"net/http"

	"github.com/suprbdev/pdbcore/introspect"

	"github.com/suprbdev/pdbr/internal/api"
	"github.com/suprbdev/pdbr/internal/plan"
)

// parseRPC handles /rpc/{fn}: arguments by name from the query string
// (GET/HEAD) or the JSON body (POST), plus the SETOF <table> read surface
// (select, filters, order, limit/offset, count) against the returned
// resource. Body.Object always holds the argument map (possibly empty).
func (p *parser) parseRPC(r *http.Request) error {
	rpc := p.match.RPC
	args := map[string]any{}
	p.req.Body = &plan.Body{Object: args}
	argByName := map[string]introspect.FuncArg{}
	for _, a := range rpc.Args {
		argByName[a.Name] = a
	}
	res := rpc.Resource
	if rpc.Returns != api.ReturnSetOfRows && rpc.Returns != api.ReturnRow {
		res = nil
	}
	var extra func(key, val string) (bool, error)
	switch p.method {
	case plan.POST:
		if err := p.parseRPCBody(r, argByName, args); err != nil {
			return err
		}
		extra = func(key, val string) (bool, error) {
			if res != nil {
				return false, nil
			}
			return false, errf(CodeUnknownArgument, "unknown query parameter %q (POST arguments go in the JSON body)", key)
		}
	default:
		extra = func(key, val string) (bool, error) {
			a, ok := argByName[key]
			if !ok {
				if res != nil {
					return false, nil
				}
				return false, errf(CodeUnknownArgument, "function %q has no argument %q", rpc.Function.Name, key)
			}
			t := p.typeFor(a.PGType, a.TypeSchema, false, "")
			v, err := p.coerceText(t, val)
			if err != nil {
				return false, errf(CodeInvalidFilter, "argument %q: %s", key, err.Detail)
			}
			args[key] = v
			return true, nil
		}
	}
	return p.parseQueryWith(r.URL.RawQuery, res, extra)
}

// parseRPCBody reads a JSON object of arguments; an absent or empty body is
// {} (every argument NULL).
func (p *parser) parseRPCBody(r *http.Request, argByName map[string]introspect.FuncArg, args map[string]any) error {
	if r.Body == nil || r.ContentLength == 0 {
		return nil
	}
	raw, err := io.ReadAll(r.Body)
	if err != nil {
		var mbe *http.MaxBytesError
		if errors.As(err, &mbe) {
			return errf(CodeBodyTooLarge, "request body exceeds %d bytes", mbe.Limit)
		}
		return errf(CodeInvalidBody, "could not read request body")
	}
	if len(bytes.TrimSpace(raw)) == 0 {
		return nil
	}
	dec := json.NewDecoder(bytes.NewReader(raw))
	dec.UseNumber()
	var obj map[string]any
	if err := dec.Decode(&obj); err != nil {
		return errf(CodeInvalidBody, "request body must be a JSON object of arguments")
	}
	if dec.More() {
		return errf(CodeInvalidBody, "request body has trailing data after the JSON object")
	}
	for k, v := range obj {
		a, ok := argByName[k]
		if !ok {
			return errf(CodeUnknownArgument, "function %q has no argument %q", p.match.RPC.Function.Name, k)
		}
		t := p.typeFor(a.PGType, a.TypeSchema, false, "")
		val, cerr := p.coerceJSON(t, v)
		if cerr != nil {
			return errf(CodeInvalidBody, "argument %q: %s", k, cerr.Detail)
		}
		args[k] = val
	}
	return nil
}
