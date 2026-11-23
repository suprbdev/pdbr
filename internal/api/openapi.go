package api

import (
	"bytes"
	"encoding/json"
	"slices"
	"sort"
	"strings"

	"github.com/suprbdev/pdbcore/contrib"
	"github.com/suprbdev/pdbcore/introspect"
	"github.com/suprbdev/pdbcore/postgis"
)

// Document is the OpenAPI 3.1 document. Field order and sorted map keys make
// the JSON rendering deterministic.
type Document struct {
	OpenAPI    string                `json:"openapi"`
	Info       Info                  `json:"info"`
	Servers    []Server              `json:"servers"`
	Tags       []Tag                 `json:"tags,omitempty"`
	Paths      map[string]*PathItem  `json:"paths"`
	Components *Components           `json:"components,omitempty"`
	Security   []map[string][]string `json:"security,omitempty"`
}

type Info struct {
	Title       string `json:"title"`
	Version     string `json:"version"`
	Description string `json:"description,omitempty"`
}

type Server struct {
	URL string `json:"url"`
}

type Tag struct {
	Name        string `json:"name"`
	Description string `json:"description,omitempty"`
}

type PathItem struct {
	Get        *Operation   `json:"get,omitempty"`
	Head       *Operation   `json:"head,omitempty"`
	Post       *Operation   `json:"post,omitempty"`
	Put        *Operation   `json:"put,omitempty"`
	Patch      *Operation   `json:"patch,omitempty"`
	Delete     *Operation   `json:"delete,omitempty"`
	Parameters []*Parameter `json:"parameters,omitempty"`
}

type Operation struct {
	Tags        []string             `json:"tags,omitempty"`
	Summary     string               `json:"summary,omitempty"`
	Description string               `json:"description,omitempty"`
	OperationID string               `json:"operationId,omitempty"`
	Deprecated  bool                 `json:"deprecated,omitempty"`
	Parameters  []*Parameter         `json:"parameters,omitempty"`
	RequestBody *RequestBody         `json:"requestBody,omitempty"`
	Responses   map[string]*Response `json:"responses"`

	XDeprecationReason string `json:"x-deprecation-reason,omitempty"`
}

type Parameter struct {
	Ref         string  `json:"$ref,omitempty"`
	Name        string  `json:"name,omitempty"`
	In          string  `json:"in,omitempty"`
	Required    bool    `json:"required,omitempty"`
	Description string  `json:"description,omitempty"`
	Deprecated  bool    `json:"deprecated,omitempty"`
	Schema      *Schema `json:"schema,omitempty"`
	Style       string  `json:"style,omitempty"`
	Explode     *bool   `json:"explode,omitempty"`

	XFilterOps         []string `json:"x-pdbr-filter-ops,omitempty"`
	XDeprecationReason string   `json:"x-deprecation-reason,omitempty"`
}

type RequestBody struct {
	Description string                `json:"description,omitempty"`
	Required    bool                  `json:"required,omitempty"`
	Content     map[string]*MediaType `json:"content"`
}

type Response struct {
	Ref         string                `json:"$ref,omitempty"`
	Description string                `json:"description,omitempty"`
	Headers     map[string]*Header    `json:"headers,omitempty"`
	Content     map[string]*MediaType `json:"content,omitempty"`
}

type Header struct {
	Description string  `json:"description,omitempty"`
	Schema      *Schema `json:"schema,omitempty"`
}

type MediaType struct {
	Schema *Schema `json:"schema,omitempty"`
}

type Components struct {
	Schemas         map[string]*Schema         `json:"schemas,omitempty"`
	Responses       map[string]*Response       `json:"responses,omitempty"`
	Parameters      map[string]*Parameter      `json:"parameters,omitempty"`
	SecuritySchemes map[string]*SecurityScheme `json:"securitySchemes,omitempty"`
}

type SecurityScheme struct {
	Type         string `json:"type"`
	Scheme       string `json:"scheme,omitempty"`
	BearerFormat string `json:"bearerFormat,omitempty"`
}

