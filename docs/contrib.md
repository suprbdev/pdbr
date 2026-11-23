# Contrib types: full-text search, trigrams, unaccent, fuzzystrmatch, hstore, ltree, intarray, ranges, inet, pgvector

pdbr gives PostgreSQL's non-scalar built-in types and the common contrib
extensions a filter and ordering surface, built on the operator vocabularies
of `pdbcore/contrib` (shared with pdbq). It is core, not a plugin. Whatever a
vocabulary needs is read from the catalog:

- **Core types** (`tsvector`, text and json/jsonb columns for full-text
  search, the six
  built-in range types and their multiranges, `inet`/`cidr`) always get their
  operators, and so do range types created with `CREATE TYPE … AS RANGE`
  (and their multiranges), which the catalog records with their subtype.
- **Extension types and operators** (`pg_trgm`, `unaccent`,
  `fuzzystrmatch`, `hstore`, `ltree`, `citext`, `intarray`, pgvector's
  `vector`) only appear when the extension is
  installed (`pg_extension`). None of them is required: without one, its
  operators, order terms and type handling are simply absent. The extension
  version matters too: `pg_trgm` older than 1.2 has no word-similarity
  operators and older than 1.4 no strict ones, and pgvector older than 0.7
  has no L1 distance (`vec_l1`) and no bit distances.
