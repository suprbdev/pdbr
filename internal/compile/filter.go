package compile

import (
	"encoding/json"
	"fmt"
	"reflect"
	"sort"
	"strconv"
	"strings"

	"github.com/suprbdev/pdbcore/contrib"
	"github.com/suprbdev/pdbcore/introspect"
	"github.com/suprbdev/pdbcore/postgis"

	"github.com/suprbdev/pdbr/internal/api"
	"github.com/suprbdev/pdbr/internal/plan"
)

// filterSQL compiles a filter tree over the rows at alias into a boolean SQL
// expression, or "" when the tree is empty. Terms of every And/Or are sorted
// by shape before rendering, so equal filter sets produce equal SQL (and
// equal parameter order) whatever the query-string order was.
func (s *stmt) filterSQL(e plan.Expr, alias string) (string, error) {
	return s.exprSQL(e, alias, true)
}

func (s *stmt) exprSQL(e plan.Expr, alias string, top bool) (string, error) {
	switch v := e.(type) {
	case nil:
		return "", nil
	case plan.And:
		return s.groupSQL(v.Terms, alias, " AND ", top)
	case *plan.And:
		if v == nil {
			return "", nil
		}
		return s.groupSQL(v.Terms, alias, " AND ", top)
	case plan.Or:
		return s.groupSQL(v.Terms, alias, " OR ", top)
	case *plan.Or:
		if v == nil {
			return "", nil
		}
		return s.groupSQL(v.Terms, alias, " OR ", top)
	case plan.Not:
		return s.notSQL(v.Term, alias)
	case *plan.Not:
		if v == nil {
			return "", nil
		}
		return s.notSQL(v.Term, alias)
	case plan.Cond:
		return s.condSQL(&v, alias)
	case *plan.Cond:
		if v == nil {
			return "", nil
		}
		return s.condSQL(v, alias)
	}
	return "", fmt.Errorf("compile: unknown filter node %T", e)
}

func (s *stmt) notSQL(term plan.Expr, alias string) (string, error) {
	sub, err := s.exprSQL(term, alias, false)
	if err != nil || sub == "" {
		return "", err
	}
	return "NOT (" + sub + ")", nil
}

func (s *stmt) groupSQL(terms []plan.Expr, alias, op string, top bool) (string, error) {
	sorted := make([]plan.Expr, 0, len(terms))
	for _, t := range terms {
		if t != nil {
			sorted = append(sorted, t)
		}
	}
	sort.SliceStable(sorted, func(i, j int) bool { return shapeKey(sorted[i]) < shapeKey(sorted[j]) })
	var parts []string
	for _, t := range sorted {
		sub, err := s.exprSQL(t, alias, false)
		if err != nil {
			return "", err
		}
		if sub != "" {
			parts = append(parts, sub)
		}
	}
	switch len(parts) {
	case 0:
		return "", nil
	case 1:
		return parts[0], nil
	}
	joined := strings.Join(parts, op)
	if top && op == " AND " {
		return joined, nil
	}
	return "(" + joined + ")", nil
}

// shapeKey is a value-independent sort key for a filter node.
func shapeKey(e plan.Expr) string {
	switch v := e.(type) {
	case plan.And:
		return "and(" + groupKey(v.Terms) + ")"
	case *plan.And:
		if v == nil {
			return ""
		}
		return "and(" + groupKey(v.Terms) + ")"
	case plan.Or:
		return "or(" + groupKey(v.Terms) + ")"
	case *plan.Or:
		if v == nil {
			return ""
		}
		return "or(" + groupKey(v.Terms) + ")"
	case plan.Not:
		return "not(" + shapeKey(v.Term) + ")"
	case *plan.Not:
		if v == nil {
			return ""
		}
		return "not(" + shapeKey(v.Term) + ")"
	case plan.Cond:
		return condKey(&v)
	case *plan.Cond:
		if v == nil {
			return ""
		}
		return condKey(v)
	}
	return "?"
}

func groupKey(terms []plan.Expr) string {
	keys := make([]string, 0, len(terms))
	for _, t := range terms {
		keys = append(keys, shapeKey(t))
	}
	sort.Strings(keys)
	return strings.Join(keys, ",")
}