// Schema is a JSON Schema fragment. Type is a string or a []string (3.1
// nullable form). An empty Schema renders as {} (any value).
type Schema struct {
	Ref                  string             `json:"$ref,omitempty"`
	Type                 any                `json:"type,omitempty"`
	Format               string             `json:"format,omitempty"`
	Pattern              string             `json:"pattern,omitempty"`
	Description          string             `json:"description,omitempty"`
	Items                *Schema            `json:"items,omitempty"`
	MinItems             *int               `json:"minItems,omitempty"`
	Properties           map[string]*Schema `json:"properties,omitempty"`
	Required             []string           `json:"required,omitempty"`
	AdditionalProperties *bool              `json:"additionalProperties,omitempty"`
	MinProperties        *int               `json:"minProperties,omitempty"`
	MaxProperties        *int               `json:"maxProperties,omitempty"`
	Enum                 []string           `json:"enum,omitempty"`
	Const                any                `json:"const,omitempty"`
	AnyOf                []*Schema          `json:"anyOf,omitempty"`
	OneOf                []*Schema          `json:"oneOf,omitempty"`
	ReadOnly             bool               `json:"readOnly,omitempty"`
	Deprecated           bool               `json:"deprecated,omitempty"`
	ContentEncoding      string             `json:"contentEncoding,omitempty"`
	Minimum              *int               `json:"minimum,omitempty"`
	Maximum              *int               `json:"maximum,omitempty"`
	ExclusiveMinimum     *int               `json:"exclusiveMinimum,omitempty"`
	ExclusiveMaximum     *int               `json:"exclusiveMaximum,omitempty"`

	XDeprecationReason string `json:"x-deprecation-reason,omitempty"`
}

// JSON renders the document (indented, trailing newline) with repeated
// parameters hoisted into components/parameters.
func (d *Document) JSON() ([]byte, error) { return d.render(true) }

// CompactJSON renders the same document as JSON without indentation.
func (d *Document) CompactJSON() ([]byte, error) { return d.render(false) }

func (d *Document) render(indent bool) ([]byte, error) {
	var buf bytes.Buffer
	enc := json.NewEncoder(&buf)
	enc.SetEscapeHTML(false)
	if indent {
		enc.SetIndent("", "  ")
	}
	hoisted, err := d.hoistParameters()
	if err != nil {
		return nil, err
	}
	if err := enc.Encode(hoisted); err != nil {
		return nil, err
	}
	return buf.Bytes(), nil
}

// SchemaRef returns a $ref to a components schema.
func SchemaRef(name string) *Schema { return &Schema{Ref: "#/components/schemas/" + name} }

func intPtr(n int) *int { return &n }

func boolPtr(b bool) *bool { return &b }

// Nullable wraps s in the 3.1 nullable form: a type array for inline
// types, anyOf [ref, null] for references.
func Nullable(s *Schema) *Schema {
	if s.Ref != "" || len(s.AnyOf) > 0 || len(s.OneOf) > 0 {
		return &Schema{AnyOf: []*Schema{s, {Type: "null"}}}
	}
	if s.Type == nil {
		return s // {} already admits null
	}
	out := *s
	switch t := s.Type.(type) {
	case string:
		out.Type = []string{t, "null"}
	case []string:
		out.Type = append(append([]string(nil), t...), "null")
	}
	return &out
}

// TypeSchema maps a PostgreSQL type to its OpenAPI schema (docs/openapi.md
// "Type map"). Arrays nest; enums and composites reference their schemas.
func (b *Built) TypeSchema(pgType, typeSchema string, enumRef string) *Schema {
	return b.typeSchema(pgType, typeSchema, enumRef)
}

func (b *Builder) typeSchema(pgType, typeSchema, enumRef string) *Schema {
	if s := vectorSchema(b.Catalog, pgType, "", false); s != nil {
		return s
	}
	return typeSchemaWith(b.Enums, b.Composites, b.Extra.Types, pgType, typeSchema, enumRef)
}

func (b *Built) typeSchema(pgType, typeSchema, enumRef string) *Schema {
	if s := vectorSchema(b.Catalog, pgType, "", false); s != nil {
		return s
	}
	return typeSchemaWith(b.Enums, b.Composites, b.Extra.Types, pgType, typeSchema, enumRef)
}

