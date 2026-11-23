package api

import (
	"strconv"
	"strings"

	"github.com/suprbdev/pdbcore/contrib"
	"github.com/suprbdev/pdbcore/introspect"
	"github.com/suprbdev/pdbcore/postgis"
)

const (
	problemResponse             = "Problem"
	methodNotAllowedResponse    = "MethodNotAllowed"
	rangeNotSatisfiableResponse = "RangeNotSatisfiable"
	mediaJSON                   = "application/json"
	mediaCSV                    = "text/csv"
	mediaGeoJSON                = "application/geo+json"
)

// Error status sets per operation kind (docs/errors.md "Status mapping"):
// every operation can hit the URL grammar (400), auth (401/403), content
// negotiation (406) and the database/infrastructure statuses; keyed paths
// add 404, writes add the read-only/constraint/body statuses.
var (
	readStatuses       = []int{400, 401, 403, 406, 500, 503, 504}
	rowStatuses        = []int{400, 401, 403, 404, 406, 500, 503, 504}
	createStatuses     = []int{400, 401, 403, 405, 406, 409, 413, 415, 500, 503, 504}
	updateStatuses     = []int{400, 401, 403, 404, 405, 406, 409, 412, 413, 415, 500, 503, 504}
	deleteStatuses     = []int{400, 401, 403, 404, 405, 406, 409, 412, 500, 503, 504}
	bulkDeleteStatuses = []int{400, 401, 403, 405, 406, 409, 500, 503, 504}
	rpcGetStatuses     = []int{400, 401, 403, 406, 500, 503, 504}
	rpcPostStatus      = []int{400, 401, 403, 405, 406, 413, 415, 500, 503, 504}
)

// pathItem renders one route: the operations its methods allow, with the
// query/header/path parameters, request bodies and responses each carries.
func (b *Builder) pathItem(rt *Route) *PathItem {
	item := &PathItem{}
	switch rt.Kind {
	case RouteList:
		b.listItem(item, rt)
	case RouteRow, RouteUnique:
		b.rowItem(item, rt)
	case RouteRPC:
		b.rpcItem(item, rt)
	}
	return item
}

// operation seeds an operation with the route's tag, id, summary, the
// owner's description/deprecation and the Problem responses for statuses.
func (b *Builder) operation(rt *Route, id, summary string, statuses []int) *Operation {
	o := &Operation{OperationID: id, Summary: summary, Responses: map[string]*Response{}}
	switch {
	case rt.Resource != nil:
		o.Tags = []string{routeTag(rt)}
		o.Description = rt.Resource.Description
		o.Deprecated = rt.Resource.Deprecated
		o.XDeprecationReason = rt.Resource.DeprecationReason
	case rt.RPC != nil:
		o.Tags = []string{routeTag(rt)}
		o.Description = rt.RPC.Description
		o.Deprecated = rt.RPC.Deprecated
		o.XDeprecationReason = rt.RPC.DeprecationReason
	}
	for _, st := range statuses {
		if st == 401 && !b.authOn() {
			continue
		}
		ref := problemResponse
		if st == 405 {
			ref = methodNotAllowedResponse
		}
		o.Responses[strconv.Itoa(st)] = &Response{Ref: "#/components/responses/" + ref}
	}
	o.Responses["default"] = &Response{Ref: "#/components/responses/" + problemResponse}
	return o
}

func appendDescription(o *Operation, text string) {
	if text == "" {
		return
	}
	if o.Description == "" {
		o.Description = text
		return
	}
	o.Description += "\n\n" + text
}

func headOf(get *Operation, id string) *Operation {
	h := *get
	h.OperationID = id
	h.Summary = get.Summary + " (headers only)"
	h.RequestBody = nil
	h.Responses = make(map[string]*Response, len(get.Responses))
	for k, r := range get.Responses {
		if r.Ref != "" || r.Content == nil {
			h.Responses[k] = r
			continue
		}
		cp := *r
		cp.Content = nil
		cp.Description = r.Description + " (same headers as GET, no body)"
		h.Responses[k] = &cp
	}
	return &h
}

// Component schema names of the GeoJSON Feature and FeatureCollection
// documents application/geo+json answers with (registered by the postgis
// plugin next to the GeoJSON geometry schema).
const (
	GeoJSONFeature           = "GeoJSONFeature"
	GeoJSONFeatureCollection = "GeoJSONFeatureCollection"
)

func jsonBody(s *Schema, required bool, desc string) *RequestBody {
	return &RequestBody{Required: required, Description: desc, Content: map[string]*MediaType{mediaJSON: {Schema: s}}}
}

// objectOrArray is the POST/PUT body shape: one object or a non-empty array.
func objectOrArray(name string) *Schema {
	return &Schema{OneOf: []*Schema{SchemaRef(name), {Type: "array", Items: SchemaRef(name), MinItems: intPtr(1)}}}
}

func (b *Builder) listItem(item *PathItem, rt *Route) {
	r := rt.Resource
	if rt.Allows(MethodGet) {
		get := b.operation(rt, r.Path+".list", "List "+r.Path, readStatuses)
		get.Parameters = append(append(b.listParams(r, false), b.acceptParam(true, r)...), countPreferParam())
		get.Parameters = append(get.Parameters, rangeParams()...)
		appendDescription(get, b.filterProse(r))
		ok := &Response{Description: "OK", Headers: b.listHeaders(r.Keyset), Content: map[string]*MediaType{
			mediaJSON: {Schema: &Schema{Type: "array", Items: SchemaRef(r.Path)}},
		}}
		b.addCSV(ok)
		b.addGeoJSON(ok, r, GeoJSONFeatureCollection)
		get.Responses["200"] = ok
		get.Responses["206"] = partialOf(ok)
		get.Responses["416"] = rangeNotSatisfiable()
		item.Get = get
		if rt.Allows(MethodHead) {
			item.Head = headOf(get, r.Path+".list.head")
		}
	}
	if rt.Allows(MethodPost) {
		post := b.operation(rt, r.Path+".create", "Create "+r.Path, createStatuses)
		post.Parameters = []*Parameter{b.selectParam(r, "Columns and embeds of the created row(s) to return."), createPreferParam(), columnsParam()}
		if r.Keyset || len(r.Uniques) > 0 {
			post.Parameters = append(post.Parameters, &Parameter{Name: "on_conflict", In: "query", Schema: &Schema{Type: "string"},
				Description: "Conflict target for Prefer: resolution= (default: the primary key): comma list equal to a unique constraint's columns. " + b.uniqueProse(r)})
		}
		post.RequestBody = jsonBody(objectOrArray(r.Path+".insert"), true, "One row, or an array of rows for a bulk insert (one INSERT, columns = union, missing keys take DEFAULT).")
		post.Responses["201"] = &Response{Description: "Created (Prefer: return=minimal omits the body).",
			Headers: writeHeaders(map[string]*Header{"Location": {Schema: &Schema{Type: "string"}, Description: "URL of the new row for a single-object body on a resource with a single-column primary key."}}),
			Content: map[string]*MediaType{mediaJSON: {Schema: &Schema{OneOf: []*Schema{SchemaRef(r.Path), {Type: "array", Items: SchemaRef(r.Path)}}}}},
		}
		post.Responses["204"] = &Response{Description: "Single object skipped by Prefer: resolution=ignore-duplicates (nothing inserted).", Headers: writeHeaders(nil)}
		post.Responses["207"] = partialReportResponse()
		item.Post = post
	}
	if rt.Allows(MethodPut) {
		put := b.operation(rt, r.Path+".upsert", "Upsert "+r.Path, createStatuses)
		put.Parameters = []*Parameter{
			b.selectParam(r, "Columns and embeds of the affected row(s) to return."),
			{Name: "on_conflict", In: "query", Schema: &Schema{Type: "string"},
				Description: "Conflict target: comma list equal to a unique constraint's columns (default: the primary key). " + b.uniqueProse(r)},
			putPreferParam(),
			columnsParam(),
		}
		put.RequestBody = jsonBody(objectOrArray(r.Path+".upsert"), true, "One row, or an array of rows; the conflict-target columns must be present in every element.")
		put.Responses["200"] = &Response{Description: "Updated (single object that hit an existing row, or any array).", Headers: writeHeaders(nil),
			Content: map[string]*MediaType{mediaJSON: {Schema: &Schema{OneOf: []*Schema{SchemaRef(r.Path), {Type: "array", Items: SchemaRef(r.Path)}}}}}}
		put.Responses["201"] = &Response{Description: "Inserted (single object that created a row; Prefer: return=minimal omits the body).",
			Headers: writeHeaders(map[string]*Header{"Location": {Schema: &Schema{Type: "string"}, Description: "URL of the new row when the resource has a single-column primary key."}}),
			Content: map[string]*MediaType{mediaJSON: {Schema: SchemaRef(r.Path)}}}
		put.Responses["204"] = &Response{Description: "Updated with Prefer: return=minimal.", Headers: writeHeaders(nil)}
		put.Responses["207"] = partialReportResponse()
		item.Put = put
	}
	if rt.Allows(MethodPatch) {
		patch := b.operation(rt, r.Path+".bulk_update", "Update the "+r.Path+" rows matching the filters", createStatuses)
		patch.Parameters = append([]*Parameter{b.selectParam(r, "Columns and embeds of the updated rows to return."), preferParam(true, true)}, b.bulkParams(r)...)
		patch.Parameters = append(patch.Parameters, columnsParam())
		appendDescription(patch, bulkProse)
		appendDescription(patch, b.filterProse(r))
		patch.RequestBody = jsonBody(SchemaRef(r.Path+".update"), true, "Columns to set on every matching row.")
		patch.Responses["200"] = &Response{Description: "Updated (the changed rows; an empty array when nothing matched).", Headers: writeHeaders(bulkRange()),
			Content: map[string]*MediaType{mediaJSON: {Schema: &Schema{Type: "array", Items: SchemaRef(r.Path)}}}}
		patch.Responses["204"] = &Response{Description: "Updated with Prefer: return=minimal.", Headers: writeHeaders(bulkRange())}
		item.Patch = patch
	}
	if rt.Allows(MethodDelete) {
		del := b.operation(rt, r.Path+".bulk_delete", "Delete the "+r.Path+" rows matching the filters", bulkDeleteStatuses)
		del.Parameters = append([]*Parameter{b.selectParam(r, "Columns and embeds of the deleted rows to return."), preferParam(true, true)}, b.bulkParams(r)...)
		appendDescription(del, bulkProse)
		appendDescription(del, b.filterProse(r))
		del.Responses["200"] = &Response{Description: "Deleted (the removed rows; an empty array when nothing matched).", Headers: writeHeaders(bulkRange()),
			Content: map[string]*MediaType{mediaJSON: {Schema: &Schema{Type: "array", Items: SchemaRef(r.Path)}}}}
		del.Responses["204"] = &Response{Description: "Deleted with Prefer: return=minimal.", Headers: writeHeaders(bulkRange())}
		item.Delete = del
	}
}

