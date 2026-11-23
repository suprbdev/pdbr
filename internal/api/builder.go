package api

import (
	"fmt"
	"log/slog"
	"sort"
	"strconv"
	"strings"

	coreinflect "github.com/suprbdev/pdbcore/inflect"
	"github.com/suprbdev/pdbcore/introspect"
	"github.com/suprbdev/pdbcore/smarttags"

	"github.com/suprbdev/pdbr/internal/inflect"
)

// ReservedSegments can never be bare resource aliases: they are routes (or
// route prefixes) of their own under base_path, or the health endpoints.
var ReservedSegments = map[string]bool{
	"rpc": true, "openapi.json": true, "healthz": true, "readyz": true, "by": true,
}

// Options control default generation.
type Options struct {
	FilterIndexedOnly bool
	// AllowColumns lists extra filterable columns per "schema.table".
	AllowColumns map[string][]string
	// Functions exposes PostgreSQL functions as RPC routes and computed columns.
	Functions bool
	// ExposePartitions makes partitions of partitioned tables resources too
	// (schema.expose_partitions); by default only the partitioned parent is.
	ExposePartitions bool
	// BasePath is server.base_path (OpenAPI servers[0].url).
	BasePath string
	// Schemas is schema.schemas; Schemas[0] gets bare aliases.
	Schemas []string
	// MaxPageSize is server.max_page_size (documented limit maximum).
	MaxPageSize int
	// Etags is server.etags.
	Etags bool
	// JWTType is rls.auth.jwt_type ("schema.type"): functions returning that
	// composite are minted into JWT strings by the exec layer.
	JWTType string
	// AuthMode is rls.auth.mode while RLS is enabled ("" otherwise): jwt
	// documents securitySchemes.bearerAuth; jwt and headers document 401.
	AuthMode string
	// CSV is server.csv (text/csv on list responses).
	CSV bool
	// Domains is server.openapi_domains: domains over built-in types get a
	// named component schema that columns, arguments and results reference.
	Domains bool
	// Aggregates is server.aggregates: the list select= parameters
	// document the aggregate grammar.
	Aggregates bool
	// Version is the binary version (OpenAPI info.version).
	Version string
	Logger  *slog.Logger
}

// OpenAPIExtras collects plugin-contributed OpenAPI pieces merged into the
// generated document at Build time.
type OpenAPIExtras struct {
	// Schemas are extra components.schemas entries (e.g. GeoJSON).
	Schemas map[string]*Schema
	// Types overrides the type map for a PostgreSQL type name (e.g.
	// "geometry" -> $ref GeoJSON); consulted before the built-in table.
	Types map[string]*Schema
	// FilterOps lists, per PostgreSQL type name, the operator vocabulary a
	// column's filter parameter documents instead of the scalar operators
	// (postgis: st_*, bbox_*, is); read by the OpenAPI parameter renderer.
	FilterOps map[string][]string
	// InputTypes overrides Types for request-body properties (postgis:
	// GeoJSON object or WKT string on input, GeoJSON only on output).
	InputTypes map[string]*Schema
}

// Builder is the mutable resource model handed to APIHook plugins; Build
// freezes it into a Built.
type Builder struct {
	Catalog *introspect.Catalog
	Inflect coreinflect.Next
	Options Options
	// Resources maps "schema.table" -> resource.
	Resources map[string]*Resource
	// RPCs maps "schema.function" -> rpc.
	RPCs map[string]*RPC
	// Enums maps "schema.name" -> enum.
	Enums map[string]*Enum
	// Composites maps "schema.name" -> composite.
	Composites map[string]*Composite
	// Domains maps "schema.name" -> domain with a named schema.
	Domains map[string]*Domain
	Extra   OpenAPIExtras
	// Surface is the query-grammar surface plugins switch on (advanced-filters,
	// postgis); parse rejects the corresponding syntax while a flag is off so
	// disabling a plugin removes its feature end to end.
	Surface Surface
	// Warnings collects generation warnings (also logged).
	Warnings []string

	log          *slog.Logger
	schemaNames  map[string]bool
	openAPINames map[string]bool
}

// Surface lists optional query-grammar features that built-in plugins enable
// through their APIHook. All false means the core grammar only.
type Surface struct {
	RelationFilters bool // rel.col=op.v, rel!none./!every., rel=exists.
	ComputedFilters bool // computed=op.v
	RelationOrder   bool // order=rel(col).asc
	ComputedOrder   bool // order=computed.desc
	Spatial         bool // st_*/bbox_* ops, .dist+near=, col(transform=,simplify=) all at once
	// SpatialFilters / SpatialOrder / SpatialProjection switch the spatial
	// halves on individually (postgis settings filters / ordering /
	// projection); Spatial implies all three. Pseudo-columns need no flag:
	// they exist only where the plugin registered Resource.Pseudo entries.
	SpatialFilters    bool
	SpatialOrder      bool
	SpatialProjection bool
	// SpatialAggregates enables the spatial aggregate functions of select=
	// (col.extent(), col.union(), col.collect(), col.centroid()); they
	// also need server.aggregates.
	SpatialAggregates bool
	// NestedKeys names the nested-mutation body keys a resource accepts
	// (relation key -> relation); nil while the nested-mutations plugin is
	// off, so bodies carrying relation keys stay invalid_body. The parser
	// reads it through parse.Options.NestedKeys; OpenAPI's .insert and
	// .update schemas render one property per key from NestedSchema.
	NestedKeys func(*Resource) map[string]*Relation
	// NestedSchema returns the OpenAPI schema of nested key rel of r in a
	// body of method m (MethodPost: the .insert schema, MethodPatch:
	// .update), or nil when the key takes no input there. A forward key
	// with a POST schema releases its FK columns from the required set.
	NestedSchema func(r *Resource, rel *Relation, m Method) *Schema
}

