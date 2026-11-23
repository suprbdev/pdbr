package compile

import (
	"errors"
	"fmt"
	"slices"
	"strconv"
	"strings"

	"github.com/suprbdev/pdbcore/introspect"
	"github.com/suprbdev/pdbcore/postgis"

	"github.com/suprbdev/pdbr/internal/api"
	"github.com/suprbdev/pdbr/internal/plan"
)

// jsonBuildObjectChunk keeps each json_build_object call under PostgreSQL's
// 100-argument limit; wider selections splice the chunks' text together.
const jsonBuildObjectChunk = 40 // pairs (80 args)

// pairList collects the members of a response object in order. Two
// members answering under the same key are a user error unless they are
// the same expression (a column selected twice, or named again after *):
// replacing one would silently drop its value, and with it any bind
// parameter its SQL holds. Spread members join their parent's list under
// the same rule.
type pairList struct {
	keys   []string // response keys
	keySQL []string // rendered keys: literals, or bind parameters for request-supplied keys
	vals   []string
	pos    map[string]int
}

func newPairList() *pairList { return &pairList{pos: map[string]int{}} }

// add appends a member under key, rendered as keySQL.
func (l *pairList) add(key, keySQL, val string) error {
	if i, ok := l.pos[key]; ok {
		if l.vals[i] == val {
			return nil
		}
		return errUser(CodeInvalidSelect, "two select items answer under %q; alias one of them", key)
	}
	l.pos[key] = len(l.vals)
	l.keys = append(l.keys, key)
	l.keySQL = append(l.keySQL, keySQL)
	l.vals = append(l.vals, val)
	return nil
}

// object renders the members as one json object.
func (l *pairList) object() string { return l.objectExcept(-1) }

// objectExcept renders every member but the one at index skip.
func (l *pairList) objectExcept(skip int) string {
	pairs := make([]string, 0, 2*len(l.vals))
	for i := range l.vals {
		if i != skip {
			pairs = append(pairs, l.keySQL[i], l.vals[i])
		}
	}
	return buildJSONObject(pairs)
}

func plainSpatial(c *introspect.Column) bool {
	return c != nil && postgis.IsSpatial(c.PGType) && !c.IsArray
}

// rowJSON compiles a projection node over its resource into a json
// expression plus the LEFT JOIN LATERAL clauses it needs against alias.
func (s *stmt) rowJSON(node *plan.Node, alias string, depth int) (string, []string, error) {
	pl, laterals, err := s.rowPairs(node, alias, depth)
	if err != nil {
		return "", nil, err
	}
	return pl.object(), laterals, nil
}

// rootJSON renders the root row of a read: the plain row object, or under
// Accept: application/geo+json a GeoJSON Feature whose geometry is the
// chosen spatial select item and whose properties are the remaining items,
// embeds included.
func (s *stmt) rootJSON(node *plan.Node, alias string) (string, []string, error) {
	if s.plan.Accept != plan.AcceptGeoJSON {
		return s.rowJSON(node, alias, 0)
	}
	cands := geoCandidates(node)
	key := s.plan.Geometry
	switch {
	case key == "" && len(cands) > 0:
		key = cands[0]
	case key == "":
		return "", nil, errNotAcceptable("application/geo+json needs a geometry or geography column in select=")
	case !slices.Contains(cands, key):
		return "", nil, errUser(CodeInvalidSelect, "geometry=%s is not a spatial column in select=", key)
	}
	s.geoKey = &key
	pl, laterals, err := s.rowPairs(node, alias, 0)
	s.geoKey = nil
	if err != nil {
		return "", nil, err
	}
	i := pl.pos[key]
	feature := buildJSONObject([]string{
		"'type'", "'Feature'",
		"'geometry'", pl.vals[i],
		"'properties'", pl.objectExcept(i),
	})
	return feature, laterals, nil
}