// vectorSchema is the schema of a pgvector value (nil for other types):
// vector and halfvec are output as arrays of numbers, sparsevec as its text
// form; on input (request bodies) every type also takes the other form.
// baseSchema is the schema of the resolved base type ("" matches by name).
func vectorSchema(cat *introspect.Catalog, pgType, baseSchema string, input bool) *Schema {
	if strings.HasPrefix(pgType, "_") {
		return nil
	}
	qualified := pgType
	if baseSchema != "" {
		qualified = baseSchema + "." + pgType
	}
	for _, v := range contrib.ForType(cat, qualified, false) {
		if v.Name != contrib.VocabVector {
			continue
		}
		list := &Schema{Type: "array", Items: &Schema{Type: "number"}, Description: "pgvector " + v.Type + ", as an array of numbers."}
		text := &Schema{Type: "string", Description: "pgvector " + v.Type + " text form, e.g. [1,2,3]."}
		if v.Type == "sparsevec" {
			list.Description = "Dense array of numbers; zero elements are left out of the sparse vector."
			text.Description = "pgvector sparsevec text form {index:value,...}/dimensions, e.g. {1:1.5,3:2}/5 (1-based indices)."
			if input {
				return &Schema{OneOf: []*Schema{text, list}}
			}
			return text
		}
		if input {
			return &Schema{OneOf: []*Schema{list, text}}
		}
		return list
	}
	return nil
}

// inputTypeSchema is typeSchema for request bodies: Extra.InputTypes wins
// over Extra.Types (postgis: GeoJSON or WKT text on the way in). baseSchema
// is the schema of the resolved base type ("" matches range types by name).
func (b *Builder) inputTypeSchema(pgType, typeSchema, baseSchema, enumRef string) *Schema {
	if b.Extra.InputTypes[pgType] == nil {
		if s := rangeInputSchema(b.Catalog, pgType, baseSchema); s != nil {
			return s
		}
		if s := vectorSchema(b.Catalog, pgType, baseSchema, true); s != nil {
			return s
		}
	}
	if len(b.Extra.InputTypes) == 0 {
		return b.typeSchema(pgType, typeSchema, enumRef)
	}
	types := make(map[string]*Schema, len(b.Extra.Types)+len(b.Extra.InputTypes))
	for k, v := range b.Extra.Types {
		types[k] = v
	}
	for k, v := range b.Extra.InputTypes {
		types[k] = v
	}
	return typeSchemaWith(b.Enums, b.Composites, types, pgType, typeSchema, enumRef)
}

func typeSchemaWith(enums map[string]*Enum, comps map[string]*Composite, types map[string]*Schema, pgType, typeSchema, enumRef string) *Schema {
	if elem, ok := strings.CutPrefix(pgType, "_"); ok {
		return &Schema{Type: "array", Items: typeSchemaWith(enums, comps, types, elem, typeSchema, "")}
	}
	if s := types[pgType]; s != nil {
		return s
	}
	if enumRef != "" {
		if e := enums[enumRef]; e != nil {
			return SchemaRef(e.Name)
		}
	}
	if e := enums[typeSchema+"."+pgType]; e != nil && !e.Enum.TableBacked {
		return SchemaRef(e.Name)
	}
	if c := comps[typeSchema+"."+pgType]; c != nil {
		return SchemaRef(c.Name)
	}
	switch pgType {
	case "int2", "int4":
		return &Schema{Type: "integer", Format: "int32"}
	case "int8":
		return &Schema{Type: "string", Description: "int8, serialized as a string to preserve precision."}
	case "numeric":
		return &Schema{Type: "string", Description: "numeric, serialized as a string to preserve precision."}
	case "money":
		return &Schema{Type: "string", Description: "money, serialized as a string to preserve precision."}
	case "float4":
		return &Schema{Type: "number", Format: "float"}
	case "float8":
		return &Schema{Type: "number", Format: "double"}
	case "bool":
		return &Schema{Type: "boolean"}
	case "text", "varchar", "bpchar", "char", "name", "citext":
		return &Schema{Type: "string"}
	case "uuid":
		return &Schema{Type: "string", Format: "uuid"}
	case "timestamptz", "timestamp":
		return &Schema{Type: "string", Format: "date-time"}
	case "date":
		return &Schema{Type: "string", Format: "date"}
	case "time", "timetz":
		return &Schema{Type: "string", Format: "time"}
	case "bytea":
		return &Schema{Type: "string", ContentEncoding: "base64"}
	case "json", "jsonb":
		return &Schema{}
	case "hstore":
		return &Schema{Type: "object", Description: "hstore, as an object of string keys to string or null values."}
	default:
		return &Schema{Type: "string"}
	}
}