// New creates a Builder with the default model generated from the catalog.
func New(cat *introspect.Catalog, inflector coreinflect.Next, opts Options) *Builder {
	if opts.Logger == nil {
		opts.Logger = slog.Default()
	}
	if inflector == nil {
		inflector = inflect.Default
	}
	b := &Builder{
		Catalog:      cat,
		Inflect:      inflector,
		Options:      opts,
		Resources:    map[string]*Resource{},
		RPCs:         map[string]*RPC{},
		Enums:        map[string]*Enum{},
		Composites:   map[string]*Composite{},
		Domains:      map[string]*Domain{},
		Extra:        OpenAPIExtras{Schemas: map[string]*Schema{}, Types: map[string]*Schema{}, FilterOps: map[string][]string{}, InputTypes: map[string]*Schema{}},
		log:          opts.Logger,
		schemaNames:  map[string]bool{},
		openAPINames: map[string]bool{"Problem": true},
	}
	for _, s := range opts.Schemas {
		b.schemaNames[s] = true
	}
	b.addResources()
	b.addRelations()
	b.addEnums()
	b.addComposites()
	b.addDomains()
	b.addFunctions()
	return b
}

// Warn records a warning and logs it.
func (b *Builder) Warn(msg string, attrs ...any) {
	b.Warnings = append(b.Warnings, msg)
	b.log.Warn(msg, attrs...)
}

// Resource returns the resource for a catalog table, or nil.
func (b *Builder) Resource(schema, table string) *Resource {
	return b.Resources[schema+"."+table]
}

// RemoveResource drops a resource and every relation pointing at it.
func (b *Builder) RemoveResource(schema, table string) {
	r := b.Resources[schema+"."+table]
	if r == nil {
		return
	}
	delete(b.Resources, schema+"."+table)
	for _, other := range b.Resources {
		for key, cands := range other.Relations {
			var keep []*Relation
			for _, c := range cands {
				if c.To != r {
					keep = append(keep, c)
				}
			}
			if len(keep) == 0 {
				delete(other.Relations, key)
			} else {
				other.Relations[key] = keep
			}
		}
	}
	for key, rpc := range b.RPCs {
		if rpc.Resource == r {
			delete(b.RPCs, key)
		}
	}
}

// RemoveMethod turns off methods on a resource; Build then drops them from
// every route (an empty route disappears, otherwise 405 + Allow).
func (b *Builder) RemoveMethod(r *Resource, methods ...Method) {
	for _, m := range methods {
		r.Methods[m] = false
	}
}

// RemoveRelation drops one relation candidate from its owning resource; the
// key vanishes with its last candidate.
func (b *Builder) RemoveRelation(rel *Relation) {
	if rel == nil || rel.From == nil {
		return
	}
	cands := rel.From.Relations[rel.Key]
	keep := cands[:0]
	for _, c := range cands {
		if c != rel {
			keep = append(keep, c)
		}
	}
	if len(keep) == 0 {
		delete(rel.From.Relations, rel.Key)
		delete(rel.From.CostMultipliers, rel.Key)
		return
	}
	rel.From.Relations[rel.Key] = keep
}

// RemoveField hides a column from responses and select=, and with it from
// filters, order=, distinct, aggregates and unique lookups; when everywhere
// is set it also leaves request bodies. A hidden primary-key column
// (Resource.HiddenKey) also drops the row path and keyset cursors.
func (b *Builder) RemoveField(r *Resource, key string, everywhere bool) {
	f := r.Field(key)
	if f == nil {
		return
	}
	cols := r.Columns[:0]
	for _, x := range r.Columns {
		if x != f {
			cols = append(cols, x)
		}
	}
	r.Columns = cols
	uniques := r.Uniques[:0]
	for _, u := range r.Uniques {
		if !containsString(u.Columns, f.Column.Name) {
			uniques = append(uniques, u)
		}
	}
	r.Uniques = uniques
	delete(r.FilterColumns, key)
	delete(r.OrderColumns, key)
	if pk := r.Table.PrimaryKey; pk != nil && containsString(pk.Columns, f.Column.Name) {
		r.HiddenKey = true
		r.Keyset = false
	}
	if everywhere {
		delete(r.InputColumns, key)
	}
}

// RemoveComputed drops a computed column from a resource.
func (b *Builder) RemoveComputed(r *Resource, key string) { delete(r.Computed, key) }