func condKey(c *plan.Cond) string {
	var b strings.Builder
	switch {
	case c.Relation != nil:
		b.WriteString("rel:")
		for _, st := range c.Relation.Steps {
			if st != nil {
				b.WriteString(st.Key + "/" + boolLit(st.Forward) + ".")
			}
		}
		b.WriteString(string(c.Relation.Quantifier))
		if c.Relation.Exists != nil {
			b.WriteString(":exists=" + boolLit(*c.Relation.Exists))
		}
		if c.Relation.Filter != nil {
			b.WriteString(":" + shapeKey(c.Relation.Filter))
		} else if c.Relation.Leaf != nil {
			b.WriteString(":" + condKey(c.Relation.Leaf))
		}
	case c.Computed != nil:
		b.WriteString("fn:" + c.Computed.Key)
	case c.Pseudo != nil:
		b.WriteString("pseudo:" + c.Pseudo.Key)
	case c.Column != nil:
		b.WriteString("col:" + c.Column.Name + c.Path.Shape())
	}
	b.WriteString("." + string(c.Op))
	if c.Negate {
		b.WriteString(".not")
	}
	if c.Mod != "" {
		b.WriteString("(" + string(c.Mod) + ")")
	}
	if c.Op == plan.OpIs {
		// The is.* operand is structural (it becomes SQL text), so it is
		// part of the shape.
		b.WriteString("." + isOperand(c.Value))
	}
	switch v := c.Value.(type) {
	case contrib.FullTextQuery:
		if v.Config != "" {
			b.WriteString(".cfg")
		}
	case map[string]any:
		b.WriteString(".obj")
	case string:
		if c.Op == plan.OpCs && api.RangeOperand(v) {
			b.WriteString(".rng")
		}
	}
	return b.String()
}

// condSQL renders one leaf predicate.
func (s *stmt) condSQL(c *plan.Cond, alias string) (string, error) {
	switch {
	case c.Relation != nil:
		return s.relationCond(c, alias)
	case c.Computed != nil:
		fn := c.Computed.Function
		t := c.Computed.Resource.Table
		if fn.ReturnsSet {
			return "", errUser(CodeInvalidFilter, "computed column %q is set-returning and cannot be filtered", c.Computed.Key)
		}
		return s.opSQL(fnReturnColumn(fn), s.computedCallSQL(fn, t, alias, nil), c)
	case c.Pseudo != nil:
		expr, err := s.contribPseudoSQL(c.Pseudo, nil, alias)
		if err != nil {
			return "", err
		}
		return s.opSQL(&introspect.Column{Name: c.Pseudo.Key, PGType: "int4", TypeSchema: "pg_catalog"}, "("+expr+")", c)
	case c.Column != nil && len(c.Path) > 0:
		return s.opSQL(s.pathColumn(c.Column, c.Path), s.jsonPathSQL(alias, c.Column, c.Path, false), c)
	case c.Column != nil:
		return s.opSQL(c.Column, colRef(alias, c.Column.Name), c)
	}
	return "", fmt.Errorf("compile: filter condition without column")
}

