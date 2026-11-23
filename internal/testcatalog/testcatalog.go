// Package testcatalog extends the shared pdbcore fixture catalog with the
// in-memory objects pdbr's hermetic tests need and the fixture database
// lacks. It is imported by tests only.
package testcatalog

import (
	"github.com/suprbdev/pdbcore/introspect"
)

// ContribSchema is the schema of the contrib table Contrib adds, and
// ContribExtSchema the schema its extensions are recorded in (off the
// search_path, so every extension name renders schema-qualified).
const (
	ContribSchema    = "kb"
	ContribExtSchema = "ext"
)

// Contrib adds the hstore, ltree, citext, pg_trgm, unaccent and vector
// extensions (in ContribExtSchema) and the table kb.docs, which has one
// column of every non-vector type a pdbcore/contrib vocabulary covers, to
// cat, and appends ContribSchema to cat.Schemas. Its indexes serve the
// trigram operators and distances on title, full-text search on search, the
// hstore, ltree, range, multirange and inet operators; body has no index
// (full-text search and the unaccent operators on a text column need
// filters.allow_columns under filters.indexed_only).
// kb.authors (citext primary key; meta, a domain in schema app over hstore)
// and kb.notes (citext foreign key to it) cover keys, joins and ordering on
// extension-owned types. kb.embeddings has a pgvector column of each type:
// emb (vector, HNSW index), half (halfvec, IVFFlat index) and sparse
// (sparsevec, no index), plus code (bit, HNSW index) for pgvector's bit
// distances, and kb.chunks a vector column embedded under it.
func Contrib(cat *introspect.Catalog) *introspect.Catalog {
	cat.Schemas = append(cat.Schemas, ContribSchema)
	cat.Extensions = append(cat.Extensions,
		&introspect.Extension{Name: "citext", Version: "1.6", Schema: ContribExtSchema},
		&introspect.Extension{Name: "hstore", Version: "1.8", Schema: ContribExtSchema},
		&introspect.Extension{Name: "ltree", Version: "1.3", Schema: ContribExtSchema},
		&introspect.Extension{Name: "pg_trgm", Version: "1.6", Schema: ContribExtSchema},
		&introspect.Extension{Name: "unaccent", Version: "1.1", Schema: ContribExtSchema},
		&introspect.Extension{Name: "vector", Version: "0.8.0", Schema: ContribExtSchema},
	)
	core := func(name string, pos int, typ string) *introspect.Column {
		return &introspect.Column{Name: name, Position: pos, PGType: typ, TypeSchema: "pg_catalog"}
	}
	ext := func(name string, pos int, typ string) *introspect.Column {
		return &introspect.Column{Name: name, Position: pos, PGType: typ, TypeSchema: ContribExtSchema, IsArray: typ[0] == '_'}
	}
	id := core("id", 1, "int4")
	id.NotNull = true
	cols := []*introspect.Column{
		id,
		core("title", 2, "text"),
		core("body", 3, "text"),
		core("search", 4, "tsvector"),
		ext("email", 5, "citext"),
		ext("attrs", 6, "hstore"),
		ext("path", 7, "ltree"),
		ext("paths", 8, "_ltree"),
		core("pages", 9, "int4range"),
		core("valid", 10, "daterange"),
		core("slots", 11, "int4multirange"),
		core("net", 12, "inet"),
	}
	idx := func(name, method string, cols ...string) *introspect.Index {
		return &introspect.Index{Name: name, Columns: cols, Method: method}
	}
	cat.Tables = append(cat.Tables, &introspect.Table{
		Schema: ContribSchema, Name: "docs", Kind: introspect.RelTable,
		Columns:    cols,
		PrimaryKey: &introspect.Constraint{Name: "docs_pkey", Columns: []string{"id"}},
		Uniques:    []*introspect.Constraint{{Name: "docs_email_key", Columns: []string{"email"}}},
		Indexes: []*introspect.Index{
			idx("docs_attrs_idx", "gin", "attrs"),
			{Name: "docs_email_key", Columns: []string{"email"}, Unique: true, Method: "btree"},
			idx("docs_net_idx", "gist", "net"),
			idx("docs_pages_idx", "gist", "pages"),
			idx("docs_path_idx", "gist", "path"),
			idx("docs_paths_idx", "gist", "paths"),
			idx("docs_search_idx", "gin", "search"),
			idx("docs_slots_idx", "gist", "slots"),
			idx("docs_title_idx", "gist", "title"),
			idx("docs_valid_idx", "gist", "valid"),
		},
		Privileges: introspect.Privileges{Select: true, Insert: true, Update: true, Delete: true},
	})
	priv := introspect.Privileges{Select: true, Insert: true, Update: true, Delete: true}
	authorEmail := ext("email", 1, "citext")
	authorEmail.NotNull = true
	meta := ext("meta", 3, "hstore")
	meta.TypeSchema = "app"
	noteID := core("id", 1, "int4")
	noteID.NotNull = true
	cat.Tables = append(cat.Tables,
		&introspect.Table{
			Schema: ContribSchema, Name: "authors", Kind: introspect.RelTable,
			Columns:    []*introspect.Column{authorEmail, core("name", 2, "text"), meta},
			PrimaryKey: &introspect.Constraint{Name: "authors_pkey", Columns: []string{"email"}},
			Indexes:    []*introspect.Index{idx("authors_meta_idx", "btree", "meta")},
			Privileges: priv,
		},
		&introspect.Table{
			Schema: ContribSchema, Name: "notes", Kind: introspect.RelTable,
			Columns:    []*introspect.Column{noteID, ext("author", 2, "citext"), core("body", 3, "text")},
			PrimaryKey: &introspect.Constraint{Name: "notes_pkey", Columns: []string{"id"}},
			ForeignKeys: []*introspect.ForeignKey{{Name: "notes_author_fkey", Columns: []string{"author"},
				RefSchema: ContribSchema, RefTable: "authors", RefColumns: []string{"email"}}},
			Indexes:    []*introspect.Index{idx("notes_author_idx", "btree", "author")},
			Privileges: priv,
		},
	)
	embID := core("id", 1, "int4")
	embID.NotNull = true
	chunkID := core("id", 1, "int4")
	chunkID.NotNull = true
	cat.Tables = append(cat.Tables,
		&introspect.Table{
			Schema: ContribSchema, Name: "embeddings", Kind: introspect.RelTable,
			Columns: []*introspect.Column{embID, core("title", 2, "text"),
				ext("emb", 3, "vector"), ext("half", 4, "halfvec"), ext("sparse", 5, "sparsevec"), core("code", 6, "bit")},
			PrimaryKey: &introspect.Constraint{Name: "embeddings_pkey", Columns: []string{"id"}},
			Indexes: []*introspect.Index{
				idx("embeddings_code_idx", "hnsw", "code"),
				idx("embeddings_emb_idx", "hnsw", "emb"),
				idx("embeddings_half_idx", "ivfflat", "half"),
			},
			Privileges: priv,
		},
		&introspect.Table{
			Schema: ContribSchema, Name: "chunks", Kind: introspect.RelTable,
			Columns:    []*introspect.Column{chunkID, core("doc_id", 2, "int4"), ext("emb", 3, "vector")},
			PrimaryKey: &introspect.Constraint{Name: "chunks_pkey", Columns: []string{"id"}},
			ForeignKeys: []*introspect.ForeignKey{{Name: "chunks_doc_id_fkey", Columns: []string{"doc_id"},
				RefSchema: ContribSchema, RefTable: "embeddings", RefColumns: []string{"id"}}},
			Indexes: []*introspect.Index{
				idx("chunks_doc_id_idx", "btree", "doc_id"),
				idx("chunks_emb_idx", "hnsw", "emb"),
			},
			Privileges: priv,
		},
	)
	return cat
}