// geoCandidates lists, in selection order, the response keys of the plain
// spatial columns a Feature geometry can come from: arrays, JSON paths,
// casts, computed columns and derived accessors never qualify.
func geoCandidates(node *plan.Node) []string {
	var keys []string
	add := func(k string) {
		if !slices.Contains(keys, k) {
			keys = append(keys, k)
		}
	}
	if node.Star {
		for _, f := range node.Resource.Columns {
			if plainSpatial(f.Column) {
				add(f.Key)
			}
		}
	}
	for i := range node.Fields {
		f := &node.Fields[i]
		if !plainSpatial(f.Column) || len(f.Path) > 0 || f.Cast != nil {
			continue
		}
		add(f.Key(node.Resource))
	}
	return keys
}

// featureCollection wraps the aggregated page rows of a GeoJSON response.
func (s *stmt) featureCollection(features string) string {
	if s.plan.Accept != plan.AcceptGeoJSON {
		return features
	}
	return buildJSONObject([]string{"'type'", "'FeatureCollection'", "'features'", features})
}

// rowPairs is rowJSON before the object is built: the members of a
// projection node, spread embeds hoisted (pairList states the duplicate-key
// rule).
func (s *stmt) rowPairs(node *plan.Node, alias string, depth int) (*pairList, []string, error) {
	if depth > s.maxDepth {
		return nil, nil, errUser(CodeEmbedDepthExceeded, "embed depth exceeds limit (%d)", s.maxDepth)
	}
	r := node.Resource
	if r == nil {
		return nil, nil, fmt.Errorf("compile: projection node without resource")
	}
	pl := newPairList()
	var laterals []string
	if node.Star {
		for _, f := range r.Columns {
			expr, err := s.columnJSON(alias, f.Column, nil, depth)
			if err != nil {
				return nil, nil, err
			}
			if err := pl.add(f.Key, quoteLiteral(f.Key), expr); err != nil {
				return nil, nil, err
			}
		}
	}
	for i := range node.Fields {
		f := &node.Fields[i]
		if f.Agg != nil {
			return nil, nil, errUser(CodeInvalidSelect, "aggregate functions are only valid on list reads, in to-many embeds and in to-one spreads")
		}
		key, expr, err := s.fieldJSON(r, f, alias, depth)
		if err != nil {
			return nil, nil, err
		}
		keySQL := ""
		if depth != 0 || s.geoKey == nil || key != *s.geoKey {
			keySQL = s.pairKey(f, key)
		}
		if err := pl.add(key, keySQL, expr); err != nil {
			return nil, nil, err
		}
	}
	for i := range node.Embeds {
		e := &node.Embeds[i]
		if e.Relation == nil {
			return nil, nil, fmt.Errorf("compile: embed %q is not resolved", e.Key)
		}
		if e.Spread {
			child, lat, err := s.spreadEmbed(e, alias, depth+1)
			if err != nil {
				return nil, nil, err
			}
			for j := range child.vals {
				if err := pl.add(child.keys[j], child.keySQL[j], child.vals[j]); err != nil {
					return nil, nil, err
				}
			}
			laterals = append(laterals, lat)
			continue
		}
		key := e.Alias
		if key == "" {
			key = e.Key
		}
		var expr, lat string
		var err error
		if e.Relation.ToMany() {
			expr, lat, err = s.manyEmbed(e, alias, depth+1)
		} else {
			expr, lat, err = s.oneEmbed(e, alias, depth+1)
		}
		if err != nil {
			return nil, nil, err
		}
		keySQL := quoteLiteral(key)
		if e.Alias != "" {
			keySQL = s.keyParam(key)
		}
		if err := pl.add(key, keySQL, expr); err != nil {
			return nil, nil, err
		}
		laterals = append(laterals, lat)
	}
	return pl, laterals, nil
}

// fieldJSON renders one selected field: its response key and value expression.
func (s *stmt) fieldJSON(r *api.Resource, f *plan.FieldSel, alias string, depth int) (string, string, error) {
	switch {
	case f.Column != nil && (len(f.Path) > 0 || f.Cast != nil):
		return f.Key(r), s.fieldValueJSON(alias, f), nil
	case f.Column != nil:
		key := f.Alias
		if key == "" {
			key = f.Column.Name
			if fld := r.FieldByColumn(f.Column.Name); fld != nil {
				key = fld.Key
			}
		}
		expr, err := s.columnJSON(alias, f.Column, f.Args, depth)
		return key, expr, err
	case f.Computed != nil:
		key := f.Alias
		if key == "" {
			key = f.Computed.Key
		}
		expr, err := s.computedJSON(f.Computed, f.Node, f.CallArgs, alias, depth+1)
		return key, expr, err
	case f.Pseudo != nil:
		key := f.Alias
		if key == "" {
			key = f.Pseudo.Key
		}
		expr, err := s.pseudoJSON(f.Pseudo, f.Args, alias)
		return key, expr, err
	}
	return "", "", fmt.Errorf("compile: empty field selection")
}