// opSQL renders `ref <op> $n` for one operator; c.Negate wraps the result.
// not.in and not.is get their idiomatic forms.
func (s *stmt) opSQL(col *introspect.Column, ref string, c *plan.Cond) (string, error) {
	if postgis.IsRaster(col.PGType) && !col.IsArray {
		return s.rasterCondSQL(ref, c)
	}
	spatial := postgis.FlavourOf(col.PGType).Spatial() && !col.IsArray
	if name, ok := spatialOpName(c.Op); ok {
		if !spatial {
			return "", errUser(CodeInvalidFilter, "operator %q applies to spatial columns only", c.Op)
		}
		expr, err := s.spatialOp(postgis.FlavourOf(col.PGType), spatialSRID(col), ref, name, c.Value)
		if err != nil {
			return "", err
		}
		return negate(expr, c.Negate), nil
	}
	if spatial && c.Op != plan.OpIs {
		return "", errUser(CodeInvalidFilter, "operator %q is not valid for spatial columns", c.Op)
	}
	cat := s.catalog()
	if cf, ok := api.ContribFilterFor(cat, col, string(c.Op), c.Value); ok && c.Mod == "" {
		expr, err := s.contribFilterSQL(cf, ref, c.Value)
		if err != nil {
			return "", err
		}
		return negate(expr, c.Negate), nil
	}
	owner, owned := api.OwnerVocabulary(cat, col)
	cmp := func(op string) string {
		if owned {
			return " " + owner.Operator(op) + " "
		}
		return " " + op + " "
	}
	value := func() string {
		if owned {
			return s.ownerParam(owner, s.param(coerceInput(col, c.Value)))
		}
		return s.scalarParam(col, c.Value)
	}
	pattern := func(ph string) string {
		if owned {
			return cmp("~~") + s.ownerParam(owner, ph)
		}
		return " LIKE " + ph
	}
	if c.Mod != "" {
		return s.modSQL(col, ref, c, cmp, owned)
	}
	switch c.Op {
	case plan.OpEq:
		return negate(ref+cmp("=")+value(), c.Negate), nil
	case plan.OpNeq:
		return negate(ref+cmp("<>")+value(), c.Negate), nil
	case plan.OpGt:
		return negate(ref+cmp(">")+value(), c.Negate), nil
	case plan.OpGte:
		return negate(ref+cmp(">=")+value(), c.Negate), nil
	case plan.OpLt:
		return negate(ref+cmp("<")+value(), c.Negate), nil
	case plan.OpLte:
		return negate(ref+cmp("<=")+value(), c.Negate), nil
	case plan.OpIsDistinct:
		if owned {
			// IS DISTINCT FROM cannot name a schema-qualified operator; the
			// operand is never NULL, so <> IS NOT FALSE is equivalent.
			return negate("("+ref+cmp("<>")+value()+") IS NOT FALSE", c.Negate), nil
		}
		return negate(ref+" IS DISTINCT FROM "+value(), c.Negate), nil
	case plan.OpIn:
		p := s.listParam(col, c.Value)
		if c.Negate {
			return ref + cmp("<>") + "ALL(" + p + ")", nil
		}
		return ref + cmp("=") + "ANY(" + p + ")", nil
	case plan.OpIs:
		return isSQL(ref, c.Value, c.Negate)
	case plan.OpLike:
		return negate(ref+pattern(s.param(likePattern(c.Value))), c.Negate), nil
	case plan.OpILike:
		if owned {
			return negate(ref+cmp("~~*")+s.ownerParam(owner, s.param(likePattern(c.Value))), c.Negate), nil
		}
		return negate(ref+" ILIKE "+s.param(likePattern(c.Value)), c.Negate), nil
	case plan.OpMatch:
		return negate(ref+cmp("~")+value(), c.Negate), nil
	case plan.OpIMatch:
		return negate(ref+cmp("~*")+value(), c.Negate), nil
	case plan.OpSw:
		return negate(ref+pattern(s.param(escapeLike(stringOf(c.Value)))+" || '%'"), c.Negate), nil
	case plan.OpEw:
		return negate(ref+pattern("'%' || "+s.param(escapeLike(stringOf(c.Value)))), c.Negate), nil
	case plan.OpCs:
		if isJSON(col) {
			return negate(ref+" @> "+s.jsonParam(c.Value)+"::jsonb", c.Negate), nil
		}
		return negate(ref+" @> "+s.listParam(col, c.Value), c.Negate), nil
	case plan.OpCd:
		if isJSON(col) {
			return negate(ref+" <@ "+s.jsonParam(c.Value)+"::jsonb", c.Negate), nil
		}
		return negate(ref+" <@ "+s.listParam(col, c.Value), c.Negate), nil
	case plan.OpOv:
		return negate(ref+" && "+s.listParam(col, c.Value), c.Negate), nil
	case plan.OpHk:
		return negate(jsonbExpr(col, ref)+" ? "+s.param(stringOf(c.Value)), c.Negate), nil
	case plan.OpJpe:
		return negate(jsonbExpr(col, ref)+" @? "+s.param(stringOf(c.Value))+"::jsonpath", c.Negate), nil
	case plan.OpJpm:
		return negate(jsonbExpr(col, ref)+" @@ "+s.param(stringOf(c.Value))+"::jsonpath", c.Negate), nil
	}
	return "", errUser(CodeInvalidFilter, "unknown filter operator %q", c.Op)
}

// modOps are the SQL operators of the any/all-modifiable operators.
var modOps = map[plan.Op]string{
	plan.OpEq: "=", plan.OpGt: ">", plan.OpGte: ">=", plan.OpLt: "<", plan.OpLte: "<=",
	plan.OpLike: "~~", plan.OpILike: "~~*", plan.OpMatch: "~", plan.OpIMatch: "~*",
}

// modSQL renders an any/all-modified operator, `ref op ANY($n)` /
// `ref op ALL($n)` with the elements as one typed array parameter (cast to
// the column's element type as for in.()); like/ilike patterns get the *
// to % translation per element.
func (s *stmt) modSQL(col *introspect.Column, ref string, c *plan.Cond, cmp func(string) string, owned bool) (string, error) {
	op, ok := modOps[c.Op]
	if !ok {
		return "", errUser(CodeInvalidFilter, "operator %q takes no any/all modifier", c.Op)
	}
	if c.Mod != plan.ModAny && c.Mod != plan.ModAll {
		return "", errUser(CodeInvalidFilter, "unknown operator modifier %q", c.Mod)
	}
	v := c.Value
	if c.Op == plan.OpLike || c.Op == plan.OpILike {
		if list, ok := v.([]any); ok {
			out := make([]any, len(list))
			for i, e := range list {
				out[i] = likePattern(e)
			}
			v = out
		}
	}
	sqlOp := cmp(op)
	switch {
	case owned:
	case c.Op == plan.OpLike:
		sqlOp = " LIKE "
	case c.Op == plan.OpILike:
		sqlOp = " ILIKE "
	}
	return negate(ref+sqlOp+strings.ToUpper(string(c.Mod))+"("+s.listParam(col, v)+")", c.Negate), nil
}

// catalog is the catalog the contrib vocabularies resolve extensions from
// (nil without a built API: core vocabularies only).
func (s *stmt) catalog() *introspect.Catalog {
	if s.c.Built == nil {
		return nil
	}
	return s.c.Built.Catalog
}