func bulkRange() map[string]*Header {
	return map[string]*Header{"Content-Range": {Schema: &Schema{Type: "string"},
		Description: "0-<n-1>/<n> of the written rows (* range under return=minimal or when nothing matched); the total is * unless Prefer: count= is given."}}
}

const bulkProse = "The filters select the rows (same grammar and filter policy as the list GET); a request without any filter answers 400 unfiltered_mutation unless server.allow_unfiltered_mutations is on."

// bulkParams are the row-selecting parameters of a bulk PATCH / DELETE: the
// logic params and one filter param per filterable column.
func (b *Builder) bulkParams(r *Resource) []*Parameter {
	return append(b.logicParams(), b.filterParams(r, nil)...)
}

// writeHeaders adds Preference-Applied to a write response's headers.
func writeHeaders(h map[string]*Header) map[string]*Header {
	out := map[string]*Header{"Preference-Applied": {Schema: &Schema{Type: "string"}, Description: "The Prefer tokens that were honoured, comma-separated."}}
	for k, v := range h {
		out[k] = v
	}
	return out
}

func (b *Builder) rowItem(item *PathItem, rt *Route) {
	r := rt.Resource
	var cols []string
	// operationIds: <name>.get|put|update|delete on the primary-key path,
	// <name>.by_<cols>[.put|.update|.delete] on unique-constraint paths.
	id, getID := r.Path, r.Path+".get"
	what := "by primary key"
	if rt.Kind == RouteRow {
		cols = r.PrimaryKey().Columns
	} else {
		cols = rt.Unique.Columns
		id = r.Path + ".by_" + strings.Join(r.keys(cols), "_")
		getID = id
		what = "by unique constraint " + rt.Unique.Name
	}
	item.Parameters = b.pathParams(r, cols)
	if rt.Allows(MethodGet) {
		get := b.operation(rt, getID, "Get one "+r.Path+" "+what, rowStatuses)
		get.Parameters = append([]*Parameter{b.selectParam(r, "Columns and embeds to return.")}, b.acceptParam(false, r)...)
		if g := b.geometryParam(r); g != nil {
			get.Parameters = append(get.Parameters, g)
		}
		ok := &Response{Description: "OK", Content: map[string]*MediaType{mediaJSON: {Schema: SchemaRef(r.Path)}}}
		b.addGeoJSON(ok, r, GeoJSONFeature)
		if r.Etag {
			get.Parameters = append(get.Parameters, &Parameter{Name: "If-None-Match", In: "header", Schema: &Schema{Type: "string"},
				Description: "Weak ETag(s) (or *) from an earlier response; a match answers 304."})
			ok.Headers = map[string]*Header{"ETag": {Schema: &Schema{Type: "string"}, Description: `Weak row hash W/"<md5>" over the exposed columns, independent of select=.`}}
			get.Responses["304"] = &Response{Description: "Not Modified (If-None-Match matched)."}
		}
		get.Responses["200"] = ok
		item.Get = get
		if rt.Allows(MethodHead) {
			item.Head = headOf(get, getID+".head")
		}
	}
	if rt.Allows(MethodPut) {
		put := b.operation(rt, id+".put", "Insert or update one "+r.Path+" "+what, createStatuses)
		put.Parameters = []*Parameter{b.selectParam(r, "Columns and embeds of the row to return."), preferParam(false, false), columnsParam()}
		put.RequestBody = jsonBody(b.keyedPutSchema(r, cols), true, "The row; key columns may be omitted (taken from the path) or must equal the path values. Upserts on this path's key: unprovided columns keep their values on update.")
		put.Responses["200"] = &Response{Description: "Updated (the row existed).", Headers: writeHeaders(nil), Content: map[string]*MediaType{mediaJSON: {Schema: SchemaRef(r.Path)}}}
		put.Responses["201"] = &Response{Description: "Inserted (Prefer: return=minimal omits the body).",
			Headers: writeHeaders(map[string]*Header{"Location": {Schema: &Schema{Type: "string"}, Description: "URL of the new row when the resource has a single-column primary key."}}),
			Content: map[string]*MediaType{mediaJSON: {Schema: SchemaRef(r.Path)}}}
		put.Responses["204"] = &Response{Description: "Updated with Prefer: return=minimal.", Headers: writeHeaders(nil)}
		item.Put = put
	}
	if rt.Allows(MethodPatch) {
		patch := b.operation(rt, id+".update", "Update one "+r.Path+" "+what, updateStatuses)
		patch.Parameters = append([]*Parameter{b.selectParam(r, "Columns and embeds of the updated row to return.")}, b.ifMatchParam(r)...)
		patch.Parameters = append(patch.Parameters, preferParam(true, false), columnsParam())
		patch.RequestBody = jsonBody(SchemaRef(r.Path+".update"), true, "Columns to change (partial update).")
		patch.Responses["200"] = &Response{Description: "Updated", Headers: writeHeaders(nil), Content: map[string]*MediaType{mediaJSON: {Schema: SchemaRef(r.Path)}}}
		patch.Responses["204"] = &Response{Description: "Updated with Prefer: return=minimal.", Headers: writeHeaders(nil)}
		item.Patch = patch
	}
	if rt.Allows(MethodDelete) {
		del := b.operation(rt, id+".delete", "Delete one "+r.Path+" "+what, deleteStatuses)
		del.Parameters = append([]*Parameter{b.selectParam(r, "Columns and embeds of the deleted row to return.")}, b.ifMatchParam(r)...)
		del.Parameters = append(del.Parameters, preferParam(true, false))
		del.Responses["200"] = &Response{Description: "Deleted (the removed row)", Headers: writeHeaders(nil), Content: map[string]*MediaType{mediaJSON: {Schema: SchemaRef(r.Path)}}}
		del.Responses["204"] = &Response{Description: "Deleted with Prefer: return=minimal.", Headers: writeHeaders(nil)}
		item.Delete = del
	}
}