// rangeInputSchema is the request-body shape of a range or multirange value
// (built-in, or a range type recorded in cat.Ranges): its literal text, or
// the structured form the contrib codec converts. Responses keep the literal
// text (PostgREST does too).
func rangeInputSchema(cat *introspect.Catalog, pgType, baseSchema string) *Schema {
	bound := func(side string) *Schema {
		return &Schema{Description: side + " bound in the element type's JSON or text form; null or absent is unbounded."}
	}
	obj := &Schema{
		Type: "object", AdditionalProperties: boolPtr(false),
		Properties: map[string]*Schema{
			"start": bound("Lower"), "end": bound("Upper"),
			"startInclusive": {Type: "boolean", Description: "Default true."},
			"endInclusive":   {Type: "boolean", Description: "Default false."},
			"isEmpty":        {Type: "boolean", Description: "The empty range; other members are ignored."},
		},
	}
	if strings.HasPrefix(pgType, "_") {
		return nil
	}
	qualified := pgType
	if baseSchema != "" {
		qualified = baseSchema + "." + pgType
	}
	for _, v := range contrib.ForType(cat, qualified, false) {
		switch v.Name {
		case contrib.VocabRange:
			return &Schema{OneOf: []*Schema{
				{Type: "string", Description: pgType + " literal, e.g. [1,5) or empty."},
				obj,
			}}
		case contrib.VocabMultirange:
			return &Schema{OneOf: []*Schema{
				{Type: "string", Description: pgType + " literal, e.g. {[1,3), [5,7)}."},
				{Type: "array", Items: &Schema{OneOf: []*Schema{{Type: "string"}, obj}}},
			}}
		}
	}
	return nil
}

// ColumnSchema maps a column to its (nullable-aware) OpenAPI schema; plugins
// use it for the schemas they add through Extra.Schemas.
func (b *Builder) ColumnSchema(c *introspect.Column) *Schema { return b.columnSchema(c, false) }

func (b *Builder) columnSchema(c *introspect.Column, readOnly bool) *Schema {
	s := b.colTypeSchema(c)
	if !c.NotNull {
		s = Nullable(s)
	}
	if readOnly {
		cp := *s
		cp.ReadOnly = true
		s = &cp
	}
	return s
}

// annotate copies s and applies the field-level description/deprecation.
func annotate(s *Schema, f *Field) *Schema {
	if f.Description == "" && !f.Deprecated {
		return s
	}
	cp := *s
	if f.Description != "" {
		cp.Description = f.Description
	}
	cp.Deprecated = f.Deprecated
	cp.XDeprecationReason = f.DeprecationReason
	return &cp
}

