# Deployment

> **Status: as built.** Dockerfile, compose files, Makefile targets, CI and
> the release workflow described here exist and were exercised (`make
> docker-build`, `make compose-up`/`compose-down`, `act -n`).

## Docker image

The root `Dockerfile` is a multi-stage build (`golang:1.26-alpine` →
`gcr.io/distroless/static-debian12:nonroot`) producing a static binary
(~15 MB image), `ENTRYPOINT ["/pdbr"]`, default command `serve`, version
stamped with `--build-arg VERSION` (`-X main.version`):

```console
$ make docker-build                     # → pdbr:<git describe>, pdbr:dev on an untagged tree
$ docker run -e PDBR_DATABASE_URL=postgres://... -p 8080:8080 pdbr:dev
```

Released images are published multi-arch (`linux/amd64`, `linux/arm64`) to
`ghcr.io/suprbdev/pdbr` and `docker.io/suprbdev/pdbr` on every `v*` tag,
tagged `X.Y.Z`, `vX.Y.Z` and `X.Y`:

```console
$ docker run -e PDBR_DATABASE_URL=postgres://... -p 8080:8080 ghcr.io/suprbdev/pdbr:0.1.0
```

Any subcommand works as the container command:
`docker run ghcr.io/suprbdev/pdbr:0.1.0 schema dump -o /out/schema.cache`.

Distroless has no shell, so run health checks from the orchestrator against
`GET /healthz` (liveness) and `GET /readyz` (readiness). Both return
`{"status":"ok"}` and live at the **root**, never under `server.base_path`:

```yaml
# kubernetes
livenessProbe:  { httpGet: { path: /healthz, port: 8080 } }
readinessProbe: { httpGet: { path: /readyz,  port: 8080 } }
```

`/healthz` only reports that the process is serving. `/readyz` also pings the
database pool, bounded by `server.readyz_timeout` (default `2s`); when the
ping fails or times out it answers `503` problem+json with code
`unavailable`, so an orchestrator stops routing to an instance that cannot
reach PostgreSQL without restarting it.

## Local stack (compose)

`compose.yaml` runs `postgis/postgis:16-3.4` initialised with the fixture
schema (`db/init/01-schema.sql`, regenerated from pdbcore with `make fixture`)
plus pdbr in watch mode with RLS off and `errors.detail: dev`. It publishes
**no host ports**; put your mappings in a gitignored `compose.override.yaml`
(compose merges it automatically):

```yaml
# compose.override.yaml
services:
  db:
    ports: ["5432:5432"]
  pdbr:
    ports: ["8080:8080"]
```

```console
$ make dev            # build + docker compose up, foreground
$ make compose-up     # same, detached
$ make compose-down   # stop and remove volumes
```

The db service has a `pg_isready` healthcheck that gates pdbr's start; the
pdbr container itself has no compose healthcheck (distroless has no shell) —
probe `GET /healthz` from outside. `compose.test.yaml` is the e2e database
only (`make test-e2e`, project `pdbr-test`, host port 5433).

## Endpoints

