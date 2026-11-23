package exec

import (
	"slices"

	"github.com/jackc/pgx/v5/pgconn"
	"github.com/suprbdev/pdbcore/introspect"

	"github.com/suprbdev/pdbr/internal/api"
)

// integrityMessages are the generic messages of integrity violations that
// name an object the serving API hides (integrityHidden).
var integrityMessages = map[string]string{
	"23502": "not-null violation",
	"23503": "foreign key violation",
	"23001": "foreign key violation",
	"23505": "unique violation",
	"23514": "check violation",
	"23P01": "exclusion violation",
}

// integrityMessage returns the generic message for class 23 code.
func integrityMessage(code string) string {
	if m, ok := integrityMessages[code]; ok {
		return m
	}
	return "constraint violation"
}

// integrityHidden reports whether a class 23 error names something built
// does not expose: a table missing from the served catalog (a schema
// contract, a bare @omit, an unexposed schema), a column hidden from both
// responses and bodies, a foreign key the catalog dropped or one whose
// referenced table is missing, or another constraint or index (primary
// key, unique, check, exclusion, an index over expressions or with a
// predicate) referencing a hidden column, or one the catalog does not
// record on a table with any hidden column. The messages
// of such errors name the table, the constraint (whose name usually
// spells its columns) and, for foreign keys, the other table.
func integrityHidden(built *api.Built, pgErr *pgconn.PgError) bool {
	if built == nil || built.Catalog == nil || pgErr.TableName == "" {
		return false
	}
	cat := built.Catalog
	t := cat.Table(pgErr.SchemaName, pgErr.TableName)
	if t == nil {
		return true
	}
	if pgErr.ColumnName != "" && columnHidden(built, t, pgErr.ColumnName) {
		return true
	}
	name := pgErr.ConstraintName
	if name == "" {
		return false
	}
	switch pgErr.Code {
	case "23503", "23001":
		for _, fk := range t.ForeignKeys {
			if fk.Name != name {
				continue
			}
			ref := cat.Table(fk.RefSchema, fk.RefTable)
			return ref == nil || anyColumnHidden(built, t, fk.Columns) || anyColumnHidden(built, ref, fk.RefColumns)
		}
		return true
	case "23502":
		return false
	}
	hidden := func(col string) bool { return columnHidden(built, t, col) }
	refs, ok := t.ConstraintReferences(name, hidden)
	if !ok {
		return slices.ContainsFunc(t.Columns, func(c *introspect.Column) bool { return hidden(c.Name) })
	}
	return refs
}

func anyColumnHidden(built *api.Built, t *introspect.Table, cols []string) bool {
	for _, c := range cols {
		if columnHidden(built, t, c) {
			return true
		}
	}
	return false
}

// columnHidden reports whether column col of t is unknown to the catalog,
// or hidden from both responses and request bodies of t's resource (the
// parent's for a partition). A table without a resource has no
// column-level hiding to honour.
func columnHidden(built *api.Built, t *introspect.Table, col string) bool {
	if t.Column(col) == nil {
		return true
	}
	r := built.Resources[t.Schema+"."+t.Name]
	if r == nil && t.PartitionOf != "" {
		r = built.Resources[t.PartitionOf]
	}
	if r == nil || r.FieldByColumn(col) != nil {
		return false
	}
	for _, c := range r.InputColumns {
		if c.Name == col {
			return false
		}
	}
	return true
}