// openAPI builds the document: info, servers, tags, security, component
// schemas (resources, their .insert/.update/.upsert bodies, enums,
// composites, plugin extras, Problem), reusable Problem responses and one
// path item per route.
func (b *Builder) openAPI(built *Built) *Document {
	base := b.Options.BasePath
	if base == "" {
		base = "/"
	}
	version := b.Options.Version
	if version == "" {
		version = "dev"
	}
	doc := &Document{
		OpenAPI: "3.1.0",
		Info:    Info{Title: "pdbr", Version: version},
		Servers: []Server{{URL: base}},
		Paths:   map[string]*PathItem{},
		Components: &Components{
			Schemas:   map[string]*Schema{},
			Responses: map[string]*Response{},
		},
	}
	doc.Components.Schemas["Problem"] = problemSchema()
	doc.Components.Responses[problemResponse] = &Response{
		Description: "Error (RFC 9457 problem details; the code member is the stable discriminator).",
		Content:     map[string]*MediaType{"application/problem+json": {Schema: SchemaRef("Problem")}},
	}
	doc.Components.Responses[methodNotAllowedResponse] = &Response{
		Description: "Method Not Allowed: the method is not registered on the path, or the server is read-only (code read_only). Allow lists the methods that exist.",
		Headers:     map[string]*Header{"Allow": {Schema: &Schema{Type: "string"}, Description: "Comma-separated methods available on this path."}},
		Content:     map[string]*MediaType{"application/problem+json": {Schema: SchemaRef("Problem")}},
	}
	doc.Components.Responses[rangeNotSatisfiableResponse] = &Response{
		Description: "Range Not Satisfiable: a count was requested and the first requested row (Range header or offset) is past the total (code range_not_satisfiable).",
		Headers:     map[string]*Header{"Content-Range": {Schema: &Schema{Type: "string"}, Description: "*/<total>"}},
		Content:     map[string]*MediaType{"application/problem+json": {Schema: SchemaRef("Problem")}},
	}
	if b.authOn() {
		doc.Components.SecuritySchemes = map[string]*SecurityScheme{}
	}
	if b.Options.AuthMode == "jwt" {
		doc.Components.SecuritySchemes["bearerAuth"] = &SecurityScheme{Type: "http", Scheme: "bearer", BearerFormat: "JWT"}
		// Anonymous requests are valid (they run as rls.anonymous_role), so
		// authentication is optional: the empty requirement comes first.
		doc.Security = []map[string][]string{{}, {"bearerAuth": {}}}
	}
	if len(doc.Components.SecuritySchemes) == 0 {
		doc.Components.SecuritySchemes = nil
	}
	for name, s := range b.Extra.Schemas {
		doc.Components.Schemas[name] = s
	}
	for _, r := range sortedResources(b.Resources) {
		doc.Components.Schemas[r.Path] = b.resourceSchema(r)
		if r.Methods[MethodPost] {
			doc.Components.Schemas[r.Path+".insert"] = b.inputSchema(r, MethodPost)
		}
		if r.Methods[MethodPatch] {
			doc.Components.Schemas[r.Path+".update"] = b.inputSchema(r, MethodPatch)
		}
		if r.Methods[MethodPut] {
			doc.Components.Schemas[r.Path+".upsert"] = b.inputSchema(r, MethodPut)
		}
	}
	for _, rpc := range sortedRPCs(b.RPCs) {
		if rpc.Record {
			doc.Components.Schemas[rpc.Resource.Path] = b.resourceSchema(rpc.Resource)
		}
		for _, r := range rpc.RowArgs {
			doc.Components.Schemas[r.Path+".row"] = b.rowInputSchema(r)
		}
	}
	for _, e := range b.Enums {
		doc.Components.Schemas[e.Name] = &Schema{Type: "string", Enum: append([]string(nil), e.Values...), Description: e.Description}
	}
	for _, d := range b.Domains {
		doc.Components.Schemas[d.Name] = b.domainSchema(d.Domain)
	}
	for _, c := range b.Composites {
		s := &Schema{Type: "object", Properties: map[string]*Schema{}}
		for _, f := range c.Composite.Fields {
			s.Properties[f.Name] = b.columnSchema(f, false)
		}
		doc.Components.Schemas[c.Name] = s
	}
	tagged := map[string]bool{}
	for _, rt := range built.Routes {
		doc.Paths[rt.Pattern] = b.pathItem(rt)
		if name := routeTag(rt); !tagged[name] {
			tagged[name] = true
			tag := Tag{Name: name}
			if rt.Resource != nil {
				tag.Description = rt.Resource.Description
			}
			doc.Tags = append(doc.Tags, tag)
		}
	}
	return doc
}

// routeTag groups operations per resource rather than per schema: the
// resource key (users, audit.users) for table routes, rpc / <schema>.rpc for
// functions. Routes are emitted resources-first in path order, so tags come
// out in the same stable order.
func routeTag(rt *Route) string {
	switch {
	case rt.Resource != nil:
		return rt.Resource.Path
	case rt.RPC != nil:
		if rt.RPC.Bare {
			return "rpc"
		}
		return rt.RPC.Function.Schema + ".rpc"
	}
	return ""
}