// RemoveRPC drops an RPC route.
func (b *Builder) RemoveRPC(schema, function string) { delete(b.RPCs, schema+"."+function) }

// RemoveFilterColumn / RemoveOrderColumn withdraw a key from the filter /
// order= vocabulary (400 column_not_filterable / column_not_orderable).
func (b *Builder) RemoveFilterColumn(r *Resource, key string) { delete(r.FilterColumns, key) }

// RemoveOrderColumn is RemoveFilterColumn for order=.
func (b *Builder) RemoveOrderColumn(r *Resource, key string) { delete(r.OrderColumns, key) }

// RemoveUnique drops the /by/ route of a unique constraint by name.
func (b *Builder) RemoveUnique(r *Resource, constraint string) {
	uniques := r.Uniques[:0]
	for _, u := range r.Uniques {
		if u.Name != constraint {
			uniques = append(uniques, u)
		}
	}
	r.Uniques = uniques
}

// RenameEnum sets the OpenAPI schema name of an enum (schema-qualified
// outside the primary schema, like the default).
func (b *Builder) RenameEnum(e *Enum, name string) {
	e.Name = b.typeSchemaName(e.Enum.Schema, name)
}

func containsString(xs []string, s string) bool {
	for _, x := range xs {
		if x == s {
			return true
		}
	}
	return false
}

func (b *Builder) primarySchema() string {
	if len(b.Options.Schemas) > 0 {
		return b.Options.Schemas[0]
	}
	return ""
}

func (b *Builder) sortedTables() []*introspect.Table {
	out := append([]*introspect.Table(nil), b.Catalog.Tables...)
	sort.SliceStable(out, func(i, j int) bool {
		if out[i].Schema != out[j].Schema {
			return out[i].Schema < out[j].Schema
		}
		return out[i].Name < out[j].Name
	})
	return out
}

func (b *Builder) sortedFunctions() []*introspect.Function {
	out := append([]*introspect.Function(nil), b.Catalog.Functions...)
	sort.SliceStable(out, func(i, j int) bool {
		if out[i].Schema != out[j].Schema {
			return out[i].Schema < out[j].Schema
		}
		return out[i].Name < out[j].Name
	})
	return out
}

func description(comment string) string {
	_, desc := smarttags.Parse(comment)
	return desc
}

func (b *Builder) addResources() {
	segments := map[string]map[string]bool{} // schema -> segment
	for _, t := range b.sortedTables() {
		if !t.Privileges.Select {
			b.log.Debug("skipping relation: no SELECT privilege", "table", t.Schema+"."+t.Name)
			continue
		}
		if t.IsPartition() && !b.Options.ExposePartitions {
			b.log.Debug("skipping relation: partition (schema.expose_partitions is off)", "table", t.Schema+"."+t.Name, "parent", t.PartitionOf)
			continue
		}
		seg := b.Inflect(inflect.KindResourcePath, coreinflect.Input{Schema: t.Schema, Table: t.Name})
		if segments[t.Schema] == nil {
			segments[t.Schema] = map[string]bool{}
		}
		if segments[t.Schema][seg] {
			b.Warn("resource path collision, keeping first", "schema", t.Schema, "table", t.Name, "segment", seg)
			continue
		}
		segments[t.Schema][seg] = true
		r := &Resource{
			Table:           t,
			Segment:         seg,
			Methods:         map[Method]bool{},
			Computed:        map[string]*Computed{},
			Pseudo:          map[string]*PseudoColumn{},
			Relations:       map[string][]*Relation{},
			FilterColumns:   map[string]bool{},
			OrderColumns:    map[string]bool{},
			InputColumns:    map[string]*introspect.Column{},
			CostMultipliers: map[string]int{},
			Description:     description(t.Comment),
		}
		r.Bare = t.Schema == b.primarySchema()
		if r.Bare && ReservedSegments[seg] {
			r.Bare = false
			b.Warn("bare alias suppressed: path segment is reserved; use the schema-qualified path", "table", t.Schema+"."+t.Name, "segment", seg)
		}
		if r.Bare && b.schemaNames[seg] {
			r.Bare = false
			b.Warn("bare alias suppressed: path segment equals a configured schema name; use the schema-qualified path", "table", t.Schema+"."+t.Name, "segment", seg)
		}
		if r.Bare {
			r.Path = seg
		} else {
			r.Path = t.Schema + "." + seg
		}
		r.Path = b.claimOpenAPIName(r.Path, "resource "+t.Schema+"."+t.Name)

		r.Methods[MethodGet] = true
		r.Methods[MethodHead] = true
		r.Methods[MethodPost] = t.Insertable()
		r.Methods[MethodPatch] = t.Updatable()
		r.Methods[MethodDelete] = t.Deletable()
		r.Methods[MethodPut] = t.Insertable() && t.Updatable() && len(t.RowKeys()) > 0

		keys := map[string]bool{}
		for _, c := range t.Columns {
			key := b.Inflect(inflect.KindFieldName, coreinflect.Input{Schema: t.Schema, Table: t.Name, Column: c.Name})
			if keys[key] {
				b.Warn("field name collision, keeping first", "table", t.Schema+"."+t.Name, "column", c.Name, "key", key)
				continue
			}
			keys[key] = true
			r.Columns = append(r.Columns, &Field{Column: c, Key: key, ReadOnly: c.Generated, Description: description(c.Comment)})
			if !c.Generated {
				r.InputColumns[key] = c
			}
		}
		for _, u := range t.Uniques {
			if u.RowKey() {
				r.Uniques = append(r.Uniques, u)
			}
		}
		sort.SliceStable(r.Uniques, func(i, j int) bool { return r.Uniques[i].Name < r.Uniques[j].Name })
		r.Keyset = t.PrimaryKey != nil
		r.Etag = b.Options.Etags && len(t.RowKeys()) > 0
		b.applyFilterPolicy(r)
		b.Resources[t.Schema+"."+t.Name] = r
	}
}