// buildJSONObject renders pairs (key literal, value expression, …) as a
// json object in pair order. json has no || operator, so an object wider
// than one json_build_object call is spliced from its chunks' text: every
// chunk but the first loses its opening brace, every chunk but the last its
// closing one.
func buildJSONObject(pairs []string) string {
	if len(pairs) == 0 {
		return "'{}'::json"
	}
	var chunks []string
	for i := 0; i < len(pairs); i += jsonBuildObjectChunk * 2 {
		end := min(i+jsonBuildObjectChunk*2, len(pairs))
		chunks = append(chunks, "json_build_object("+strings.Join(pairs[i:end], ", ")+")")
	}
	if len(chunks) == 1 {
		return chunks[0]
	}
	for i, c := range chunks {
		switch i {
		case 0:
			chunks[i] = "left(" + c + "::text, -1)"
		case len(chunks) - 1:
			chunks[i] = "substr(" + c + "::text, 2)"
		default:
			chunks[i] = "substr(left(" + c + "::text, -1), 2)"
		}
	}
	return "(" + strings.Join(chunks, " || ', ' || ") + ")::json"
}

// columnJSON renders a column value for inclusion in the JSON response.
func (s *stmt) columnJSON(alias string, c *introspect.Column, args map[string]any, depth int) (string, error) {
	ref := colRef(alias, c.Name)
	if comp := s.compositeFor(c); comp != nil {
		return s.compositeJSON(ref, comp, c.IsArray, depth+1)
	}
	if postgis.IsSpatial(c.PGType) && !c.IsArray {
		return s.spatialFieldSQL(ref, c, args)
	}
	if c.IsArray {
		if postgis.IsSpatial(c.PGType) {
			// to_json on a geometry[] would emit WKB hex strings; map each
			// element through ST_AsGeoJSON instead.
			return s.spatialArrayJSON(ref), nil
		}
		return "to_json(" + ref + ")", nil
	}
	return s.exprJSON(ref, c), nil
}

// exprJSON renders a scalar value expression for the JSON response: 64-bit
// and arbitrary-precision numbers as strings (no precision loss), bytea as
// base64, spatial values as GeoJSON, pgvector vector and halfvec values as
// arrays of numbers; enum labels pass through verbatim.
func (s *stmt) exprJSON(ref string, c *introspect.Column) string {
	if postgis.IsSpatial(c.PGType) {
		return postgis.OutputJSONSQL(ref)
	}
	if v, ok := api.VectorVocabulary(s.catalog(), c); ok {
		return v.Codec.OutputJSONSQL(ref)
	}
	switch strings.TrimPrefix(c.PGType, "_") {
	case "int8", "numeric", "money":
		return "(" + ref + ")::text"
	case "bytea":
		return "encode(" + ref + ", 'base64')"
	}
	return ref
}

// spatialArrayJSON renders a geometry/geography array as a JSON array of
// GeoJSON objects, preserving element order and NULL-ness of the array itself.
func (s *stmt) spatialArrayJSON(ref string) string {
	elem := s.alias("g")
	return fmt.Sprintf("CASE WHEN %s IS NULL THEN NULL ELSE (SELECT coalesce(json_agg(%s ORDER BY %s.o), '[]'::json) FROM (SELECT generate_subscripts(%s, 1) AS o) %s) END",
		ref, postgis.OutputJSONSQL("("+ref+")["+elem+".o]"), elem, ref, elem)
}

// spatialFieldSQL renders a selected spatial column, applying the optional
// transform (reproject) and simplify (vertex reduction) arguments before the
// value is encoded as GeoJSON.
func (s *stmt) spatialFieldSQL(ref string, c *introspect.Column, args map[string]any) (string, error) {
	expr, err := s.spatialArgs(ref, postgis.FlavourOf(c.PGType), args)
	if err != nil {
		return "", err
	}
	return postgis.OutputJSONSQL(expr), nil
}