// authOn reports whether requests can carry credentials (401 reachable).
func (b *Builder) authOn() bool {
	return b.Options.AuthMode == "jwt" || b.Options.AuthMode == "headers"
}

func problemSchema() *Schema {
	return &Schema{
		Type:        "object",
		Description: "RFC 9457 problem details with a stable code extension.",
		Required:    []string{"type", "title", "status"},
		Properties: map[string]*Schema{
			"type":     {Type: "string", Description: "Always about:blank."},
			"title":    {Type: "string", Description: "HTTP reason phrase."},
			"status":   {Type: "integer"},
			"detail":   {Type: "string", Description: "Human-readable message; database messages are sanitized per errors.detail."},
			"instance": {Type: "string", Description: "Request path without the query string."},
			"code":     {Type: "string", Description: "Stable machine-readable code: pg:<SQLSTATE> for database errors, a pdbr code otherwise."},
			"pg":       {Type: "object", Description: "PostgreSQL error detail (sqlstate, message, detail, hint, table, constraint); present only under errors.detail: dev."},
		},
	}
}

func (b *Builder) resourceSchema(r *Resource) *Schema {
	s := &Schema{Type: "object", Properties: map[string]*Schema{}, Description: r.Description, Deprecated: r.Deprecated, XDeprecationReason: r.DeprecationReason}
	for _, f := range r.Columns {
		s.Properties[f.Key] = annotate(b.columnSchema(f.Column, f.ReadOnly), f)
		if f.Column.NotNull {
			s.Required = append(s.Required, f.Key)
		}
	}
	for _, key := range sortedKeys(r.Computed) {
		c := r.Computed[key]
		f := c.Function
		var p *Schema
		switch {
		case c.SetOf != nil:
			p = &Schema{Type: "array", Items: SchemaRef(c.SetOf.Path)}
		case f.ReturnsSet:
			p = &Schema{Type: "array", Items: b.returnTypeSchema(f)}
		default:
			p = Nullable(b.returnTypeSchema(f))
		}
		cp := *p
		cp.ReadOnly = true
		cp.Description = "Computed (" + f.Schema + "." + f.Name + "). Select explicitly: select=" + key + computedArgSyntax(c) + "."
		if len(c.Args) > 0 {
			cp.Description += " Arguments: " + computedArgDocs(c) + "."
		}
		if c.Description != "" {
			cp.Description = c.Description + " " + cp.Description
		}
		cp.Deprecated = c.Deprecated
		cp.XDeprecationReason = c.DeprecationReason
		s.Properties[key] = &cp
	}
	for _, key := range sortedKeys(r.Pseudo) {
		pc := r.Pseudo[key]
		p := &Schema{}
		if pc.Schema != nil {
			p = pc.Schema
			if !pc.Column.NotNull || pc.Score != nil {
				p = Nullable(p)
			}
		}
		cp := *p
		cp.ReadOnly = true
		sel := key
		if pc.Score != nil && pc.Score.Param == "near" && postgis.FlavourOf(pc.Column.PGType) == postgis.Geometry {
			sel += "[(spheroid=true)]"
		}
		cp.Description = strings.TrimSpace(pc.Description + " Derived from " + pc.Column.Name + ". Select explicitly: select=" + sel + ".")
		s.Properties[key] = &cp
	}
	for _, key := range sortedKeys(r.Relations) {
		cands := r.Relations[key]
		shapes := make([]*Schema, 0, len(cands))
		hints := make([]string, 0, len(cands))
		for _, rel := range cands {
			if !rel.Embeddable {
				continue
			}
			hint := rel.Hints[0]
			if rel.Forward {
				hint = rel.Hints[len(rel.Hints)-1]
			}
			if rel.ToMany() {
				shapes = append(shapes, &Schema{Type: "array", Items: SchemaRef(rel.To.Path)})
				hints = append(hints, hint+" (array)")
			} else {
				shapes = append(shapes, Nullable(SchemaRef(rel.To.Path)))
				hints = append(hints, hint+" (object)")
			}
		}
		if len(shapes) == 0 {
			continue
		}
		var p *Schema
		if len(shapes) == 1 {
			p = shapes[0]
			p.Description = "Embed via select=" + key + "(...)." + relationNote(cands[0])
		} else {
			p = &Schema{OneOf: shapes, Description: "Ambiguous relation; embed via select=" + key + "!<hint>(...) with hint one of: " + strings.Join(hints, ", ") + "."}
		}
		s.Properties[key] = p
	}
	sort.Strings(s.Required)
	return s
}

