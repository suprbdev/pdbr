package api

import (
	"strconv"
	"strings"

	"github.com/suprbdev/pdbcore/introspect"
	"github.com/suprbdev/pdbcore/postgis"
)

const (
	problemResponse          = "Problem"
	methodNotAllowedResponse = "MethodNotAllowed"
	mediaJSON                = "application/json"
	mediaCSV                 = "text/csv"
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
		get.Parameters = append(b.listParams(r, false), b.acceptParam()...)
		appendDescription(get, b.filterProse(r))
		ok := &Response{Description: "OK", Headers: b.listHeaders(r.Keyset), Content: map[string]*MediaType{
			mediaJSON: {Schema: &Schema{Type: "array", Items: SchemaRef(r.Path)}},
		}}
		b.addCSV(ok)
		get.Responses["200"] = ok
		item.Get = get
		if rt.Allows(MethodHead) {
			item.Head = headOf(get, r.Path+".list.head")
		}
	}
	if rt.Allows(MethodPost) {
		post := b.operation(rt, r.Path+".create", "Create "+r.Path, createStatuses)
		post.Parameters = []*Parameter{b.selectParam(r, "Columns and embeds of the created row(s) to return."), preferParam(false)}
		post.RequestBody = jsonBody(objectOrArray(r.Path+".insert"), true, "One row, or an array of rows for a bulk insert (one INSERT, columns = union, missing keys take DEFAULT).")
		post.Responses["201"] = &Response{Description: "Created (Prefer: return=minimal omits the body).",
			Headers: writeHeaders(map[string]*Header{"Location": {Schema: &Schema{Type: "string"}, Description: "URL of the new row for a single-object body on a resource with a single-column primary key."}}),
			Content: map[string]*MediaType{mediaJSON: {Schema: &Schema{OneOf: []*Schema{SchemaRef(r.Path), {Type: "array", Items: SchemaRef(r.Path)}}}}},
		}
		item.Post = post
	}
	if rt.Allows(MethodPut) {
		put := b.operation(rt, r.Path+".upsert", "Upsert "+r.Path, createStatuses)
		put.Parameters = []*Parameter{
			b.selectParam(r, "Columns and embeds of the affected row(s) to return."),
			{Name: "on_conflict", In: "query", Schema: &Schema{Type: "string"},
				Description: "Conflict target: comma list equal to a unique constraint's columns (default: the primary key). " + b.uniqueProse(r)},
			preferParam(false),
		}
		put.RequestBody = jsonBody(objectOrArray(r.Path+".upsert"), true, "One row, or an array of rows; the conflict-target columns must be present in every element.")
		put.Responses["200"] = &Response{Description: "Updated (single object that hit an existing row, or any array).", Headers: writeHeaders(nil),
			Content: map[string]*MediaType{mediaJSON: {Schema: &Schema{OneOf: []*Schema{SchemaRef(r.Path), {Type: "array", Items: SchemaRef(r.Path)}}}}}}
		put.Responses["201"] = &Response{Description: "Inserted (single object that created a row; Prefer: return=minimal omits the body).",
			Headers: writeHeaders(map[string]*Header{"Location": {Schema: &Schema{Type: "string"}, Description: "URL of the new row when the resource has a single-column primary key."}}),
			Content: map[string]*MediaType{mediaJSON: {Schema: SchemaRef(r.Path)}}}
		put.Responses["204"] = &Response{Description: "Updated with Prefer: return=minimal.", Headers: writeHeaders(nil)}
		item.Put = put
	}
	if rt.Allows(MethodPatch) {
		patch := b.operation(rt, r.Path+".bulk_update", "Update the "+r.Path+" rows matching the filters", createStatuses)
		patch.Parameters = append([]*Parameter{b.selectParam(r, "Columns and embeds of the updated rows to return."), preferParam(true)}, b.bulkParams(r)...)
		appendDescription(patch, bulkProse)
		appendDescription(patch, b.filterProse(r))
		patch.RequestBody = jsonBody(SchemaRef(r.Path+".update"), true, "Columns to set on every matching row.")
		patch.Responses["200"] = &Response{Description: "Updated (the changed rows; an empty array when nothing matched).", Headers: writeHeaders(nil),
			Content: map[string]*MediaType{mediaJSON: {Schema: &Schema{Type: "array", Items: SchemaRef(r.Path)}}}}
		patch.Responses["204"] = &Response{Description: "Updated with Prefer: return=minimal.", Headers: writeHeaders(nil)}
		item.Patch = patch
	}
	if rt.Allows(MethodDelete) {
		del := b.operation(rt, r.Path+".bulk_delete", "Delete the "+r.Path+" rows matching the filters", bulkDeleteStatuses)
		del.Parameters = append([]*Parameter{b.selectParam(r, "Columns and embeds of the deleted rows to return."), preferParam(true)}, b.bulkParams(r)...)
		appendDescription(del, bulkProse)
		appendDescription(del, b.filterProse(r))
		del.Responses["200"] = &Response{Description: "Deleted (the removed rows; an empty array when nothing matched).", Headers: writeHeaders(nil),
			Content: map[string]*MediaType{mediaJSON: {Schema: &Schema{Type: "array", Items: SchemaRef(r.Path)}}}}
		del.Responses["204"] = &Response{Description: "Deleted with Prefer: return=minimal.", Headers: writeHeaders(nil)}
		item.Delete = del
	}
}

