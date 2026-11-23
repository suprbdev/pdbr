package parse

import (
	"bytes"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"strings"

	"github.com/suprbdev/pdbcore/introspect"

	"github.com/suprbdev/pdbr/internal/api"
	"github.com/suprbdev/pdbr/internal/plan"
)

// parseRPC handles /rpc/{fn}: arguments by name from the query string
// (GET/HEAD) or the JSON body (POST), plus the SETOF <table> read surface
// (select, filters, order, limit/offset, count) against the returned
// resource. Body.Object always holds the argument map (possibly empty): an
// absent key is an omitted argument (its DEFAULT applies), a nil value an
// explicit null. A VARIADIC argument takes one element per repetition of
// its query parameter, or a JSON array in the body.
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
			if row := rpc.RowArgs[key]; row != nil {
				return true, p.rowArgText(a, row, args, val)
			}
			t := p.typeOf(api.ArgColumn(p.built.Catalog, a))
			if a.Variadic() {
				v, err := p.coerceText(t.elem(), val)
				if err != nil {
					return false, errf(CodeInvalidFilter, "argument %q: %s", key, err.Detail)
				}
				list, _ := args[key].([]any)
				args[key] = append(list, v)
				return true, nil
			}
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
// {} (every argument omitted).
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
		if row := p.match.RPC.RowArgs[k]; row != nil {
			val, rerr := p.rowArg(a, row, v, strings.HasPrefix(a.PGType, "_"), CodeInvalidBody)
			if rerr != nil {
				return rerr
			}
			args[k] = val
			continue
		}
		t := p.typeOf(api.ArgColumn(p.built.Catalog, a))
		val, cerr := p.coerceJSON(t, v)
		if cerr != nil {
			return errf(CodeInvalidBody, "argument %q: %s", k, cerr.Detail)
		}
		args[k] = val
	}
	return nil
}

// rowArgText parses a row-typed argument from the query string: the JSON
// object (array of objects) as text, or for a VARIADIC argument one JSON
// object per repetition.
func (p *parser) rowArgText(a introspect.FuncArg, res *api.Resource, args map[string]any, text string) error {
	v, ok := decodeJSONArg(text)
	if !ok {
		return errf(CodeInvalidFilter, "argument %q: %q is not valid JSON", a.Name, text)
	}
	if a.Variadic() {
		row, err := p.rowObject(a, res, v, CodeInvalidFilter)
		if err != nil {
			return err
		}
		list, _ := args[a.Name].([]any)
		args[a.Name] = append(list, row)
		return nil
	}
	val, err := p.rowArg(a, res, v, strings.HasPrefix(a.PGType, "_"), CodeInvalidFilter)
	if err != nil {
		return err
	}
	args[a.Name] = val
	return nil
}