// ownerParam casts an operand expression to the extension-owned type of
// owner (citext, ltree through their codec; hstore literal text directly).
func (s *stmt) ownerParam(owner contrib.Vocabulary, expr string) string {
	if owner.Codec != nil && owner.Codec.Arg == contrib.ArgText {
		return owner.Codec.SQL(expr)
	}
	if strings.Trim(strings.TrimPrefix(expr, "$"), "0123456789") != "" {
		expr = "(" + expr + ")"
	}
	return expr + "::" + typeRef(owner.Schema, owner.Type)
}

// keyEqSQL renders ref = placeholder for a key or filter column, casting
// the placeholder to an extension-owned column type.
func (s *stmt) keyEqSQL(col *introspect.Column, ref, ph string) string {
	if owner := s.owner(col); owner != nil {
		return ref + " " + owner.Operator("=") + " " + s.ownerParam(*owner, ph)
	}
	return ref + " = " + ph
}

// contribFilterSQL renders a pdbcore/contrib operator: the operand goes
// through Op.Params into bind parameters and Op.Render builds the predicate
// from their placeholders. An hstore operand given as hstore literal text
// is cast to the column type instead; an unaccent_ilike pattern gets the
// like/ilike wildcard translation first.
func (s *stmt) contribFilterSQL(cf api.ContribFilter, ref string, v any) (string, error) {
	if cf.Vocabulary.Name == contrib.VocabUnaccent && cf.Op.Name == "unaccentLike" {
		v = likePattern(v)
	}
	if lit, ok := v.(string); ok && cf.Op.Arg == contrib.ArgHstore {
		op := "@>"
		if cf.Op.Name == "containedBy" {
			op = "<@"
		}
		return "(" + ref + ") " + cf.Vocabulary.Operator(op) + " " + s.ownerParam(cf.Vocabulary, s.param(lit)), nil
	}
	switch cf.Op.Arg {
	case contrib.ArgVectorDistance, contrib.ArgCubeDistance:
		if s.plan.Vec == nil {
			return "", errUser(CodeInvalidParameter, "filter %s requires ~vec=", cf.Name)
		}
		key := "vector"
		if cf.Op.Arg == contrib.ArgCubeDistance {
			key = "cube"
		}
		v = map[string]any{key: s.plan.Vec, "distance": v}
	case contrib.ArgLatLonDistance:
		ll, ok := s.plan.LatLon.(map[string]any)
		if !ok {
			return "", errUser(CodeInvalidParameter, "filter %s requires ~latlon=", cf.Name)
		}
		v = map[string]any{"lat": ll["lat"], "lon": ll["lon"], "distance": v}
	}
	if cf.Op.Arg == contrib.ArgTextDistance {
		if s.plan.Levenshtein == nil {
			return "", errUser(CodeInvalidParameter, "filter %s requires ~levenshtein=", cf.Name)
		}
		v = map[string]any{"text": s.plan.Levenshtein, "distance": v}
	}
	params, err := cf.Op.Params(v)
	if err != nil {
		return "", errUser(CodeInvalidFilter, "%s: %s", cf.Name, err.Error())
	}
	expr := cf.Op.Render(ref, s.params(params)...)
	if expr == "" {
		return "", fmt.Errorf("compile: contrib operator %s rendered no SQL", cf.Name)
	}
	return expr, nil
}

// params adds every value and returns the placeholders in order.
func (s *stmt) params(vals []any) []string {
	out := make([]string, len(vals))
	for i, v := range vals {
		out[i] = s.param(v)
	}
	return out
}

func negate(expr string, neg bool) string {
	if neg {
		return "NOT (" + expr + ")"
	}
	return expr
}

// isOperand normalizes the is.* operand: nil / "null" -> null, true/false.
func isOperand(v any) string {
	switch x := v.(type) {
	case nil:
		return "null"
	case bool:
		return boolLit(x)
	case string:
		switch strings.ToLower(x) {
		case "null", "true", "false", "unknown":
			return strings.ToLower(x)
		}
	}
	return "?"
}

// isSQL renders the structural is.* forms; the operand becomes SQL text, so
// it is validated here rather than parameterized.
func isSQL(ref string, v any, neg bool) (string, error) {
	kw := ""
	switch isOperand(v) {
	case "null":
		kw = "NULL"
	case "true":
		kw = "TRUE"
	case "false":
		kw = "FALSE"
	case "unknown":
		kw = "UNKNOWN"
	default:
		return "", errUser(CodeInvalidFilter, "is accepts null, true or false")
	}
	if neg {
		return ref + " IS NOT " + kw, nil
	}
	return ref + " IS " + kw, nil
}

func isJSON(col *introspect.Column) bool {
	base := strings.TrimPrefix(col.PGType, "_")
	return !col.IsArray && (base == "json" || base == "jsonb")
}