func (b *Builder) rpcItem(item *PathItem, rt *Route) {
	rpc := rt.RPC
	fn := rpc.Function.Schema + "." + rpc.Function.Name
	setOfRows := rpc.Returns == ReturnSetOfRows && rpc.Resource != nil
	setOf := setOfRows || rpc.Returns == ReturnSetOfScalar || rpc.Returns == ReturnSetOfComposite
	if rt.Allows(MethodGet) {
		get := b.operation(rt, "rpc."+rpc.Path, "Call "+fn, rpcGetStatuses)
		argNames := map[string]bool{}
		for _, a := range rpc.Args {
			argNames[a.Name] = true
			param := b.argParam(a)
			if row := rpc.RowArgs[a.Name]; row != nil {
				b.rowArgParam(param, a, row)
			}
			get.Parameters = append(get.Parameters, param)
		}
		get.Parameters = append(get.Parameters, b.rpcListParams(rpc, argNames)...)
		if setOf {
			get.Parameters = append(get.Parameters, b.acceptParam(true, rpc.Resource)...)
			get.Parameters = append(get.Parameters, rangeParams()...)
		} else if rpc.Returns == ReturnRow {
			get.Parameters = append(get.Parameters, b.acceptParam(false, rpc.Resource)...)
		}
		if rpc.Resource != nil && (setOfRows || rpc.Returns == ReturnRow) && !rpc.hasArg("geometry") {
			if g := b.geometryParam(rpc.Resource); g != nil {
				get.Parameters = append(get.Parameters, g)
			}
		}
		if setOfRows {
			get.Parameters = append(get.Parameters, countPreferParam())
		}
		if setOfRows {
			appendDescription(get, b.filterProse(rpc.Resource))
		}
		for k, v := range b.rpcResponses(rpc, true) {
			get.Responses[k] = v
		}
		if setOfRows {
			get.Responses["206"] = partialOf(get.Responses["200"])
			get.Responses["416"] = rangeNotSatisfiable()
		}
		item.Get = get
		if rt.Allows(MethodHead) {
			item.Head = headOf(get, "rpc."+rpc.Path+".head")
		}
	}
	if rt.Allows(MethodPost) {
		statuses := rpcPostStatus
		if !rpc.Mutation {
			statuses = rpcGetStatuses
		}
		post := b.operation(rt, "rpc."+rpc.Path+".post", "Call "+fn, statuses)
		post.Parameters = b.rpcListParams(rpc, nil)
		post.RequestBody = jsonBody(b.rpcBodySchema(rpc), rpcRequiresArgs(rpc), "Arguments by name; an omitted argument takes its DEFAULT (arguments without one are required, 400 missing_argument); null is SQL NULL.")
		if setOfRows {
			appendDescription(post, b.filterProse(rpc.Resource))
		}
		for k, v := range b.rpcResponses(rpc, false) {
			post.Responses[k] = v
		}
		item.Post = post
	}
}

// listParams are the read parameters of a collection: select, order, paging,
// count, distinct, the logic params, near (postgis), trgm (pg_trgm), vec
// (pgvector, cube), latlon (earthdistance) and one filter param per
// filterable column.
func (b *Builder) listParams(r *Resource, rpc bool) []*Parameter {
	out := []*Parameter{
		b.listSelectParam(r, "Columns and embeds to return."),
		b.orderParam(r),
		{Name: "limit", In: "query", Schema: &Schema{Type: "integer", Minimum: intPtr(1), Maximum: intPtr(b.maxPageSize())},
			Description: "Page size (default and maximum server.max_page_size)."},
		{Name: "offset", In: "query", Schema: &Schema{Type: "integer", Minimum: intPtr(0)}, Description: "Rows to skip."},
	}
	if r.Keyset && !rpc {
		out = append(out,
			&Parameter{Name: "after", In: "query", Schema: &Schema{Type: "string"}, Description: "Keyset cursor (X-Next-Cursor): the page after this row under the same order=. Combines with offset, not with before or distinct."},
			&Parameter{Name: "before", In: "query", Schema: &Schema{Type: "string"}, Description: "Keyset cursor (X-Prev-Cursor): the page before this row under the same order=."},
		)
	}
	out = append(out, &Parameter{Name: "count", In: "query", Schema: &Schema{Type: "boolean"}, Description: "count=true adds X-Total-Count (rows matching the filters, ignoring paging)."})
	if !rpc {
		out = append(out, &Parameter{Name: "distinct", In: "query", Schema: &Schema{Type: "string"},
			Description: "Comma list of orderable columns: DISTINCT ON those columns (they lead the ORDER BY). Not combinable with cursors."})
	}
	out = append(out, b.logicParams()...)
	if g := b.geometryParam(r); g != nil && !rpc {
		out = append(out, g)
	}
	if near := b.nearParam(r); near != nil {
		out = append(out, near)
	}
	if trgm := b.trgmParam(r); trgm != nil {
		out = append(out, trgm)
	}
	if vec := b.vecParam(r); vec != nil {
		out = append(out, vec)
	}
	if lev := b.levenshteinParam(r); lev != nil {
		out = append(out, lev)
	}
	if ll := b.latlonParam(r); ll != nil {
		out = append(out, ll)
	}
	return append(out, b.filterParams(r, nil)...)
}

// rpcListParams are the collection parameters a set-returning function
// accepts: the full read surface for SETOF <table> (filter params shadowed
// by an argument name are skipped: arguments win), limit/offset otherwise.
func (b *Builder) rpcListParams(rpc *RPC, shadow map[string]bool) []*Parameter {
	switch rpc.Returns {
	case ReturnSetOfRows:
		if rpc.Resource == nil {
			return nil
		}
		r := rpc.Resource
		out := []*Parameter{
			b.listSelectParam(r, "Columns and embeds of the returned rows."),
			b.orderParam(r),
			{Name: "limit", In: "query", Schema: &Schema{Type: "integer", Minimum: intPtr(1), Maximum: intPtr(b.maxPageSize())},
				Description: "Page size (default and maximum server.max_page_size)."},
			{Name: "offset", In: "query", Schema: &Schema{Type: "integer", Minimum: intPtr(0)}, Description: "Rows to skip (functions page by offset only)."},
			{Name: "count", In: "query", Schema: &Schema{Type: "boolean"}, Description: "count=true adds X-Total-Count."},
		}
		out = append(out, b.logicParams()...)
		return append(out, b.filterParams(r, shadow)...)
	case ReturnSetOfScalar, ReturnSetOfComposite:
		return []*Parameter{
			{Name: "limit", In: "query", Schema: &Schema{Type: "integer", Minimum: intPtr(1), Maximum: intPtr(b.maxPageSize())},
				Description: "Page size (default and maximum server.max_page_size)."},
			{Name: "offset", In: "query", Schema: &Schema{Type: "integer", Minimum: intPtr(0)}, Description: "Rows to skip."},
		}
	}
	return nil
}

func (b *Builder) maxPageSize() int {
	if b.Options.MaxPageSize > 0 {
		return b.Options.MaxPageSize
	}
	return 100
}