// claimOpenAPIName reserves a components.schemas name, suffixing on
// collision so two objects never share a schema entry.
func (b *Builder) claimOpenAPIName(name, owner string) string {
	out := name
	for b.openAPINames[out] {
		out += "_"
	}
	if out != name {
		b.Warn("schema name collision, appending suffix", "owner", owner, "name", name, "renamed", out)
	}
	b.openAPINames[out] = true
	return out
}

// applyFilterPolicy resolves FilterColumns/OrderColumns under the
// indexed-only policy plus allow_columns. Composite-typed columns are never
// filterable: they have no scalar operator set.
func (b *Builder) applyFilterPolicy(r *Resource) {
	t := r.Table
	allowed := map[string]bool{}
	if b.Options.FilterIndexedOnly {
		allowed = t.IndexedColumns()
		for _, extra := range b.Options.AllowColumns[t.Schema+"."+t.Name] {
			allowed[extra] = true
		}
	}
	for _, f := range r.Columns {
		c := f.Column
		if b.Options.FilterIndexedOnly && !allowed[c.Name] {
			continue
		}
		if b.Catalog.Composite(c.TypeSchema, strings.TrimPrefix(c.PGType, "_")) != nil {
			continue
		}
		r.FilterColumns[f.Key] = true
		r.OrderColumns[f.Key] = true
	}
}

// costMultiplier reads @costMultiplier off an FK comment straight from the
// catalog so it works with the smart-comments plugin disabled; values below
// 1 are ignored so a subtree can never escape the cost limit.
func costMultiplier(comment string) int {
	tags, _ := smarttags.Parse(comment)
	if tags == nil {
		return 0
	}
	n, err := strconv.Atoi(tags.First("costMultiplier"))
	if err != nil || n < 1 {
		return 0
	}
	return n
}

func (b *Builder) addRelations() {
	for _, t := range b.sortedTables() {
		from := b.Resource(t.Schema, t.Name)
		if from == nil {
			continue
		}
		fks := append([]*introspect.ForeignKey(nil), t.ForeignKeys...)
		sort.SliceStable(fks, func(i, j int) bool { return fks[i].Name < fks[j].Name })
		for _, fk := range fks {
			to := b.Resource(fk.RefSchema, fk.RefTable)
			if to == nil {
				continue
			}
			hints := append([]string{fk.Name}, fk.Columns...)
			mult := costMultiplier(fk.Comment)
			fkey := b.Inflect(inflect.KindRelationForward, coreinflect.Input{
				Schema: fk.RefSchema, Table: fk.RefTable, Column: fk.Columns[0], Columns: fk.Columns, Constraint: fk.Name,
			})
			from.Relations[fkey] = append(from.Relations[fkey], &Relation{
				Key: fkey, Forward: true, FK: fk, From: from, To: to, Hints: hints,
				Filterable: true, Orderable: true, Embeddable: true,
			})
			bkey := b.Inflect(inflect.KindRelationBackward, coreinflect.Input{
				Schema: t.Schema, Table: t.Name, Columns: fk.Columns, Constraint: fk.Name,
			})
			to.Relations[bkey] = append(to.Relations[bkey], &Relation{
				Key: bkey, Forward: false, FK: fk, From: to, To: from, Hints: hints, CostMultiplier: mult,
				Filterable: true, Orderable: true, Embeddable: true,
			})
			if mult > 0 {
				to.CostMultipliers[bkey] = mult
			}
		}
	}
	for _, r := range b.Resources {
		for key, cands := range r.Relations {
			if len(cands) > 1 {
				b.log.Debug("ambiguous relation key: hint required", "resource", r.Path, "key", key, "candidates", len(cands))
			}
		}
	}
}

func (b *Builder) typeSchemaName(schema, name string) string {
	if schema == b.primarySchema() {
		return name
	}
	return schema + "." + name
}

// enumDescription folds per-value descriptions (@enum tables'
// @enumDescription column) into the schema description, since OpenAPI 3.1
// has no per-value description slot: "value: description" lines after the
// type comment.
func enumDescription(e *introspect.Enum) string {
	desc := description(e.Comment)
	var lines []string
	for i, v := range e.Values {
		if i < len(e.ValueDescriptions) && e.ValueDescriptions[i] != "" {
			lines = append(lines, v+": "+e.ValueDescriptions[i])
		}
	}
	if len(lines) == 0 {
		return desc
	}
	if desc != "" {
		desc += "\n\n"
	}
	return desc + strings.Join(lines, "\n")
}