const bulkProse = "The filters select the rows (same grammar and filter policy as the list GET); a request without any filter answers 400 unfiltered_mutation unless server.allow_unfiltered_mutations is on."

// bulkParams are the row-selecting parameters of a bulk PATCH / DELETE: the
// logic params and one filter param per filterable column.
func (b *Builder) bulkParams(r *Resource) []*Parameter {
	return append(b.logicParams(), b.filterParams(r, nil)...)
}

// writeHeaders adds Preference-Applied to a write response's headers.
func writeHeaders(h map[string]*Header) map[string]*Header {
	out := map[string]*Header{"Preference-Applied": {Schema: &Schema{Type: "string"}, Description: "The Prefer tokens that were honoured (return=, handling=, max-affected=), comma-separated."}}
	for k, v := range h {
		out[k] = v
	}
	return out
}

func (b *Builder) rowItem(item *PathItem, rt *Route) {
	r := rt.Resource
	var cols []string
	// operationIds: <name>.get|update|delete on the primary-key path,
	// <name>.by_<cols>[.update|.delete] on unique-constraint paths.
	id, getID := r.Path, r.Path+".get"
	what := "by primary key"
	if rt.Kind == RouteRow {
		cols = r.Table.PrimaryKey.Columns
	} else {
		cols = rt.Unique.Columns
		id = r.Path + ".by_" + strings.Join(r.keys(cols), "_")
		getID = id
		what = "by unique constraint " + rt.Unique.Name
	}
	item.Parameters = b.pathParams(r, cols)
	if rt.Allows(MethodGet) {
		get := b.operation(rt, getID, "Get one "+r.Path+" "+what, rowStatuses)
		get.Parameters = []*Parameter{b.selectParam(r, "Columns and embeds to return.")}
		ok := &Response{Description: "OK", Content: map[string]*MediaType{mediaJSON: {Schema: SchemaRef(r.Path)}}}
		if r.Etag {
			get.Parameters = append(get.Parameters, &Parameter{Name: "If-None-Match", In: "header", Schema: &Schema{Type: "string"},
				Description: "Weak ETag(s) (or *) from an earlier response; a match answers 304."})
			ok.Headers = map[string]*Header{"ETag": {Schema: &Schema{Type: "string"}, Description: `Weak row hash W/"<md5>" over all columns, independent of select=.`}}
			get.Responses["304"] = &Response{Description: "Not Modified (If-None-Match matched)."}
		}
		get.Responses["200"] = ok
		item.Get = get
		if rt.Allows(MethodHead) {
			item.Head = headOf(get, getID+".head")
		}
	}
	if rt.Allows(MethodPatch) {
		patch := b.operation(rt, id+".update", "Update one "+r.Path+" "+what, updateStatuses)
		patch.Parameters = append([]*Parameter{b.selectParam(r, "Columns and embeds of the updated row to return.")}, b.ifMatchParam(r)...)
		patch.Parameters = append(patch.Parameters, preferParam(true))
		patch.RequestBody = jsonBody(SchemaRef(r.Path+".update"), true, "Columns to change (partial update).")
		patch.Responses["200"] = &Response{Description: "Updated", Headers: writeHeaders(nil), Content: map[string]*MediaType{mediaJSON: {Schema: SchemaRef(r.Path)}}}
		patch.Responses["204"] = &Response{Description: "Updated with Prefer: return=minimal.", Headers: writeHeaders(nil)}
		item.Patch = patch
	}
	if rt.Allows(MethodDelete) {
		del := b.operation(rt, id+".delete", "Delete one "+r.Path+" "+what, deleteStatuses)
		del.Parameters = append([]*Parameter{b.selectParam(r, "Columns and embeds of the deleted row to return.")}, b.ifMatchParam(r)...)
		del.Parameters = append(del.Parameters, preferParam(true))
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
			get.Parameters = append(get.Parameters, b.argParam(a))
		}
		get.Parameters = append(get.Parameters, b.rpcListParams(rpc, argNames)...)
		if setOf {
			get.Parameters = append(get.Parameters, b.acceptParam()...)
		}
		if setOfRows {
			appendDescription(get, b.filterProse(rpc.Resource))
		}
		for k, v := range b.rpcResponses(rpc, true) {
			get.Responses[k] = v
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
// count, distinct, the logic params, near (postgis) and one filter param per
// filterable column.
func (b *Builder) listParams(r *Resource, rpc bool) []*Parameter {
	out := []*Parameter{
		b.selectParam(r, "Columns and embeds to return."),
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
	if near := b.nearParam(r); near != nil {
		out = append(out, near)
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
			b.selectParam(r, "Columns and embeds of the returned rows."),
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
	sb.WriteString(" Comma list of items; item = column | alias:column | embed | *. Columns: ")
	sb.WriteString(strings.Join(fieldKeys(r.Columns), ", "))
	if len(r.Computed) > 0 {
		sb.WriteString(". Computed (select explicitly): " + strings.Join(sortedKeys(r.Computed), ", "))
	}
	if len(r.Pseudo) > 0 {
		sb.WriteString(". Derived (postgis, select explicitly): " + strings.Join(sortedKeys(r.Pseudo), ", "))
	}
	if embeds := b.embedKeys(r); len(embeds) > 0 {
		sb.WriteString(". Embeds: " + strings.Join(embeds, ", ") + " (rel(...) with a nested select; rel!inner(...) drops parents without a match; per-embed params rel.order=, rel.limit=, rel.offset=, rel.<col>=<op>.<value>)")
	}
	if b.Surface.SpatialProjection && hasSpatial(r) {
		sb.WriteString(". Spatial columns and their derived fields take projection args: col(transform=<srid>,simplify=<tolerance>)")
	}
	sb.WriteString(".")
	return &Parameter{Name: "select", In: "query", Schema: &Schema{Type: "string"}, Description: sb.String()}
}

func (b *Builder) orderParam(r *Resource) *Parameter {
	var sb strings.Builder
	sb.WriteString("Comma list of <col>[.asc|.desc][.nullsfirst|.nullslast] (default asc, nulls last). Orderable columns: ")
	sb.WriteString(strings.Join(orderableKeys(r), ", "))
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

// nearParam documents near= when distance ordering is on and the resource
// has an orderable spatial column.
func (b *Builder) nearParam(r *Resource) *Parameter {
	if !b.Surface.SpatialOrder || len(spatialOrderKeys(r)) == 0 {
		return nil
	}
	return &Parameter{Name: "near", In: "query", Schema: &Schema{Type: "string"},
		Description: "Reference point (GeoJSON or WKT/EWKT) for order=<col>.dist; required whenever a .dist term is present."}
}

// filterParams renders one parameter per filterable column (catalog order)
// with the operator vocabulary in the description and x-pdbr-filter-ops.
func (b *Builder) filterParams(r *Resource, shadow map[string]bool) []*Parameter {
	var out []*Parameter
	for _, f := range r.Columns {
		if !r.FilterColumns[f.Key] || shadow[f.Key] {
			continue
		}
		ops := b.filterOps(f.Column)
		p := &Parameter{
			Name: f.Key, In: "query", Schema: &Schema{Type: "string"}, XFilterOps: ops,
			Description: "Filter: " + f.Key + "=<op>.<value> (" + typeLabel(f.Column) + "). Ops: " + strings.Join(ops, ", ") + ". Prefix not. to negate (" + f.Key + "=not." + ops[0] + ".<value>).",
			Deprecated:  f.Deprecated, XDeprecationReason: f.DeprecationReason,
		}
		if f.Description != "" {
			p.Description = f.Description + " " + p.Description
		}
		out = append(out, p)
	}
	return out
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

func (b *Builder) acceptParam() []*Parameter {
	if !b.Options.CSV {
		return nil
	}
	return []*Parameter{{Name: "Accept", In: "header", Schema: &Schema{Type: "string", Enum: []string{mediaJSON, mediaCSV}},
		Description: "text/csv streams the rows as RFC 4180 CSV (header row in select= order; arrays/json/composites as JSON text; NULL empty); not available with embeds in select= (406)."}}
}

func (b *Builder) ifMatchParam(r *Resource) []*Parameter {
	if !r.Etag {
		return nil
	}
	return []*Parameter{{Name: "If-Match", In: "header", Schema: &Schema{Type: "string"},
		Description: "Weak ETag(s) (or *) the row must still carry; a mismatch answers 412 etag_mismatch, a missing row 404."}}
}

// preferParam documents the Prefer header of a write; maxAffected adds the
// PATCH / DELETE row cap.
func preferParam(maxAffected bool) *Parameter {
	desc := "Comma list of preferences. return=representation (default) | minimal | headers-only: minimal and headers-only answer 204 without a body (201 + Location for a single-object create). handling=lenient (default) ignores unknown or malformed preferences; handling=strict answers 400 invalid_preference."
	if maxAffected {
		desc += " max-affected=<n> (with handling=strict): touching more than n rows answers 400 max_affected_exceeded and writes nothing."
	}
	return &Parameter{Name: "Prefer", In: "header", Schema: &Schema{Type: "string"}, Description: desc}
}

func (b *Builder) listHeaders(keyset bool) map[string]*Header {
	h := map[string]*Header{
		"X-Total-Count": {Schema: &Schema{Type: "integer"}, Description: "Total rows after filters; present with count=true."},
		"Link":          {Schema: &Schema{Type: "string"}, Description: `<url>; rel="next", <url>; rel="prev" — the request URL with after=/before= (or offset=) replaced.`},
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
		s := b.typeSchema(f.Column.PGType, f.Column.TypeSchema, f.Column.EnumRef)
		out = append(out, &Parameter{Name: f.Key, In: "path", Required: true, Schema: s, Description: "Percent-encoded value; composite keys are comma-separated in key order."})
	}
	return out
}

// argParam documents one function argument as a GET query parameter:
// scalars keep their type, structured values (composites, arrays, json,
// GeoJSON) travel as text.
func (b *Builder) argParam(a introspect.FuncArg) *Parameter {
	pgType := a.PGType
	if a.Variadic() {
		pgType = strings.TrimPrefix(pgType, "_")
	}
	s := b.typeSchema(pgType, a.TypeSchema, "")
	desc := "Argument (" + a.PGType + ")" + argDefaultProse(a)
	if s.Ref != "" && b.enumByRef(s.Ref) == nil || s.Type == "array" || (s.Type == nil && s.Ref == "") {
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
		prop := *Nullable(b.inputTypeSchema(a.PGType, a.TypeSchema, ""))
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
			return ok(SchemaRef(rpc.Resource.Path))
		}
		return ok(Nullable(SchemaRef(rpc.Resource.Path)))
	case ReturnSetOfRows:
		return list(SchemaRef(rpc.Resource.Path), map[string]*Header{
			"X-Total-Count": {Schema: &Schema{Type: "integer"}, Description: "Total rows after filters; present with count=true."},
			"Link":          {Schema: &Schema{Type: "string"}, Description: `offset= links: <url>; rel="next", <url>; rel="prev".`},
		})
	case ReturnComposite:
		return ok(Nullable(SchemaRef(rpc.Composite.Name)))
	case ReturnSetOfComposite:
		return list(SchemaRef(rpc.Composite.Name), nil)
	case ReturnSetOfScalar:
		return list(b.typeSchema(f.ReturnType, f.ReturnTypeSchema, ""), nil)
	default:
		return ok(Nullable(b.typeSchema(f.ReturnType, f.ReturnTypeSchema, "")))
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

func orderableKeys(r *Resource) []string {
	var out []string
	for _, f := range r.Columns {
		if r.OrderColumns[f.Key] {
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
	var out []string
	for _, f := range r.Columns {
		if r.OrderColumns[f.Key] && !f.Column.IsArray && postgis.FlavourOf(f.Column.PGType).Spatial() {
			out = append(out, f.Key)
		}
	}
	return out
}
