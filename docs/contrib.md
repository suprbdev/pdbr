# Contrib types: full-text search, trigrams, hstore, ltree, ranges, inet

pdbr gives PostgreSQL's non-scalar built-in types and the common contrib
extensions a filter and ordering surface, built on the operator vocabularies
of `pdbcore/contrib` (shared with pdbq). It is core, not a plugin. Whatever a
vocabulary needs is read from the catalog:

- **Core types** (`tsvector`, text columns for full-text search, the six
  built-in range types and their multiranges, `inet`/`cidr`) always get their
  operators, and so do range types created with `CREATE TYPE … AS RANGE`
  (and their multiranges), which the catalog records with their subtype.
- **Extension types and operators** (`pg_trgm`, `hstore`, `ltree`, `citext`)
  only appear when the extension is installed (`pg_extension`). The extension
  version matters too: `pg_trgm` older than 1.2 has no word-similarity
  operators and older than 1.4 no strict ones.
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
| `fts` | `@@ to_tsquery` | `tsvector`; text-like (`text`, `varchar`, `char(n)`, `citext`) through `to_tsvector` |
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
config, `default_text_search_config` applies. A malformed `fts` query, an
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

### hstore (pdbr)

| op | SQL | Operand |
|---|---|---|
| `hk` | `?` | key (the same spelling as jsonb's `hk`) |
| `hk_all` | `?&` | `{a,b}` key list |
| `hk_any` | `?\|` | `{a,b}` key list |
| `cs` | `@>` | JSON object `{"color":"red"}` (→ `"ext".hstore($1::text[])`) or an hstore literal `color=>red` |
| `cd` | `<@` | same |

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
- The direction keyword is the usual one: the default is `asc` and nulls sort
  last. Ranks grow with relevance, so ask for `.desc`. Distances shrink with
  similarity, so `asc` (the default) is nearest-first, which a GiST trigram
  index serves directly (KNN).
- The terms combine with the PK tiebreaker and keyset cursors like any other
  expression term.

## Output

Values come out the way PostgREST returns them:

| Type | JSON |
|---|---|
| `hstore` | object of string → string\|null (hstore's own cast to `jsonb`); OpenAPI `type: object` |
| ranges, multiranges | literal text (`"[1,5)"`, `"empty"`, `"{[1,3),[5,7)}"`), normalized by PostgreSQL |
| `ltree`, `tsvector`, `inet`, `cidr`, `citext` | string |
| `ltree[]` | array of strings |

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

Other shapes are `400 invalid_body`: a non-string hstore value, a
fractional bound on an integer range, an object for a multirange. OpenAPI
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
full-text search on a text column (only an expression index on
`to_tsvector('<config>', col)` serves it, and expression indexes have no
columns) need the column in `filters.allow_columns`, or
`filters.indexed_only: false`. Trigram distance ordering needs a GiST index
under the policy. Rank ordering needs a column that accepts a full-text
filter. A rejected operator is `400 column_not_filterable`
(`column_not_orderable` for order terms).

OpenAPI lists exactly the accepted operators per column in
`x-pdbr-filter-ops` (pinned to the parser by `TestFilterOpsMatchParser`,
under both policies), the rank and distance terms in the `order`
description, and the `trgm` parameter on resources that have a distance term.

## Differences from PostgREST

- **Spellings.** `fts`/`plfts`/`phfts`/`wfts` with `(config)`, `isdistinct`,
  `ov`/`cs`/`cd`/`sl`/`sr`/`nxr`/`nxl`/`adj` and the jsonb/array/ltree
  `cs`/`cd` match PostgREST. The trigram ops (`trgm_*`), hstore
  `hk`/`hk_all`/`hk_any`, the ltree words (`ancestor`, `descendant`,
  `*_any`, `lquery`, `lquery_any`, `ltxtquery`), inet
  `cs_strict`/`cd_strict`, element containment `cs.5` on ranges, JSON-object
  range and hstore operands, and the `rank`/`rank_cd`/`trgm_*` order terms are
  pdbr extensions.
- **inet `cs`/`cd`** mean `>>=`/`<<=`, where PostgREST's literal `@>`/`<@`
  fails on inet.
- **Full-text search on json/jsonb columns**, which PostgREST supports through
  `to_tsvector(jsonb)`, is not offered.
- **`match`/`imatch`** (regex) and the `any`/`all` modifiers are not
  implemented.
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
`containsOrContainedBy` → `cs`, `cd`, `cs_strict`, `cd_strict`, `ov`.
