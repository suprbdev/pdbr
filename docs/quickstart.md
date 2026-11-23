# Quickstart

## With the bundled dev stack

```console
$ make dev            # or: make compose-up (detached) … make compose-down
```

This builds the image and starts PostgreSQL (`postgis/postgis:16-3.4`) with
the demo schema (users, posts, comments, places, events, a `metrics` view and
a handful of functions — the same fixture pdbcore ships for both pdbq and
pdbr, mounted from `db/init/01-schema.sql`, which `make fixture` regenerates
from `pdbcore/testutil`) and pdbr in watch mode without authentication,
running every request as the connection role (`auth.mode: none`,
`roles.switch: false`), and `errors.detail: dev`. See [deployment.md](deployment.md#local-stack-compose) for the
Makefile targets.

The stack publishes no ports by itself — networking setups differ, so port
mappings live in a gitignored `compose.override.yaml` that compose picks up
automatically. Create one that fits your machine:

```yaml
# compose.override.yaml
services:
  db:
    ports:
      - "5432:5432"
  pdbr:
    ports:
      - "8080:8080"
```

The API is then at `http://localhost:8080` (`server.base_path` defaults to
`""`, the root, as in PostgREST; set it to e.g. `/api` to prefix every
route). Try:

```console
# list with column selection and an embedded backward relation
$ curl -s 'localhost:8080/users?select=id,email,posts(id,title)&limit=2' | jq

# one row by primary key (note the weak ETag)
$ curl -si localhost:8080/users/1

# filter + order + count (published is not indexed in the fixture, so it needs
# filters.allow_columns or indexed_only: false — see filtering.md; title is)
$ curl -si 'localhost:8080/posts?title=ilike.*hello*&order=id.desc&count=true' | grep -i x-total-count

# create
$ curl -si localhost:8080/posts \
    -H 'Content-Type: application/json' \
    -d '{"author_id":1,"title":"Hello","published":true}'
HTTP/1.1 201 Created
Location: /posts/4
...

# call a function
$ curl -s 'localhost:8080/rpc/search_posts?term=hello&select=id,title'
$ curl -s localhost:8080/rpc/add_numbers -H 'Content-Type: application/json' -d '{"a":2,"b":3}'
5

# the OpenAPI 3.1 document
$ curl -s localhost:8080/openapi.json | jq '.paths | keys'
```

Errors come back as RFC 9457 problem details
(`application/problem+json`) with a stable `code` ([errors.md](errors.md)):

```console
$ curl -s 'localhost:8080/users?nope=eq.1'
{"type":"about:blank","title":"Bad Request","status":400,"detail":"unknown column \"nope\"","instance":"/users","code":"unknown_column"}
```

Because watch mode is on, `ALTER TABLE`/`CREATE TABLE` in the database is
picked up automatically: pdbr re-introspects, rebuilds the routes and the
OpenAPI document, and swaps them in without a restart — in-flight requests
finish on the old build.

## Against your own database

```console
$ pdbr config init                        # starter pdbr.yaml
$ $EDITOR pdbr.yaml                       # set database.url
$ pdbr serve
```

The starter runs without credentials, as the connection role (`auth: {mode:
none}`, `roles: {switch: false}`), so it works with only `database.url`
set; pdbr logs a warning about it. Before production, configure `auth`
(a JWT secret, secret file or JWKS URL, or a trusted gateway) and `roles`
(see [rls.md](rls.md)): the built-in defaults verify JWTs and refuse
requests without credentials, and pdbr does not start in `jwt` mode
without a key.

Every option is also an env var (`PDBR_DATABASE_URL=...`) or flag
(`--database.url=...`); precedence is flags > env > file > defaults. See
[configuration.md](configuration.md).

## What gets generated

For each table/view in `schema.schemas` the connection role can `SELECT`
(names are raw PostgreSQL identifiers — no inflection):

| Postgres | REST |
|---|---|
| table `users` | resource `/users` (and canonical `/public/users`); JSON keys = column names |
| rows | `GET /users?select=&order=&limit=&offset=&after=&before=&count=&distinct=&{col}={op}.{value}` → JSON array; `HEAD` for headers only |
| primary key | `GET /users/{pk}` (composite: `/{v1},{v2}`) with `ETag`, `If-None-Match` → `304` |
| unique constraints | `GET /users/by/email/{email}`; multi-column `/memberships/by/org_id,slug/7,eng` |
| foreign keys | embeds in `select=`: `posts?select=*,users(email)` (forward, object) and `users?select=*,posts(title)` (backward, array); `posts!inner(...)` restricts the parent |
| `INSERT`/`UPDATE`/`DELETE` privileges | `POST /users` (object or array), `PATCH`/`DELETE /users/{pk}` and `.../by/...`, `PUT /users` upsert (`?on_conflict=`); missing privilege → method absent, `405` + `Allow` |
| enums | JSON strings with the pg labels verbatim; OpenAPI `enum` |
| composite types | nested JSON objects in responses and bodies; arrays too |
| stable/immutable functions | `GET /rpc/{fn}?arg=value` (also `POST`); `SETOF table` results take `select`/filters/`order`/`limit`/`offset`/`count` |
| volatile functions | `POST /rpc/{fn}` with a JSON body (GET → `405`) |
| functions taking a row type | computed columns in `select=` (`users_post_count` → `post_count`) |
| PostGIS columns | GeoJSON in/out, spatial filter ops, `near=` distance ordering, `<col>_area` style pseudo-columns ([postgis.md](postgis.md)) |
| everything above | `GET /openapi.json` ([openapi.md](openapi.md)) |

One-off requests from the shell, without a running server:

```console
$ pdbr request GET '/users?select=email&limit=3'
$ pdbr request POST /users --body '{"email":"x@y.z"}'
$ pdbr request GET '/users?mood=eq.happy' --print-sql
```

Next: [api.md](api.md), [filtering.md](filtering.md),
[mutations.md](mutations.md), [errors.md](errors.md),
[configuration.md](configuration.md), [rls.md](rls.md), [cli.md](cli.md).
