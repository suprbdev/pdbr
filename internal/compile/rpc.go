package compile

import (
	"encoding/json"
	"errors"
	"fmt"
	"strconv"
	"strings"

	"github.com/suprbdev/pdbcore/introspect"
	"github.com/suprbdev/pdbcore/postgis"

	"github.com/suprbdev/pdbr/internal/api"
	"github.com/suprbdev/pdbr/internal/plan"
)

// rpc compiles /rpc/{fn} requests. Arguments are passed by name
// (introspect.Function.CallSQL) as `$n::type` in declaration order: an
// absent argument is left out so PostgreSQL applies its DEFAULT (absent
// without a default → 400 missing_argument), an explicit null is
// `NULL::type`, and a variadic function is called positionally with
// `VARIADIC <array>`. Then the return kind picks the shape: scalar /
// composite values are materialized in a subquery and rendered like column
// values; void yields a constant (exec answers 204); a single table row is projected over `FROM fn(...) AS t`
// with embeds; SETOF <table> reuses the list compiler with the function call
// as the root row source (offset paging only); SETOF <scalar|composite>
// aggregates the function output into an array. Minted (auth.mint_type) functions
// keep their composite raw so the claims stay JSON numbers.
func (s *stmt) rpc() (*Statement, error) {
	rpc := s.plan.RPC
	fn := rpc.Function
	args := map[string]any{}
	if s.plan.Body != nil && s.plan.Body.Object != nil {
		args = s.plan.Body.Object
	}
	call, err := s.rpcCall(fn, args)
	if err != nil {
		return nil, err
	}
	var st *Statement
	switch rpc.Returns {
	case api.ReturnVoid:
		st = &Statement{SQL: fmt.Sprintf("SELECT 'null'::json AS data FROM (SELECT %s) AS __v", call), Shape: ShapeVoid}
	case api.ReturnRow:
		st, err = s.rpcRow(rpc, call)
	case api.ReturnSetOfRows:
		st, err = s.rpcSetOfRows(rpc, call)
	case api.ReturnSetOfScalar, api.ReturnSetOfComposite:
		st, err = s.rpcSetOfValues(rpc, call)
	default:
		st, err = s.rpcValue(rpc, call)
	}
	if err != nil {
		return nil, err
	}
	st.Args = s.ps.Args()
	st.Mutation = rpc.Mutation
	return st, nil
}

// rpcCall renders the function call passing exactly the arguments present
// in args, each as a typed parameter (nil as a typed NULL).
func (s *stmt) rpcCall(fn *introspect.Function, args map[string]any) (string, error) {
	call, err := fn.CallSQL(func(a introspect.FuncArg) (string, bool, error) {
		v, ok := args[a.Name]
		if !ok {
			return "", false, nil
		}
		if v == nil {
			return "NULL::" + argTypeSQL(a, s.c.Built), true, nil
		}
		p, err := s.argParam(a, v)
		return p, true, err
	})
	var missing *introspect.MissingArgsError
	if errors.As(err, &missing) {
		return "", errUser(CodeMissingArgument, "function %q: missing required argument(s): %s", fn.Name, strings.Join(missing.Args, ", "))
	}
	return call, err
}

// argColumn views a function argument as a column so the parameter helpers
// (typed array casts, json text, spatial constructors) apply unchanged; a
// domain argument takes its base type (api.ArgColumn).
func argColumn(built *api.Built, a introspect.FuncArg) *introspect.Column {
	var cat *introspect.Catalog
	if built != nil {
		cat = built.Catalog
	}
	return api.ArgColumn(cat, a)
}

// argTypeSQL renders the SQL type name of an argument: bare for pg_catalog
// types, schema-qualified otherwise; arrays as element[].
func argTypeSQL(a introspect.FuncArg, built *api.Built) string {
	col := argColumn(built, a)
	if col.IsArray {
		cast := arrayCast(built, col)
		return cast[strings.LastIndex(cast, "::")+2:]
	}
	return typeRef(a.TypeSchema, a.PGType)
}

func typeRef(schema, name string) string {
	if schema == "" || schema == "pg_catalog" {
		return quoteIdent(name)
	}
	return quoteIdent(schema) + "." + quoteIdent(name)
}

