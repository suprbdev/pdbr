# Metrics

pdbr can serve [Prometheus](https://prometheus.io/) metrics: HTTP requests by
route class, database operations, connection pool statistics, schema
rebuilds, compile time and a few pdbr-specific counters. The instruments come
from the shared `pdbcore/metrics` package, so pdbq exports the same series
under its own `pdbq_` prefix.

## Enabling

Metrics are off by default.

```yaml
metrics:
  enabled: true
  path: /metrics   # default
  addr: ""         # default: served on server.addr
```

The same keys are available as `PDBR_METRICS_ENABLED`, `PDBR_METRICS_PATH`,
`PDBR_METRICS_ADDR` and the `--metrics.enabled`, `--metrics.path` and
`--metrics.addr` flags ([configuration.md](configuration.md)).

| Key | Default | Notes |
|---|---|---|
| `metrics.enabled` | `false` | serve the endpoint and record every metric |
| `metrics.path` | `/metrics` | URL path of the endpoint; starts with `/`, not `/` itself, no query or fragment |
| `metrics.addr` | `""` | separate listen address (e.g. `:9090` or `127.0.0.1:9090`); empty serves the endpoint on `server.addr` |

### On the main listener (`metrics.addr` empty)

The endpoint lives at `metrics.path` on `server.addr`, outside
`server.base_path`, next to `/healthz`. It bypasses auth, CORS, gzip and the
request timeout, answers `GET` and `HEAD` (anything else is a `405`
problem), and its scrapes are counted under the `metrics` route class.

Validation rejects a `metrics.path` that collides with one of pdbr's own
routes on that listener: `/healthz`, `/readyz`, `server.base_path` itself or
any path below it (the API and `{base}/openapi.json`). With
`server.base_path: ""` the API owns every path, so only `/healthz`, `/readyz`
and `/openapi.json` are rejected and `metrics.path` takes precedence over a
resource of the same name: the fixture's `metrics` view, for example, is not
reachable at `/metrics` while metrics are on. Pick another path or a
separate address in that case.

### On a separate listener (`metrics.addr` set)

`pdbr serve` runs a second HTTP server on `metrics.addr` that serves only
`metrics.path` (every other path is a `404` problem); the main listener no
longer answers it. Both listeners start and shut down together, and if either
fails (for example the port is taken) `pdbr serve` stops with that error.
`metrics.addr` must be a `host:port` address different from `server.addr`.
Scrapes on this listener are not counted in the HTTP metrics.

### Embedding

`pdbr.New(cfg)` creates the instruments (on a private registry carrying the
Go runtime and process collectors) when `metrics.enabled` is set, and
`App.Metrics()` returns them (`nil` when disabled). `App.Handler(ctx)` serves
the endpoint at `metrics.path` when `metrics.addr` is empty; with
`metrics.addr` set only `App.Serve` runs the second listener, so an embedder
mounts `app.Metrics().Handler()` wherever it likes.

To register on your own registry, or to tell several Apps in one process
apart, build the instance yourself and pass it with `pdbr.WithMetrics`:

```go
m, err := metrics.New(metrics.Options{
	Namespace:   "pdbr",
	Registerer:  reg, // a *prometheus.Registry, or pass Gatherer as well
	ConstLabels: prometheus.Labels{"api": "billing"},
})
app := pdbr.New(cfg, pdbr.WithMetrics(m))
```

`WithMetrics` records into `m` whether or not `metrics.enabled` is set; the
endpoint itself is still served only when it is. One `*metrics.Metrics`
serves one App: the App registers its pool and its counters on it, and a
second App on the same instance fails at `Handler`/`Serve` with a duplicate
registration error.

## Metrics

Every name carries the `pdbr_` prefix. With [schema contracts](contracts.md)
configured, every series except `pdbr_db_pool_*` and the runtime series also
carries a `contract` label, and requests to `/healthz`, `/readyz`, the
metrics endpoint and paths outside every contract are not counted.

| Metric | Type | Labels |
|---|---|---|
| `pdbr_http_requests_total`, `pdbr_http_request_duration_seconds` | counter, histogram | `route`, `method`, `status_class` |
| `pdbr_http_requests_in_flight` | gauge | |
| `pdbr_db_duration_seconds` | histogram | `op` |
| `pdbr_db_errors_total` | counter | `op`, `sqlstate_class` (`23`, `40`, ..., `canceled`, `timeout`, `other`) |
| `pdbr_db_retries_total` | counter | `op` |
| `pdbr_db_pool_{acquired,idle,constructing,total,max}_conns` | gauge | |
| `pdbr_db_pool_{acquires,canceled_acquires,empty_acquires,new_conns,max_lifetime_destroys,max_idle_destroys}_total`, `pdbr_db_pool_acquire_duration_seconds_total`, `pdbr_db_pool_empty_acquire_wait_seconds_total` | counter | |
| `pdbr_schema_rebuilds_total` | counter | `result` (`success`, `failure`) |
| `pdbr_schema_last_rebuild_timestamp_seconds` | gauge | |
| `pdbr_compile_duration_seconds` | histogram | |
| `pdbr_cost_limit_rejections_total` | counter | |
| `pdbr_aggregate_requests_total` | counter | |

Plus the standard `go_*` and `process_*` collectors on the default private
registry.

- **`method`** is the request method (`GET`, `HEAD`, `POST`, `PUT`, `PATCH`,
  `DELETE`, `OPTIONS`; anything else is `other`); **`status_class`** is
  `2xx`, `3xx`, `4xx` or `5xx`.
- **`op`** of the database metrics: `query` for a statement run on the pool
  outside a transaction (reads without RLS identity, stable RPCs),
  `query_tx` for one attempt of a pipelined transaction (writes under
  `transactions.mutations`, RLS requests, `Prefer: max-affected`). A retried
  transaction (`transactions.max_retries`) reports each attempt and counts
  the retries in `pdbr_db_retries_total`. A request whose database error
  maps to a problem response (a unique violation → `409`, a timeout →
  `504`) shows in `pdbr_db_errors_total` with its SQLSTATE class; zero rows
  is not an error.
- **`pdbr_db_pool_*`** are read from the pgx pool at scrape time.
- **`pdbr_schema_*`** move only in watch mode: each rebuild after a DDL
  change counts a `success` (and sets the timestamp) or a `failure` (the old
  API keeps serving).
- **`pdbr_compile_duration_seconds`** times the compile chain (core compiler
  plus CompileHooks) of every request that passed the read-only and cost
  gates.
- **`pdbr_cost_limit_rejections_total`** counts requests answered with
  `400 cost_exceeded` because their estimated cost is over `server.max_cost`
  ([pagination.md](pagination.md)). A steady rate means clients need a
  `limit=` or the limit needs tuning.
- **`pdbr_aggregate_requests_total`** counts executed requests whose
  `select=` uses an aggregate function anywhere, embeds included
  ([aggregates.md](aggregates.md)); aggregates scan every matching row, so
  this is the series to watch next to `pdbr_db_duration_seconds`. Requests
  rejected while parsing (for example `400 aggregates_disabled`) are not
  counted.

### Route classes

`route` is one of a small fixed set and never a raw path, so cardinality
stays bounded however many tables and functions are exposed:

| `route` | Requests |
|---|---|
| `resource` | matched a table or view route below `server.base_path` (lists, rows, `by/` lookups, writes; 405s on those paths too) |
| `rpc` | matched `{base}/rpc/{fn}` |
| `openapi` | `{base}/openapi.json` (when `server.openapi` is on) |
| `health` | `/healthz`, `/readyz` |
| `metrics` | scrapes of `metrics.path` on the main listener |
| `other` | everything else: unknown paths (404), CORS preflights, and API requests auth rejected before routing (401, 403) |

## Prometheus scrape config

```yaml
scrape_configs:
  - job_name: pdbr
    metrics_path: /metrics
    static_configs:
      - targets: ["pdbr:9090"]   # metrics.addr; or pdbr:8080 on the main listener
```

On Kubernetes, expose `metrics.addr` as a separate container port and point
a `ServiceMonitor` or `prometheus.io/port` annotation at it.

Some useful queries:

```promql
# request rate and error ratio by route class
sum by (route) (rate(pdbr_http_requests_total[5m]))
sum by (route) (rate(pdbr_http_requests_total{status_class="5xx"}[5m]))
  / sum by (route) (rate(pdbr_http_requests_total[5m]))

# p95 latency of resource requests
histogram_quantile(0.95, sum by (le) (rate(pdbr_http_request_duration_seconds_bucket{route="resource"}[5m])))

# pool saturation
pdbr_db_pool_acquired_conns / pdbr_db_pool_max_conns
```

## Security

The endpoint has no authentication. It exposes no row data, but it does show
traffic volume, error rates by SQLSTATE class, pool sizing and process
details. Keep it off the public listener: set `metrics.addr` to an address
only the scraper can reach (`127.0.0.1:9090`, or a port the ingress does not
route), or, on the main listener, block `metrics.path` at the reverse proxy.