// spatialArgs applies simplify= then transform= to a spatial expression.
func (s *stmt) spatialArgs(ref string, flavour postgis.Flavour, args map[string]any) (string, error) {
	expr := ref
	if v, ok := args["simplify"]; ok && v != nil {
		tol, err := toFloat(v)
		if err != nil || tol < 0 {
			return "", errUser(CodeInvalidSelect, "simplify must be a number >= 0")
		}
		expr = postgis.SimplifySQL(expr, s.param(tol), flavour)
	}
	if v, ok := args["transform"]; ok && v != nil {
		srid, err := toFloat(v)
		if err != nil || srid <= 0 || srid != float64(int(srid)) {
			return "", errUser(CodeInvalidSelect, "transform must be a positive integer SRID")
		}
		// Simplify already produced a geometry; keep the cast consistent.
		f := flavour
		if expr != ref {
			f = postgis.Geometry
		}
		expr = postgis.TransformSQL(expr, int(srid), f)
	}
	return expr, nil
}

// pseudoJSON renders a pseudo-column: a score, a contrib accessor or
// derived value, or a derived spatial accessor (<col>_area, …), whose
// projection arguments apply to the owning column before the accessor
// runs.
func (s *stmt) pseudoJSON(p *api.PseudoColumn, args map[string]any, alias string) (string, error) {
	if p.Score != nil {
		return s.scoreSQL(p, args, alias)
	}
	if p.Contrib != nil {
		return s.contribPseudoSQL(p, args, alias)
	}
	d, ok := derivedFor(p.Accessor)
	if !ok {
		return "", fmt.Errorf("compile: unknown spatial accessor %q", p.Accessor)
	}
	flavour := postgis.FlavourOf(p.Column.PGType)
	expr, err := s.spatialArgs(colRef(alias, p.Column.Name), flavour, args)
	if err != nil {
		return "", err
	}
	if expr != colRef(alias, p.Column.Name) {
		flavour = postgis.Geometry
	}
	return d.JSONSQL(expr, flavour), nil
}

// derivedFor matches an accessor name ("area", "geometry_type",
// "GeometryType") against the pdbcore accessor table.
func derivedFor(accessor string) (postgis.Derived, bool) {
	want := strings.ToLower(strings.ReplaceAll(accessor, "_", ""))
	for _, d := range postgis.Deriveds {
		if strings.ToLower(d.Suffix) == want {
			return d, true
		}
	}
	return postgis.Derived{}, false
}

// computedJSON compiles a computed column: the row-type function called with
// an explicit ROW(...)::table cast, which works over any row source — a table
// scan, a function row source, or a mutation RETURNING CTE (whose whole-row
// variable is an anonymous record the function would reject). Set-returning
// functions aggregate into an array (SETOF <scalar>) or an array of fully
// projected rows (SETOF <table>).
func (s *stmt) computedJSON(c *api.Computed, node *plan.Node, args map[string]any, alias string, depth int) (string, error) {
	fn := c.Function
	call := s.computedCallSQL(fn, c.Resource.Table, alias, args)
	ret := fnReturnColumn(fn)
	if fn.ReturnsSet {
		if c.SetOf != nil {
			if depth > s.maxDepth {
				return "", errUser(CodeEmbedDepthExceeded, "embed depth exceeds limit (%d)", s.maxDepth)
			}
			if node == nil {
				node = &plan.Node{Resource: c.SetOf, Star: true}
			} else if node.Resource == nil {
				n := *node
				n.Resource = c.SetOf
				node = &n
			}
			rowAlias := s.alias("t")
			jsonExpr, laterals, err := s.rowJSON(node, rowAlias, depth)
			if err != nil {
				return "", err
			}
			sub := s.alias("s")
			return fmt.Sprintf("(SELECT coalesce(json_agg(%s.data), '[]'::json) FROM (\n%s\n) %s)",
				sub,
				indent(fmt.Sprintf("SELECT %s AS data\nFROM %s %s%s", jsonExpr, call, rowAlias, joinLaterals(laterals))),
				sub), nil
		}
		elem := s.alias("v")
		return fmt.Sprintf("(SELECT coalesce(json_agg(%s), '[]'::json) FROM %s %s(v))",
			s.exprJSON(elem+".v", ret), call, elem), nil
	}
	if comp := s.compositeFor(ret); comp != nil {
		return s.compositeJSON(call, comp, ret.IsArray, depth)
	}
	if ret.IsArray {
		if postgis.IsSpatial(ret.PGType) {
			sub := s.alias("a")
			return fmt.Sprintf("(SELECT %s FROM (SELECT %s AS v) %s)", s.spatialArrayJSON(sub+".v"), call, sub), nil
		}
		return "to_json(" + call + ")", nil
	}
	return s.exprJSON(call, ret), nil
}