// jsonbExpr renders a column reference as jsonb; json columns are cast
// because the jsonpath and key operators are defined only for jsonb.
func jsonbExpr(col *introspect.Column, ref string) string {
	if strings.TrimPrefix(col.PGType, "_") == "json" {
		return ref + "::jsonb"
	}
	return ref
}

// likePattern applies the URL wildcard translation: `*` becomes `%`; a
// literal `%` passes through.
func likePattern(v any) any {
	if str, ok := v.(string); ok {
		return strings.ReplaceAll(str, "*", "%")
	}
	return v
}

func escapeLike(v string) string {
	v = strings.ReplaceAll(v, `\`, `\\`)
	v = strings.ReplaceAll(v, `%`, `\%`)
	return strings.ReplaceAll(v, `_`, `\_`)
}

func stringOf(v any) string {
	switch x := v.(type) {
	case string:
		return x
	case json.RawMessage:
		return string(x)
	case []byte:
		return string(x)
	case nil:
		return ""
	case fmt.Stringer:
		return x.String()
	}
	return fmt.Sprint(v)
}

// scalarParam adds one coerced scalar parameter for col.
func (s *stmt) scalarParam(col *introspect.Column, v any) string {
	if col.IsArray {
		return s.listParam(col, v)
	}
	return s.param(coerceInput(col, v))
}

// jsonParam adds a json/jsonb operand: raw JSON text (string / RawMessage)
// passes through, anything else is marshalled.
func (s *stmt) jsonParam(v any) string { return s.param(jsonText(v)) }

// jsonText renders a JSON value as text: strings and raw bytes are taken
// as JSON text already, anything else is marshalled.
func jsonText(v any) string {
	switch x := v.(type) {
	case string:
		return x
	case json.RawMessage:
		return string(x)
	case []byte:
		return string(x)
	}
	return toJSONParam(v)
}

// listParam adds a list-valued operand (in, array ops) as one typed array
// parameter: `$n::text[]`, or `$n::text[]::"s"."enum"[]` for enum,
// extension (citext, hstore, ltree), range and multirange element types
// (PostgreSQL would otherwise describe the parameter with an array OID the
// driver has no encode plan for). The cast depends only on the column type,
// never on the value.
func (s *stmt) listParam(col *introspect.Column, v any) string {
	return s.param(coerceList(col, v)) + arrayCast(s.c.Built, col)
}

// arrayCast renders the array type cast for col's element type.
func arrayCast(built *api.Built, col *introspect.Column) string {
	base := strings.TrimPrefix(col.PGType, "_")
	if col.EnumRef != "" {
		return "::text[]"
	}
	if cast, ok := domainArrayCast(built, col); ok {
		return cast
	}
	if built != nil && built.Catalog != nil && built.Catalog.Enum(baseSchema(col), base) != nil {
		return "::text[]::" + quoteIdent(baseSchema(col)) + "." + quoteIdent(base) + "[]"
	}
	_, isRange := contrib.Range(base)
	_, isMulti := contrib.Multirange(base)
	if isRange || isMulti {
		return "::text[]::" + quoteIdent(base) + "[]"
	}
	elem := *col
	elem.IsArray, elem.PGType = false, base
	var cat *introspect.Catalog
	if built != nil {
		cat = built.Catalog
	}
	if owner, ok := api.OwnerVocabulary(cat, &elem); ok {
		return "::text[]::" + typeRef(owner.Schema, owner.Type) + "[]"
	}
	if schema := baseSchema(col); schema == "" || schema == "pg_catalog" {
		return "::" + quoteIdent(base) + "[]"
	}
	return "::text[]::" + quoteIdent(baseSchema(col)) + "." + quoteIdent(base) + "[]"
}

// domainArrayCast renders the cast for a list value of a column whose
// element type is a domain over a built-in type (the base type's array,
// then the domain's array type), or of a domain over a built-in array type
// (the base array type, then the domain); ok=false for any other column.
func domainArrayCast(built *api.Built, col *introspect.Column) (string, bool) {
	if built == nil || built.Catalog == nil {
		return "", false
	}
	d := built.Catalog.DomainByRef(col.Domain)
	if d == nil || d.BaseTypeSchema != "pg_catalog" {
		return "", false
	}
	domain := quoteIdent(d.Schema) + "." + quoteIdent(d.Name)
	if d.IsArrayBase {
		return "::" + quoteIdent(strings.TrimPrefix(d.BaseType, "_")) + "[]::" + domain, true
	}
	return "::" + quoteIdent(d.BaseType) + "[]::" + domain + "[]", true
}

// baseSchema is the schema of col's PGType: BaseTypeSchema, which differs
// from TypeSchema for a domain (or an array of one) defined in another
// schema than its base type, or TypeSchema when it is unknown.
func baseSchema(col *introspect.Column) string {
	if col.BaseTypeSchema != "" {
		return col.BaseTypeSchema
	}
	return col.TypeSchema
}

// coerceList coerces a list operand element-wise. A string is taken as a
// PostgreSQL array literal ({a,b}) and passed through for the server to
// parse; typed slices are widened to []any.
func coerceList(col *introspect.Column, v any) any {
	elem := *col
	elem.IsArray = false
	switch x := v.(type) {
	case nil:
		return nil
	case string, json.RawMessage, []byte:
		return stringOf(x)
	case []any:
		out := make([]any, len(x))
		for i, item := range x {
			out[i] = coerceInput(&elem, item)
		}
		return out
	}
	rv := reflect.ValueOf(v)
	if rv.Kind() == reflect.Slice {
		out := make([]any, rv.Len())
		for i := range out {
			out[i] = coerceInput(&elem, rv.Index(i).Interface())
		}
		return out
	}
	return []any{coerceInput(&elem, v)}
}

// coerceInput converts an input value into a driver-friendly parameter for
// col: decoded json/jsonb values are re-encoded as JSON text (strings are
// taken as JSON text already), array elements coerce recursively,
// everything else (enum labels verbatim, int8/numeric strings, ISO
// timestamps) passes through.
func coerceInput(col *introspect.Column, v any) any {
	if col == nil || v == nil {
		return v
	}
	if n, ok := v.(json.Number); ok {
		// Cursor values decode as json.Number; the driver text-encodes a
		// plain string for any numeric type.
		return n.String()
	}
	base := strings.TrimPrefix(col.PGType, "_")
	if col.IsArray {
		if list, ok := v.([]any); ok {
			elem := *col
			elem.IsArray = false
			out := make([]any, len(list))
			for i, item := range list {
				out[i] = coerceInput(&elem, item)
			}
			return out
		}
		return v
	}
	switch base {
	case "json", "jsonb":
		// A string is already JSON text (parse hands jsonb operands and
		// body values through as text); decoded values are marshalled.
		switch x := v.(type) {
		case string:
			return x
		case json.RawMessage:
			return string(x)
		case []byte:
			return string(x)
		}
		return toJSONParam(v)
	}
	return v
}

// toJSONParam renders any value as JSON text.
func toJSONParam(v any) string {
	b, err := json.Marshal(v)
	if err != nil {
		return "null"
	}
	return string(b)
}

// ---- relation filters ----

// relationCond compiles a relation filter (advanced_filters): (NOT) EXISTS
// over the related table joined to the current row. Quantifiers apply to
// to-many relations (api.Relation.ToMany): some = EXISTS(match), none =
// NOT EXISTS(match), every = NOT EXISTS(NOT match). exists.true/false take
// no leaf.
func (s *stmt) relationCond(c *plan.Cond, alias string) (string, error) {
	rp := c.Relation
	if len(rp.Steps) == 0 || rp.Steps[0] == nil {
		return "", fmt.Errorf("compile: relation filter without steps")
	}
	quant := rp.Quantifier
	if quant == "" || quant == plan.Exists {
		quant = plan.Some
	}
	if !rp.Steps[0].ToMany() && quant != plan.Some {
		return "", errUser(CodeInvalidFilter, "quantifiers apply to to-many relations only")
	}
	if rp.Exists != nil && !*rp.Exists {
		quant = plan.None
	}
	expr, err := s.relationExists(rp.Steps, rp, alias, quant)
	if err != nil {
		return "", err
	}
	if c.Negate {
		return "NOT (" + expr + ")", nil
	}
	return expr, nil
}

// relationExists renders the EXISTS chain for steps; the leaf predicate
// applies to the innermost step's rows.
func (s *stmt) relationExists(steps []*api.Relation, rp *plan.RelationPath, alias string, quant plan.Quantifier) (string, error) {
	rel := steps[0]
	sub := s.alias("r")
	where := s.joinConds(rel, sub, alias)
	var cond string
	var err error
	if len(steps) > 1 {
		cond, err = s.relationExists(steps[1:], rp, sub, plan.Some)
	} else {
		cond, err = s.relationLeaf(rp, sub)
	}
	if err != nil {
		return "", err
	}
	if quant == plan.Every {
		if cond == "" {
			return "true", nil // every row trivially matches an empty filter
		}
		where += " AND NOT (" + cond + ")"
	} else if cond != "" {
		where += " AND (" + cond + ")"
	}
	prefix := "EXISTS"
	if quant == plan.None || quant == plan.Every {
		prefix = "NOT EXISTS"
	}
	return fmt.Sprintf("%s (SELECT 1 FROM %s %s WHERE %s)", prefix, s.sourceRef(rel.To.Table, sub), sub, where), nil
}

// relationLeaf renders the predicate over the related table: the full
// Filter when present, else the single Leaf condition.
func (s *stmt) relationLeaf(rp *plan.RelationPath, alias string) (string, error) {
	if rp.Filter != nil {
		return s.exprSQL(rp.Filter, alias, false)
	}
	if rp.Leaf != nil {
		return s.condSQL(rp.Leaf, alias)
	}
	return "", nil
}

// ---- spatial ----

// spatialOpName maps a URL operator (st_intersects, bbox_contains,
// st_dwithin, …) to the pdbcore operator name.
func spatialOpName(op plan.Op) (string, bool) {
	if name, ok := plan.SpatialOps[op]; ok {
		return name, true
	}
	name := strings.ToLower(string(op))
	if !strings.HasPrefix(name, "st_") && !strings.HasPrefix(name, "bbox_") {
		return "", false
	}
	name = strings.ReplaceAll(strings.TrimPrefix(name, "st_"), "_", "")
	for _, o := range postgis.Ops {
		if o.Kind() != postgis.ArgBoolean && strings.ToLower(o.Name) == name {
			return o.Name, true
		}
	}
	return "", false
}

// spatialOp compiles one spatial operator. Every operand — geometry,
// bounding-box corner, distance — travels as a bind parameter; the SQL text
// depends only on the operator and the column's flavour and typmod SRID.
func (s *stmt) spatialOp(f postgis.Flavour, srid int, ref, name string, val any) (string, error) {
	spec, _ := postgis.OpByName(name)
	// PostGIS defines only some predicates for geography (&&, ST_Intersects,
	// ST_Covers, ST_CoveredBy, ST_DWithin); the rest resolve on geometry only,
	// so both sides are cast (planar semantics, same as pdbq).
	render := f
	if f == postgis.Geography && !spec.Geography {
		render = postgis.Geometry
		ref += "::geometry"
	}
	switch spec.Kind() {
	case postgis.ArgBBox:
		box, err := s.bboxExpr(render, val)
		if err != nil {
			return "", err
		}
		return spec.Render(ref, box), nil
	case postgis.ArgDWithin:
		geomVal, distVal, spheroid, err := dwithinOperand(val)
		if err != nil {
			return "", err
		}
		geom, err := s.spatialOperandAs(f, render, srid, geomVal)
		if err != nil {
			return "", err
		}
		dist, err := toFloat(distVal)
		if err != nil || dist < 0 {
			return "", errUser(CodeInvalidFilter, "%s distance must be a number >= 0", name)
		}
		// Geometry columns measure in SRID units; the spheroid flag asks for
		// metres by casting both sides to geography.
		if spheroid && f == postgis.Geometry {
			return spec.Render(ref+"::geography", "("+geom+")::geography", s.param(dist)), nil
		}
		return spec.Render(ref, geom, s.param(dist)), nil
	default:
		geom, err := s.spatialOperandAs(f, render, srid, val)
		if err != nil {
			return "", err
		}
		return spec.Render(ref, geom), nil
	}
}

// dwithinOperand accepts a plan.DWithin, a (geom, dist[, spheroid]) list or
// an object with geometry / distance / spheroid members.
func dwithinOperand(val any) (geom, dist any, spheroid bool, err error) {
	switch x := val.(type) {
	case plan.DWithin:
		return x.Geom, x.Distance, x.Spheroid != nil && *x.Spheroid, nil
	case *plan.DWithin:
		if x == nil {
			break
		}
		return x.Geom, x.Distance, x.Spheroid != nil && *x.Spheroid, nil
	case []any:
		if len(x) < 2 || len(x) > 3 {
			return nil, nil, false, errUser(CodeInvalidFilter, "st_dwithin takes (geometry, distance[, spheroid])")
		}
		sp := false
		if len(x) == 3 {
			sp, err = toBool(x[2])
			if err != nil {
				return nil, nil, false, errUser(CodeInvalidFilter, "st_dwithin spheroid must be true or false")
			}
		}
		return x[0], x[1], sp, nil
	case map[string]any:
		g, ok := x["geometry"]
		if !ok || g == nil {
			return nil, nil, false, errUser(CodeInvalidFilter, "st_dwithin requires a geometry")
		}
		d, ok := x["distance"]
		if !ok || d == nil {
			return nil, nil, false, errUser(CodeInvalidFilter, "st_dwithin requires a distance")
		}
		sp := false
		for _, k := range []string{"spheroid", "useSpheroid", "use_spheroid"} {
			if v, ok := x[k]; ok && v != nil {
				sp, err = toBool(v)
				if err != nil {
					return nil, nil, false, errUser(CodeInvalidFilter, "st_dwithin spheroid must be true or false")
				}
			}
		}
		return g, d, sp, nil
	}
	return nil, nil, false, errUser(CodeInvalidFilter, "st_dwithin takes (geometry, distance[, spheroid])")
}

// spatialOperand renders a geometry operand — an already parsed
// postgis.Value, or raw GeoJSON / WKT input — as a parameterized PostGIS
// constructor expression for a column of flavour f and typmod SRID srid.
func (s *stmt) spatialOperand(f postgis.Flavour, srid int, v any) (string, error) {
	return s.spatialOperandAs(f, f, srid, v)
}

// spatialOperandAs parses the operand for a column of flavour f and typmod
// SRID srid (which fix SRID defaults: geography input is tagged 4326, bare
// WKT gets the column's SRID, GeoJSON is reprojected to it) but casts the
// expression to render, so a geography column compared through a
// geometry-only predicate gets a geometry operand with the same SRID. An
// already parsed postgis.Value keeps the SRID handling it was parsed with.
func (s *stmt) spatialOperandAs(f, render postgis.Flavour, srid int, v any) (string, error) {
	switch x := v.(type) {
	case postgis.Value:
		x.Flavour = render
		return x.SQL(s.param(x.Param)), nil
	case *postgis.Value:
		if x != nil {
			val := *x
			val.Flavour = render
			return val.SQL(s.param(val.Param)), nil
		}
	case json.RawMessage:
		var decoded any
		if err := json.Unmarshal(x, &decoded); err == nil {
			v = decoded
		} else {
			v = string(x)
		}
	}
	val, err := postgis.ParseFor(v, f, srid)
	if err != nil {
		return "", errUser(CodeInvalidFilter, "%s", err.Error())
	}
	val.Flavour = render
	return val.SQL(s.param(val.Param)), nil
}

// bboxExpr renders a plan.BBox or (minx,miny,maxx,maxy[,srid]) as
// ST_MakeEnvelope over four bind parameters. The SRID is structural (an
// integer literal in the SQL text) exactly like a page size, so the four
// coordinates remain the only value-carrying parts.
func (s *stmt) bboxExpr(f postgis.Flavour, v any) (string, error) {
	var raw []any
	switch x := v.(type) {
	case plan.BBox:
		raw = []any{x.MinX, x.MinY, x.MaxX, x.MaxY, x.SRID}
	case *plan.BBox:
		if x == nil {
			return "", errUser(CodeInvalidFilter, "bounding box takes (minx, miny, maxx, maxy[, srid])")
		}
		raw = []any{x.MinX, x.MinY, x.MaxX, x.MaxY, x.SRID}
	case []any:
		raw = x
	case map[string]any:
		for _, k := range []string{"minX", "minY", "maxX", "maxY"} {
			val, ok := x[k]
			if !ok {
				val, ok = x[strings.ToLower(k)]
			}
			if !ok || val == nil {
				return "", errUser(CodeInvalidFilter, "bounding box requires %s", k)
			}
			raw = append(raw, val)
		}
		if srid, ok := x["srid"]; ok && srid != nil {
			raw = append(raw, srid)
		}
	default:
		return "", errUser(CodeInvalidFilter, "bounding box takes (minx, miny, maxx, maxy[, srid])")
	}
	if len(raw) < 4 || len(raw) > 5 {
		return "", errUser(CodeInvalidFilter, "bounding box takes (minx, miny, maxx, maxy[, srid])")
	}
	coords := make([]string, 4)
	for i := range 4 {
		n, err := toFloat(raw[i])
		if err != nil {
			return "", errUser(CodeInvalidFilter, "bounding box coordinates must be numbers")
		}
		coords[i] = s.param(n)
	}
	srid := 0
	if len(raw) == 5 {
		n, err := toFloat(raw[4])
		if err != nil || n != float64(int(n)) || n < 0 {
			return "", errUser(CodeInvalidFilter, "bounding box srid must be an integer")
		}
		srid = int(n)
	}
	return postgis.BBoxSQL(coords[0], coords[1], coords[2], coords[3], srid, f), nil
}

// spatialSRID is the typmod SRID of a spatial column, 0 when unconstrained.
func spatialSRID(col *introspect.Column) int {
	if col == nil || col.Spatial == nil {
		return 0
	}
	return col.Spatial.SRID
}

// toFloat normalizes the numeric shapes a decoded value can arrive as.
func toFloat(v any) (float64, error) {
	switch n := v.(type) {
	case float64:
		return n, nil
	case float32:
		return float64(n), nil
	case int:
		return float64(n), nil
	case int32:
		return float64(n), nil
	case int64:
		return float64(n), nil
	case json.Number:
		return n.Float64()
	case string:
		f, err := strconv.ParseFloat(strings.TrimSpace(n), 64)
		if err != nil {
			return 0, fmt.Errorf("not a number")
		}
		return f, nil
	}
	return 0, fmt.Errorf("not a number")
}

func toBool(v any) (bool, error) {
	switch b := v.(type) {
	case bool:
		return b, nil
	case string:
		return strconv.ParseBool(strings.TrimSpace(b))
	}
	return false, fmt.Errorf("not a boolean")
}