func (b *Builder) selectParam(r *Resource, lead string) *Parameter {
	var sb strings.Builder
	sb.WriteString(lead)
	sb.WriteString(" Comma list of items; item = column | alias:column | column->key | column->>key | column::type | embed | *. JSON paths (->key, ->0, ->-1, ->>key, keys double-quoted when they hold . , : ( ) ! or spaces; ->> last) apply to json, jsonb, array and composite columns and answer under the last key; casts take built-in scalar types (int, bigint, numeric, text, date, ...) and the API's enums. Columns: ")
	sb.WriteString(strings.Join(fieldKeys(r.Columns), ", "))
	if keys := b.hstoreKeys(r); len(keys) > 0 {
		sb.WriteString(". hstore columns (" + strings.Join(keys, ", ") + ") give the text value of one key as col->key (or col->>key)")
	}
	if len(r.Computed) > 0 {
		keys := sortedKeys(r.Computed)
		for i, k := range keys {
			keys[i] = k + computedArgSyntax(r.Computed[k])
		}
		sb.WriteString(". Computed (select explicitly): " + strings.Join(keys, ", "))
	}
	if derived := pseudoKeys(r, false); len(derived) > 0 {
		for i, k := range derived {
			derived[i] = k + r.Pseudo[k].SelectArgSyntax()
		}
		sb.WriteString(". Derived (select explicitly): " + strings.Join(derived, ", "))
	}
	if scores := pseudoKeys(r, true); len(scores) > 0 {
		for i, k := range scores {
			scores[i] = k + r.Pseudo[k].SelectArgSyntax()
		}
		sb.WriteString(". Scores against near=, trgm=, vec=, latlon= or levenshtein= (select explicitly; the parameter is required): " + strings.Join(scores, ", "))
	}
	if rasters := fieldKeys(r.Rasters); len(rasters) > 0 {
		sb.WriteString(". Raster columns (" + strings.Join(rasters, ", ") + ") are not readable (400 invalid_select): select their derived columns, or expose the bytes through a computed column")
	}
	if embeds := b.embedKeys(r); len(embeds) > 0 {
		sb.WriteString(". Embeds: " + strings.Join(embeds, ", ") + " (rel(...) with a nested select; rel!inner(...) drops parents without a match; ...rel(...) spreads the embed's items into the row: values for a to-one relation, one array per item for a to-many one; per-embed params rel.order=, rel.limit=, rel.offset=, rel.<col>=<op>.<value>)")
	}
	if b.Surface.SpatialProjection && hasSpatial(r) {
		sb.WriteString(". Spatial columns and their derived fields take projection args: col(transform=<srid>,simplify=<tolerance>,buffer=<distance>,precision=<0-15 digits>)")
	}
	sb.WriteString(".")
	return &Parameter{Name: "select", In: "query", Schema: &Schema{Type: "string"}, Description: sb.String()}
}

// listSelectParam is selectParam for a collection read: with
// server.aggregates it also documents the aggregate functions.
func (b *Builder) listSelectParam(r *Resource, lead string) *Parameter {
	p := b.selectParam(r, lead)
	if !b.Options.Aggregates {
		return p
	}
	var sb strings.Builder
	sb.WriteString(p.Description)
	sb.WriteString(" Aggregate functions (server.aggregates): count(), <col>.count(), <col>.sum(), <col>.avg(), <col>.max(), <col>.min()")
	if b.Surface.SpatialAggregates && hasSpatial(r) {
		sb.WriteString(", and on spatial columns <col>.extent(), <col>.union(), <col>.collect(), <col>.centroid() (GeoJSON)")
	}
	sb.WriteString("; alias:<col>.sum(), input cast <col>::<type>.sum() (also over a JSON path: <col>->key::numeric.sum()), output cast <col>.sum()::<type>. The other items of the same select (columns, computed fields, the items of to-one spreads ...rel(col); not embeds or to-many spreads) group the rows; aggregates also apply inside to-many embeds and to-one spreads. Casts: built-in scalar types (int, bigint, numeric, float8, text, date, timestamptz, interval, ...) and the API's enums. order= takes only grouping columns; no after/before or distinct.")
	p.Description = sb.String()
	return p
}

func (b *Builder) orderParam(r *Resource) *Parameter {
	var sb strings.Builder
	sb.WriteString("Comma list of <col>[.asc|.desc][.nullsfirst|.nullslast] (default asc, nulls last); json, jsonb and array columns also order by a JSON path (<col>->key, <col>->>key). Orderable columns: ")
	sb.WriteString(strings.Join(b.orderableKeys(r), ", "))
	var hs []string
	for _, k := range b.hstoreKeys(r) {
		if r.OrderColumns[k] {
			hs = append(hs, k+"->key")
		}
	}
	if len(hs) > 0 {
		sb.WriteString(". hstore values by key: " + strings.Join(hs, ", "))
	}
	if b.Surface.ComputedOrder {
		if keys := computedKeys(r, func(c *Computed) bool { return c.Orderable && !c.Function.ReturnsSet }); len(keys) > 0 {
			sb.WriteString(". Computed: " + strings.Join(keys, ", "))
		}
	}
	if b.Surface.RelationOrder {
		if keys := relationKeys(r, func(rel *Relation) bool { return rel.Orderable && rel.Embeddable }); len(keys) > 0 {
			sb.WriteString(". Related columns as rel(col) for: " + strings.Join(keys, ", "))
		}
	}
	if b.Surface.SpatialOrder {
		if keys := spatialOrderKeys(r); len(keys) > 0 {
			for i, k := range keys {
				keys[i] = k + ".dist"
			}
			sb.WriteString(". Distance ordering (needs near=<point>): " + strings.Join(keys, ", "))
		}
	}
	ranks, dists := b.contribOrderKeys(r)
	if len(ranks) > 0 {
		sb.WriteString(". Full-text relevance against the column's fts/plfts/phfts/wfts filter (higher is better, use .desc): " + strings.Join(ranks, ", "))
	}
	if len(dists["trgm"]) > 0 {
		sb.WriteString(". Trigram distance to trgm=<text> (lower is better): " + strings.Join(dists["trgm"], ", "))
	}
	if len(dists["vec"]) > 0 {
		sb.WriteString(". pgvector or cube distance to vec=<vector> (lower is closer; asc is nearest first): " + strings.Join(dists["vec"], ", "))
	}
	if len(dists["latlon"]) > 0 {
		sb.WriteString(". Distance on the Earth's surface to latlon=<lat>,<lon> (earth_dist in metres, earth_miles in statute miles; asc is nearest first): " + strings.Join(dists["latlon"], ", "))
	}
	if len(dists["levenshtein"]) > 0 {
		sb.WriteString(". Levenshtein edit distance to levenshtein=<text> (lower is closer): " + strings.Join(dists["levenshtein"], ", "))
	}
	if keys := pseudoOrderKeys(r); len(keys) > 0 {
		sb.WriteString(". Derived: " + strings.Join(keys, ", "))
	}
	sb.WriteString(".")
	return &Parameter{Name: "order", In: "query", Schema: &Schema{Type: "string"}, Description: sb.String()}
}

func (b *Builder) logicParams() []*Parameter {
	const grammar = ": comma list of items in parentheses, item = <col>.[not.]<op>.<value> | and(...) | or(...) | not.and(...) | not.or(...); values with commas or parentheses in double quotes."
	return []*Parameter{
		{Name: "and", In: "query", Schema: &Schema{Type: "string"}, Description: "Conjunction" + grammar + " Also the only way to filter a column named like a reserved parameter."},
		{Name: "or", In: "query", Schema: &Schema{Type: "string"}, Description: "Disjunction" + grammar},
		{Name: "not.and", In: "query", Schema: &Schema{Type: "string"}, Description: "Negated conjunction" + grammar},
		{Name: "not.or", In: "query", Schema: &Schema{Type: "string"}, Description: "Negated disjunction" + grammar},
	}
}

// pseudoKeys lists r's pseudo-columns, sorted: the score pseudo-columns
// (scores true) or the derived accessors.
func pseudoKeys(r *Resource, scores bool) []string {
	var out []string
	for _, k := range sortedKeys(r.Pseudo) {
		if (r.Pseudo[k].Score != nil) == scores {
			out = append(out, k)
		}
	}
	return out
}

// hasScore reports whether r has a score pseudo-column measured against
// param.
func hasScore(r *Resource, param string) bool {
	for _, pc := range r.Pseudo {
		if pc.Score != nil && pc.Score.Param == param {
			return true
		}
	}
	return false
}