// argParam binds one argument value as a typed parameter expression.
func (s *stmt) argParam(a introspect.FuncArg, v any) (string, error) {
	col := argColumn(s.c.Built, a)
	base := strings.TrimPrefix(col.PGType, "_")
	switch {
	case s.plan.RPC != nil && s.plan.RPC.RowArgs[a.Name] != nil:
		return s.populateSQL(typeRef(a.TypeSchema, base), col.IsArray, toJSONParam(v)), nil
	case s.compositeFor(col) != nil:
		if col.IsArray {
			return s.populateSQL(typeRef(a.TypeSchema, base), true, jsonArrayText(v)), nil
		}
		return s.populateSQL(typeRef(a.TypeSchema, base), false, jsonText(v)), nil
	case postgis.IsSpatial(base) && !col.IsArray:
		expr, err := s.spatialOperand(postgis.FlavourOf(base), 0, v)
		if err != nil {
			return "", err
		}
		return expr, nil
	case col.IsArray:
		return s.listParam(col, v), nil
	}
	return s.param(coerceInput(col, v)) + "::" + argTypeSQL(a, s.c.Built), nil
}

// jsonArrayText joins per-element JSON texts (or an already-encoded array)
// into one JSON array document.
func jsonArrayText(v any) string {
	switch x := v.(type) {
	case string:
		return x
	case json.RawMessage:
		return string(x)
	case []any:
		parts := make([]string, len(x))
		for i, item := range x {
			switch e := item.(type) {
			case nil:
				parts[i] = "null"
			case string:
				parts[i] = e
			case json.RawMessage:
				parts[i] = string(e)
			default:
				parts[i] = toJSONParam(e)
			}
		}
		return "[" + strings.Join(parts, ",") + "]"
	}
	return toJSONParam(v)
}

// valueJSON renders an already-materialized function result (a stable
// reference such as __fn.v) as json with the column representation rules
// (int8 as text, composites expanded, nested rows projected to their
// resource's readable columns, spatial as GeoJSON).
func (s *stmt) valueJSON(ref string, fn *introspect.Function, depth int) (string, error) {
	if rc := s.structuredReturn(fn); rc != nil {
		return s.typedJSON(ref, rc, depth-1)
	}
	ret := fnReturnColumn(fn)
	if ret.IsArray {
		if postgis.IsSpatial(ret.PGType) {
			return s.spatialArrayJSON(ref), nil
		}
		return "to_json(" + ref + ")", nil
	}
	if postgis.IsSpatial(ret.PGType) {
		return postgis.OutputSQL(ref, postgis.Output{JSON: true}), nil
	}
	return "to_json(" + s.exprJSON(ref, ret) + ")", nil
}

// rpcValue compiles scalar and composite returns.
func (s *stmt) rpcValue(rpc *api.RPC, call string) (*Statement, error) {
	expr := "to_jsonb(__fn.v)"
	shape := ShapeScalar
	if rpc.Mint {
		shape = ShapeJWT
	} else {
		var err error
		if expr, err = s.valueJSON("__fn.v", rpc.Function, 0); err != nil {
			return nil, err
		}
	}
	return &Statement{SQL: fmt.Sprintf("SELECT %s AS data\nFROM (SELECT %s AS v) AS __fn", expr, call), Shape: shape}, nil
}

// rpcSetOfValues compiles SETOF <scalar> / SETOF <composite> (or a domain
// over a composite or row type, whose values a FROM item also expands into
// columns, so the whole row is read): the function output, bounded by
// limit/offset (max_page_size when absent), aggregated in output order
// into a JSON array. A volatile function is evaluated once in a materialized
// CTE; limit=0 then returns [] after reading it for its side effects.
func (s *stmt) rpcSetOfValues(rpc *api.RPC, call string) (*Statement, error) {
	fn := rpc.Function
	limit, err := s.embedLimit(s.plan.Page)
	if err != nil {
		return nil, err
	}
	if s.plan.Page.Offset < 0 {
		return nil, errUser(CodeInvalidParameter, "offset must be >= 0")
	}
	once := rpc.Mutation
	if once && limit == 0 {
		cte := s.callCTE(valueSource(rpc, s.structuredReturn(fn), call))
		return &Statement{SQL: cte + "SELECT '[]'::json AS data\nFROM (SELECT count(*) FROM " + callCTEName + ") AS __n", Shape: setShape(rpc)}, nil
	}
	pageSQL := " LIMIT " + strconv.Itoa(limit)
	if s.plan.Page.Offset > 0 {
		pageSQL += " OFFSET " + s.param(s.plan.Page.Offset)
	}
	src := valueSource(rpc, s.structuredReturn(fn), call)
	prefix := ""
	if once {
		prefix = s.callCTE(src)
		src = "SELECT v FROM " + callCTEName
	}
	expr := "to_jsonb(__s.v)"
	if !rpc.Mint {
		if expr, err = s.valueJSON("__s.v", fn, 0); err != nil {
			return nil, err
		}
	}
	return &Statement{SQL: fmt.Sprintf("%sSELECT coalesce(json_agg(%s), '[]'::json) AS data\nFROM (%s%s) AS __s", prefix, expr, src, pageSQL), Shape: setShape(rpc)}, nil
}

