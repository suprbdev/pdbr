# pdbr

[![CI](https://github.com/suprbdev/pdbr/actions/workflows/ci.yaml/badge.svg)](https://github.com/suprbdev/pdbr/actions/workflows/ci.yaml)
[![Release](https://img.shields.io/github/v/release/suprbdev/pdbr)](https://github.com/suprbdev/pdbr/releases/latest)
[![Go Reference](https://pkg.go.dev/badge/github.com/suprbdev/pdbr.svg)](https://pkg.go.dev/github.com/suprbdev/pdbr)
[![License: MIT](https://img.shields.io/badge/license-MIT-blue.svg)](LICENSE)

Zero-boilerplate REST API for PostgreSQL, in the spirit of PostgREST: point
it at a live database and it introspects `pg_catalog`, generates a REST
surface (resources, embeds, filters, keyset pagination, CRUD, RPC, OpenAPI
3.1), and compiles every HTTP request into **one** parameterized SQL statement
that returns the response JSON directly — no N+1, no per-row marshaling in Go.
It is the sibling of [pdbq](https://github.com/suprbdev/pdbq) (same engine,
GraphQL); the shared half lives in
[pdbcore](docs/pdbcore.md).

```console
$ pdbr serve --database.url postgres://localhost/mydb --auth.mode none --roles.switch=false
time=... msg=introspected tables=12 enums=3 functions=2 took=38ms
time=... msg=listening addr=:8080 base_path="" openapi=true
$ curl 'localhost:8080/users?select=id,email,posts(title)&mood=eq.happy&limit=2'
```

## Highlights

- **Single-statement compilation** — embeds become `LEFT JOIN LATERAL`,
  lists become `json_agg`, rows become `json_build_object`; PostgreSQL
  returns the response JSON directly. Mutations are `WITH __mut AS (DML
  RETURNING …) SELECT …`, nested mutations one multi-CTE statement.
- **PostgREST-style URL grammar** — `select=` with embeds and aliases,
  `col=op.value` filters with `and=`/`or=`/`not.` logic, `order=`,
  `limit`/`offset`, keyset `after=`/`before=` cursors, `count=true`,
  `distinct=`, `/by/{cols}/{vals}` unique lookups, `/rpc/{fn}`.
- **RLS-aware by default** — each request runs with `SET LOCAL ROLE` and
  claims exposed via `set_config('jwt.claims.*', ...)` (and optionally as
  one JSON object such as PostgREST's `request.jwt.claims`) from a verified
  JWT or trusted gateway headers; requests without credentials are refused
  unless an anonymous role is configured. The claim settings are shared
  with pdbq and PostGraphile so existing policies work unchanged.
- **Filters with an indexed-only policy** — only indexed columns are
  filterable/orderable out of the box (`filters.indexed_only: false` opens it
  up), with per-type operator sets and `@filterable` smart-comment opt-in.
- **HTTP done properly** — RFC 9457 problem+json errors with stable `code`s,
  weak ETags computed in SQL with `If-None-Match`/`If-Match`, `Prefer:
  return=minimal`, `text/csv` streaming, `Link`/`X-Next-Cursor` headers,
  `405` + `Allow` for what a resource does not support.
- **OpenAPI 3.1** — `GET /openapi.json` is a pure function of the
  database (+ config + plugins), deterministic and pinned by golden tests. No embedded
  UI, no CDN assets: bring your own tooling.
- **Schema cache** — `pdbr schema dump` / `serve --schema.cache_path` boots
  without touching `pg_catalog`; `pdbr schema check` is a CI drift gate; cache
  files are interchangeable with pdbq.
- **Watch mode** — a DDL event trigger (or poll fallback) re-introspects and
  atomically swaps the API during development.
- **Plugins** — five small hook interfaces (catalog, inflection, API,
  compile, request) with four built-ins: `smart_comments`, `advanced_filters`,
  `postgis`, `nested_mutations`.
- **Schema contracts** — per-audience APIs (`public`, `partner`,
  `internal`) selected by `@contract` / `@behavior ±contract:<name>` smart
  comments, each under its own base path with its own OpenAPI document, see
  [docs/contracts.md](docs/contracts.md).
- **Pipe-friendly CLI** — `pdbr request GET '/users?select=id' | jq` prints
  the body, exits non-zero on 4xx/5xx, and `--print-sql` shows the exact
  statement without a database.
- **Library embedding (experimental)** — `pdbr.New(cfg, pdbr.WithPlugins(...)).Handler(ctx)`
  mounts the whole API (watch mode included) in your own `net/http` mux. The Go
  API is outside the 1.0 compatibility promise.

## Installation

Prebuilt binaries for Linux, macOS and Windows (amd64/arm64) are attached to
each release with a `checksums.txt` (signed with cosign), a CycloneDX SBOM and
build provenance attestations, see
[SECURITY.md](SECURITY.md#verifying-releases):

```console
$ VERSION=$(curl -fsSL https://api.github.com/repos/suprbdev/pdbr/releases/latest | sed -n 's/.*"tag_name": *"v\([^"]*\)".*/\1/p')
$ curl -fsSL https://github.com/suprbdev/pdbr/releases/download/v$VERSION/pdbr_${VERSION}_linux_amd64.tar.gz | tar xz
$ ./pdbr --version
```

Or the (multi-arch, distroless) Docker image:

```console
$ docker run --rm ghcr.io/suprbdev/pdbr:latest --version
```

Or build from source:

```console
$ go install github.com/suprbdev/pdbr/cmd/pdbr@latest
```

## Quickstart

```console
$ make dev                   # PostGIS-enabled Postgres + fixture + pdbr in watch mode (no host ports: add compose.override.yaml)
$ curl localhost:8080/openapi.json | jq .paths | head
```

Or against your own database:

```console
$ pdbr config init          # writes a starter pdbr.yaml: no auth, connection role
$ pdbr serve
```

The starter (and the first example above) run without authentication as the
connection role; configure `auth` and `roles` before production
([docs/rls.md](docs/rls.md)). The defaults verify JWTs and refuse requests
without credentials.

See [docs/quickstart.md](docs/quickstart.md) for the full tour, including
[Secure setup](docs/quickstart.md#secure-setup) with roles, a JWT and a
row-level security policy. Coming from PostgREST? See
[docs/migrating-from-postgrest.md](docs/migrating-from-postgrest.md).

## Stability

pdbr follows semantic versioning. Configuration keys, the CLI, the HTTP surface
and REST grammar, error codes and metrics are covered by the 1.0 compatibility
promise; a few features (contracts, nested and partial mutations, rasters and
others) and the Go embedding and plugin API are **experimental** and outside
it. Details, the deprecation policy and the supported PostgreSQL versions
(14 to 19) are in [docs/compatibility.md](docs/compatibility.md). Report
vulnerabilities as described in [SECURITY.md](SECURITY.md). See
[CONTRIBUTING.md](CONTRIBUTING.md) and [CHANGELOG.md](CHANGELOG.md).

## Documentation

| Doc | Contents |
|---|---|
| [docs/features.md](docs/features.md) | Feature support checklist (parity with pdbq, new-to-REST, tiers) |
| [docs/quickstart.md](docs/quickstart.md) | First run, first requests |
| [docs/api.md](docs/api.md) | Routes, `select=` grammar, embeds, hints, headers |
| [docs/filtering.md](docs/filtering.md) | Filter operators, logic, relation/computed filters, indexed-only policy, ordering, distinct |
| [docs/pagination.md](docs/pagination.md) | limit/offset, keyset cursors, `count=true`, `Link` headers, cost limit |
| [docs/aggregates.md](docs/aggregates.md) | `select=` aggregate functions (`count()`, `sum`, `avg`, `max`, `min`, spatial `extent`/`union`/`collect`/`centroid`), grouping, embeds, ordering |
| [docs/mutations.md](docs/mutations.md) | POST/PATCH/PUT/DELETE, bulk, upsert, If-Match, Prefer, nested mutations |
| [docs/rpc.md](docs/rpc.md) | Functions as endpoints, computed columns, JWT minting |
| [docs/errors.md](docs/errors.md) | Problem Details format, `code` catalogue, status mapping |
| [docs/openapi.md](docs/openapi.md) | What the generated OpenAPI 3.1 document contains |
| [docs/configuration.md](docs/configuration.md) | Config layering; full reference in `examples/pdbr.example.yaml` |
| [docs/upgrading.md](docs/upgrading.md) | Breaking changes and config migration between releases |
| [docs/compatibility.md](docs/compatibility.md) | Versioning, what 1.0 freezes, experimental features, deprecation, supported versions |
| [docs/migrating-from-postgrest.md](docs/migrating-from-postgrest.md) | Moving from PostgREST v12 or v13 |
| [docs/cli.md](docs/cli.md) | All subcommands, scripting patterns |
| [docs/rls.md](docs/rls.md) | Roles, JWT/header claims, policies |
| [docs/smart-comments.md](docs/smart-comments.md) | `@omit`/`@name`/`@foreignKey`/… tags with REST semantics |
| [docs/postgis.md](docs/postgis.md) | GeoJSON, spatial filters, pseudo-columns, distance ordering |
| [docs/contrib.md](docs/contrib.md) | Full-text search, trigrams, unaccent, hstore, ltree, ranges, inet, citext, pgvector: operators, rank/similarity/vector-distance ordering, values |
| [docs/plugins.md](docs/plugins.md) | Hook surfaces, writing a plugin, built-ins as worked examples |
| [docs/caching.md](docs/caching.md) | Schema cache and CI/CD gates |
| [docs/deployment.md](docs/deployment.md) | Docker image, health checks, production checklist |
| [docs/contracts.md](docs/contracts.md) | Schema contracts: per-audience APIs from `@contract` tags, base paths, security model |
| [docs/metrics.md](docs/metrics.md) | Prometheus `/metrics`: enabling, separate listener, metric and route-class reference, scrape config |
| [docs/architecture.md](docs/architecture.md) | Pipeline, `plan.Request`, compiler contract, invariants, gotchas |
| [docs/pdbcore.md](docs/pdbcore.md) | The shared pdbcore module (pdbq ↔ pdbr) and the dependency bump procedure |

## Development

```console
$ make test          # unit + golden tests (no database needed)
$ make test-e2e      # end-to-end suite against a disposable PostGIS Postgres (-p pdbr-test, port 5433)
$ make bench         # API-build, compile and OpenAPI benchmarks
$ make fuzz          # fuzz the filter/spatial/URL-grammar → SQL paths (FUZZTIME=30s each)
$ make lint          # golangci-lint (config committed)
$ make example-config # regenerate examples/pdbr.example.yaml from struct tags
$ make fixture       # regenerate db/init/01-schema.sql from pdbcore's fixture
$ make dev           # live stack with watch mode
$ make docker-build  # distroless image
$ make help          # every target
```

Golden tests are the compiler's regression net: each `.http` request in
`internal/compile/testdata/` has its compiled SQL snapshotted next to it
(regenerate with `go test ./internal/compile/ -update`). `CLAUDE.md` is the
maintainer's map of the code (architecture, contracts, gotchas).

Load benchmarks (pdbr and PostgREST lanes next to pdbq) are tracked in
[pg-vs-pdbq](https://github.com/suprbdev/pg-vs-pdbq), not here.

## License

MIT, (c) 2026 suprbdev.
