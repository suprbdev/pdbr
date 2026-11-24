# Schema contracts

> **Experimental.** Schema contracts are outside the 1.0 compatibility promise and may change in a minor release. See [Compatibility and versioning](compatibility.md#experimental-features).

A schema contract is a named REST API for one audience — `public`,
`partner`, `internal` — built from the same database as every other
contract. Each contract is a subset of the full API: smart comments tag
tables, columns, functions and relations into or out of contracts, and pdbr
builds one route table and one OpenAPI document per contract, each under
its own base path.

```yaml
server:
  base_path: /api
contracts:
  - name: internal     # served under /api/internal
  - name: public       # served under /api/public
  - name: partner
    default: exclude   # only what is tagged into partner
    path: /partners    # served under /partners
```

```sql
COMMENT ON TABLE app.audit_log IS '@contract internal';
COMMENT ON COLUMN app.users.email IS '@behavior -contract:public';
COMMENT ON TABLE app.orders IS '@contract internal, partner';
COMMENT ON FUNCTION app.partner_feed() IS '@behavior +contract:partner';
```

Without `contracts` nothing changes: pdbr serves the full API under
`server.base_path` and ignores contract tags.

The tags, their precedence and the security model are shared with pdbq
through `pdbcore`, so one set of comments drives both products.

## Configuration

`contracts` is a list; each entry has:

| Key | Default | Meaning |
|---|---|---|
| `name` | required | Lower-case letters, digits, `_` and `-`, starting with a letter. Matched case-insensitively against the tags. |
| `default` | `include` | Policy for tables and functions without a tag for this contract: `include` (everything not tagged out) or `exclude` (only what is tagged in). |
| `path` | `<server.base_path>/<name>` | Base path of the contract's routes and OpenAPI document. `/` serves it at the root, like an empty `server.base_path`. |

Contracts can also be listed by name, with the default policy and path:
`contracts: [public, partner]` in YAML, `PDBR_CONTRACTS=public,partner` in
the environment.

`pdbr config validate` rejects duplicate names, unknown policies, malformed
paths (`/` or `/segment[/segment...]` of letters, digits, `_ . ~ -`, no
trailing slash), two contracts on one path, overlapping paths (`/a` and
`/a/b`, or `/` next to any other contract), a `metrics.path` inside a
contract's base path, and contracts with the `smart_comments` plugin
disabled: hidden columns are removed by the plugin's `@omit` handling (see
[Security](#security)).

## Endpoints

With contracts configured **only the contracts are served**:
`server.base_path` itself serves nothing (it only prefixes the default
contract paths). The full, unfiltered API is not reachable over HTTP unless
a contract with `default: include` and no tags against it stands in for it.
Each contract with base path `B` serves exactly what a single-API pdbr with
`server.base_path: B` would serve for its subset:

| Path | |
|---|---|
| `B/{resource}`, `B/{resource}/{pk}`, `B/{resource}/by/…`, `B/rpc/{fn}` | the contract's routes |
| `B/openapi.json` | the contract's OpenAPI document (`servers[0].url` is `B`, after `openapi.server_url`), when `openapi.enabled` |

`/healthz`, `/readyz` and the metrics endpoint stay at the root; any other
path is a `404` problem. Every `server.*` setting (limits, CORS, timeouts,
ETags, CSV, read-only) applies to every contract.

## Tags

Contract membership is read from the same `COMMENT`s as the other
[smart comments](smart-comments.md):

| Tag | Effect for contract `c` |
|---|---|
| `@contract a, b` | The object belongs to contracts `a` and `b` only (repeatable; lists merge). A bare `@contract` puts it in no contract. |
| `@behavior +contract:c` (or `contract:c`) | The object belongs to `c`, in addition to any `@contract` list. |
| `@behavior -contract:c` | The object is hidden from `c`, whatever else says. |

For a contract `c` and an object, in order:

1. `-contract:c` hides the object.
2. `@contract` listing `c`, or `+contract:c`, shows it.
3. A `@contract` tag that does not list `c` hides it.
4. Otherwise a **table or function** follows the contract's `default`
   policy, and a **column or foreign key** follows its table: it is shown
   wherever its table is.

Tags apply to:

| Object | Hiding it removes |
|---|---|
| Table, view, materialized view | Everything a bare `@omit` on the table removes: its routes, its `components.schemas` entries, embeds and relation filters in both directions. Also its partitions, every function taking or returning its row type (RPCs and computed columns) and every column typed as its row type. |
| Column | Everything a bare `@omit` on the column removes: the response key, `select=`, filters (including `or`/`and`/`not` trees and relation filters), `order=`, `distinct=`, aggregates, request body keys, the ETag hash, and every key containing it (`/{pk}` and `/by/…` routes, keyset cursors, `on_conflict=`, `Location`). See [smart-comments.md](smart-comments.md#columns). |
| Function | Its `/rpc/{fn}` route, or its computed column. |
| Foreign key (`COMMENT ON CONSTRAINT`) | The embed keys in both directions, relation filters and orderings, nested-mutation keys. |

Functions follow the contract policy, not the table of their first
argument: in a `default: exclude` contract tag computed-column functions in
along with their table.

Logical `@foreignKey` relations go with either end but cannot be tagged on
their own. `@enum` tables become enums at introspection and are not
filtered.

## Security

A contract only ever removes surface. pdbr copies the introspected catalog
for each contract, drops the hidden tables, functions and foreign keys from
the copy, and marks every hidden column with a bare `@omit` before the
normal API build runs on the copy, so the `@omit` handling of every plugin
removes the column from its surface too. Nothing a contract does can add a
route, a key, a privilege or a row:

- A contract's API is always a subset of the full API; `+contract:` tags
  only choose among objects the full API exposes.
- Row-level security, roles and claims are unchanged. A contract narrows
  what a client can ask for; PostgreSQL still decides which rows the role
  sees. Point different audiences at different contracts *and* give them
  different roles when the data itself must differ.
- A hidden column is hidden from filters, ordering and keys too (`400
  unknown_column` / `404`), so it cannot be recovered by bisecting with
  range filters or probed through `/by/…` routes.
- Hiding a foreign key column does not hide the relation it backs: tag the
  constraint as well when the related row must not be embeddable.
- A polymorphic function cannot be resolved to a hidden table's row type
  (`@anyelement app.secrets` skips the function in contracts hiding
  `secrets`), and every function whose result, arguments or output
  columns use that row type, directly or through a domain, a composite
  attribute or an array, is left out (table columns of such a type are
  hidden), so no function returns hidden rows whole.
- A visible table's row nested in another value (an array of rows, a
  composite attribute, a `RETURNS TABLE` column, a computed column
  returning the row) renders only the columns the contract shows
  ([rpc.md](rpc.md#return-shapes)).
- Integrity errors do not name hidden objects: in `errors.detail: prod`, a
  `23*` error whose table, column or constraint is hidden from the
  contract (or a foreign key to a hidden table) carries a generic detail
  such as `foreign key violation` instead of PostgreSQL's message (see
  [errors.md](errors.md#errorsdetail)). The status and code stay, so a
  `409` on deleting a row still tells the client some row references it.

## Watch mode and metrics

Watch mode rebuilds every contract after each DDL change (tag changes are
DDL too) and swaps each one in independently; a contract whose rebuild
fails keeps serving its previous API. Without watch mode every contract is built once,
at boot: a table, function or tag created later is in no contract (a new
function answers `404` everywhere) until pdbr restarts.

With `metrics.enabled`, every series of the [metric reference](metrics.md)
carries a `contract` label, except the pool and runtime series, which are
shared. Requests to `/healthz`, `/readyz`, the metrics endpoint and paths
outside every contract are not counted. An embedder passing its own
instance with `pdbr.WithMetrics` records every contract into that one
instance, without a contract label.

## CLI

```console
$ pdbr schema print --contract public          # OpenAPI document of one contract
$ pdbr schema print --contract partner --yaml
```

`schema print` without `--contract` prints the full API. The schema cache
(`schema dump` / `schema check`) holds the catalog, which every contract is
derived from, so one cache serves all contracts. `pdbr request` runs
against the served contracts, so give the contract's full path
(`pdbr request GET /api/public/users`); `--print-sql` compiles against the
full API.