// callCTEName names the CTE that holds a volatile function's output.
const callCTEName = "__call"

// callCTE renders the leading WITH clause that evaluates a volatile
// set-returning function exactly once; every later read of the output
// (page, look-ahead row, total) scans this CTE instead of calling the
// function again.
func (s *stmt) callCTE(query string) string {
	return "WITH " + callCTEName + " AS MATERIALIZED (" + query + ")\n"
}

// valueSource renders the query yielding one column v per output value of a
// SETOF <scalar|composite> call.
func valueSource(rpc *api.RPC, structured *introspect.Column, call string) string {
	if rpc.Mint || rpc.Returns == api.ReturnSetOfComposite || structured != nil && !strings.HasPrefix(rpc.Function.ReturnType, "_") {
		return fmt.Sprintf("SELECT __v AS v FROM %s AS __v", call)
	}
	return fmt.Sprintf("SELECT __v.v FROM %s AS __v(v)", call)
}

func setShape(rpc *api.RPC) Shape {
	if rpc.Mint {
		return ShapeJWTSet
	}
	return ShapeScalar
}

// rpcNode returns the projection node for a table-returning function.
func (s *stmt) rpcNode(r *api.Resource) *plan.Node {
	if s.plan.Select != nil {
		n := *s.plan.Select
		if n.Resource == nil {
			n.Resource = r
		}
		return &n
	}
	return &plan.Node{Resource: r, Star: true}
}

// rpcRow compiles a single table-row return: the projection (with embeds)
// over the call as a row source. A strict function returning NULL yields one
// all-null row, which the CASE surfaces as JSON null (200, body null). A
// record of output parameters is always an object, all-null columns
// included.
func (s *stmt) rpcRow(rpc *api.RPC, call string) (*Statement, error) {
	node := s.rpcNode(rpc.Resource)
	alias := s.alias("t")
	jsonExpr, laterals, err := s.rootJSON(node, alias)
	if err != nil {
		return nil, err
	}
	where, err := s.whereClause(node, s.plan.Filter, alias, nil)
	if err != nil {
		return nil, err
	}
	if !rpc.Record {
		jsonExpr = fmt.Sprintf("CASE WHEN %s IS NULL THEN NULL ELSE %s END", alias, jsonExpr)
	}
	sql := fmt.Sprintf("SELECT %s AS data\nFROM %s AS %s%s%s", jsonExpr, call, alias, joinLaterals(laterals), where)
	return &Statement{SQL: sql, Shape: ShapeScalar}, nil
}

// rpcSetOfRows compiles SETOF <table>: the list compiler over the function
// call as the root row source. Cursors are off (function output has no
// stable keyset), so the page carries offset links only; the count subquery
// re-renders the same call, reusing the argument parameters. A volatile
// function is evaluated once in a materialized CTE that the page and the
// count both read.
func (s *stmt) rpcSetOfRows(rpc *api.RPC, call string) (*Statement, error) {
	p := *s.plan
	p.Resource = rpc.Resource
	s.plan = &p
	s.rootSource = call
	if !rpc.Mutation {
		return s.list()
	}
	columns := s.callCTE("SELECT * FROM " + call)
	s.rootSource = callCTEName
	st, err := s.list()
	if err != nil {
		return nil, err
	}
	st.SQL = columns + st.SQL
	return st, nil
}