func (b *Builder) addEnums() {
	enums := append([]*introspect.Enum(nil), b.Catalog.Enums...)
	sort.SliceStable(enums, func(i, j int) bool {
		if enums[i].Schema != enums[j].Schema {
			return enums[i].Schema < enums[j].Schema
		}
		return enums[i].Name < enums[j].Name
	})
	for _, e := range enums {
		out := &Enum{Enum: e, Description: enumDescription(e)}
		out.Name = b.claimOpenAPIName(b.typeSchemaName(e.Schema, e.Name), "enum "+e.Schema+"."+e.Name)
		for _, v := range e.Values {
			out.Values = append(out.Values, b.Inflect(inflect.KindEnumValue, coreinflect.Input{Schema: e.Schema, Enum: e.Name, Value: v}))
		}
		b.Enums[e.Schema+"."+e.Name] = out
	}
}

func (b *Builder) addComposites() {
	cs := append([]*introspect.Composite(nil), b.Catalog.Composites...)
	sort.SliceStable(cs, func(i, j int) bool {
		if cs[i].Schema != cs[j].Schema {
			return cs[i].Schema < cs[j].Schema
		}
		return cs[i].Name < cs[j].Name
	})
	for _, c := range cs {
		out := &Composite{Composite: c}
		out.Name = b.claimOpenAPIName(b.typeSchemaName(c.Schema, c.Name), "composite "+c.Schema+"."+c.Name)
		b.Composites[c.Schema+"."+c.Name] = out
	}
}

func hasUnnamedArg(f *introspect.Function) bool {
	for _, a := range f.Args {
		if a.Name == "" {
			return true
		}
	}
	return false
}

// pseudoTypes are the pg_catalog pseudo-types a function cannot be called
// with (or return) from a plain SQL statement with typed parameters:
// polymorphic types need a concrete argument type to resolve, records have
// no addressable shape, the rest are internal.
var pseudoTypes = map[string]bool{
	"record": true, "_record": true, "internal": true, "cstring": true, "unknown": true, "any": true,
	"language_handler": true, "fdw_handler": true, "index_am_handler": true, "tsm_handler": true, "table_am_handler": true,
	"trigger": true, "event_trigger": true, "pg_ddl_command": true,
	"anyelement": true, "anyarray": true, "anynonarray": true, "anyenum": true, "anyrange": true, "anymultirange": true,
	"anycompatible": true, "anycompatiblearray": true, "anycompatiblenonarray": true, "anycompatiblerange": true, "anycompatiblemultirange": true,
}

func isPseudoType(schema, name string) bool {
	return (schema == "" || schema == "pg_catalog") && pseudoTypes[strings.TrimPrefix(name, "_")]
}

// pseudoTyped reports the first pseudo-typed argument name, or "return"
// when the return type is one; "" when the signature is fully callable. A
// record return described by output parameters (returnsRecordColumns) is
// callable when those columns are.
func pseudoTyped(f *introspect.Function) string {
	if returnsRecordColumns(f) {
		for _, c := range f.ReturnColumns {
			if isPseudoType(c.TypeSchema, c.PGType) {
				return "return"
			}
		}
	} else if isPseudoType(f.ReturnTypeSchema, f.ReturnType) {
		return "return"
	}
	for _, a := range f.Args {
		if isPseudoType(a.TypeSchema, a.PGType) || a.PGType == "void" {
			return a.Name
		}
	}
	return ""
}

func (b *Builder) rowTypeArg(f *introspect.Function) (int, *introspect.Table) {
	for i, a := range f.Args {
		if t := b.Catalog.Table(a.TypeSchema, a.PGType); t != nil {
			return i, t
		}
	}
	return -1, nil
}

func (b *Builder) addFunctions() {
	if !b.Options.Functions {
		return
	}
	segments := map[string]map[string]bool{} // schema -> segment
	for _, f := range b.sortedFunctions() {
		fkey := f.Schema + "." + f.Name
		if hasUnnamedArg(f) {
			b.log.Debug("skipping function: unnamed argument", "function", fkey)
			continue
		}
		if what := pseudoTyped(f); what != "" {
			b.log.Debug("skipping function: pseudo-type or polymorphic signature", "function", fkey, "position", what)
			continue
		}
		if i, t := b.rowTypeArg(f); i == 0 {
			b.addComputed(t, f)
			continue
		} else if i > 0 {
			b.log.Debug("skipping function: row-type argument not in first position", "function", fkey, "arg", f.Args[i].Name)
			continue
		}
		rpc := &RPC{
			Function:    f,
			Methods:     map[Method]bool{MethodPost: true},
			Mutation:    f.Volatility == introspect.VolatilityVolatile,
			Mint:        b.Options.JWTType != "" && f.ReturnTypeSchema+"."+f.ReturnType == b.Options.JWTType,
			Args:        f.Args,
			Description: description(f.Comment),
		}
		if !rpc.Mutation {
			rpc.Methods[MethodGet] = true
			rpc.Methods[MethodHead] = true
		}
		if !b.resolveReturn(rpc) {
			continue
		}
		seg := b.Inflect(inflect.KindRPCName, coreinflect.Input{Schema: f.Schema, Function: f.Name})
		if segments[f.Schema] == nil {
			segments[f.Schema] = map[string]bool{}
		}
		if segments[f.Schema][seg] {
			b.Warn("skipping function: overload or path collision, keeping first (later overloads are unreachable)", "function", fkey, "segment", seg)
			continue
		}
		segments[f.Schema][seg] = true
		rpc.Segment = seg
		rpc.Bare = f.Schema == b.primarySchema()
		if rpc.Bare {
			rpc.Path = seg
		} else {
			rpc.Path = f.Schema + "." + seg
		}
		if rpc.Record {
			rpc.Resource.Path = b.claimOpenAPIName("rpc."+rpc.Path, "function "+fkey+" result")
		}
		b.RPCs[fkey] = rpc
	}
}