// relationNote describes the PostgreSQL 18 foreign key variants in an embed
// property: a PERIOD key embeds every referenced row whose period overlaps,
// a NOT ENFORCED key may reference no row at all.
func relationNote(rel *Relation) string {
	var out string
	if rel.Forward && rel.FK.Period {
		out += " Temporal (PERIOD) foreign key: every " + rel.To.Path + " row whose period overlaps this row's."
	}
	if rel.Forward && rel.FK.NotEnforced {
		out += " NOT ENFORCED foreign key: the referenced row may be missing (null)."
	}
	return out
}

// inputColumns lists the body keys a write method accepts, in catalog order.
func (r *Resource) inputColumns(m Method) []*Field {
	allowed := r.InputColumnsFor(m)
	out := make([]*Field, 0, len(allowed))
	for _, f := range r.Columns {
		if allowed[f.Key] != nil {
			out = append(out, f)
		}
	}
	return out
}

// insertRequired reports whether a column must be present in a create
// body: NOT NULL without a default (identity/generated columns are not
// input columns at all).
func insertRequired(c *introspect.Column) bool {
	return c.NotNull && !c.HasDefault && !c.Generated
}

// inputSchema renders <name>.insert / .update / .upsert: the accepted body
// keys per method, the required set (create: NOT NULL without default;
// upsert: plus the primary key when it is insertable; update: none, but at
// least one key), nested-input properties (Surface.NestedSchema), and
// additionalProperties: false because unknown keys are 400 invalid_body.
func (b *Builder) inputSchema(r *Resource, m Method) *Schema {
	s := &Schema{Type: "object", Properties: map[string]*Schema{}, AdditionalProperties: boolPtr(false)}
	fields := r.inputColumns(m)
	required := map[string]bool{}
	for _, f := range fields {
		p := b.colInputTypeSchema(f.Column)
		if !f.Column.NotNull {
			p = Nullable(p)
		}
		s.Properties[f.Key] = annotate(p, f)
		if m != MethodPatch && insertRequired(f.Column) {
			required[f.Key] = true
		}
	}
	switch m {
	case MethodPost:
		s.Description = "Create body for " + r.Path + ": one object, or an array of objects for a bulk insert (missing keys take their column default)."
		b.nestedInputProps(r, s, required, m)
	case MethodPatch:
		s.Description = "Update body for " + r.Path + ": any subset of the writable columns; at least one key."
		s.MinProperties = intPtr(1)
		b.nestedInputProps(r, s, required, m)
	case MethodPut:
		s.Description = "Upsert body for " + r.Path + ": inserted or updated on the conflict target (the primary key, or ?on_conflict=<unique columns>), whose columns must be present."
		if pk := r.PrimaryKey(); pk != nil {
			all := true
			for _, c := range pk.Columns {
				if f := r.FieldByColumn(c); f == nil || s.Properties[f.Key] == nil {
					all = false
				}
			}
			if all {
				for _, c := range pk.Columns {
					required[r.FieldByColumn(c).Key] = true
				}
			}
		}
	}
	for k := range required {
		s.Required = append(s.Required, k)
	}
	sort.Strings(s.Required)
	return s
}

// nestedInputProps adds the nested-mutations properties to a .insert or
// .update schema: one per key of Surface.NestedKeys(r) for which
// Surface.NestedSchema renders a schema. A forward key of a create body
// supplies the FK columns, which leave the required set.
func (b *Builder) nestedInputProps(r *Resource, s *Schema, required map[string]bool, m Method) {
	if b.Surface.NestedKeys == nil || b.Surface.NestedSchema == nil {
		return
	}
	keys := b.Surface.NestedKeys(r)
	for _, key := range sortedKeys(keys) {
		rel := keys[key]
		if rel == nil || rel.To == nil || s.Properties[key] != nil {
			continue
		}
		ns := b.Surface.NestedSchema(r, rel, m)
		if ns == nil {
			continue
		}
		s.Properties[key] = ns
		if rel.Forward && m == MethodPost {
			for _, c := range rel.EqualityColumns() {
				if f := r.FieldByColumn(c); f != nil {
					delete(required, f.Key)
				}
			}
		}
	}
}

