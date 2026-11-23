# RLS & authentication

> **Status: as built.** Verified by the RLS matrices in `test/e2e_test.go`
> (reads, `roles.allowed`), `test/e2e_mutations_test.go` (writes, column
> grants), `test/e2e_rpc_test.go` (JWT mint round trip) and
> `test/e2e_auth_test.go` (anonymous refusal, headers mode fail-closed,
> `claims.json_setting`, `roles.switch: false`); the auth code is
> `pdbcore/auth`, the transaction identity code `pdbcore/pgexec`.

Identity is configured in three sections:

| Section | Question it answers |
|---|---|
| `auth` | how a request proves who it is: a bearer JWT (`jwt`, the default), claim headers from a trusted gateway (`headers`) or nothing (`none`) |
| `roles` | which database role the request runs as (`switch`, `anonymous`, `default`, `allowed`) |
| `claims` | where the request's claims are exposed to SQL (`prefix`, `json_setting`) |

Whenever a request resolves to a role or claims to set, the statement runs
inside a transaction that first applies the identity, in one statement sent
in the same pipelined batch as the request's statement (one network round
trip):

1. `SET LOCAL ROLE "<role>"` (as `set_config('role', $1, true)`, the same
   operation with the role as a bind parameter) — the role from the
   credential (see [Role resolution](#role-resolution)). A role must match
   `[A-Za-z0-9_]+` and must not be `none`, `public` or a `pg_*` role, in
   any letter case: the configured roles are checked at startup, and a
   credential naming such a role is rejected with `403 role_not_allowed`
   before any SQL runs (`none` would otherwise reset the role to the
   connection role and bypass RLS).
2. `set_config('<claims.prefix>.<key>', $n, true)` for every claim whose key
   matches `[A-Za-z0-9_]+` (others are skipped). String claims are set
   verbatim; any other JSON value is set as its JSON text. With the default
   prefix `jwt.claims`, policies read them via
   `current_setting('jwt.claims.user_id', true)`.
3. With `claims.json_setting` set, `set_config('<json_setting>', $n, true)`
   with every claim as one JSON object (`{}` without credentials).

Only `SET LOCAL` / transaction-scoped `set_config` are ever used, so pooled
connections cannot leak one request's identity into another. A request with
no role to switch to and nothing to set (`roles.switch: false` and no
claims, or an empty `claims.prefix` and no `json_setting`) runs outside a
transaction; every mutation runs in one regardless. There is one operation per HTTP request, so pdbr has no
`transactions.per_request` knob.

## Anonymous requests are refused by default

`roles.anonymous` is empty by default: in `jwt` and `headers` mode a request
without credentials is answered `401` with `code: auth_required` and
`WWW-Authenticate: Bearer`, before any SQL runs. Name the role
unauthenticated requests run as to allow them:

```yaml
roles:
  anonymous: anonymous
```

Every `401` from authentication carries a `WWW-Authenticate` challenge
(RFC 6750): `Bearer` without credentials, `Bearer error="invalid_token"`
for a credential that cannot be used; a `403 role_not_allowed` carries
`Bearer error="insufficient_scope"`.

## Claims settings: `jwt.claims` and `request.jwt.claims`

`claims.prefix` defaults to **`jwt.claims`**, PostGraphile's namespace: a
policy reading `current_setting('jwt.claims.user_id', true)` ports
unchanged from PostGraphile, and pdbq uses the same default, so policies
are written once for both products. An empty prefix sets no per-claim
settings.

`claims.json_setting` additionally sets every claim as one JSON object.
Set it to `request.jwt.claims` and PostgREST / Supabase policies work
unchanged:

```yaml
claims:
  json_setting: request.jwt.claims
```

```sql
CREATE POLICY own_notes ON notes FOR ALL TO app_user
    USING (owner = (current_setting('request.jwt.claims', true)::json->>'sub')::uuid);
```

Without credentials the setting holds `{}`, so `->>` yields `NULL` instead
of failing on an empty string. The JSON setting must not lie under
`claims.prefix` (a claim of that name would overwrite it).

## Claim sources (`auth.mode`)

- **`jwt`** (default): `Authorization: Bearer <token>` (the scheme matches
  in any letter case), HMAC-verified with `auth.jwt.secret` or the contents
  of `auth.jwt.secret_file` (HS256/384/512; trailing newlines are stripped,
  so a Docker or Kubernetes secret file works as is); optional
  `auth.jwt.issuer` / `auth.jwt.audience` checks. Tokens must carry an
  `exp` claim — tokens without one are rejected, so a stolen token cannot
  stay valid forever; `auth.jwt.leeway` tolerates clock skew on `exp`,
  `nbf` and `iat`. A missing token is anonymous (or `401 auth_required`
  without `roles.anonymous`); an invalid token is a `401` problem with
  `code: invalid_token`. A secret, secret file or JWKS URL is required:
  pdbr refuses to start in `jwt` mode without one.

  Set `auth.jwt.jwks_url` for asymmetric verification instead (RS256/384/512,
  ES256/384/512): keys are fetched from the JWKS endpoint, matched by `kid`,
  and cached for `auth.jwt.jwks_cache_ttl` (default 1h; an unknown `kid`
  refreshes early, so key rotation just works). In JWKS mode the HMAC
  algorithms are off the allowlist entirely — no downgrade to
  HMAC-with-public-key.

  `auth.jwt.role_claim_path` locates the role in the token (default
  `role`): dot-separated keys with optional array indexes and double-quoted
  keys, e.g. `app_metadata.role` (Supabase), `realm_access.roles[0]`
  (Keycloak) or `"https://example.com/role"` (Auth0 namespaced claims). A
  missing claim falls back to `roles.default`; a role claim that is not a
  non-empty string is `401 invalid_token`.
- **`headers`**: for deployments behind a trusted gateway that has already
  authenticated the caller. Headers matching `auth.headers.prefix`
  (default `X-Pdbr-Claim-`) become claims: `X-Pdbr-Claim-User-Id: 42` →
  claim `user_id = "42"` → `jwt.claims.user_id = '42'`. The role comes from
  the header named after `auth.headers.role_claim` (`X-Pdbr-Claim-Role`).

  Every claim has exactly **one** header spelling: the part after the prefix
  is alphanumeric words joined by single hyphens, lower-cased with hyphens
  turned into underscores (`User-Id` → `user_id`; the prefix and the words
  match in any letter case). A request is rejected with `401 invalid_token`
  when a prefixed header has any other shape (`X-Pdbr-Claim-User_Id`,
  punctuation), is repeated, or folds onto the same claim as another
  header, so a client cannot smuggle a second spelling past a gateway that
  only strips the canonical one. Set `auth.headers.claims` to the claim
  names the gateway sends (e.g. `[user_id, tenant_id]`; the role claim is
  always accepted) to reject every other claim header with `401` as well.

  The gateway must remove **every** client-supplied header that starts with
  the prefix, in any letter case, before adding its own.

  `auth.headers.trusted_proxies` is **required** in headers mode: the
  gateway's CIDR blocks (or single IPs), e.g. `["10.0.0.0/8",
  "192.168.1.5"]`. Claim headers arriving from any other peer are rejected
  with `401 invalid_token` (requests from them without claim headers are
  anonymous). pdbr refuses to start in headers mode without trusted
  proxies.

  The header prefix is product-namespaced (`X-Pdbr-Claim-`, pdbq's is
  `X-Pdbq-Claim-`) while the settings SQL reads are not (`jwt.claims.*`):
  names SQL sees are product-neutral so policies port between products,
  names on the wire between a gateway and the server are product-namespaced
  like every other pdbr header.
- **`none`**: no credentials; every request runs as `roles.anonymous`, or
  as the connection role with `roles.switch: false` (one of the two is
  required). `pdbr request` and the other one-off CLI commands force this
  mode with `roles.switch: false` — see [cli.md](cli.md).

The verification code (`Authenticator`, JWKS cache, trusted-proxy check,
role resolution) lives in `pdbcore/auth` and is shared with pdbq; the
configuration keys are the same, only the header prefix default differs.

## Role resolution

| Precedence | Source |
|---|---|
| 1 | the role claim (`auth.jwt.role_claim_path` / `auth.headers.role_claim`, default `role`) of a verified credential |
| 2 | `roles.default` — authenticated request without a role claim |
| 3 | `roles.anonymous` — no credential, or an authenticated request without a role claim when `roles.default` is empty |

A request that resolves to no role at all (no role claim and both
`roles.default` and `roles.anonymous` empty) is refused with `403
role_not_allowed`.

The connection role needs `GRANT anonymous, app_user TO <connection role>`
(or be superuser) so `SET LOCAL ROLE` succeeds; a role that exists but was
never granted fails inside PostgreSQL and surfaces as a `403`
(`code: pg:42501`).

### Restricting assumable roles

By default any role named in the role claim may be assumed (the database
still rejects roles not granted to the connection role). Set
`roles.allowed` to pin the set explicitly:

```yaml
roles:
  allowed: [app_user, app_admin]
```

A request whose role claim resolves to anything else is rejected with a
`403` problem, `code: role_not_allowed` — the credential itself is valid, the
caller is just not permitted that role (pdbq answers `401` here; pdbr
deliberately uses `403`). `roles.default` and `roles.anonymous` are always
allowed — the operator configured those deliberately.

Leaving `roles.allowed` empty in `jwt` or `headers` mode means a client that
controls its claims can name any role granted to the connection role —
including privileged ones. pdbr logs a startup warning in that
configuration; set the list explicitly in production.

## Example: roles, grants, policies

From the shared fixture (`pdbcore/testutil`, `FixtureSQL`):

```sql
CREATE ROLE anonymous NOLOGIN;
CREATE ROLE app_user NOLOGIN;

GRANT USAGE ON SCHEMA public TO anonymous, app_user;
GRANT SELECT ON users, posts, places TO anonymous;
GRANT SELECT, INSERT, UPDATE, DELETE ON users, posts, places TO app_user;

ALTER TABLE posts ENABLE ROW LEVEL SECURITY;
CREATE POLICY posts_public ON posts FOR SELECT
    USING (published
           OR author_id = NULLIF(current_setting('jwt.claims.user_id', true), '')::integer);
CREATE POLICY posts_own_writes ON posts FOR ALL TO app_user
    USING (author_id = NULLIF(current_setting('jwt.claims.user_id', true), '')::integer)
    WITH CHECK (author_id = NULLIF(current_setting('jwt.claims.user_id', true), '')::integer);
```

Against this schema, with a token `{"role":"app_user","user_id":1,"exp":…}`:

```
GET /posts                       → published posts + user 1's drafts
GET /posts?author_id=eq.2        → only user 2's published posts
POST /posts {"author_id":2,…}    → 403 (WITH CHECK fails, SQLSTATE 42501)
```

With `roles.anonymous: anonymous`, the same requests without a token run as
`anonymous` (without it they are `401 auth_required`): only published rows,
and every write is `403` because `anonymous` has no `INSERT` grant. The
privilege check also feeds route generation: a resource whose *connection*
role lacks `INSERT` gets no `POST` route at all (`405` + `Allow`), whereas a
grant missing only for the *switched* role is discovered at execution time
and answered with `403`.

The e2e RLS matrix (anonymous sees 3 posts, `app_user` #1 sees 4,
`app_user` #2 sees 3) is the acceptance test for this section.

## Mutations, `RETURNING`, and column privileges

PostgreSQL evaluates `RETURNING` with the same privilege rules as a `SELECT`:
each projected column needs a column-level `SELECT` grant, and (with RLS
enabled) each returned row must pass the table's `SELECT` policies. Three
consequences for generated mutations:

- **Column-level grants.** pdbr compiles a mutation's `RETURNING` list from
  the request's `select=` (default `*` → every column), so with an explicit
  `select=` it only names the columns the response actually needs. A role
  granted `SELECT` on a subset of columns can therefore run
  `POST`/`PATCH`/`PUT`/`DELETE` as long as its `select=` stays within that
  subset. The exception is selecting a **computed column**: the underlying
  function takes the whole row type, so the statement must return every
  column and the role needs `SELECT` on all of them. An embed that leads
  back to the mutated table (`POST /posts?select=id,users(posts(id))`)
  reads a visibility CTE merging the table with the mutation's own rows; it
  projects the columns those embeds read, plus, for bulk `PATCH`/`DELETE`
  and bulk upserts, the primary-key (or other `NOT NULL` unique key)
  columns that identify the written rows, and `RETURNING` widens to match.
  Its table half runs under the role's policies, so rows the `SELECT`
  policies hide stay hidden, and rows an `UPDATE`/`DELETE` policy skipped
  keep their old version
  ([mutations.md](mutations.md#embeds-back-to-the-mutated-table)).

- **ETags hash every exposed column.** A single-row `GET`, and
  `PATCH`/`DELETE` with `If-Match`, compute
  `md5(to_jsonb(ROW(<exposed columns>))::text)` in SQL, independent of
  `select=` (columns hidden with `@omit` / `@omit read` are left out). Under
  column-level grants that expression fails with `42501` → `403` when the
  role cannot read an exposed column, even when the selected columns are
  readable. Grant `SELECT` on the exposed columns, hide the others with
  `@omit read`, or set `server.etags: false` for such deployments (`If-Match`
  is then ignored and no `ETag` header is sent).

- **`SELECT` policies apply to rows returned by the mutation itself.** This
  matters for nested creates (the `nested_mutations` plugin): all inserts run
  in one statement, and rows inserted by an earlier CTE are not visible to a
  policy's subquery in the same snapshot. A child-table `SELECT` policy that
  `EXISTS`-checks its parent row can never pass when the parent was created
  in the same statement — the insert succeeds but the `RETURNING` row is
  filtered, which PostgreSQL reports as an error. Write such policies against
  values that don't require same-statement visibility (e.g. compare the FK
  column to a claim setting directly), or split the creates into separate
  requests.

- **Nested operations run under the same policies.** A nested `connect`,
  `update`, `delete` or `disconnect` whose row the role's `UPDATE` or
  `DELETE` policy (or, for a forward `connect`, `SELECT` policy) filters out
  matches nothing: `connect`, `update` and `delete` then fail and roll back
  the whole request (`404 not_found`, `nested "…": matched 0 rows`), a
  filtered `disconnect` leaves the row as it was. A `WITH CHECK` failure is
  `403 pg:42501`. A `PATCH` whose body only carries nested keys still locks
  its row with `SELECT … FOR UPDATE`, so it needs the `UPDATE` privilege and
  passes the `UPDATE` policies like any update.

## Minting JWTs from a function (`auth.mint_type`)

Set `auth.mint_type` to a schema-qualified composite type (e.g.
`public.jwt`); any function returning it yields a signed HS256 token string
instead of the composite object. The composite's fields become the claims,
and an `exp` field becomes the token expiry. Requires `auth.jwt.secret` or
`auth.jwt.secret_file`; `auth.jwt.issuer` / `auth.jwt.audience` are
embedded when set. This is
`pdbcore/jwtmint`, shared with pdbq.

```sql
CREATE TYPE jwt AS (exp bigint, user_id integer, role text);

CREATE FUNCTION authenticate(user_email text) RETURNS jwt
VOLATILE LANGUAGE sql AS $$
    SELECT (extract(epoch FROM now())::bigint + 3600, id, 'app_user')::jwt
    FROM users WHERE email = user_email;
$$;
```

```yaml
auth:
  mode: jwt
  jwt:
    secret_file: /run/secrets/pdbr_jwt
  mint_type: public.jwt
roles:
  anonymous: anonymous
  allowed: [app_user]
```

```console
$ curl -s -X POST localhost:8080/rpc/authenticate \
    -H 'content-type: application/json' \
    -d '{"user_email":"ada@example.com"}'
"eyJhbGciOiJIUzI1NiIsInR5cCI6IkpXVCJ9.…"

$ curl -s localhost:8080/posts -H "Authorization: Bearer eyJ…"
```

`authenticate` is volatile, so it is `POST`-only (`GET` → `405`). A function
returning `NULL` (unknown email) yields JSON `null` — no token, `200`.
`SETOF jwt` functions return an array of tokens. Minting happens in Go on the
function's JSON result.

Settled (M4): OpenAPI describes minted RPC responses as `string`
(`format: jwt`, array thereof for `SETOF`) rather than the composite schema;
the composite itself is still emitted under `components.schemas` since other
functions may return it. The api model marks such functions `RPC.Mint`; the
compiler emits the composite raw (`to_jsonb`) so `exp` stays a JSON number,
and `exec` signs it with `pdbcore/jwtmint` after unwrapping.

## Running as the connection role (`roles.switch: false`)

`roles.switch: false` runs every request as the connection role: no
`SET LOCAL ROLE`, so row-level security policies written for request roles
do not apply (policies on the connection role still do). Credentials are
still verified and `roles.allowed` still enforced, and the claims settings
are still set, so functions and policies reading claims keep working. pdbr
logs a loud warning at startup. Fine for local development (the `pdbr
config init` starter and the dev compose stack use it with `auth.mode:
none`), never for production.

## Startup checks and warnings

pdbr refuses to start (and `pdbr config validate` fails) when `auth.mode`
is `jwt` without a secret, secret file or JWKS URL, `headers` without
`auth.headers.trusted_proxies`, or `none` while `roles.switch` is on
without `roles.anonymous`; when `auth.jwt.secret_file` cannot be read; and
when `auth.mint_type` is set without an HMAC secret (minting signs with
it; JWKS keys are verify-only). See
[configuration.md](configuration.md#validation-rules).

It logs at `warn` on start when:

- `roles.switch` is off (loud);
- `auth.mode` is `jwt` or `headers`, `roles.switch` is on and
  `roles.allowed` is empty.

## Related

- [smart-comments.md](smart-comments.md) — `@omit create|update|delete` removes
  methods regardless of grants.
- [plugins.md](plugins.md) — `exec.Operation.Claims` / `.Role` let a
  `RequestHook` inspect or veto the resolved identity.