// returnsRecordColumns reports whether f returns record rows whose columns
// are its output parameters (OUT/INOUT/TABLE, at least two of them).
func returnsRecordColumns(f *introspect.Function) bool {
	return f.ReturnType == "record" && (f.ReturnTypeSchema == "" || f.ReturnTypeSchema == "pg_catalog") && len(f.ReturnColumns) > 1
}

// recordResource is the read-only resource a record-returning function's
// rows are compiled and documented against: one column per output
// parameter, every non-composite column filterable and orderable (the
// function output is scanned, so there is no index policy to honour). It
// has no routes, relations or keys and is not part of Resources.
func (b *Builder) recordResource(f *introspect.Function) *Resource {
	t := &introspect.Table{Schema: f.Schema, Name: f.Name, Kind: introspect.RelView, Privileges: introspect.Privileges{Select: true}}
	r := &Resource{
		Table:           t,
		Segment:         f.Name,
		Methods:         map[Method]bool{},
		Computed:        map[string]*Computed{},
		Pseudo:          map[string]*PseudoColumn{},
		Relations:       map[string][]*Relation{},
		FilterColumns:   map[string]bool{},
		OrderColumns:    map[string]bool{},
		InputColumns:    map[string]*introspect.Column{},
		CostMultipliers: map[string]int{},
		Description:     "Result row of " + f.Schema + "." + f.Name + ".",
	}
	for i, a := range f.ReturnColumns {
		c := ArgColumn(b.Catalog, a)
		c.Position = i + 1
		t.Columns = append(t.Columns, c)
		r.Columns = append(r.Columns, &Field{Column: c, Key: a.Name})
		if b.Catalog.Composite(c.TypeSchema, strings.TrimPrefix(c.PGType, "_")) == nil {
			r.FilterColumns[a.Name] = true
			r.OrderColumns[a.Name] = true
		}
	}
	return r
}

// resolveReturn classifies the return shape; it returns false when the
// function returns a catalog table that is not exposed (nothing to compile
// the rows against).
func (b *Builder) resolveReturn(rpc *RPC) bool {
	f := rpc.Function
	fkey := f.Schema + "." + f.Name
	switch {
	case f.ReturnType == "void":
		rpc.Returns = ReturnVoid
		return true
	case returnsRecordColumns(f):
		rpc.Resource = b.recordResource(f)
		rpc.Record = true
		rpc.Returns = ReturnRow
		if f.ReturnsSet {
			rpc.Returns = ReturnSetOfRows
		}
		return true
	case b.Catalog.Table(f.ReturnTypeSchema, f.ReturnType) != nil:
		r := b.Resource(f.ReturnTypeSchema, f.ReturnType)
		if r == nil {
			b.Warn("skipping function: returns a table that is not exposed", "function", fkey, "table", f.ReturnTypeSchema+"."+f.ReturnType)
			return false
		}
		rpc.Resource = r
		rpc.Returns = ReturnRow
		if f.ReturnsSet {
			rpc.Returns = ReturnSetOfRows
		}
		return true
	case b.Catalog.Composite(f.ReturnTypeSchema, f.ReturnType) != nil:
		rpc.Composite = b.Composites[f.ReturnTypeSchema+"."+f.ReturnType]
		rpc.Returns = ReturnComposite
		if f.ReturnsSet {
			rpc.Returns = ReturnSetOfComposite
		}
		return true
	default:
		rpc.Returns = ReturnScalar
		if f.ReturnsSet {
			rpc.Returns = ReturnSetOfScalar
		}
		return true
	}
}

// extraArgsSupported reports whether every argument after the first (the
// row) is a plain IN parameter, which select=field(k=v) can pass by name.
func extraArgsSupported(f *introspect.Function) bool {
	for _, a := range f.Args[1:] {
		if a.Variadic() || a.Mode == introspect.ArgModeInOut {
			return false
		}
	}
	return true
}

// extraArgsDefaulted reports whether every argument after the first (the
// row) has a DEFAULT, so the function can be called with the row alone.
func extraArgsDefaulted(f *introspect.Function) bool {
	for _, a := range f.Args[1:] {
		if !a.HasDefault {
			return false
		}
	}
	return true
}

