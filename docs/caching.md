# Schema cache & CI/CD

> **Status: as built.** `pdbr schema dump|check`, `serve --schema.cache_path`
> and the pdbq interoperability below are implemented and covered by
> `cmd/pdbr/schema_test.go` and `test/e2e_cache_test.go`.

Introspection needs broad `pg_catalog` access and adds cold-start latency.
The schema cache removes both:

```console
$ pdbr schema dump -o schema.cache              # at build/deploy time
$ pdbr serve --schema.cache_path schema.cache   # at runtime
```

The cache is gzipped JSON of the introspected catalog wrapped in an envelope
carrying a **format version** (`CatalogFormatVersion`, currently 10), the
**writer** (product, version and pdbcore version) and a **content hash**;
`serve` refuses mismatched versions (naming the writer) and corrupted files, and
the loader caps decompressed size at 256 MiB as a gzip-bomb guard. Booting from
cache never touches `pg_catalog`, so the runtime role only needs privileges on
the actual data.

## CI drift gate

```console
$ pdbr schema check --cache schema.cache   # exit 1 when live DB drifted
```

`check` loads the cache file **before** it touches the database, so a
missing, corrupt or stale-format file fails fast with exit 1 and no
connection attempt. It then introspects live (ignoring `schema.cache_path`),
compares catalog hashes (which cover the PostgreSQL major version, not the
minor one, so a minor upgrade is not drift) and, on drift, prints the report
to **stdout** and exits 1; a matching cache prints `schema cache is up to date` and exits 0.
The report lists every difference, one per line, after the hash summary
(lines come from `pdbcore/introspect.Diff`):

```
schema drift detected: cache 3e1f0a9c2b7d != live 88ad41c0e5f2
  table public.users: column phone added (text)
  table public.posts: column body type changed (text -> varchar)
  view public.metrics removed
  enum public.mood changed
  function public.publish_post added
```

Typical pipeline:

1. Migrations run.
2. `pdbr schema check` — fails the build if the committed cache no longer
   matches the migrated database.
3. On failure, regenerate (`pdbr schema dump`), review the diff of
   `pdbr schema print` output (the OpenAPI document — the contract your
   clients depend on), commit both.

Because `pdbr schema print` and `pdbr request --print-sql` both work with
`--schema.cache_path` and no database, a pipeline can also diff the OpenAPI
document and golden SQL against a previous version without a live database.

## Interactions

- `watch.enabled` and `schema.cache_path` are mutually exclusive (validated
  at startup): watch mode exists to chase a changing schema, the cache
  exists to freeze one.
- The cache stores the *catalog*, not the routes or the OpenAPI document —
  plugins, `server.base_path`, `schema.schemas` aliasing, `filters.*` and
  every other config still apply at boot, so changing plugin or server config
  does not require a new dump. Only DDL (and comments, which are catalog
  data) does.
- `pdbr schema dump` always introspects live, even when `schema.cache_path`
  is set, so it can never re-dump a stale cache.
- Booting from a cache does not require a reachable database at all: the
  pool is opened lazily, so `serve`, `schema print` and `request --print-sql`
  work offline; only data requests then answer `503 unavailable`
  (`TestCacheBootWithoutDatabase`).
- The cache holds privileges as seen by the role that dumped it. Dump with
  the same connection role the server will use, or routes/methods gated on
  `SELECT`/`INSERT`/… may not match what the runtime role can actually do.

## Interoperability with pdbq

Cache files are produced and read by `pdbcore/cache` with a single shared
`CatalogFormatVersion`, so a file written by `pdbq schema dump` loads in
`pdbr serve --schema.cache_path` and vice-versa. One dump per database can
serve both products; a `pdbr schema check` against a cache produced by pdbq
(or the other way round) is a valid drift gate. Note that the two products
apply different `schema.schemas` and function settings at boot — the cache
holds whatever the dumping side was configured to introspect, so keep those
settings aligned when sharing a file.

Verified end to end: `pdbq schema dump` → `pdbr schema check` (clean),
`pdbr serve --schema.cache_path` and `pdbr request` over the same fixture
database produce the same catalog hash and OpenAPI document as pdbr's own
introspection (`test/e2e_cache_test.go` `TestCacheInteropPdbq`, run with
`PDBR_TEST_PDBQ_DIR=/path/to/pdbq` next to `PDBR_TEST_DATABASE_URL`).

## Watch mode (the opposite of caching)

`watch.enabled: true` re-introspects on DDL and hot-swaps the routes and the
OpenAPI document; in-flight requests finish on the runtime they started on.
pdbr installs an event trigger named `pdbr_watch` (function
`pdbr_watch_notify`) that `NOTIFY`s `watch.channel` (`pdbr_ddl`) — distinct
from pdbq's `pdbq_watch`/`pdbq_ddl`, so both servers can watch one database.
When the connection role may not create event triggers (not a superuser),
the watcher logs a warning and falls back to polling the catalog hash every
`watch.poll_interval` (5s). The watcher starts from the hash of the catalog
the boot API was built from and re-introspects once as soon as it is
listening, so DDL that lands between boot introspection and the watcher
starting is picked up too. Both paths are exercised by
`test/e2e_watch_test.go`.
