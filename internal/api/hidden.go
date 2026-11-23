package api

import (
	"slices"
	"strings"

	"github.com/suprbdev/pdbcore/introspect"
	"github.com/suprbdev/pdbcore/postgis"
)

// ColumnTypeSchema returns the schema of c's resolved type (PGType): its
// BaseTypeSchema, or TypeSchema when that is unknown (hand-built catalogs).
// For a column declared as a domain, TypeSchema is the domain's schema.
func ColumnTypeSchema(c *introspect.Column) string {
	if c.BaseTypeSchema != "" {
		return c.BaseTypeSchema
	}
	return c.TypeSchema
}

// RowTable returns the table whose row type c's type is (or an array of
// it), or nil.
func RowTable(cat *introspect.Catalog, c *introspect.Column) *introspect.Table {
	if cat == nil || c == nil {
		return nil
	}
	return cat.Table(ColumnTypeSchema(c), strings.TrimPrefix(c.PGType, "_"))
}

// RenderedFields returns the attributes of comp that responses carry:
// every attribute but raster ones, whose bytes are never returned.
func RenderedFields(comp *introspect.Composite) []*introspect.Column {
	out := make([]*introspect.Column, 0, len(comp.Fields))
	for _, f := range comp.Fields {
		if !postgis.IsRaster(strings.TrimPrefix(f.PGType, "_")) {
			out = append(out, f)
		}
	}
	return out
}

// NestsRows reports whether a value of c's type is, or holds through
// composite attributes and arrays, a table row or a raster. Such a value
// is only ever read through its rendered form (a row as its resource's
// readable columns, no raster bytes): JSON paths and casts apply to that
// form, and it cannot be filtered, ordered, grouped or compared.
func NestsRows(cat *introspect.Catalog, c *introspect.Column) bool {
	return nestsRows(cat, c, map[string]bool{})
}

func nestsRows(cat *introspect.Catalog, c *introspect.Column, seen map[string]bool) bool {
	if cat == nil || c == nil {
		return false
	}
	elem := strings.TrimPrefix(c.PGType, "_")
	if postgis.IsRaster(elem) || RowTable(cat, c) != nil {
		return true
	}
	key := ColumnTypeSchema(c) + "." + elem
	comp := cat.Composite(ColumnTypeSchema(c), elem)
	if comp == nil || seen[key] {
		return false
	}
	seen[key] = true
	for _, f := range comp.Fields {
		if nestsRows(cat, f, seen) {
			return true
		}
	}
	return false
}

// unexposedIn returns the "schema.type" of the first type the API must not
// expose that a value of schema.typ is or holds, or "": the row type of a
// hidden table (see Options.HiddenTables; hidden is then true), or a type
// from a schema outside the introspected ones that is no known table,
// enum, composite, domain, range, spatial or extension type (typically the
// row type of a table in an unexposed schema). Domains resolve to their
// base type (domain is the "schema.name" ref of the domain typ is, if it
// is one), composites are walked attribute by attribute and arrays by
// element type. When nested is set, the row type of a table that has no
// resource counts as hidden too: such a row has no readable column list to
// render it through. Every value reached through a domain or a composite
// is nested.
func (b *Builder) unexposedIn(schema, typ, domain string, nested bool, seen map[string]bool) (ref string, hidden bool) {
	elem := strings.TrimPrefix(typ, "_")
	if domain != "" {
		d := b.Catalog.DomainByRef(domain)
		if d == nil {
			return "", false
		}
		return b.unexposedIn(d.BaseTypeSchema, d.BaseType, "", true, seen)
	}
	if d := b.Catalog.Domain(schema, elem); d != nil {
		return b.unexposedIn(d.BaseTypeSchema, d.BaseType, "", true, seen)
	}
	key := schema + "." + elem
	if b.Options.HiddenTables[key] {
		return key, true
	}
	if b.Catalog.Table(schema, elem) != nil {
		if nested && b.Resource(schema, elem) == nil {
			return key, true
		}
		return "", false
	}
	if comp := b.Catalog.Composite(schema, elem); comp != nil {
		if seen[key] {
			return "", false
		}
		seen[key] = true
		for _, f := range comp.Fields {
			if ref, hidden := b.unexposedIn(ColumnTypeSchema(f), f.PGType, "", true, seen); ref != "" {
				return ref, hidden
			}
		}
		return "", false
	}
	if b.foreignType(schema, elem) {
		return key, false
	}
	return "", false
}