// addComputed exposes a stable/immutable function whose first argument is
// t's row type as a computed column on t's resource. Extra arguments are
// passed with select=key(k=v); filters and order= only admit columns whose
// extra arguments all have defaults.
func (b *Builder) addComputed(t *introspect.Table, f *introspect.Function) {
	fkey := f.Schema + "." + f.Name
	r := b.Resource(t.Schema, t.Name)
	if r == nil {
		return
	}
	switch {
	case f.Volatility == introspect.VolatilityVolatile:
		b.log.Debug("skipping computed column: volatile", "function", fkey)
		return
	case f.ReturnType == "void":
		b.log.Debug("skipping computed column: void return", "function", fkey)
		return
	case !extraArgsSupported(f):
		b.log.Debug("skipping computed column: variadic and INOUT extra arguments are not supported", "function", fkey)
		return
	}
	key := b.Inflect(inflect.KindComputedField, coreinflect.Input{Schema: f.Schema, Table: t.Name, Function: f.Name})
	if r.Field(key) != nil {
		b.Warn("computed column skipped: a real column owns the key", "function", fkey, "key", key)
		return
	}
	if _, dup := r.Computed[key]; dup {
		b.Warn("computed column key collision, keeping first", "function", fkey, "key", key)
		return
	}
	c := &Computed{
		Key: key, Function: f, Resource: r, Description: description(f.Comment),
		Args:       f.Args[1:],
		Filterable: !f.ReturnsSet && extraArgsDefaulted(f),
		Orderable:  !f.ReturnsSet && extraArgsDefaulted(f),
	}
	if f.ReturnsSet && b.Catalog.Table(f.ReturnTypeSchema, f.ReturnType) != nil {
		c.SetOf = b.Resource(f.ReturnTypeSchema, f.ReturnType)
		if c.SetOf == nil {
			b.Warn("computed column skipped: returns a table that is not exposed", "function", fkey)
			return
		}
	}
	r.Computed[key] = c
}

// Built is the frozen model: lookup tables, router table, OpenAPI document.
type Built struct {
	Catalog *introspect.Catalog
	Options Options
	// Resources maps "schema.table" -> resource.
	Resources map[string]*Resource
	// ByPath maps resource path key ("users", "audit.users") -> resource.
	ByPath map[string]*Resource
	// RPCs maps "schema.function" -> rpc; RPCByPath maps its path key.
	RPCs      map[string]*RPC
	RPCByPath map[string]*RPC
	// Enums / Composites / Domains are keyed by "schema.name".
	Enums      map[string]*Enum
	Composites map[string]*Composite
	Domains    map[string]*Domain
	// Routes is the deterministic router table.
	Routes []*Route
	// OpenAPI is built once; render with (*Document).JSON.
	OpenAPI  *Document
	Surface  Surface
	Extra    OpenAPIExtras
	Warnings []string

	bare       map[string]*Resource            // segment -> resource
	qualified  map[string]map[string]*Resource // schema -> segment -> resource
	bareRPC    map[string]*RPC
	qualRPC    map[string]map[string]*RPC
	schemas    map[string]bool
	byResource map[*Resource]map[RouteKind]*Route
	byUnique   map[*Resource]map[string]*Route
	byRPC      map[*RPC]*Route
}