// nearParam documents near= when the resource has a spatial column
// orderable by distance (with distance ordering on), a distance
// pseudo-column or a raster pixel value pseudo-column.
func (b *Builder) nearParam(r *Resource) *Parameter {
	if (!b.Surface.SpatialOrder || len(spatialOrderKeys(r)) == 0) && !hasScore(r, "near") {
		return nil
	}
	return &Parameter{Name: "near", In: "query", Schema: &Schema{Type: "string"},
		Description: "Reference point (GeoJSON or WKT/EWKT) for order=<col>.dist, the <col>_distance columns and the raster <col>_value columns; required whenever one is present."}
}

// latlonParam documents latlon= when the resource has a column orderable
// by an earthdistance distance, an earthdistance score or an earth_lt /
// earth_miles_lt filter.
func (b *Builder) latlonParam(r *Resource) *Parameter {
	if _, dists := b.contribOrderKeys(r); len(dists["latlon"]) == 0 && !hasScore(r, "latlon") && !b.hasThreshold(r, "latlon") {
		return nil
	}
	return &Parameter{Name: "latlon", In: "query", Schema: &Schema{Type: "string"},
		Description: "Reference location <lat>,<lon> in degrees (e.g. 51.508,-0.128) for order=<col>.earth_dist|earth_miles, the <col>_earth_distance and <col>_earth_distance_miles columns and the earth_lt / earth_miles_lt threshold filters (earthdistance); required whenever one is present."}
}

// trgmParam documents trgm= when the resource has a column orderable by
// trigram distance.
func (b *Builder) trgmParam(r *Resource) *Parameter {
	if _, dists := b.contribOrderKeys(r); len(dists["trgm"]) == 0 && !hasScore(r, "trgm") {
		return nil
	}
	return &Parameter{Name: "trgm", In: "query", Schema: &Schema{Type: "string"},
		Description: "Reference text for order=<col>.trgm_dist|trgm_wdist|trgm_swdist and the <col>_similarity, <col>_word_similarity and <col>_strict_word_similarity columns (pg_trgm); required whenever one is present."}
}

// vecParam documents vec= when the resource has a column orderable by
// vector distance.
func (b *Builder) vecParam(r *Resource) *Parameter {
	if _, dists := b.contribOrderKeys(r); len(dists["vec"]) == 0 && !hasScore(r, "vec") && !b.hasVectorThreshold(r) {
		return nil
	}
	return &Parameter{Name: "vec", In: "query", Schema: &Schema{Type: "string"},
		Description: "Reference vector for order=<col>.vec_l2|vec_ip|vec_cos|vec_l1|vec_chebyshev|vec_hamming|vec_jaccard, the vector and cube distance columns (<col>_cosine_distance, <col>_chebyshev_distance, ...) and the vec_*_lt threshold filters (pgvector, cube): a JSON array of numbers ([0.1,0.2,0.3]), for cube columns also a pair of corner arrays ([[0,0],[1,1]]), or the column type's text form (a bit string such as 1010 for bit columns, (1, 2) for cube); required whenever one is present."}
}

// levenshteinParam documents levenshtein= when the resource has a column
// orderable by Levenshtein distance, a Levenshtein score or a
// levenshtein_lt filter.
func (b *Builder) levenshteinParam(r *Resource) *Parameter {
	if _, dists := b.contribOrderKeys(r); len(dists["levenshtein"]) == 0 && !hasScore(r, "levenshtein") && !b.hasThreshold(r, "levenshtein") {
		return nil
	}
	return &Parameter{Name: "levenshtein", In: "query", Schema: &Schema{Type: "string"},
		Description: "Reference text (at most 255 characters) for order=<col>.levenshtein, the <col>_levenshtein_distance columns and the levenshtein_lt threshold filters (fuzzystrmatch); required whenever one is present."}
}

// hasVectorThreshold reports whether a filterable column of r takes a
// vec_*_lt threshold filter.
func (b *Builder) hasVectorThreshold(r *Resource) bool { return b.hasThreshold(r, "vec") }

// hasThreshold reports whether a filterable column of r takes a distance
// threshold filter measured against param (vec, latlon, levenshtein).
func (b *Builder) hasThreshold(r *Resource, param string) bool {
	for _, f := range r.Columns {
		if !r.FilterColumns[f.Key] {
			continue
		}
		for _, cf := range contribFilters(b.Catalog, b.Options, r, f.Column) {
			if ThresholdParam(cf.Name) == param {
				return true
			}
		}
	}
	return false
}

// pseudoOrderKeys lists r's orderable pseudo-columns, sorted.
func pseudoOrderKeys(r *Resource) []string {
	var out []string
	for _, k := range sortedKeys(r.Pseudo) {
		if r.Pseudo[k].Orderable {
			out = append(out, k)
		}
	}
	return out
}

// contribOrderKeys lists the contrib order terms of r's orderable columns:
// rank terms (columns that also take a full-text filter) and distance terms
// keyed by the query parameter they measure against (trgm, vec).
func (b *Builder) contribOrderKeys(r *Resource) (ranks []string, dists map[string][]string) {
	dists = map[string][]string{}
	for _, f := range r.Columns {
		if !r.OrderColumns[f.Key] {
			continue
		}
		for _, o := range contribOrders(b.Catalog, b.Options, r, f.Column) {
			switch {
			case o.Rank != "" && r.FilterColumns[f.Key]:
				ranks = append(ranks, f.Key+"."+o.Name)
			case o.Rank == "":
				dists[o.Param] = append(dists[o.Param], f.Key+"."+o.Name)
			}
		}
	}
	return ranks, dists
}

// filterParams renders one parameter per filterable column (catalog order)
// with the operator vocabulary in the description and x-pdbr-filter-ops.
func (b *Builder) filterParams(r *Resource, shadow map[string]bool) []*Parameter {
	var out []*Parameter
	for _, f := range r.Columns {
		if !r.FilterColumns[f.Key] || shadow[f.Key] {
			continue
		}
		ops := b.filterOps(r, f.Column)
		p := &Parameter{
			Name: f.Key, In: "query", Schema: &Schema{Type: "string"}, XFilterOps: ops,
			Description: "Filter: " + f.Key + "=<op>.<value> (" + typeLabel(f.Column) + "). Ops: " + strings.Join(ops, ", ") + ". Prefix not. to negate (" + f.Key + "=not." + ops[0] + ".<value>).",
			Deprecated:  f.Deprecated, XDeprecationReason: f.DeprecationReason,
		}
		if mods := modifiableOps(ops); len(mods) > 0 {
			p.Description += " Modifiers: " + strings.Join(mods, ", ") + " also take (any) or (all) with a {a,b} list (" + f.Key + "=" + mods[0] + "(any).{a,b})."
		}
		switch {
		case isHstore(b.Catalog, f.Column):
			p.Description += " Values by key: " + f.Key + "->key=<op>.<value> (or ->>key) compares the key's value as text (the text operators, match and imatch included)."
		case jsonPathFilterable(f.Column):
			p.Description += " JSON paths: " + f.Key + "->key=<op>.<value> compares jsonb (eq, neq, gt, gte, lt, lte, in, is, isdistinct, cs, cd, hk, jpe, jpm; JSON operands), " + f.Key + "->>key=<op>.<value> text (the text operators, match and imatch included)."
		}
		if f.Description != "" {
			p.Description = f.Description + " " + p.Description
		}
		out = append(out, p)
	}
	for _, f := range r.Rasters {
		if !r.FilterColumns[f.Key] || shadow[f.Key] || f.Column.IsArray {
			continue
		}
		ops := b.filterOps(r, f.Column)
		p := &Parameter{
			Name: f.Key, In: "query", Schema: &Schema{Type: "string"}, XFilterOps: ops,
			Description: "Filter: " + f.Key + "=<op>.<value> (raster). Ops: " + strings.Join(ops, ", ") + ". Prefix not. to negate.",
			Deprecated:  f.Deprecated, XDeprecationReason: f.DeprecationReason,
		}
		if containsString(ops, "intersects") {
			p.Description += " intersects.<geometry> (GeoJSON or WKT/EWKT, in the raster's SRID) holds when the raster's convex hull intersects the geometry."
		}
		if f.Description != "" {
			p.Description = f.Description + " " + p.Description
		}
		out = append(out, p)
	}
	for _, k := range sortedKeys(r.Pseudo) {
		pc := r.Pseudo[k]
		if !pc.Filterable || shadow[k] {
			continue
		}
		ops := pseudoFilterOps()
		out = append(out, &Parameter{
			Name: k, In: "query", Schema: &Schema{Type: "string"}, XFilterOps: ops,
			Description: "Filter on the derived " + k + " (integer; " + strings.TrimSuffix(pc.Description, ".") + "): " + k + "=<op>.<value>. Ops: " + strings.Join(ops, ", ") + ". Prefix not. to negate.",
		})
	}
	return out
}