- **Extension schemas.** An extension may live in a schema that is not on the
  `search_path`. Every extension type, operator and function is then rendered
  schema-qualified, for example `(t0."attrs") OPERATOR("ext".?) $1::text`,
  `$1::"ext".ltree` or `"ext".hstore($1::text[])`. That includes the generic
  comparisons on extension types (see [citext](#citext)).

Without the extensions and without columns of these types, routes and the
OpenAPI document stay byte-identical (`TestContribGatedOnExtensions`). The
only change every database sees is the PostgREST `isdistinct` operator on
every comparable column.

Operands are always bind parameters, cast inside the SQL (`$1::regconfig`,
`int4range($1::int4, $2::int4, $3::text)`). The SQL text depends on which
operator, which column and the operand's form (a range literal or an element,
a JSON object or an hstore literal, with or without a text search config). It
never depends on the values. `FuzzFilter` covers every vocabulary.

## Operators

PostgREST spells operators after the SQL operator they render (`cs` is `@>`
on every type). pdbr keeps those spellings wherever PostgREST has them and
adds namespaced ones for the rest. Everything marked *pdbr* is an extension
beyond PostgREST.

### Full-text search (PostgREST)

| op | SQL | Columns |
|---|---|---|
| `fts` | `@@ to_tsquery` | `tsvector`; text-like (`text`, `varchar`, `char(n)`, `citext`) and `json`/`jsonb` through `to_tsvector` |
| `plfts` | `@@ plainto_tsquery` | same |
| `phfts` | `@@ phraseto_tsquery` | same |
| `wfts` | `@@ websearch_to_tsquery` | same |

Each takes an optional text search configuration in parentheses. It is a
bind parameter cast to `regconfig`, and may be schema-qualified:

```http
GET /api/articles?search=fts(english).fat%20%26%20cat:*
GET /api/articles?body=wfts(pg_catalog.english).%22fat%20cat%22%20-rat
GET /api/articles?search=not.phfts.fat%20cat
GET /api/articles?or=(search.plfts.fat%20cat,body.fts(simple).rat)
```

On a text column the column is converted with `to_tsvector` in the same
config as the query (`to_tsvector($2::regconfig, col) @@
websearch_to_tsquery($2::regconfig, $1::text)`), like PostgREST. Without a
config, `default_text_search_config` applies. A `json` or `jsonb` column is
searched the same way, as in PostgREST: `to_tsvector(config, doc)` reads the
document's string values (also inside arrays and nested objects) and
ignores its keys, numbers and booleans, so `meta=fts.summary` does not
match `{"summary": "…"}`:

```http
GET /api/articles?meta=wfts(english).cat%20story&order=meta.rank.desc
```

The jsonb operators (`cs`, `cd`, `hk`, JSON paths, …) keep working on the
same column; the full-text spellings do not clash with them. Like full-text
search on a text column, it needs `filters.allow_columns` under
`filters.indexed_only` ([the indexed-only policy](#the-indexed-only-policy)). A malformed `fts` query, an
unknown config or bad ltree syntax is a `400` (`pg:42601` / `pg:42704`), not
a 500.

### Ranges and multiranges (PostgREST)

| op | SQL | Meaning |
|---|---|---|
| `ov` | `&&` | overlaps |
| `cs` | `@>` | contains the range, or the element (`cs.5`, `cs.2020-01-15`) |
| `cd` | `<@` | contained by |
| `sl` | `<<` | strictly left of |
| `sr` | `>>` | strictly right of |
| `nxr` | `&<` | does not extend to the right of |
| `nxl` | `&>` | does not extend to the left of |
| `adj` | `-\|-` | adjacent to |

Columns of type `int4range`, `int8range`, `numrange`, `tsrange`,
`tstzrange`, `daterange`, the matching multiranges (PostgreSQL 14+) and any
custom range type (`CREATE TYPE s.floatrange AS RANGE (subtype = float8)`)
or its multirange take these. A custom type's constructor and element casts
are schema-qualified (`"s"."floatrange"($1::float8, $2::float8, $3::text)`),
so the type works off the `search_path`; a domain over a range resolves to
the range. A type that merely shares a range's name in another schema gets
no range operators. The operand is a range literal (`[1,5)`, `(,10]`, `empty`) or, as a
pdbr extension, a JSON object
`{"start":…,"end":…,"startInclusive":…,"endInclusive":…,"isEmpty":…}`
(start/end null or absent means unbounded, inclusivity defaults to `[)`).
Either way it becomes three parameters for the type's constructor. Integer
ranges reject non-integer bounds with a `400`. On a multirange column the
operand is still a range. `cs` with an operand that is not a range literal or
object is element containment, a pdbr extension: in PostgREST `cs.5`
fails to parse as a range.

```http
GET /api/bookings?seats=cs.2
GET /api/bookings?stay_days=ov.[2026-01-10,2026-01-12)
GET /api/bookings?pages=cs.{"start":11,"end":13}
GET /api/bookings?blackout=sl.[2099-01-01,2099-01-02)
```

Arrays keep their own `cs`/`cd`/`ov` (array containment and overlap with an
`{a,b}` literal). `sl`, `sr`, `nxr`, `nxl` and `adj` apply to ranges only.

### `isdistinct` (PostgREST)

`col=isdistinct.v` renders `col IS DISTINCT FROM $1` and
`not.isdistinct.v` renders `NOT (col IS DISTINCT FROM $1)`. It works on
every column `eq` works on, and the operand is coerced like `eq`'s.

### Trigram similarity (`pg_trgm`, pdbr)

| op | SQL | Threshold setting |
|---|---|---|
| `trgm_sim` | `col % $1` | `pg_trgm.similarity_threshold` (0.3) |
| `trgm_wsim` | `col %> $1` (= `$1 <% col`, word similarity; pg_trgm 1.2+) | `pg_trgm.word_similarity_threshold` (0.6) |
| `trgm_swsim` | `col %>> $1` (= `$1 <<% col`, strict word similarity; 1.4+) | `pg_trgm.strict_word_similarity_threshold` (0.5) |

These apply to text-like columns. The column is on the left, the form the
`gin_trgm_ops`/`gist_trgm_ops` index classes serve.

### unaccent (pdbr)

| op | SQL | `$1` | Matches |
|---|---|---|---|
| `unaccent_eq` | `lower(unaccent(col)) = lower(unaccent($1))` | the value | equal |
| `unaccent_ilike` | `unaccent(col) ILIKE unaccent($1)` | the pattern, `*` → `%` | the `ilike` pattern (`%` any run, `_` one character) |
| `unaccent_sw` | same | escaped value + `%` | starts with the value |
| `unaccent_includes` | same | `%` + escaped value + `%` | contains the value |

Comparisons ignore both accents and case (`Crème` matches `creme`,
`CREME` and `crème`). They apply to text-like columns (`text`, `varchar`,
`char(n)`, `citext`) when the `unaccent` extension is installed, take `not.`
and work inside `and=`/`or=`:

```http
GET /api/cities?name=unaccent_eq.Sao%20Paulo
GET /api/recipes?title=unaccent_ilike.*creme*brulee
GET /api/people?or=(last_name.unaccent_sw.Mull,first_name.not.unaccent_includes.zoe)
```

`unaccent_ilike` translates `*` to `%` exactly as `like`/`ilike` do.
`unaccent_sw` and `unaccent_includes` match their value literally: pdbr
escapes `%`, `_` and `\` in it before adding the wildcard. Every call is
the two-argument `"ext".unaccent('"ext"."unaccent"'::regdictionary, …)`
form, which names the dictionary explicitly, so it works with the extension
installed in a schema off the `search_path` (the one-argument form looks the
dictionary up through the `search_path` at execution time and fails there).

`unaccent` is `STABLE`, not `IMMUTABLE`, so no index can serve these
operators (an expression index over an `IMMUTABLE` wrapper function does
not match the rendered expression). Under `filters.indexed_only` they are
therefore `400 column_not_filterable` unless the column is listed in
`filters.allow_columns` (or the policy is off); see
[the indexed-only policy](#the-indexed-only-policy).

### fuzzystrmatch (pdbr)

| op | SQL | Operand |
|---|---|---|
| `levenshtein_lt` | `levenshtein_less_equal(col, $lev, $d) < $d` | the exclusive bound `$d`, a non-negative integer; the text is `levenshtein=` |
| `soundex` | `soundex(col) = soundex($1)` | text |
| `metaphone` | `metaphone(col, 255) = metaphone($1, 255)` | text, at most 255 bytes |
| `dmetaphone` | `dmetaphone(col) = dmetaphone($1)` (primary Double Metaphone code) | text |

They apply to text-like columns (`text`, `varchar`, `char(n)`, `citext`)
when the `fuzzystrmatch` extension is installed, take `not.` and work in
`or=`/`and=` and embed filters:

```http
GET /api/people?name=levenshtein_lt.3&levenshtein=Jon&order=name.levenshtein&limit=10
GET /api/people?or=(name.soundex.Robert,name.dmetaphone.Smith)
```

`levenshtein_lt` measures from the request-wide `levenshtein=` text, like
the vector thresholds measure from `vec=` (see [Ordering](#ordering) and
[Scores](#scores)); without it the request is `400 invalid_filter`.
fuzzystrmatch refuses texts over 255 characters (`metaphone`: bytes):
a longer `levenshtein=` or `metaphone` operand is `400 invalid_filter`, and
a longer column value has no distance or code, so neither an operator nor
its `not.` form matches it (and its distance sorts last) instead of the
request failing. Levenshtein compares case-sensitively (on citext too); the
phonetic codes ignore case. No index serves any of them (only an expression
index on, say, `soundex(col)` could, and it does not match the rendered
SQL), so under `filters.indexed_only` they need the column in
`filters.allow_columns`, like the unaccent operators.

### hstore (pdbr)

| op | SQL | Operand |
|---|---|---|
| `hk` | `?` | key (the same spelling as jsonb's `hk`) |
| `hk_all` | `?&` | `{a,b}` key list |
| `hk_any` | `?\|` | `{a,b}` key list |
| `cs` | `@>` | JSON object `{"color":"red"}` (→ `"ext".hstore($1::text[])`) or an hstore literal `color=>red` |
| `cd` | `<@` | same |
| `hk_def` | `defined(col, $1)` | key: present with a non-null value |

The value of one key is the JSON-path spelling `attrs->color` (or
`attrs->>color`; both are hstore's `->`, which yields text): in `select=`
(answering under the key, `color`, unless aliased, and castable:
`attrs->>n::int`), in filters with the text operators
(`attrs->>color=eq.red`, `attrs->size=like.X*`, `attrs->color=is.null`
for an absent or null value) and in `order=` (`attrs->color.desc`, keyset
cursors included). The key is a bind parameter. Only one key step is
allowed (`attrs->a->b` and `attrs->0` are `400`), and, as for JSON paths,
the column must be filterable / orderable. `hk_def` and `cd` are not
index-served and need `filters.allow_columns` under the policy.

`eq`, `neq`, `in`, `is` and `isdistinct` compare whole values given as hstore
literals, through the extension's `=` operator. hstore has no `<`/`>`
operators, so `gt`/`gte`/`lt`/`lte` are `400 invalid_filter`.

### ltree (pdbr; `cs`/`cd` as in PostgREST)

| op | SQL | Operand |
|---|---|---|
| `ancestor` (alias `cs`) | `@>` | path: the column is an ancestor of, or equal to, it |
| `descendant` (alias `cd`) | `<@` | path |
| `ancestor_any` | `@> ltree[]` | `{a.b,c}` path list |
| `descendant_any` | `<@ ltree[]` | path list |
| `lquery` | `~` | lquery (`*.astro*.*`) |
| `lquery_any` | `? lquery[]` | `{*.a,*.b}` |
| `ltxtquery` | `@` | ltxtquery (`astro* & !moon`) |

`ltree[]` columns take `ancestor`, `descendant`, `lquery`, `lquery_any` and
`ltxtquery`, which hold when some element matches, plus the array operators.
Comparisons (`eq`, `gt`, …) use the extension's operators.

Derived pseudo-columns (see [Derived columns](#derived-columns-ltree-and-intarray)):
`<col>_nlevel`, `<col>_parent`, `<col>_lca(path=…)` and
`<col>_subpath(offset=…[,length=…])` on ltree columns, `<col>_lca` on
`ltree[]` columns.

### intarray (pdbr)

| op | SQL | Operand |
|---|---|---|
| `query_int` | `col @@ $1::"ext".query_int` | a `query_int` boolean query over the elements: `1&(2\|3)`, `!4` (URL-encode `&` and `\|`) |

`int4[]` columns take it when the `intarray` extension is installed, besides
the core array operators (`cs`, `cd`, `ov`), which intarray's own operator
classes (`gin__int_ops`, `gist__int_ops`) also serve. A GIN or GiST index on
the column admits it under `filters.indexed_only`. A malformed query is
`400 pg:42601`; intarray rejects arrays holding NULL elements (`400
pg:22004` when such a row is evaluated). `<col>_icount` selects, filters and
orders by the element count (see [Derived columns](#derived-columns-ltree-and-intarray)).

### inet / cidr (pdbr; `ov` as in PostgREST)

| op | SQL | Meaning |
|---|---|---|
| `cs` | `>>=` | contains or equals (`net=cs.10.1.2.3`) |
| `cd` | `<<=` | is contained by or equals (`net=cd.10.0.0.0/8`) |
| `cs_strict` | `>>` | strictly contains |
| `cd_strict` | `<<` | is strictly contained by |
| `ov` | `&&` | contains or is contained by |

PostgREST renders `cs`/`cd` literally as `@>`/`<@`, which inet does not have.
Its `sl`/`sr` are `<<`/`>>`, which on inet mean "contained by"/"contains". pdbr
gives `cs`/`cd` the containment meaning they have on every other type and
does not offer `sl`/`sr` on inet.

### citext

citext columns take the text operators. Their comparison and pattern
operators are case-insensitive but live in the extension's schema. Spelled
plainly, PostgreSQL resolves `email = $1` to the case-sensitive **text**
operator through citext's implicit cast whenever that schema is off the
`search_path`. pdbr therefore renders every comparison on a citext column
with the extension's operator and a citext-typed operand:

```sql
t0."email" OPERATOR("ext".=) $1::"ext".citext
t0."email" OPERATOR("ext".~~*) $2::"ext".citext          -- ilike
t0."email" OPERATOR("ext".~~) ($3 || '%')::"ext".citext  -- sw
t0."email" OPERATOR("ext".=) ANY($4::text[]::"ext"."citext"[])
t0."email" OPERATOR("ext".~) $5::"ext".citext            -- match (case-insensitive on citext)
t0."email" OPERATOR("ext".~~) ALL($6::text[]::"ext"."citext"[])  -- like(all)
```

The same goes for every key and correlation comparison: single-row reads,
`PATCH`/`DELETE`/`PUT` by primary key or `/by/email/ADA@EXAMPLE.COM`
(including the `If-Match` row hash), bulk writes by filter, nested-mutation
`connect` lookups, and embed, relation-filter and related-order joins on
extension-typed foreign keys. Keyset cursor predicates over citext, ltree
or hstore sort values (plain, related or computed order terms) use the
extension's comparison operators, so paging follows `ORDER BY` exactly. For
hstore these are its B-tree class operators `#<#`/`#>#`, since it has no
`<`/`>`. Backward related ordering over a citext column uses citext's own
`min`/`max` aggregates. An operand cast always names the extension's type
(`$1::"ext"."hstore"`), also for a column whose type is a domain over it.

## Ordering

| term | Expression | Best first |
|---|---|---|
| `<col>.rank` | `ts_rank(vector, query)` | `.desc` |
| `<col>.rank_cd` | `ts_rank_cd(vector, query)` | `.desc` |
| `<col>.trgm_dist` | `col <-> $trgm` (1 − similarity) | `asc` (default) |
| `<col>.trgm_wdist` | `col <->> $trgm` (1 − word similarity; pg_trgm 1.2+) | `asc` |
| `<col>.trgm_swdist` | `col <->>> $trgm` (1 − strict word similarity; 1.4+) | `asc` |
| `<col>.vec_l2` | `col <-> $vec::vector` (Euclidean distance) | `asc` (default) |
| `<col>.vec_ip` | `col <#> $vec::vector` (negative inner product) | `asc` |
| `<col>.vec_cos` | `col <=> $vec::vector` (cosine distance, 1 − cosine similarity) | `asc` |
| `<col>.vec_l1` | `col <+> $vec::vector` (taxicab distance; pgvector 0.7+) | `asc` |
| `<col>.levenshtein` | `levenshtein(col, $levenshtein)` (fuzzystrmatch edit distance) | `asc` (default) |
| `<col>->key` | the hstore value of `key` (text) | either |
| `<col>_nlevel`, `<col>_icount` | `nlevel(col)`, `icount(col)` (allowlisted columns under the policy) | either |

```http
GET /api/articles?search=wfts(english).fat%20cat&order=search.rank.desc&limit=10
GET /api/articles?order=title.trgm_dist&trgm=postgres&limit=10
GET /api/articles?title=trgm_sim.postgres&order=title.trgm_dist,id&trgm=postgres
```

- **rank / rank_cd** rank against the request's own full-text filter on the
  same column: the single non-negated `fts`/`plfts`/`phfts`/`wfts`
  condition among the conjuncts of that scope (the root filters including
  `and=(…)` groups, or the embed's for `posts.order=`; not under `or=` or
  `not.`), with the same variant, query and config. Without one the request
  is `400 invalid_filter`, and so is a scope with two of them.
- **trgm_dist** and the word forms measure the distance to the `trgm=` query
  parameter, the way `near=` works for `.dist`. One `trgm=` serves the root
  and every embed. A distance term without `trgm=` is `400 invalid_filter`,
  and so is `trgm=` without one.
- `trgm` (like `near`) is reserved only on routes where no column, computed
  column or RPC argument is named so. Where one is, `trgm=` is that filter or
  argument, and the trigram distance terms are not offered
  (`400 column_not_orderable`; OpenAPI lists neither the terms nor a `trgm`
  parameter). No escape spelling exists; rename the column, or expose it
  through a view, to get distance ordering there.
- **vec_l2**, **vec_ip**, **vec_cos** and **vec_l1** measure the distance of
  a pgvector column to the `vec=` query parameter, the same way: one `vec=`
  serves the root and every embed, a vector term without `vec=` or `vec=`
  without one is `400 invalid_filter`, and `vec` is reserved only where no
  column, computed column or RPC argument is named so. See
  [pgvector](#pgvector-pdbr).
- **levenshtein** measures the edit distance to the `levenshtein=` text the
  same way (one `levenshtein=` per request, reserved only where no column,
  computed column or RPC argument is named so; a term without it, or
  `levenshtein=` without a term, score or `levenshtein_lt` filter, is `400
  invalid_filter`). No index serves it, so under `filters.indexed_only` it
  needs the column in `filters.allow_columns` (`400 column_not_orderable`
  otherwise). Values over 255 characters have no distance and sort last.
- The direction keyword is the usual one: the default is `asc` and nulls sort
  last. Ranks grow with relevance, so ask for `.desc`. Distances shrink with
  similarity, so `asc` (the default) is nearest-first, which a GiST trigram
  index (or an HNSW/IVFFlat vector index) serves directly (KNN).
- The terms combine with the PK tiebreaker and keyset cursors like any other
  expression term.

## pgvector (pdbr)

Columns of pgvector's `vector`, `halfvec` and `sparsevec` types (extension
`vector`, any schema) are vectors to pdbr when the extension is installed.

- **Output**: `vector` and `halfvec` as JSON arrays of numbers
  (`"emb": [0.1, 0.2, 0.3]`, rendered as `(col)::text::json`), `sparsevec`
  as its text form (`"{1:1.5,3:2}/5"`, 1-based indices). In CSV the cell
  holds the same JSON text, like other structured values.
- **Input** (`POST`/`PUT`/`PATCH` bodies, bulk and nested rows, `RPC`
  arguments in a `POST` body, elements of `vector[]`): a JSON array of
  numbers or the type's text form. For `sparsevec` the array is dense and
  its zeros are left out. Either form becomes one text parameter cast to the
  column type (`$n::"ext".vector`); `null` stays `NULL`. Bad input (an empty
  array, a non-number element, more than 16000 elements) is `400
  invalid_body`; a malformed text form or a wrong dimension count is
  rejected by PostgreSQL (`400 pg:22P02` / `pg:22000`). `GET` RPC arguments
  take the text form.
- **Filters**: `is.null` / `not.is.null`, and distance thresholds against
  `vec=` (below). Comparisons (`eq`, `lt`, `in`, …) are `400
  invalid_filter`, as on spatial columns.
- **Ordering**: only by distance to `vec=`, nearest first under `asc`:

```http
GET /api/items?order=embedding.vec_cos&vec=%5B0.1%2C0.2%2C0.3%5D&limit=10
GET /api/items?order=embedding.vec_l2&vec=[0.1,0.2,0.3]&limit=10
GET /api/docs?select=id,chunks(id,text)&chunks.order=embedding.vec_ip&chunks.limit=3&vec=%5B1%2C0%2C0%5D
```

  `vec=` is a JSON array of numbers (URL-encoded, or raw where the client
  allows `[`/`]`) or the column type's text form, and is checked against
  every vector term's column (`400 invalid_filter` when it is not a vector
  value). Its dimensions must match the column's; PostgreSQL rejects a
  mismatch (`400 pg:22000`). A plain `order=embedding`, `distinct=` on a
  vector column, related ordering by one (`rel(embedding)`) and `min()`/`max()`
  over one are rejected. The terms take the PK tiebreaker and keyset cursors
  like any other expression term (the anchor row's distance is re-evaluated),
  and `.desc` (farthest first) works but no index serves it.
- **Index policy**: under `filters.indexed_only` a distance term is
  accepted only when an index of the table covers the column with an access
  method that serves it: HNSW for all four, IVFFlat for `vec_l2`, `vec_ip`
  and `vec_cos` on `vector` and `halfvec` (not `vec_l1`, not `sparsevec`).
  Operator classes are not introspected, so an HNSW index built with
  `vector_l2_ops` admits `vec_cos` too, which then scans; the same
  approximation as for trigram indexes. Otherwise the term is `400
  column_not_orderable` unless the column is in `filters.allow_columns`.
  HNSW and IVFFlat scans are approximate nearest-neighbour searches (see
  pgvector's `hnsw.ef_search` / `ivfflat.probes`), and these indexes do not
  store NULL vectors: when PostgreSQL serves the order from one (the PK
  tiebreaker is applied by an incremental sort on top), rows whose vector is
  NULL are not returned, where a sequential scan would list them last. Add
  `<col>=not.is.null` to make that explicit.
- **Distance thresholds**: "similar enough" cut-offs, measured from the
  same `vec=` as the order terms and scores:

| filter | SQL |
|---|---|
| `<col>=vec_l2_lt.<d>` | `(col <-> $vec::vector) < $d` |
| `<col>=vec_ip_lt.<d>` | `(col <#> $vec::vector) < $d` (negative inner product) |
| `<col>=vec_cos_lt.<d>` | `(col <=> $vec::vector) < $d` |
| `<col>=vec_l1_lt.<d>` | `(col <+> $vec::vector) < $d` (pgvector 0.7+) |

```http
GET /api/items?select=id,embedding_cosine_distance&embedding=vec_cos_lt.0.2&order=embedding.vec_cos&vec=%5B0.1%2C0.2%2C0.3%5D&limit=10
```

  The operand is the exclusive bound, a finite number (`400 invalid_filter`
  otherwise); `not.`, `or=(…)`, `and=(…)` and embed filters take them like
  any operator, and a threshold without `vec=` is `400 invalid_filter`.
  pgvector indexes serve ordering, not these predicates, so under
  `filters.indexed_only` they are accepted only on columns listed in
  `filters.allow_columns` (`400 column_not_filterable` otherwise); combined
  with nearest-first ordering by the same distance and `limit=`, an
  approximate index scan feeds the filter.
- **Distance columns**: `<col>_l2_distance`, `<col>_negative_inner_product`,
  `<col>_cosine_distance` and `<col>_l1_distance` select the distances from
  `vec=` (see [Scores](#scores)).
- **Bit vectors**: with pgvector 0.7 or later, `bit` and `varbit` columns
  (binary embeddings; still plain comparable columns) take pgvector's
  Hamming (`<~>`) and Jaccard (`<%>`) distances: order terms `vec_hamming`
  and `vec_jaccard`, thresholds `vec_hamming_lt` and `vec_jaccard_lt`, and
  the `<col>_hamming_distance` / `<col>_jaccard_distance` columns, all
  against `vec=` given as a bit string (`vec=1010`, cast to `varbit`; a
  length mismatch is rejected by pgvector). A bit column gets this only where
  an HNSW (`bit_hamming_ops`, `bit_jaccard_ops`) or IVFFlat
  (`bit_hamming_ops`) index covers it, or the policy admits it
  (`filters.allow_columns`, or `indexed_only: false`), so installing pgvector
  leaves other bit columns alone. One `vec=` serves the whole request, so a
  request mixes bit and vector distances only when the value parses for
  both. Binary quantization keeps such a column next to the full vector and
  re-ranks the coarse Hamming candidates by the exact distance:

```sql
ALTER TABLE items ADD COLUMN code bit(1536)
  GENERATED ALWAYS AS (binary_quantize(embedding)::bit(1536)) STORED;
CREATE INDEX ON items USING hnsw (code bit_hamming_ops);
-- one-statement re-ranking, exposed as GET /api/rpc/search_items?q=...&n=10
CREATE FUNCTION search_items(q vector(1536), n int DEFAULT 10) RETURNS SETOF items
  STABLE LANGUAGE sql AS $$
  SELECT * FROM (
    SELECT * FROM items ORDER BY code <~> binary_quantize(q)::bit(1536) LIMIT n * 10
  ) AS candidates
  ORDER BY embedding <=> q LIMIT n
$$;
```

  Without the function, `order=code.vec_hamming&limit=100&vec=<sign bits>`
  fetches the coarse candidates (with `select=id,embedding`) and the client
  re-ranks them: one `vec=` cannot hold both the bit string and the vector.
- **OpenAPI**: `vector`/`halfvec` are `type: array, items: {type: number}`
  in responses and `oneOf [array, string]` in request bodies; `sparsevec` is
  `string` (`oneOf [string, array]` in bodies). The `order` description
  lists the vector terms per column, `x-pdbr-filter-ops` the thresholds the
  policy admits, and the `vec` parameter is documented on resources that
  have a vector term, score column or threshold.

## Scores

Score pseudo-columns select the similarity or distance a reference
parameter measures, the way `<col>_distance` selects the spatial distance
from `near=` ([postgis.md](postgis.md#selecting-the-distance)). They are
named after pdbcore's expressions in snake_case, the names pdbq gives its
score fields:

| pseudo-column | value | reference |
|---|---|---|
| `<col>_similarity` | `similarity(col, $trgm)`, 0 to 1 | `trgm=` |
| `<col>_word_similarity` | `word_similarity($trgm, col)` (pg_trgm 1.2+) | `trgm=` |
| `<col>_strict_word_similarity` | `strict_word_similarity($trgm, col)` (1.4+) | `trgm=` |
| `<col>_l2_distance`, `_negative_inner_product`, `_cosine_distance`, `_l1_distance` | `col <-> $vec`, `<#>`, `<=>`, `<+>` | `vec=` |
| `<col>_hamming_distance`, `<col>_jaccard_distance` (bit columns) | `col <~> $vec::varbit`, `<%>` | `vec=` |
| `<col>_levenshtein_distance` (integer) | `levenshtein(col, $levenshtein)`; null for values over 255 characters | `levenshtein=` |

```http
GET /api/articles?select=id,title,title_similarity&title=trgm_sim.postgres&trgm=postgres&order=title.trgm_dist&limit=10
GET /api/items?select=id,embedding_cosine_distance&order=embedding.vec_cos&vec=%5B0.1%2C0.2%2C0.3%5D&limit=10
GET /api/docs?select=id,chunks(id,embedding_cosine_distance)&chunks.order=embedding.vec_cos&chunks.limit=3&vec=%5B1%2C0%2C0%5D
```

- They are selected explicitly (never part of `*`), aliasable
  (`score:title_similarity`), and work in embeds, spreads, row paths and
  mutation responses. The reference parameter is required whenever one is
  selected (`400 invalid_filter`), serves every score, order term and
  threshold of the request, and is bound again for each score, so the SQL
  text depends only on which scores are selected. They take no arguments,
  casts, filters or ordering (order by the matching `.trgm_*`/`.vec_*`
  term instead; a trigram distance is 1 − its similarity).
- Trigram scores exist on text columns that take the trigram operators
  (a GIN or GiST trigram index, or `filters.allow_columns`, under
  `filters.indexed_only`), so a GIN-indexed column has scores without
  distance ordering. Vector scores exist on every vector column. Being
  projections of the returned rows, neither depends on the ordering index
  or `@omit order`; `@omit read` removes them with the column.
- A resource with a column or computed column named `trgm` (`vec`) has no
  such parameter and no trigram (vector) scores; a score selected in an
  embed under a root where the parameter is a filter is `400
  invalid_select`. A real, computed or derived column owning a score's key
  keeps it (with a warning).
- Each costs 1 per row in `server.max_cost`, like a column. OpenAPI lists
  them as read-only nullable `number` properties of the resource schema and
  in the `select` description.

## Headlines

A `<col>_headline` pseudo-column returns a `ts_headline` snippet: the
document with the words the request's full-text filter matched marked up,
next to `order=<col>.rank`. It exists on every column that accepts a
full-text filter under the filter policy:

- on a text-like, `json` or `jsonb` column the document is the column
  itself: `body_headline` highlights `body` against the `body=fts…` filter;
- on a `tsvector` column (typically generated from other columns) the
  document is another column of the resource named by `document=`: a
  readable text-like, `json` or `jsonb` column. `search_headline` needs it
  (`400 invalid_select` without it, or with a column that is not one), and
  exists only on resources with such a column.

```http
GET /api/articles?select=id,title,search_headline(document=body,max_words=20,min_words=5,start_sel="<mark>",stop_sel="</mark>")&search=wfts(english).fat%20cat&order=search.rank.desc&limit=10
GET /api/articles?select=id,body_headline(max_fragments=2,fragment_delimiter="%20…%20")&body=plfts.cheese
GET /api/articles?select=id,meta_headline&meta=fts(english).story
GET /api/authors?select=name,articles(id,search_headline(document=title))&articles.search=fts.cat&articles.limit=3
```

- **The query** is the scope's single non-negated `fts`/`plfts`/`phfts`/`wfts`
  filter on the headline's column, the same rule as the rank terms: the
  root filters including `and=(…)` groups for the root select and a
  mutation's response, the embed's own filters (`articles.search=…`) for an
  embed or spread, never under `or=` or `not.`. Without one, or with two,
  the request is `400 invalid_filter`. The query and config are bound
  again for the headline, like the rank terms.
- **Options** are select arguments, each a ts_headline option:
  `max_words`, `min_words`, `short_word` (integers up to 100; `min_words`
  stays below `max_words`, with PostgreSQL's defaults 35 and 15 filled in),
  `max_fragments` (0 to 10), `highlight_all` (`true`/`false`), and
  `start_sel`, `stop_sel`, `fragment_delimiter` (1 to 32 bytes, no `"`, `\` or
  control characters; double-quote them when they contain `,`, `(` or `)`).
  Anything else is `400 invalid_select` (`400 unknown_argument` for an
  unknown name). pdbr renders the options as one options text and binds it
  as a parameter, always (an empty text means PostgreSQL's defaults), so
  the SQL text never depends on the options.
- **Value.** A string for a text document, null for a null document. For a
  `json`/`jsonb` document it is the document with its string values
  highlighted (`{"summary": "A <b>story</b> about cats", …}`). OpenAPI
  lists headlines as read-only nullable string properties (`{}` for json
  documents) with their select syntax, and in the `select` description.
- Like the other pseudo-columns they are selected explicitly (never part of
  `*`), aliasable, take no casts, filters or ordering, and work in embeds,
  spreads, row paths and mutation responses (`PATCH`/`DELETE` by filter,
  where the filter is the scope). `@omit read` removes a column's
  headline and its use as a `document=`; `@omit filter` or an indexed-only
  rejection of its full-text filter removes the headline. A column hidden
  from a schema contract cannot be named as a document there. A real,
  computed or other pseudo-column owning the key keeps it (with a warning).
- **Bounding.** `ts_headline` re-parses the whole document on every call,
  so a headline is expensive per row. It costs
  `pdbcore/contrib.HeadlineCost` (10) per row in `server.max_cost` instead
  of 1. It runs only on the rows the response returns: the filter and the
  ordering run first, and PostgreSQL evaluates the projection after the
  `LIMIT` (the page plus its one look-ahead row; `EXPLAIN ANALYZE` shows it
  above the `Limit`, or above a top-N `Sort` in an embed). The options above
  bound what it returns: at most 10 fragments of at most 100 words, or the
  whole document with `highlight_all=true`.

## Derived columns (ltree and intarray)

Some accessors of the vocabularies are pseudo-columns next to the column
they derive from, selected explicitly like the scores (never part of `*`),
aliasable, and usable in embeds, spreads, row paths and mutation responses:

| pseudo-column | value | type | filter / order |
|---|---|---|---|
| `<col>_nlevel` (ltree) | `nlevel(col)`, the number of labels | integer | yes |
| `<col>_parent` (ltree) | `subpath(col, 0, nlevel(col) - 1)`: empty for a one-label path, null for the empty path | string | no |
| `<col>_lca(path=a.b)` (ltree) | `lca(col, $path)`, the longest common ancestor with `path` | string | no |
| `<col>_subpath(offset=1[,length=2])` (ltree) | `subpath(col, $offset[, $length])`; a negative offset counts from the end, length (≥ 0) defaults to the rest; null when offset lies outside the path | string | no |
| `<col>_lca` (ltree[]) | `lca` of the non-null elements; null for an empty array | string | no |
| `<col>_icount` (int4[] with intarray) | `icount(col)`, the number of elements | integer | yes |

```http
GET /api/categories?select=id,path_nlevel,path_parent,top:path_subpath(offset=0,length=1)&path_nlevel=lte.2&order=path_nlevel.desc
GET /api/categories?select=id,common:path_lca(path=top.science.physics)
GET /api/items?select=id,tags_icount&tags=query_int.1%26(2%7C3)&order=tags_icount.desc
```

The select arguments are bind parameters (the SQL text does not depend on
whether `length` is given), and malformed ones are `400 invalid_select`
(`path_subpath` without arguments, a non-integer offset, a negative length,
an offset beyond ±65535). `<col>_nlevel` and `<col>_icount` take the integer
filter operators (`eq … in, is, isdistinct`, `not.`, `or=`/`and=`, embed
and relation filters, `(any)`/`(all)`) and `order=` terms (keyset cursors
included). No index serves them, so under `filters.indexed_only` they
filter and order only when the column is in `filters.allow_columns` (and
filterable / orderable itself): `400 column_not_filterable` /
`column_not_orderable` otherwise; selecting them is always allowed. A real
or computed column owning the key keeps it (with a warning). `@omit read`
removes them with the column, `@omit filter` / `@omit order` their filter
and order surface. Each costs 1 per row in `server.max_cost`. OpenAPI lists
them as read-only properties (with their select syntax) and in the `select`
description, documents the filterable ones as filter parameters and the
orderable ones in the `order` description. The other vocabularies'
accessors (inet `family`, range bounds, vector `dims`/`norm`) are not
exposed.

## Output

Values come out the way PostgREST returns them:

| Type | JSON |
|---|---|
| `hstore` | object of string → string\|null (hstore's own cast to `jsonb`); OpenAPI `type: object` |
| ranges, multiranges | literal text (`"[1,5)"`, `"empty"`, `"{[1,3),[5,7)}"`), normalized by PostgreSQL |
| `ltree`, `tsvector`, `inet`, `cidr`, `citext` | string |
| `ltree[]` | array of strings |
| `vector`, `halfvec` (pgvector) | array of numbers (`[0.1,0.2,0.3]`) |
| `sparsevec` (pgvector) | its text form (`"{1:1.5,3:2}/5"`) |

Structured range output (`{start, end, …}`, as `contrib.Codec.OutputSQL`
renders it) is not offered. PostgREST returns the literal and pdbr matches
it, so the structured form is accepted on input only.

## Input (POST / PUT / PATCH bodies)

| Type | Accepted JSON |
|---|---|
| `hstore` | object of string → string\|null (`{"color":"red","n":null}` → `"ext".hstore($n::text[])`), or an hstore literal string (`"a=>1, b=>2"`) |
| ranges | literal string (`"[1,5)"`), or `{"start":1,"end":5,"endInclusive":true,"isEmpty":false}` → `int4range($1::int4, $2::int4, $3::text)` |
| multiranges | literal string (`"{[1,3), [5,7)}"`), or an array of range literals and objects |
| `ltree`, `citext`, `tsvector`, `inet`, `cidr` | string |
| `ltree[]` | array of strings |
| `vector`, `halfvec`, `sparsevec` | array of numbers (`[0.1, 0.2, 0.3]`; for `sparsevec` a dense array whose zeros are left out), or the type's text form (`"[0.1,0.2,0.3]"`, `"{1:1.5,3:2}/5"`) → `$n::"ext".vector` |

Other shapes are `400 invalid_body`: a non-string hstore value, a
fractional bound on an integer range, an object for a multirange, an empty
vector or one with a non-number element. OpenAPI
documents ranges and multiranges as `oneOf [literal string, object / array]`
in the `.insert`/`.update`/`.upsert` schemas. The same JSON forms are
accepted for RPC arguments in a `POST` body and for the elements of array
columns and arguments (`hstore[]`, `int4range[]`, …); they are converted to
the type's literal text and bound as one parameter each. `GET` RPC
arguments are query text, so they take the literal (`?h=a%3D%3E1`).

## The indexed-only policy

`filters.indexed_only` (the default) applies per operator. Each contrib
operator carries pdbcore's index hints (access method and operator class),
and it is accepted only when an index of the table covers the column with a
hinted access method: GIN or GiST for trigram, hstore and `tsvector`
operators, GiST for ltree, GiST or SP-GiST for range and inet operators.
Index operator classes are not introspected, so a B-tree index on a text
column does not admit `trgm_sim`, while any GIN index on it would. Operators
no index can serve (hstore `cd`, ltree `*_any`, `ancestor` on `ltree[]`) and
full-text search on a text, json or jsonb column (only an expression index
on `to_tsvector('<config>', col)` serves it, and expression indexes have no
columns) need the column in `filters.allow_columns`, or
`filters.indexed_only: false`. The unaccent operators are never
index-served and always need `filters.allow_columns` under the policy.
Trigram distance ordering needs a GiST index under the policy, vector
distance ordering an HNSW index (or IVFFlat, which serves `vec_l2`,
`vec_ip` and `vec_cos` on `vector` and `halfvec`). `query_int` needs a GIN
or GiST index. The fuzzystrmatch operators, `order=<col>.levenshtein` and
`<col>_levenshtein_distance`, `hk_def`, and filtering or ordering by
`<col>_nlevel` / `<col>_icount` are never index-served and need
`filters.allow_columns`. Rank ordering needs a
column that accepts a full-text filter. A rejected operator is `400
column_not_filterable` (`column_not_orderable` for order terms).

The vector and bit distance thresholds, which no index serves, need the
column in `filters.allow_columns` under the policy.

OpenAPI lists exactly the accepted operators per column in
`x-pdbr-filter-ops` (pinned to the parser by `TestFilterOpsMatchParser`,
under both policies), the rank and distance terms in the `order`
description, and the `trgm` / `vec` parameter on resources that have a
trigram / vector distance term, score column or threshold.

## Differences from PostgREST

- **Spellings.** `fts`/`plfts`/`phfts`/`wfts` with `(config)`, `isdistinct`,
  `ov`/`cs`/`cd`/`sl`/`sr`/`nxr`/`nxl`/`adj` and the jsonb/array/ltree
  `cs`/`cd` match PostgREST. The trigram ops (`trgm_*`), hstore
  `hk`/`hk_all`/`hk_any`, the ltree words (`ancestor`, `descendant`,
  `*_any`, `lquery`, `lquery_any`, `ltxtquery`), inet
  `cs_strict`/`cd_strict`, the unaccent ops (`unaccent_*`), element
  containment `cs.5` on ranges, JSON-object range and hstore operands, and
  the `rank`/`rank_cd`/`trgm_*`/`vec_*`/`levenshtein` order terms, the
  `vec_*_lt` and `levenshtein_lt` thresholds, the fuzzystrmatch phonetic
  operators, `hk_def`, `query_int`, the score and derived pseudo-columns
  with `trgm=`/`vec=`/`levenshtein=` are pdbr extensions. hstore values by
  key (`attrs->color`) work in PostgREST too, which renders the arrow
  literally; pdbr restricts it to one key and treats `->` and `->>` alike.
- **pgvector output.** `vector` and `halfvec` values are JSON arrays of
  numbers. PostgREST returns their text form as a JSON string
  (`"[0.1,0.2,0.3]"`); pdbr parses it, so clients get numbers without a
  second decode. `sparsevec` stays a string in both. Input takes either form.
- **inet `cs`/`cd`** mean `>>=`/`<<=`, where PostgREST's literal `@>`/`<@`
  fails on inet.
- **Headlines** (`<col>_headline`, `ts_headline`) are a pdbr extension;
  full-text search on json/jsonb columns matches PostgREST.
- **The indexed-only policy** has no PostgREST counterpart.

## Differences from pdbq

pdbq exposes the same pdbcore vocabularies as typed GraphQL filter fields.
pdbr maps them to URL spellings: `matches`/`matchesPlain`/`matchesPhrase`/
`matchesWebsearch` → `fts`/`plfts`/`phfts`/`wfts`; `similarTo`,
`wordSimilarTo`, `strictWordSimilarTo` → `trgm_sim`, `trgm_wsim`,
`trgm_swsim`; hstore `hasKey`/`hasAllKeys`/`hasAnyKeys`/`contains`/
`containedBy` → `hk`/`hk_all`/`hk_any`/`cs`/`cd`; ltree `ancestorOf`,
`descendantOf`, `ancestorOfAny`, `descendantOfAny`, `matchesLquery`,
`matchesAnyLquery`, `matchesLtxtquery` → `ancestor`, `descendant`,
`ancestor_any`, `descendant_any`, `lquery`, `lquery_any`, `ltxtquery`;
range `overlaps`, `contains`/`containsElement`, `containedBy`,
`strictlyLeftOf`, `strictlyRightOf`, `notExtendsRightOf`, `notExtendsLeftOf`,
`adjacentTo` → `ov`, `cs`, `cd`, `sl`, `sr`, `nxr`, `nxl`, `adj`; inet
`containsOrEquals`, `containedByOrEquals`, `contains`, `containedBy`,
`containsOrContainedBy` → `cs`, `cd`, `cs_strict`, `cd_strict`, `ov`;
unaccent `unaccentEqualTo`, `unaccentLike`, `unaccentStartsWith`,
`unaccentIncludes` → `unaccent_eq`, `unaccent_ilike`, `unaccent_sw`,
`unaccent_includes`; the vector distances `l2Distance`,
`negativeInnerProduct`, `cosineDistance`, `l1Distance` → the order terms
`vec_l2`, `vec_ip`, `vec_cos`, `vec_l1` (bit `hammingDistance`,
`jaccardDistance` → `vec_hamming`, `vec_jaccard`), and their `LessThan`
thresholds → `vec_l2_lt`, … `vec_jaccard_lt`. pdbq's score fields
(`<field>Similarity`, `<field>CosineDistance`, …) are pdbr's score
pseudo-columns (`<col>_similarity`, `<col>_cosine_distance`, …); pdbq reads
each list's own `similarTo:`/`nearVector:` and its threshold operators carry
their own vector, where pdbr has one request-wide `trgm=`/`vec=`, and a
pdbq score without its reference is `null` where pdbr answers `400`.
pdbq's `<field>Headline(document: …, options: {…})` is pdbr's
`<col>_headline(document=…, max_words=…)`; pdbq offers it on `tsvector`
columns over text documents only, because it exposes no `to_tsvector`
filters over text or json columns (no index serves them), so it has no
`body_headline` on a text column and no full-text search or headlines on
json/jsonb columns. hstore `hasDefinedKey` is `hk_def`; fuzzystrmatch `levenshteinDistanceLessThan`,
`soundexEqualTo`, `metaphoneEqualTo`, `doubleMetaphoneEqualTo` are
`levenshtein_lt`, `soundex`, `metaphone`, `dmetaphone`, the
`levenshteinDistance` ordering and score are `<col>.levenshtein` and
`<col>_levenshtein_distance` against `levenshtein=` (pdbq's threshold
carries its own `{text, distance}`); intarray `matchesQueryInt` is
`query_int`. The hstore `value` expression is the `attrs->key` path, and
the ltree `nlevel`, `parent`, `lca`, `subpath` and intarray `icount` values
are the derived pseudo-columns `<col>_nlevel`, … (`subpath`/`lca` with
select arguments). The other derived accessors (inet `family`, range
bounds, vector `dims`/`norm`) are not exposed by pdbr.