// rowParamSQL renders alias's row as an explicit ROW(...)::table value.
func rowParamSQL(t *introspect.Table, alias string) string {
	cols := make([]string, len(t.Columns))
	for i, c := range t.Columns {
		cols[i] = colRef(alias, c.Name)
	}
	return "ROW(" + strings.Join(cols, ", ") + ")::" + tableRef(t)
}

// computedCallSQL renders a computed-column function called on the current
// row: the row is its first argument, the extra arguments present in args
// are bound as typed parameters and the rest are left to their DEFAULT. A
// required argument that is absent fails the request (missing_argument).
func (s *stmt) computedCallSQL(fn *introspect.Function, t *introspect.Table, alias string, args map[string]any) string {
	call, err := fn.CallSQL(func(a introspect.FuncArg) (string, bool, error) {
		if a.Name == fn.Args[0].Name {
			return rowParamSQL(t, alias), true, nil
		}
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
		s.fail(errUser(CodeMissingArgument, "computed column %q: missing required argument(s): %s", fn.Name, strings.Join(missing.Args, ", ")))
		return "NULL"
	}
	if err != nil {
		s.fail(fmt.Errorf("compile: computed column %s.%s: %w", fn.Schema, fn.Name, err))
		return "NULL"
	}
	return call
}

// fnReturnColumn describes a function's return type as a column so the
// scalar/array renderers apply the same representation fixes.
func fnReturnColumn(fn *introspect.Function) *introspect.Column {
	return &introspect.Column{
		Name:       fn.Name,
		PGType:     fn.ReturnType,
		TypeSchema: fn.ReturnTypeSchema,
		IsArray:    strings.HasPrefix(fn.ReturnType, "_"),
	}
}

// compositeFor resolves a column to its catalog composite type, or nil.
func (s *stmt) compositeFor(c *introspect.Column) *introspect.Composite {
	if c == nil || s.c.Built == nil || s.c.Built.Catalog == nil {
		return nil
	}
	return s.c.Built.Catalog.Composite(c.TypeSchema, strings.TrimPrefix(c.PGType, "_"))
}

// compositeJSON renders a composite (or composite-array) value as a nested
// object with every attribute. NULL detection uses the text representation
// because `row IS NULL` is also true for a row whose attributes are all NULL.
func (s *stmt) compositeJSON(ref string, comp *introspect.Composite, isArray bool, depth int) (string, error) {
	if depth > s.maxDepth {
		return "", errUser(CodeEmbedDepthExceeded, "embed depth exceeds limit (%d)", s.maxDepth)
	}
	if isArray {
		// unnest(composite[]) in FROM expands the composite into columns, so
		// index the array by subscript instead to keep each element whole.
		elem := s.alias("e")
		inner, err := s.compositeJSON("("+ref+")["+elem+".o]", comp, false, depth)
		if err != nil {
			return "", err
		}
		return fmt.Sprintf("CASE WHEN %s IS NULL THEN NULL ELSE (SELECT coalesce(json_agg(%s ORDER BY %s.o), '[]'::json) FROM (SELECT generate_subscripts(%s, 1) AS o) %s) END",
			ref, inner, elem, ref, elem), nil
	}
	var pairs []string
	for _, f := range comp.Fields {
		attr := "(" + ref + ")." + quoteIdent(f.Name)
		var expr string
		if nested := s.compositeFor(f); nested != nil {
			var err error
			expr, err = s.compositeJSON(attr, nested, f.IsArray, depth+1)
			if err != nil {
				return "", err
			}
		} else if f.IsArray {
			if postgis.IsSpatial(f.PGType) {
				expr = s.spatialArrayJSON(attr)
			} else {
				expr = "to_json(" + attr + ")"
			}
		} else {
			expr = s.exprJSON(attr, f)
		}
		pairs = append(pairs, quoteLiteral(f.Name), expr)
	}
	return fmt.Sprintf("CASE WHEN (%s)::text IS NULL THEN NULL ELSE %s END", ref, buildJSONObject(pairs)), nil
}

// embedNode returns the projection node of an embed (nil select inside the
// parentheses means every column of the related resource).
func embedNode(e *plan.Embed) *plan.Node {
	if e.Node == nil {
		return &plan.Node{Resource: e.Relation.To, Star: true}
	}
	if e.Node.Resource == nil {
		n := *e.Node
		n.Resource = e.Relation.To
		return &n
	}
	return e.Node
}

// oneEmbed compiles a to-one embed: one lateral join fetching the
// referenced row's JSON (null when the FK is null or the embed filter
// excludes the row).
func (s *stmt) oneEmbed(e *plan.Embed, parentAlias string, depth int) (string, string, error) {
	rel := e.Relation
	node := embedNode(e)
	alias := s.alias("t")
	jsonExpr, laterals, err := s.rowJSON(node, alias, depth)
	if err != nil {
		return "", "", err
	}
	conds := []string{s.joinConds(rel, alias, parentAlias)}
	more, err := s.parentConds(node, e.Filter, alias)
	if err != nil {
		return "", "", err
	}
	conds = append(conds, more...)
	latAlias := s.alias("l")
	lat := fmt.Sprintf("LEFT JOIN LATERAL (\n%s\n) %s ON true",
		indent(fmt.Sprintf("SELECT %s AS data\nFROM %s %s%s\nWHERE %s\nLIMIT 1",
			jsonExpr, s.sourceRef(rel.To.Table, alias), alias, joinLaterals(laterals), strings.Join(conds, " AND "))),
		latAlias)
	return latAlias + ".data", lat, nil
}

// manyEmbed compiles a to-many embed (backward, or forward over a PERIOD
// key): a lateral join aggregating the related rows' JSON into an array,
// with the embed's own filter, order and offset paging. The correlation is
// rendered by listInner once the child alias exists (a textual placeholder
// could collide with user JSON keys).
func (s *stmt) manyEmbed(e *plan.Embed, parentAlias string, depth int) (string, string, error) {
	rel := e.Relation
	corr := func(childAlias string) string { return s.joinConds(rel, childAlias, parentAlias) }
	inner, err := s.listInner(embedNode(e), e.Filter, e.Order, e.Page, corr, depth)
	if err != nil {
		return "", "", err
	}
	sub := s.alias("s")
	latAlias := s.alias("l")
	body := fmt.Sprintf("SELECT coalesce(json_agg(%s.data), '[]'::json) AS data\nFROM (\n%s\n) %s", sub, indent(inner), sub)
	lat := fmt.Sprintf("LEFT JOIN LATERAL (\n%s\n) %s ON true", indent(body), latAlias)
	return latAlias + ".data", lat, nil
}

// spreadEmbed compiles a spread embed into the members it hoists into the
// parent object and the lateral join producing their values (one column
// per member): a to-one spread selects the related row's values (all null
// without one), a to-many spread aggregates each value into an array over
// the related rows, in one pass so the arrays are correlated (same order,
// the embed's order= then its primary key).
func (s *stmt) spreadEmbed(e *plan.Embed, parentAlias string, depth int) (*pairList, string, error) {
	rel := e.Relation
	node := embedNode(e)
	var child *pairList
	project := func(alias string) (string, []string, error) {
		pl, laterals, err := s.rowPairs(node, alias, depth)
		if err != nil {
			return "", nil, err
		}
		child = pl
		cols := make([]string, len(pl.vals))
		for i, v := range pl.vals {
			cols[i] = v + " AS v" + strconv.Itoa(i)
		}
		if len(cols) == 0 {
			cols = []string{"NULL AS v"}
		}
		return strings.Join(cols, ", "), laterals, nil
	}
	latAlias := s.alias("l")
	var body string
	if rel.ToMany() {
		if node.Aggregates() {
			return nil, "", errUser(CodeInvalidSelect, "aggregate functions are not supported inside to-many spreads")
		}
		corr := func(childAlias string) string { return s.joinConds(rel, childAlias, parentAlias) }
		inner, err := s.listInnerWith(node, e.Filter, e.Order, e.Page, corr, project)
		if err != nil {
			return nil, "", err
		}
		sub := s.alias("s")
		aggs := make([]string, len(child.vals))
		for i := range child.vals {
			v := "v" + strconv.Itoa(i)
			aggs[i] = fmt.Sprintf("coalesce(json_agg(%s.%s), '[]'::json) AS %s", sub, v, v)
		}
		if len(aggs) == 0 {
			aggs = []string{"NULL AS v"}
		}
		body = fmt.Sprintf("SELECT %s\nFROM (\n%s\n) %s", strings.Join(aggs, ", "), indent(inner), sub)
	} else {
		alias := s.alias("t")
		cols, laterals, err := project(alias)
		if err != nil {
			return nil, "", err
		}
		conds := []string{s.joinConds(rel, alias, parentAlias)}
		more, err := s.parentConds(node, e.Filter, alias)
		if err != nil {
			return nil, "", err
		}
		conds = append(conds, more...)
		body = fmt.Sprintf("SELECT %s\nFROM %s %s%s\nWHERE %s\nLIMIT 1",
			cols, s.sourceRef(rel.To.Table, alias), alias, joinLaterals(laterals), strings.Join(conds, " AND "))
	}
	for i := range child.vals {
		child.vals[i] = latAlias + ".v" + strconv.Itoa(i)
	}
	return child, fmt.Sprintf("LEFT JOIN LATERAL (\n%s\n) %s ON true", indent(body), latAlias), nil
}

// listInner builds `SELECT <json> AS data FROM ... WHERE ... ORDER BY ...
// LIMIT/OFFSET` for an embedded list. corr, when set, renders the condition
// correlating the subquery to a parent row given the child alias.
func (s *stmt) listInner(node *plan.Node, filter plan.Expr, order []plan.OrderTerm, page plan.Page, corr func(childAlias string) string, depth int) (string, error) {
	if node.Aggregated() {
		return s.aggListInner(node, filter, order, page, corr, depth)
	}
	return s.listInnerWith(node, filter, order, page, corr, func(alias string) (string, []string, error) {
		jsonExpr, laterals, err := s.rowJSON(node, alias, depth)
		return jsonExpr + " AS data", laterals, err
	})
}

// listInnerWith is listInner with the select list rendered by project over
// the scan alias.
func (s *stmt) listInnerWith(node *plan.Node, filter plan.Expr, order []plan.OrderTerm, page plan.Page, corr func(childAlias string) string, project func(alias string) (string, []string, error)) (string, error) {
	t := node.Resource.Table
	alias := s.alias("t")
	selectList, laterals, err := project(alias)
	if err != nil {
		return "", err
	}
	var extra []string
	if corr != nil {
		extra = append(extra, corr(alias))
	}
	where, err := s.whereClause(node, filter, alias, extra)
	if err != nil {
		return "", err
	}
	orderSQL := s.renderOrder(s.orderTerms(t, order), alias, false)
	limit, err := s.embedLimit(page)
	if err != nil {
		return "", err
	}
	pageSQL := "\nLIMIT " + strconv.Itoa(limit)
	if page.Offset > 0 {
		pageSQL += "\nOFFSET " + s.param(page.Offset)
	}
	return fmt.Sprintf("SELECT %s\nFROM %s %s%s%s%s%s", selectList, s.sourceRef(t, alias), alias, joinLaterals(laterals), where, orderSQL, pageSQL), nil
}

// embedLimit resolves an embed's page size: the cap when absent, rejected
// (never clamped) when above it.
func (s *stmt) embedLimit(page plan.Page) (int, error) {
	if page.Limit <= 0 {
		return s.maxPageSize, nil
	}
	if page.Limit > s.maxPageSize {
		return 0, errPageSize(page.Limit, s.maxPageSize)
	}
	return page.Limit, nil
}