// pseudoFilterOps is the operator vocabulary of a filterable (integer)
// pseudo-column.
func pseudoFilterOps() []string {
	return []string{"eq", "neq", "gt", "gte", "lt", "lte", "in", "is", "isdistinct"}
}

// hstoreKeys lists the keys of r's hstore columns, in catalog order.
func (b *Builder) hstoreKeys(r *Resource) []string {
	var out []string
	for _, f := range r.Columns {
		if isHstore(b.Catalog, f.Column) {
			out = append(out, f.Key)
		}
	}
	return out
}

// isHstore reports whether c is an hstore column of an installed hstore
// extension: its JSON-path grammar reads the value of one key.
func isHstore(cat *introspect.Catalog, c *introspect.Column) bool {
	v, ok := OwnerVocabulary(cat, c)
	return ok && v.Name == contrib.VocabHstore
}

// filterProse describes the filter forms that are not enumerated as
// parameters: computed-column filters, relation filters and per-embed
// parameters — each only when the surface is on and the resource offers it.
func (b *Builder) filterProse(r *Resource) string {
	var parts []string
	if b.Surface.ComputedFilters {
		if keys := computedKeys(r, func(c *Computed) bool { return c.Filterable && !c.Function.ReturnsSet }); len(keys) > 0 {
			parts = append(parts, "Computed-column filters (advanced-filters plugin): "+strings.Join(keys, ", ")+" as <computed>=<op>.<value> with the scalar operators of the return type.")
		}
	}
	if b.Surface.RelationFilters {
		if keys := relationKeys(r, func(rel *Relation) bool { return rel.Filterable && rel.Embeddable }); len(keys) > 0 {
			parts = append(parts, "Relation filters (advanced-filters plugin) on "+strings.Join(keys, ", ")+
				": <rel>.<col>=<op>.<value> (some related row matches), <rel>!none.<col>=…, <rel>!every.<col>=… (backward relations), <rel>=exists.true|false; hint ambiguous keys as <rel>!<fk>.")
		}
	}
	if len(parts) == 0 {
		return ""
	}
	return strings.Join(parts, " ")
}

func (b *Builder) uniqueProse(r *Resource) string {
	if len(r.Uniques) == 0 {
		return "This resource has no unique constraint."
	}
	sets := make([]string, 0, len(r.Uniques))
	for _, u := range r.Uniques {
		sets = append(sets, strings.Join(r.keys(u.Columns), ","))
	}
	return "Unique column sets: " + strings.Join(sets, "; ") + "."
}

// acceptParam documents the Accept header of a read: text/csv on lists when
// server.csv is on, application/geo+json when res has a spatial column.
func (b *Builder) acceptParam(list bool, res *Resource) []*Parameter {
	csv := list && b.Options.CSV
	geo := b.geoJSONAvailable(res)
	if !csv && !geo {
		return nil
	}
	enum := []string{mediaJSON}
	var desc []string
	if geo {
		enum = append(enum, mediaGeoJSON)
		desc = append(desc, "application/geo+json answers a GeoJSON FeatureCollection (a single Feature on a row route): the first spatial select item (or geometry=) is the geometry, the other items are the properties; 406 without a spatial select item.")
	}
	if csv {
		enum = append(enum, mediaCSV)
		desc = append(desc, "text/csv streams the rows as RFC 4180 CSV (header row in select= order; arrays/json/composites as JSON text; NULL empty); not available with embeds in select= (406).")
	}
	return []*Parameter{{Name: "Accept", In: "header", Schema: &Schema{Type: "string", Enum: enum}, Description: strings.Join(desc, " ")}}
}

// geoJSONAvailable reports whether application/geo+json is advertised for
// res: it has a spatial column and the postgis plugin contributed the
// Feature schemas.
func (b *Builder) geoJSONAvailable(res *Resource) bool {
	return res != nil && res.HasSpatial() && b.Extra.Schemas[GeoJSONFeature] != nil && b.Extra.Schemas[GeoJSONFeatureCollection] != nil
}

// geometryParam documents geometry=, the choice of the Feature geometry.
func (b *Builder) geometryParam(r *Resource) *Parameter {
	if !b.geoJSONAvailable(r) || r.NameTaken("geometry") {
		return nil
	}
	return &Parameter{Name: "geometry", In: "query", Schema: &Schema{Type: "string"},
		Description: "With Accept: application/geo+json: the response key of the spatial select item to use as the Feature geometry (default: the first spatial item); the other items become properties."}
}

// addGeoJSON adds the application/geo+json representation to a read response.
func (b *Builder) addGeoJSON(r *Response, res *Resource, schema string) {
	if r.Content != nil && b.geoJSONAvailable(res) {
		r.Content[mediaGeoJSON] = &MediaType{Schema: SchemaRef(schema)}
	}
}

func (b *Builder) ifMatchParam(r *Resource) []*Parameter {
	if !r.Etag {
		return nil
	}
	return []*Parameter{{Name: "If-Match", In: "header", Schema: &Schema{Type: "string"},
		Description: "Weak ETag(s) (or *) the row must still carry; a mismatch answers 412 etag_mismatch, a missing row 404."}}
}

// preferParam documents the Prefer header of a write; maxAffected adds the
// PATCH / DELETE row cap, count the bulk-write count= token.
func preferParam(maxAffected, count bool) *Parameter {
	desc := "Comma list of preferences. return=representation (default) | minimal | headers-only: minimal and headers-only answer 204 without a body (201 + Location for a single-object create). handling=lenient (default) ignores unknown or malformed preferences; handling=strict answers 400 invalid_preference. tx=commit | rollback ends the transaction accordingly (rollback still answers with the result; honoured only when server.tx_end is commit-allow-override or rollback-allow-override). timezone=<IANA name> sets the transaction's timezone (names from pg_timezone_names; unknown names are ignored)."
	if maxAffected {
		desc += " max-affected=<n> (with handling=strict): touching more than n rows answers 400 max_affected_exceeded and writes nothing."
	}
	if count {
		desc += " count=exact|planned|estimated puts the number of written rows into the Content-Range total."
	}
	return &Parameter{Name: "Prefer", In: "header", Schema: &Schema{Type: "string"}, Description: desc}
}

// columnsParam documents the columns= query parameter of POST, PUT and PATCH.
func columnsParam() *Parameter {
	return &Parameter{Name: "columns", In: "query", Schema: &Schema{Type: "string"},
		Description: "Comma list of body keys to use; any other key in the body is ignored. A listed key an element lacks takes the column DEFAULT (a bulk body inserts the listed columns in one statement)."}
}

// createPreferParam documents the Prefer header of a POST.
func createPreferParam() *Parameter {
	p := preferParam(false, false)
	p.Description += " partial=items (bulk body; honoured only when server.partial_bulk_writes is on) runs each element in its own savepoint: a failing element is reported without rolling back the others, and the response is the per-element report (207 when one failed). resolution=merge-duplicates upserts on the conflict target (the primary key or ?on_conflict=; needs the PUT method), resolution=ignore-duplicates skips conflicting rows (only inserted rows are returned; a skipped single object answers 204). missing=default is accepted and changes nothing: keys absent from a bulk element always take the column DEFAULT."
	return p
}

// putPreferParam documents the Prefer header of a PUT on the collection path.
func putPreferParam() *Parameter {
	p := preferParam(false, false)
	p.Description += " partial=items (bulk body; honoured only when server.partial_bulk_writes is on) runs each element in its own savepoint and answers the per-element report."
	return p
}