// filterOps is the operator vocabulary of a filterable column, mirroring
// the parser's op/type compatibility (parse.coerceOperand): plugin types
// come from Extra.FilterOps, spatial columns without the plugin only
// support is, arrays and json have their containment operators, text its
// pattern operators, hstore no ordering comparisons, pgvector types only
// is; the contrib operators
// the filter policy admits (ContribFilters) follow the core ones.
func (b *Builder) filterOps(r *Resource, c *introspect.Column) []string {
	base := strings.TrimPrefix(c.PGType, "_")
	isArray := c.IsArray || strings.HasPrefix(c.PGType, "_")
	if ops, ok := b.Extra.FilterOps[base]; ok && !isArray {
		return append([]string(nil), ops...)
	}
	ops := coreFilterOps(b.Catalog, c, base, isArray)
	for _, f := range contribFilters(b.Catalog, b.Options, r, c) {
		if !slices.Contains(ops, f.Name) {
			ops = append(ops, f.Name)
		}
	}
	return ops
}

func coreFilterOps(cat *introspect.Catalog, c *introspect.Column, base string, isArray bool) []string {
	if postgis.FlavourOf(base).Spatial() {
		return []string{"is"}
	}
	if isArray {
		return []string{"eq", "neq", "gt", "gte", "lt", "lte", "is", "isdistinct", "cs", "cd", "ov"}
	}
	if v, ok := OwnerVocabulary(cat, c); ok && v.Name == contrib.VocabHstore {
		return []string{"eq", "neq", "in", "is", "isdistinct"}
	}
	if _, ok := VectorVocabulary(cat, c); ok {
		return []string{"is"}
	}
	switch base {
	case "jsonb":
		return []string{"eq", "neq", "gt", "gte", "lt", "lte", "is", "isdistinct", "cs", "cd", "hk", "jpe", "jpm"}
	case "json":
		return []string{"is", "cs", "cd", "hk", "jpe", "jpm"}
	case "text", "varchar", "bpchar", "char", "name", "citext":
		if c.EnumRef == "" {
			return []string{"eq", "neq", "gt", "gte", "lt", "lte", "in", "is", "isdistinct", "like", "ilike", "sw", "ew", "match", "imatch"}
		}
	}
	return []string{"eq", "neq", "gt", "gte", "lt", "lte", "in", "is", "isdistinct"}
}

// FilterOps exposes the operator vocabulary of column c of r (docs and
// tests): exactly the operators the parser accepts for it.
func (b *Built) FilterOps(r *Resource, c *introspect.Column) []string {
	return (&Builder{Catalog: b.Catalog, Options: b.Options, Extra: b.Extra}).filterOps(r, c)
}

func sortedKeys[V any](m map[string]V) []string {
	out := make([]string, 0, len(m))
	for k := range m {
		out = append(out, k)
	}
	sort.Strings(out)
	return out
}

// computedArgSyntax renders the select= call form of c: "" without extra
// arguments, "(k=<type>,...)" otherwise.
func computedArgSyntax(c *Computed) string {
	if len(c.Args) == 0 {
		return ""
	}
	parts := make([]string, len(c.Args))
	for i, a := range c.Args {
		parts[i] = a.Name + "=<" + strings.TrimPrefix(a.PGType, "_") + ">"
	}
	return "(" + strings.Join(parts, ",") + ")"
}

// computedArgDocs lists c's extra arguments with their type and whether
// they are required or defaulted.
func computedArgDocs(c *Computed) string {
	parts := make([]string, len(c.Args))
	for i, a := range c.Args {
		parts[i] = a.Name + " (" + a.PGType + ", required)"
		if a.HasDefault {
			parts[i] = a.Name + " (" + a.PGType + ", optional"
			if a.Default != "" {
				parts[i] += ", default " + a.Default
			}
			parts[i] += ")"
		}
	}
	return strings.Join(parts, ", ")
}
