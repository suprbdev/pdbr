# CLI reference

```
pdbr serve                                run the API server
pdbr request METHOD PATH [flags]          one-off request, body to stdout
pdbr schema dump|check|print              schema cache + OpenAPI tools
pdbr config init|validate|example         configuration tools
pdbr plugins list                         registered plugins + hooks
pdbr completion bash|zsh|fish|powershell  shell completion script
pdbr --version                            print the version
```

All commands accept `--config` plus the config-path flags (`--database.url`,
`--server.base_path`, ...; full list in
[configuration.md](configuration.md#flags)).

## pdbr serve

Loads config, connects (or boots from `--schema.cache_path`), builds the API
and listens on `server.addr` until `SIGINT`/`SIGTERM`, then shuts down
gracefully (10s). Startup warnings (RLS off, headers mode without trusted
proxies, empty `allowed_roles`, suppressed bare aliases, skipped overloads) go
to the log.

```console
$ pdbr serve
$ pdbr serve --database.url postgres://... --server.base_path "" --watch.enabled
```

## pdbr request

Runs a single request through the real handler in-process — no listener, no
HTTP client — and prints the result. Pipe-friendly.

```
pdbr request METHOD PATH [--body @file|-|'{json}'] [--header 'K: V']... [--accept text/csv] [-i] [--print-sql]
```

- `PATH` may omit `server.base_path`: `/users?select=id` and
  `/api/users?select=id` are the same request. Everything after `?` is the
  query string, so quote it for the shell.
- `--body` takes inline JSON, `@file`, or `-` for stdin. Bodies are sent as
  `application/json`.
- `--header 'K: V'` is repeatable (`Prefer`, `If-Match`, `If-None-Match`,
  `Accept-Encoding`, ...). `--accept text/csv` is shorthand for
  `--header 'Accept: text/csv'`.
- Auth mode is forced to `none` (`rls.auth.mode=none`): there is no
  credential to present, so the request runs as `rls.anonymous_role` when
  RLS is enabled, or as the connection role otherwise. To exercise a role,
  point the config at a connection role instead or use `curl` against a
  running server.
- Output: the status line goes to **stderr**, the body to **stdout**; `-i`
  also prints response headers (sorted by name) to stdout before the body,
  then a blank line (like `curl -i`). CSV bodies (`--accept text/csv`) are
  streamed verbatim (RFC 4180, CRLF line ends).

```console
$ pdbr request GET '/users?select=id,email,posts(title)&limit=2'
$ pdbr request GET /users/1 -i
$ pdbr request POST /users --body '{"email":"a@b.c"}'
$ pdbr request POST /users --body @user.json --header 'Prefer: return=minimal'
$ echo '{"title":"x","author_id":1}' | pdbr request POST /posts --body -
$ pdbr request PATCH /users/1 --body '{"full_name":"Ann"}' --header 'If-Match: W/"9f2c..."'
$ pdbr request DELETE /users/1
$ pdbr request GET '/rpc/search_posts?q=pg'
$ pdbr request GET '/posts?select=id,title' --accept text/csv > posts.csv
$ pdbr request GET /openapi.json > openapi.json
```

Exit codes: `0` on 2xx/3xx, `1` transport/config error (bad config, no
database, cache mismatch), `2` on any 4xx/5xx — the problem body is still
printed to stdout, so scripts can distinguish "down" from "bad request" and
still read the `code`.

### --print-sql

`--print-sql` prints the compiled statement and its parameter list instead of
executing it. Parsing, coercion and planning run exactly as they would for a
real request, so what you see is exactly what would run:

```console
$ pdbr request GET '/users?select=id,email&mood=eq.happy&limit=2' --print-sql
-- request: GET /api/users?select=id,email&mood=eq.happy&limit=2
SELECT coalesce(json_agg(...), '[]'::json) AS data FROM ...
-- args: ["happy",2]
```

The format is `-- request: METHOD PATH\n<SQL>\n-- args: [json]\n` — identical
to the golden files in `internal/compile/testdata`, so a `.sql` golden and
`--print-sql` output for the same request diff clean. Nothing is executed
(mutations and volatile RPCs are safe to inspect); combined with
`--schema.cache_path` it needs no database connection at all. Requests that
never reach the compiler (`/openapi.json`, `/healthz`, a `405`) print no
SQL and exit `2` with the problem body when applicable.

## pdbr schema

```console
$ pdbr schema dump -o schema.cache          # introspect + write versioned cache
$ pdbr schema check --cache schema.cache    # exit 1 on drift (CI gate)
$ pdbr schema print                         # OpenAPI 3.1 JSON to stdout
$ pdbr schema print --yaml                  # same document as YAML
```

`dump` always introspects the live database (it ignores `schema.cache_path`)
and writes the gzipped catalog envelope. `check` loads the cache, introspects
live, and prints a line-per-difference report on drift:

```
schema drift detected: cache 3e1f0a9c2b7d != live 88ad41c0e5f2
  table public.users: column phone added (text)
  function public.search_posts changed
```

`print` emits the same document as `GET {base}/openapi.json` (see
[openapi.md](openapi.md); the e2e suite asserts the two are byte-identical);
with `--schema.cache_path` it needs no database. `--yaml` re-encodes the JSON
document as YAML in the same key order (`api.Document.YAML`), so JSON and
YAML always carry the same content in the same order.
Cache files are produced by pdbcore and interchangeable with pdbq — see
[caching.md](caching.md).

## pdbr config

```console
$ pdbr config init      # write a starter pdbr.yaml (refuses to overwrite)
$ pdbr config validate  # load defaults+file+env+flags, run every validation rule
$ pdbr config example   # print the annotated reference generated from struct tags
```

`example` needs no config or database; `make example-config` redirects it to
`examples/pdbr.example.yaml`. See
[configuration.md](configuration.md#validation-rules) for the checks
`validate` runs.

## pdbr plugins list

```console
$ pdbr plugins list
smart-comments       priority=50   enabled  hooks: catalog, inflection, api
advanced-filters     priority=150  enabled  hooks: api
postgis              priority=160  enabled  hooks: api
nested-mutations     priority=200  enabled  hooks: api, compile, request
```

With `plugins.disabled: [nested-mutations]` the last line reads
`nested-mutations     priority=200  disabled hooks: api, compile, request`.

Format: `%-20s priority=%-4d %-8s hooks: %s` — name, priority, `enabled` /
`disabled` (per `plugins.disabled`), then the hooks the plugin implements
(`catalog`, `inflection`, `api`, `compile`, `request`). Works without a
valid database config. See [plugins.md](plugins.md).

## Shell completions

`pdbr completion bash|zsh|fish|powershell` prints a completion script for the
given shell (cobra's generator). Flag values with a fixed vocabulary complete
too, on every subcommand that takes them: `--errors.detail` → `dev prod
strict`, `--log.level` → `debug info warn error`.

```console
# bash (requires bash-completion)
$ pdbr completion bash > /etc/bash_completion.d/pdbr
# zsh
$ pdbr completion zsh > "${fpath[1]}/_pdbr"
# fish
$ pdbr completion fish > ~/.config/fish/completions/pdbr.fish
```

Run `pdbr completion <shell> --help` for per-shell install notes.

## Scripting patterns

Bodies are plain JSON on stdout, so `jq` composes naturally:

```console
# emails of the first 100 users
$ pdbr request GET '/users?select=email' | jq -r '.[].email'

# walk every page with keyset cursors
$ cur=""; while :; do
    out=$(pdbr request GET "/posts?select=id&limit=100${cur:+&after=$cur}" -i)
    printf '%s\n' "$out" | sed '1,/^$/d' | jq -c '.[]'
    cur=$(printf '%s\n' "$out" | awk 'tolower($1)=="x-next-cursor:"{print $2}' | tr -d '\r')
    [ -n "$cur" ] || break
  done

# create from a JSON file, capture the new id
$ id=$(pdbr request POST /users --body @user.json | jq -r '.id')

# error code without parsing stderr
$ pdbr request GET /users/999999 | jq -r '.code'    # not_found (exit 2)

# CI: fail on drift, show the report
$ pdbr schema check --cache schema.cache || exit 1

# feed the OpenAPI document to a generator
$ pdbr schema print --schema.cache_path schema.cache > openapi.json
```

`--print-sql` in a `git diff`-style review:

```console
$ pdbr request GET '/users?select=*,posts(*)' --print-sql > /tmp/a.sql
$ pdbr request GET '/users?select=*,posts!inner(*)' --print-sql > /tmp/b.sql
$ diff /tmp/a.sql /tmp/b.sql
```