// partialReportResponse documents the 207 answer of a bulk write under
// Prefer: partial=items.
func partialReportResponse() *Response {
	item := &Schema{Type: "object", Required: []string{"index", "status"}, Properties: map[string]*Schema{
		"index":  {Type: "integer", Description: "Position of the element in the request body."},
		"status": {Type: "integer", Description: "The HTTP status the element would have had as a request of its own: 201 (200 for PUT), 204 for an element resolution=ignore-duplicates skipped, or the error status."},
		"row":    {Description: "The written row (absent under return=minimal and for a skipped or failed element)."},
		"code":   {Type: "string", Description: "Error code of a failed element, as in the problem document (for example pg:23505)."},
		"detail": {Type: "string", Description: "Error detail of a failed element."},
		"pg":     {Type: "object", Description: "Database error details (errors.detail: dev only)."},
	}}
	return &Response{
		Description: "Prefer: partial=items: the per-element report, 207 when at least one element failed (201, or 200 for PUT, when none did). Elements that did not fail are committed.",
		Headers:     writeHeaders(nil),
		Content: map[string]*MediaType{mediaJSON: {Schema: &Schema{Type: "object", Required: []string{"n", "failed", "items"}, Properties: map[string]*Schema{
			"n":      {Type: "integer", Description: "Elements that succeeded."},
			"failed": {Type: "integer", Description: "Elements that failed."},
			"items":  {Type: "array", Items: item},
		}}}},
	}
}

// keyedPutSchema is the body of PUT on a single-row path: the .upsert
// properties, with the path's key columns no longer required.
func (b *Builder) keyedPutSchema(r *Resource, key []string) *Schema {
	s := b.inputSchema(r, MethodPut)
	s.Description = "Upsert body for one " + r.Path + " row addressed by the path; key columns are optional and must match the path when present."
	isKey := map[string]bool{}
	for _, c := range key {
		if f := r.FieldByColumn(c); f != nil {
			isKey[f.Key] = true
		}
	}
	var req []string
	for _, k := range s.Required {
		if !isKey[k] {
			req = append(req, k)
		}
	}
	s.Required = req
	return s
}

// countPreferParam documents Prefer: count= on list reads.
func countPreferParam() *Parameter {
	return &Parameter{Name: "Prefer", In: "header", Schema: &Schema{Type: "string"},
		Description: "count=exact (like count=true) | planned (pg_class.reltuples) | estimated (exact up to server.max_page_size rows, planned above) adds the total to Content-Range and X-Total-Count; planned/estimated fall back to exact for filtered, distinct, !inner or function requests and for views and RLS-enabled tables (Preference-Applied names the mode used). handling=strict answers 400 invalid_preference for unknown or malformed preferences."}
}

// rangeParams documents the Range / Range-Unit request headers of list reads.
func rangeParams() []*Parameter {
	return []*Parameter{
		{Name: "Range", In: "header", Schema: &Schema{Type: "string"},
			Description: "Row window as <first>-<last> (inclusive, zero-based) or <first>- (PostgREST Range-Unit items): an alternative to offset/limit, intersected with them when both are given. The size is clamped to server.max_page_size. Answers 206 when a known total (count=) exceeds the returned rows and 416 when <first> is past it; an empty window is 400 invalid_range; not combinable with after/before."},
		{Name: "Range-Unit", In: "header", Schema: &Schema{Type: "string", Enum: []string{"items"}},
			Description: "Unit of the Range header; the Range header is ignored when this is present and not items."},
	}
}

// partialOf is the 206 twin of a list 200 response: same body, with the
// headers that tell a partial page apart (the rest match the 200).
func partialOf(ok *Response) *Response {
	return &Response{
		Description: "Partial Content: a count was requested and the returned rows are a strict subset of the total. Same body and further headers (Link, cursors, Preference-Applied) as 200.",
		Headers:     map[string]*Header{"Content-Range": ok.Headers["Content-Range"], "X-Total-Count": ok.Headers["X-Total-Count"]},
		Content:     ok.Content,
	}
}

func rangeNotSatisfiable() *Response {
	return &Response{Ref: "#/components/responses/" + rangeNotSatisfiableResponse}
}

// contentRangeHeader documents Content-Range on list responses.
func contentRangeHeader() *Header {
	return &Header{Schema: &Schema{Type: "string"}, Description: "<first>-<last>/<total> of the returned rows by position (offset-based; omitted on after=/before= pages); * for an empty page or an unknown total (no count requested)."}
}

func (b *Builder) listHeaders(keyset bool) map[string]*Header {
	h := map[string]*Header{
		"X-Total-Count":      {Schema: &Schema{Type: "integer"}, Description: "Total rows after filters; present with count=true or Prefer: count=."},
		"Content-Range":      contentRangeHeader(),
		"Link":               {Schema: &Schema{Type: "string"}, Description: `<url>; rel="next", <url>; rel="prev" — the request URL with after=/before= (or offset=) replaced.`},
		"Preference-Applied": {Schema: &Schema{Type: "string"}, Description: "The Prefer tokens that were honoured (count=<mode used>, handling=)."},
	}
	if keyset {
		h["X-Next-Cursor"] = &Header{Schema: &Schema{Type: "string"}, Description: "Cursor of the last row when a next page exists (after=)."}
		h["X-Prev-Cursor"] = &Header{Schema: &Schema{Type: "string"}, Description: "Cursor of the first row when a previous page exists (before=)."}
	}
	return h
}

func (b *Builder) addCSV(r *Response) {
	if b.Options.CSV && r.Content != nil {
		r.Content[mediaCSV] = &MediaType{Schema: &Schema{Type: "string"}}
	}
}

func (b *Builder) pathParams(r *Resource, cols []string) []*Parameter {
	out := make([]*Parameter, 0, len(cols))
	for _, c := range cols {
		f := r.FieldByColumn(c)
		if f == nil {
			out = append(out, &Parameter{Name: c, In: "path", Required: true, Schema: &Schema{Type: "string"}})
			continue
		}
		s := b.colTypeSchema(f.Column)
		out = append(out, &Parameter{Name: f.Key, In: "path", Required: true, Schema: s, Description: "Percent-encoded value; composite keys are comma-separated in key order."})
	}
	return out
}

// argParam documents one function argument as a GET query parameter:
// scalars keep their type, structured values (composites, arrays, json,
// GeoJSON) travel as JSON text, hstore as its literal text.
func (b *Builder) argParam(a introspect.FuncArg) *Parameter {
	col := ArgColumn(b.Catalog, a)
	if a.Variadic() {
		col.PGType, col.IsArray = strings.TrimPrefix(col.PGType, "_"), false
	}
	s := b.colTypeSchema(col)
	desc := "Argument (" + a.PGType + ")" + argDefaultProse(a)
	switch {
	case s.Type == "object":
		s = &Schema{Type: "string"}
		desc += " As literal text (hstore: a=>1, b=>2)."
	case s.Ref != "" && b.enumByRef(s.Ref) == nil && b.domainByRef(s.Ref) == nil || s.Type == "array" || (s.Type == nil && s.Ref == ""):
		s = &Schema{Type: "string"}
		desc += " Structured value as JSON text (spatial: GeoJSON or WKT)."
	}
	p := &Parameter{Name: a.Name, In: "query", Required: !a.HasDefault, Schema: s, Description: desc}
	if a.Variadic() {
		p.Schema = &Schema{Type: "array", Items: s}
		p.Style, p.Explode = "form", boolPtr(true)
		p.Description += " VARIADIC: repeat the parameter once per element."
	}
	return p
}

// rowArgSchema is the body schema of a row-typed argument: a reference to
// the resource's .row component, or an array of them.
func rowArgSchema(a introspect.FuncArg, row *Resource) *Schema {
	ref := SchemaRef(row.Path + ".row")
	if strings.HasPrefix(a.PGType, "_") {
		return &Schema{Type: "array", Items: Nullable(ref)}
	}
	return ref
}

// rowArgParam documents a row-typed GET argument, which travels as JSON
// text (one object per repetition for a VARIADIC argument).
func (b *Builder) rowArgParam(p *Parameter, a introspect.FuncArg, row *Resource) {
	value := "A " + row.Path + " row as JSON text: an object"
	if strings.HasPrefix(a.PGType, "_") && !a.Variadic() {
		value = "An array of " + row.Path + " rows as JSON text: objects"
	}
	p.Description = "Argument (" + a.PGType + ")" + argDefaultProse(a) + " " + value +
		" keyed by the readable columns, all optional (schema " + row.Path + ".row)."
	if a.Variadic() {
		p.Description += " VARIADIC: repeat the parameter once per element."
	}
}