// Build freezes the builder.
func (b *Builder) Build() (*Built, error) {
	out := &Built{
		Catalog:    b.Catalog,
		Options:    b.Options,
		Resources:  b.Resources,
		ByPath:     map[string]*Resource{},
		RPCs:       b.RPCs,
		RPCByPath:  map[string]*RPC{},
		Enums:      b.Enums,
		Composites: b.Composites,
		Domains:    b.Domains,
		Surface:    b.Surface,
		Extra:      b.Extra,
		Warnings:   append([]string(nil), b.Warnings...),
		bare:       map[string]*Resource{},
		qualified:  map[string]map[string]*Resource{},
		bareRPC:    map[string]*RPC{},
		qualRPC:    map[string]map[string]*RPC{},
		schemas:    b.schemaNames,
		byResource: map[*Resource]map[RouteKind]*Route{},
		byUnique:   map[*Resource]map[string]*Route{},
		byRPC:      map[*RPC]*Route{},
	}
	for _, r := range sortedResources(b.Resources) {
		if _, dup := out.ByPath[r.Path]; dup {
			return nil, fmt.Errorf("api: duplicate resource path %q", r.Path)
		}
		out.ByPath[r.Path] = r
		if r.Bare {
			out.bare[r.Segment] = r
		}
		if out.qualified[r.Table.Schema] == nil {
			out.qualified[r.Table.Schema] = map[string]*Resource{}
		}
		out.qualified[r.Table.Schema][r.Segment] = r
		out.byResource[r] = map[RouteKind]*Route{}
		out.byUnique[r] = map[string]*Route{}
		base := r.urlPrefix()
		listMethods := methodList(r.Methods, MethodGet, MethodHead, MethodPost, MethodPut, MethodPatch, MethodDelete)
		if r.OmitList || !r.Methods[MethodGet] {
			listMethods = methodList(r.Methods, MethodPost, MethodPut)
		}
		if ms := listMethods; len(ms) > 0 {
			rt := &Route{Kind: RouteList, Pattern: base, Methods: ms, Resource: r}
			out.Routes = append(out.Routes, rt)
			out.byResource[r][RouteList] = rt
		}
		rowMethods := methodList(r.Methods, MethodGet, MethodHead, MethodPut, MethodPatch, MethodDelete)
		if pk := r.PrimaryKey(); pk != nil && !r.HiddenKey && len(rowMethods) > 0 {
			rt := &Route{Kind: RouteRow, Pattern: base + "/" + r.keyTemplate(pk.Columns), Methods: keyedMethods(r, rowMethods, pk.Columns), Resource: r}
			out.Routes = append(out.Routes, rt)
			out.byResource[r][RouteRow] = rt
		}
		if len(rowMethods) > 0 {
			for _, u := range r.Uniques {
				rt := &Route{
					Kind:     RouteUnique,
					Pattern:  base + "/by/" + strings.Join(r.keys(u.Columns), ",") + "/" + r.keyTemplate(u.Columns),
					Methods:  keyedMethods(r, rowMethods, u.Columns),
					Resource: r,
					Unique:   u,
				}
				out.Routes = append(out.Routes, rt)
				out.byUnique[r][u.Name] = rt
			}
		}
	}
	for _, rpc := range sortedRPCs(b.RPCs) {
		if _, dup := out.RPCByPath[rpc.Path]; dup {
			return nil, fmt.Errorf("api: duplicate rpc path %q", rpc.Path)
		}
		out.RPCByPath[rpc.Path] = rpc
		if rpc.Bare {
			out.bareRPC[rpc.Segment] = rpc
		}
		if out.qualRPC[rpc.Function.Schema] == nil {
			out.qualRPC[rpc.Function.Schema] = map[string]*RPC{}
		}
		out.qualRPC[rpc.Function.Schema][rpc.Segment] = rpc
		rt := &Route{Kind: RouteRPC, Pattern: rpc.urlPath(), Methods: methodList(rpc.Methods, MethodGet, MethodHead, MethodPost), RPC: rpc}
		out.Routes = append(out.Routes, rt)
		out.byRPC[rpc] = rt
	}
	out.OpenAPI = b.openAPI(out)
	return out, nil
}

// keyedMethods drops PUT from a single-row route whose key has a column a
// PUT body may not write (generated, identity, @omit create or update): the
// upsert would insert the path value into it.
func keyedMethods(r *Resource, methods []Method, key []string) []Method {
	writable := map[string]bool{}
	for _, c := range r.InputColumnsFor(MethodPut) {
		writable[c.Name] = true
	}
	for _, name := range key {
		if c := r.Table.Column(name); c == nil || c.Generated || !writable[name] {
			out := make([]Method, 0, len(methods))
			for _, m := range methods {
				if m != MethodPut {
					out = append(out, m)
				}
			}
			return out
		}
	}
	return methods
}

func sortedResources(m map[string]*Resource) []*Resource {
	out := make([]*Resource, 0, len(m))
	for _, r := range m {
		out = append(out, r)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Path < out[j].Path })
	return out
}

func sortedRPCs(m map[string]*RPC) []*RPC {
	out := make([]*RPC, 0, len(m))
	for _, r := range m {
		out = append(out, r)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Path < out[j].Path })
	return out
}

// urlPrefix is the documented URL of the resource below base_path.
func (r *Resource) urlPrefix() string {
	if r.Bare {
		return "/" + r.Segment
	}
	return "/" + r.Table.Schema + "/" + r.Segment
}

// URLPath returns the documented list URL below base_path.
func (r *Resource) URLPath() string { return r.urlPrefix() }

func (r *Resource) keys(cols []string) []string {
	out := make([]string, len(cols))
	for i, c := range cols {
		if f := r.FieldByColumn(c); f != nil {
			out[i] = f.Key
		} else {
			out[i] = c
		}
	}
	return out
}

func (r *Resource) keyTemplate(cols []string) string {
	ks := r.keys(cols)
	for i, k := range ks {
		ks[i] = "{" + k + "}"
	}
	return strings.Join(ks, ",")
}

func (rpc *RPC) urlPath() string {
	if rpc.Bare {
		return "/rpc/" + rpc.Segment
	}
	return "/" + rpc.Function.Schema + "/rpc/" + rpc.Segment
}

// URLPath returns the documented URL below base_path.
func (rpc *RPC) URLPath() string { return rpc.urlPath() }

// RouteFor returns the route of the given kind on r (nil when absent).
func (b *Built) RouteFor(r *Resource, kind RouteKind) *Route { return b.byResource[r][kind] }

// UniqueRoute returns the /by/ route for a unique constraint (nil when absent).
func (b *Built) UniqueRoute(r *Resource, u *introspect.Constraint) *Route {
	if u == nil {
		return nil
	}
	return b.byUnique[r][u.Name]
}

// RPCRoute returns the route of an RPC.
func (b *Built) RPCRoute(rpc *RPC) *Route { return b.byRPC[rpc] }
