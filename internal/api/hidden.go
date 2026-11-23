package api

import (
	"slices"
	"strings"

	"github.com/suprbdev/pdbcore/introspect"
	"github.com/suprbdev/pdbcore/postgis"
)

// hiddenRowType returns the "schema.table" key of the hidden table (see
// Options.HiddenTables) whose row type, or array of it, schema.typ is, or "".
func (b *Builder) hiddenRowType(schema, typ string) string {
	key := schema + "." + strings.TrimPrefix(typ, "_")
	if b.Options.HiddenTables[key] {
		return key
	}
	return ""
}

// unexposedType returns the "schema.type" of the first type f returns or
// takes (result, arguments, output columns) that the API must not expose,
// or "": the row type of a hidden table (hidden is then true), or a type
// from a schema outside the introspected ones that is no known table,
// enum, composite, domain, range, spatial or extension type. The latter
// is typically the row type of a table in an unexposed schema. Either
// would otherwise be rendered whole as JSON (or accepted as a raw record
// literal).
func (b *Builder) unexposedType(f *introspect.Function) (ref string, hidden bool) {
	refs := []introspect.FuncArg{{PGType: f.ReturnType, TypeSchema: f.ReturnTypeSchema, Domain: f.ReturnDomain}}
	refs = append(append(refs, f.Args...), f.ReturnColumns...)
	for _, a := range refs {
		if key := b.hiddenRowType(a.TypeSchema, a.PGType); key != "" {
			return key, true
		}
		if b.foreignType(a) {
			return a.TypeSchema + "." + strings.TrimPrefix(a.PGType, "_"), false
		}
	}
	return "", false
}

// foreignType reports whether a's type lives in a schema that is neither
// built in nor introspected and resolves to nothing the catalog describes.
// The rls.auth.jwt_type composite (Options.JWTType) is never foreign: its
// values are minted into tokens, not rendered.
func (b *Builder) foreignType(a introspect.FuncArg) bool {
	elem := strings.TrimPrefix(a.PGType, "_")
	cat := b.Catalog
	switch {
	case a.TypeSchema == "" || a.TypeSchema == "pg_catalog" || a.Domain != "",
		slices.Contains(cat.Schemas, a.TypeSchema),
		a.TypeSchema+"."+elem == b.Options.JWTType,
		cat.Table(a.TypeSchema, elem) != nil,
		cat.Composite(a.TypeSchema, elem) != nil,
		cat.Enum(a.TypeSchema, elem) != nil,
		postgis.IsSpatial(elem):
		return false
	}
	for _, r := range cat.Ranges {
		if (r.Schema == a.TypeSchema && r.Name == elem) || (r.MultirangeSchema == a.TypeSchema && r.Multirange == elem) {
			return false
		}
	}
	for _, e := range cat.Extensions {
		if e.Schema == a.TypeSchema {
			return false
		}
	}
	return true
}

// hideRowTypedColumns removes the columns of r typed as a hidden table's
// row type (or an array of it) from every surface.
func (b *Builder) hideRowTypedColumns(r *Resource) {
	for _, f := range append([]*Field(nil), r.Columns...) {
		if key := b.hiddenRowType(f.Column.TypeSchema, f.Column.PGType); key != "" {
			b.log.Info("hiding column: typed as the row type of a hidden table",
				"table", r.Table.Schema+"."+r.Table.Name, "column", f.Column.Name, "type", key)
			b.RemoveField(r, f.Key, true)
		}
	}
}