// rowInputSchema is the <resource>.row component: the value of a function
// argument typed as the resource's row type. Every readable column is an
// optional property; an omitted one is NULL in the row the function gets.
func (b *Builder) rowInputSchema(r *Resource) *Schema {
	s := &Schema{Type: "object", Properties: map[string]*Schema{}, AdditionalProperties: boolPtr(false),
		Description: "A " + r.Path + " row passed to a function argument: any subset of its readable columns; omitted columns are null."}
	for _, f := range r.Columns {
		s.Properties[f.Key] = annotate(Nullable(b.colInputTypeSchema(f.Column)), f)
	}
	return s
}

// argDefaultProse completes an argument description: required, or the
// DEFAULT an omitted argument takes.
func argDefaultProse(a introspect.FuncArg) string {
	switch {
	case !a.HasDefault:
		return "; required."
	case a.Default != "":
		return "; optional, default " + a.Default + "."
	}
	return "; optional (has a default)."
}

// domainByRef returns the domain whose component a $ref names, or nil;
// only a domain over a scalar base type qualifies, an array-valued one
// travels as JSON text like other arrays.
func (b *Builder) domainByRef(ref string) *Domain {
	name := strings.TrimPrefix(ref, "#/components/schemas/")
	for _, d := range b.Domains {
		if d.Name == name && !d.Domain.IsArrayBase {
			return d
		}
	}
	return nil
}

func (b *Builder) enumByRef(ref string) *Enum {
	name := strings.TrimPrefix(ref, "#/components/schemas/")
	for _, e := range b.Enums {
		if e.Name == name {
			return e
		}
	}
	return nil
}

func (b *Builder) rpcBodySchema(rpc *RPC) *Schema {
	s := &Schema{Type: "object", Properties: map[string]*Schema{}, AdditionalProperties: boolPtr(false)}
	for _, a := range rpc.Args {
		var prop Schema
		if row := rpc.RowArgs[a.Name]; row != nil {
			prop = *Nullable(rowArgSchema(a, row))
		} else {
			prop = *Nullable(b.colInputTypeSchema(ArgColumn(b.Catalog, a)))
		}
		if a.HasDefault {
			prop.Description = strings.TrimPrefix(argDefaultProse(a), "; ")
		} else {
			s.Required = append(s.Required, a.Name)
		}
		if a.Variadic() {
			prop.Description = strings.TrimSpace(prop.Description + " VARIADIC: a JSON array of the elements.")
		}
		s.Properties[a.Name] = &prop
	}
	return s
}

// rpcRequiresArgs reports whether any argument lacks a DEFAULT.
func rpcRequiresArgs(rpc *RPC) bool {
	for _, a := range rpc.Args {
		if !a.HasDefault {
			return true
		}
	}
	return false
}

// rpcResponses renders the success response per return kind (docs/rpc.md).
func (b *Builder) rpcResponses(rpc *RPC, get bool) map[string]*Response {
	f := rpc.Function
	ok := func(s *Schema) map[string]*Response {
		return map[string]*Response{"200": {Description: "OK", Content: map[string]*MediaType{mediaJSON: {Schema: s}}}}
	}
	list := func(s *Schema, headers map[string]*Header) map[string]*Response {
		out := ok(&Schema{Type: "array", Items: s})
		out["200"].Headers = headers
		if get {
			b.addCSV(out["200"])
		}
		return out
	}
	geo := func(out map[string]*Response, schema string) map[string]*Response {
		if get {
			b.addGeoJSON(out["200"], rpc.Resource, schema)
		}
		return out
	}
	if rpc.Mint {
		jwtSchema := &Schema{Type: "string", Format: "jwt", Description: "Signed HS256 JWT minted from the returned " + rpc.Function.ReturnType + " row."}
		if rpc.Returns == ReturnSetOfComposite {
			return list(jwtSchema, nil)
		}
		return ok(Nullable(jwtSchema))
	}
	switch rpc.Returns {
	case ReturnVoid:
		return map[string]*Response{"204": {Description: "No Content (void function)."}}
	case ReturnRow:
		if rpc.Record {
			return geo(ok(SchemaRef(rpc.Resource.Path)), GeoJSONFeature)
		}
		return geo(ok(Nullable(SchemaRef(rpc.Resource.Path))), GeoJSONFeature)
	case ReturnSetOfRows:
		return geo(list(SchemaRef(rpc.Resource.Path), map[string]*Header{
			"X-Total-Count": {Schema: &Schema{Type: "integer"}, Description: "Total rows after filters; present with count=true or Prefer: count=."},
			"Content-Range": contentRangeHeader(),
			"Link":          {Schema: &Schema{Type: "string"}, Description: `offset= links: <url>; rel="next", <url>; rel="prev".`},
		}), GeoJSONFeatureCollection)
	case ReturnComposite:
		return ok(Nullable(SchemaRef(rpc.Composite.Name)))
	case ReturnSetOfComposite:
		return list(SchemaRef(rpc.Composite.Name), nil)
	case ReturnSetOfScalar:
		return list(b.returnTypeSchema(f), nil)
	default:
		return ok(Nullable(b.returnTypeSchema(f)))
	}
}

func typeLabel(c *introspect.Column) string {
	if elem, ok := strings.CutPrefix(c.PGType, "_"); ok {
		return elem + "[]"
	}
	return c.PGType
}

func fieldKeys(fs []*Field) []string {
	out := make([]string, len(fs))
	for i, f := range fs {
		out[i] = f.Key
	}
	return out
}

func (b *Builder) orderableKeys(r *Resource) []string {
	var out []string
	for _, f := range r.Columns {
		if _, vector := DistanceOnly(b.Catalog, f.Column); r.OrderColumns[f.Key] && !vector {
			out = append(out, f.Key)
		}
	}
	return out
}

func computedKeys(r *Resource, keep func(*Computed) bool) []string {
	var out []string
	for _, key := range sortedKeys(r.Computed) {
		if keep(r.Computed[key]) {
			out = append(out, key)
		}
	}
	return out
}

func relationKeys(r *Resource, keep func(*Relation) bool) []string {
	var out []string
	for _, key := range sortedKeys(r.Relations) {
		for _, rel := range r.Relations[key] {
			if keep(rel) {
				out = append(out, key)
				break
			}
		}
	}
	return out
}

func (b *Builder) embedKeys(r *Resource) []string {
	return relationKeys(r, func(rel *Relation) bool { return rel.Embeddable })
}

func hasSpatial(r *Resource) bool {
	for _, f := range r.Columns {
		if postgis.FlavourOf(strings.TrimPrefix(f.Column.PGType, "_")).Spatial() {
			return true
		}
	}
	return false
}

func spatialOrderKeys(r *Resource) []string {
	if r.NameTaken("near") {
		return nil
	}
	var out []string
	for _, f := range r.Columns {
		if r.OrderColumns[f.Key] && !f.Column.IsArray && postgis.FlavourOf(f.Column.PGType).Spatial() {
			out = append(out, f.Key)
		}
	}
	return out
}

// jsonPathFilterable reports whether filters and order terms take JSON
// paths on c: json, jsonb and (non-spatial) array columns.
func jsonPathFilterable(c *introspect.Column) bool {
	base := strings.TrimPrefix(c.PGType, "_")
	if c.IsArray || strings.HasPrefix(c.PGType, "_") {
		return !postgis.FlavourOf(base).Spatial()
	}
	return base == "json" || base == "jsonb"
}

// modifiableOps lists the operators of ops that take the any/all modifier.
func modifiableOps(ops []string) []string {
	var out []string
	for _, op := range ops {
		switch op {
		case "eq", "gt", "gte", "lt", "lte", "like", "ilike", "match", "imatch":
			out = append(out, op)
		}
	}
	return out
}

func (r *RPC) hasArg(name string) bool {
	for _, a := range r.Args {
		if a.Name == name {
			return true
		}
	}
	return false
}