// unexposedType is unexposedIn over every type f returns or takes (result,
// arguments, output columns). An argument or a single non-array result of
// a table's row type is not nested: rowArgResources and resolveReturn
// decide those.
func (b *Builder) unexposedType(f *introspect.Function) (ref string, hidden bool) {
	direct := !strings.HasPrefix(f.ReturnType, "_") && f.ReturnDomain == ""
	if ref, hidden := b.unexposedIn(f.ReturnTypeSchema, f.ReturnType, f.ReturnDomain, !direct, map[string]bool{}); ref != "" {
		return ref, hidden
	}
	for _, a := range f.Args {
		if ref, hidden := b.unexposedIn(a.TypeSchema, a.PGType, a.Domain, false, map[string]bool{}); ref != "" {
			return ref, hidden
		}
	}
	for _, a := range f.ReturnColumns {
		if ref, hidden := b.unexposedIn(a.TypeSchema, a.PGType, a.Domain, true, map[string]bool{}); ref != "" {
			return ref, hidden
		}
	}
	return "", false
}

// returnsRaster reports whether f returns raster values (or arrays of
// them) directly: their bytes are never returned.
func (b *Builder) returnsRaster(f *introspect.Function) bool {
	return postgis.IsRaster(strings.TrimPrefix(ReturnColumn(b.Catalog, f).PGType, "_"))
}

// foreignType reports whether schema.elem lives in a schema that is
// neither built in nor introspected and resolves to nothing the catalog
// describes. The rls.auth.jwt_type composite (Options.JWTType) is never
// foreign: its values are minted into tokens, not rendered.
func (b *Builder) foreignType(schema, elem string) bool {
	cat := b.Catalog
	switch {
	case schema == "" || schema == "pg_catalog",
		slices.Contains(cat.Schemas, schema),
		schema+"."+elem == b.Options.JWTType,
		cat.Table(schema, elem) != nil,
		cat.Composite(schema, elem) != nil,
		cat.Enum(schema, elem) != nil,
		postgis.IsSpatial(elem):
		return false
	}
	for _, r := range cat.Ranges {
		if (r.Schema == schema && r.Name == elem) || (r.MultirangeSchema == schema && r.Multirange == elem) {
			return false
		}
	}
	for _, e := range cat.Extensions {
		if e.Schema == schema {
			return false
		}
	}
	return true
}

// hideUnexposedColumns removes from every surface the columns of every
// resource whose type is or holds a type unexposedIn reports: the row
// type of a hidden table or of a table without a resource, or a foreign
// type. It runs once all resources exist.
func (b *Builder) hideUnexposedColumns() {
	for _, key := range sortedKeys(b.Resources) {
		r := b.Resources[key]
		for _, f := range append([]*Field(nil), r.Columns...) {
			c := f.Column
			ref, hidden := b.unexposedIn(ColumnTypeSchema(c), c.PGType, "", true, map[string]bool{})
			switch {
			case hidden:
				b.log.Info("hiding column: its type holds rows of a hidden or unexposed table",
					"table", r.Table.Schema+"."+r.Table.Name, "column", c.Name, "type", ref)
			case ref != "":
				b.Warn("hiding column: its type holds a type from a schema outside schema.schemas (the row type of an unexposed table?)",
					"table", r.Table.Schema+"."+r.Table.Name, "column", c.Name, "type", ref)
			default:
				continue
			}
			b.RemoveField(r, f.Key, true)
		}
	}
}