| Path | Purpose |
|---|---|
| `{base}/{table}`, `{base}/{table}/{pk}`, `{base}/{table}/by/...`, `{base}/{schema}/...` | the API ([quickstart.md](quickstart.md#what-gets-generated)) |
| `{base}/rpc/{fn}` | functions |
| `{base}/openapi.json` | OpenAPI 3.1 (`server.openapi`, on by default) |
| `GET /healthz`, `/readyz` | liveness/readiness (outside `base_path`) |
| `GET /metrics` | Prometheus metrics when `metrics.enabled` (`metrics.path`; on `metrics.addr` when set) — see [metrics.md](metrics.md) |
| `OPTIONS *` | CORS preflight when `server.cors_origins` is set |

Filters, `select=` and RPC arguments travel in the URL, so proxy, load
balancer and access logs along the path record them. Where that matters,
prefer `POST /rpc/{fn}` with a body for sensitive arguments and truncate query
strings in intermediary log formats.

## Production checklist

- **Terminate TLS in front of pdbr.** The server speaks plain HTTP only —
  JWTs, claims and all data cross the wire in cleartext until a
  TLS-terminating reverse proxy, load balancer or ingress controller sits in
  front. Never expose the pdbr port directly to untrusted networks.
- `rls.enabled: true` with a **non-superuser** connection role that has been
  granted the request roles (`GRANT anonymous, app_user TO pdbr_conn`). Set
  `rls.allowed_roles` to the exact list a token may name; a role outside it
  → `403 role_not_allowed`.
- `rls.auth.mode: jwt` with a strong `jwt_secret` or a `jwks_url` (or
  `headers` strictly behind a gateway that strips every inbound
  `X-Pdbr-Claim-*` header in any letter case, with
  `rls.auth.trusted_proxies` set to that gateway and `rls.auth.header_claims`
  listing the claims it sends; pdbr answers `401 invalid_token` to a claim
  header that is misspelt (an underscore, punctuation), repeated, a second
  spelling of another claim, or outside `header_claims` — see
  [rls.md](rls.md)).
- `errors.detail: prod` (default) — constraint violations pass through,
  internals do not. Use `strict` if constraint/column names in error
  messages are themselves sensitive (hides all database error messages;
  `code` remains).
- Decide on `server.openapi`. It is on by default because a REST API without
  its contract is hard to consume, but the document is a complete map of
  every exposed table, column, relation and function to unauthenticated
  callers. Set `server.openapi: false` and ship the document from
  `pdbr schema print` out-of-band if that map is sensitive.
- `server.cors_origins`: list exact origins; `*` is for public read APIs
  only.
- `server.compression: true` when no proxy compresses for you; leave it off
  behind one that does.
- Timeouts: `server.request_timeout` (30s) bounds the HTTP request,
  `database.statement_timeout` (30s) backstops runaway statements (`504`).
  Set both to fit your workload; keep the statement timeout ≤ the request
  timeout.
- Resource limits: `database.max_conns` bounds the pool; each request uses
  at most one connection. `server.max_page_size`, `server.max_cost`,
  `server.max_embed_depth` and `server.max_body_bytes` bound the cost of a
  single request.
- `server.read_only: true` for read replicas, maintenance windows and demos
  (every write → `405 read_only`).
- Boot from a schema cache (see [caching.md](caching.md)) so the runtime
  role needs no `pg_catalog` privileges and cold start skips introspection.
- Leave `watch.enabled` off — it installs an event trigger and re-introspects
  on DDL, both dev-time behaviours; it is refused alongside a cache anyway.
- Metrics: with `metrics.enabled`, set `metrics.addr` (e.g. `:9090`) to a
  port only the scraper reaches, so the unauthenticated endpoint stays off
  the public listener ([metrics.md](metrics.md#security)).
- Consider `server.etags: false` when column-level grants deny `SELECT` on
  some columns: the ETag hashes the whole row in SQL and those resources
  would otherwise `403`.

## Reverse proxies

- **`server.base_path`** (default `/api`) is part of every route pdbr
  serves; a proxy that strips a prefix must strip the same one, or set
  `server.base_path` to match what pdbr actually receives. Health endpoints
  are at the root regardless.
- **`Link` and `Location` headers** are built from the request URL as pdbr
  sees it (path + query, relative). Cursor `Link` values reuse the incoming
  path, so a prefix-rewriting proxy should rewrite response `Link`/`Location`
  headers too, or leave the prefix intact.
- **`X-Forwarded-*`** headers are not interpreted. `rls.auth.trusted_proxies`
  is matched against the direct TCP peer address, so in `headers` auth mode
  the gateway that injects `X-Pdbr-Claim-*` must be the direct peer (or every
  hop in between must be trusted and pass the headers through unchanged).
  `X-Forwarded-Proto` / `X-Forwarded-Host` are irrelevant because pdbr emits
  relative `Link`/`Location` URLs and never redirects.
- **CORS**: either let pdbr answer preflight (`server.cors_origins`) or
  handle it entirely at the proxy — not both, to avoid duplicated headers.
- **Rate limiting**: pdbr has no built-in rate limiter; the per-request
  limits above bound one request, not many in parallel. Put `limit_req`
  (nginx), `local_ratelimit` (Envoy) or your gateway's budget in front,
  keyed by client IP at minimum (or by JWT subject when a gateway
  authenticates).

## Environment-variable examples

```console
PDBR_DATABASE_URL=postgres://pdbr_conn:***@db:5432/app
PDBR_DATABASE_MAX__CONNS=20
PDBR_SERVER_ADDR=:8080
PDBR_SERVER_BASE__PATH=/api
PDBR_SERVER_OPENAPI=false
PDBR_SERVER_CORS__ORIGINS=https://app.example.com
PDBR_SERVER_COMPRESSION=true
PDBR_SCHEMA_CACHE__PATH=/etc/pdbr/schema.cache
PDBR_RLS_ALLOWED__ROLES=anonymous,app_user
PDBR_RLS_AUTH_JWKS__URL=https://auth.example.com/.well-known/jwks.json
PDBR_RLS_AUTH_JWT__ISSUER=https://auth.example.com/
PDBR_RLS_AUTH_JWT__AUDIENCE=app
PDBR_ERRORS_DETAIL=prod
PDBR_LOG_FORMAT=json
```

The same as flags: `pdbr serve --schema.cache_path /etc/pdbr/schema.cache
--server.openapi=false --errors.detail prod`.

## Resource sizing

pdbr does no row marshaling in Go — PostgreSQL builds the response JSON and
Go streams one `jsonb` value (or encodes it as CSV) — so memory per request is
roughly the response size plus a small constant, and CPU is dominated by
parsing/compiling the request. A single instance with the default
`database.max_conns: 10` is bounded by the database, not the process; scale
horizontally behind the proxy and raise `max_conns` in step with the
database's connection budget (or put a pooler in front of the database).
The OpenAPI document and the router table are built once per catalog and
shared read-only across requests.

## Releasing

`make release` (interactive) refuses a dirty tree, prompts for `vX.Y.Z`,
runs `make test lint`, creates an annotated tag and pushes it. The `Release`
workflow (`.github/workflows/release.yaml`) then, on the `v*` tag:

1. runs `go test -race ./...`;
2. cross-compiles `linux/amd64`, `linux/arm64`, `darwin/amd64`,
   `darwin/arm64`, `windows/amd64` (`pdbr_X.Y.Z_<os>_<arch>.tar.gz` / `.zip`)
   with `-X main.version=vX.Y.Z`;
3. writes `checksums.txt` (sha256);
4. creates the GitHub release with generated notes and the archives attached;
5. builds and pushes the multi-arch image (`linux/amd64`, `linux/arm64`) to
   `ghcr.io/suprbdev/pdbr` and `docker.io/suprbdev/pdbr`, tagged `X.Y.Z`,
   `vX.Y.Z` and `X.Y` (Docker Hub needs the `DOCKERHUB_USERNAME` /
   `DOCKERHUB_TOKEN` repository secrets; ghcr uses `GITHUB_TOKEN`).

Publishing steps are skipped under [act](https://github.com/nektos/act)
(`if: ${{ !env.ACT }}`), so `act -n -W .github/workflows/release.yaml`
dry-runs the whole thing locally.

### Dependency on pdbcore

pdbr consumes `github.com/suprbdev/pdbcore` as an ordinary tagged module
(`v0.1.0`); no `replace` directive, vendoring or side-by-side checkout is
needed to build, test or run CI.

Later pdbcore releases: bump with `go get github.com/suprbdev/pdbcore@vX.Y.Z`
and `go mod tidy`, regenerate goldens and `db/init/01-schema.sql`
(`make fixture`) if the fixture changed, run `make test test-e2e lint`, then
`make release`. Verify the published release lists 5 archives +
`checksums.txt` and that `docker run ghcr.io/suprbdev/pdbr:X.Y.Z --version`
prints `vX.Y.Z`.
