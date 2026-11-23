# RLS & authentication

> **Status: as built.** Verified by the RLS matrices in `test/e2e_test.go`
> (reads, `allowed_roles`), `test/e2e_mutations_test.go` (writes, column
> grants) and `test/e2e_rpc_test.go` (JWT mint round trip); the auth code is
> `pdbcore/auth`, the transaction identity code `pdbcore/pgexec`.

RLS support is **on by default**. Whenever a request resolves to a role or
carries claims (under `rls.enabled` that is every request, since anonymous
requests still switch to `rls.anonymous_role`), the statement runs inside a
transaction that:

1. `SET LOCAL ROLE "<role>"` — the role from the request's claims
   (`rls.role_claim`, default claim `role`), falling back to
   `rls.default_role`, then `rls.anonymous_role` for unauthenticated
   requests. A role must match `[A-Za-z0-9_]+` and must not be `none`,
   `public` or a `pg_*` role, in any letter case: the configured roles are
   checked at startup, and a credential naming such a role is rejected with
   `403 role_not_allowed` before any SQL runs (`none` would otherwise reset
   the role to the connection role and bypass RLS).
2. `SELECT set_config('<rls.claims_prefix>.<key>', $n, true)` for every
   claim whose key matches `[A-Za-z0-9_]+` (others are skipped). String
   claims are set verbatim; any other JSON value is set as its JSON text.
   With the default prefix, policies read them via
   `current_setting('pdbq.claims.user_id', true)`.

Only `SET LOCAL` / transaction-scoped `set_config` are ever used, so pooled
connections cannot leak one request's identity into another. Reads that
resolve to no role and no claims (only possible with `rls.enabled: false` or
an empty `anonymous_role`) run outside a transaction; mutations are wrapped
according to `transactions.mutations` regardless. There is one operation per
HTTP request, so pdbr has no `transactions.per_request` knob.

## Claims prefix: `pdbq.claims` by default

`rls.claims_prefix` defaults to **`pdbq.claims`**, not `pdbr.claims`. This is
deliberate: pdbr and pdbq are meant to sit in front of the same database, and
policies are written once, in SQL, against the setting name. Keeping the
prefix identical means every policy, function, and view written for pdbq
works unchanged with pdbr (and the reverse). Set the prefix explicitly if you
want a product-specific namespace, in which case both products need the same
override.

## Claim sources (`rls.auth.mode`)

- **`jwt`** (default): `Authorization: Bearer <token>`, HMAC-verified with
  `rls.auth.jwt_secret` (HS256/384/512); optional `jwt_issuer` /
  `jwt_audience` checks. Tokens must carry an `exp` claim — tokens without
  one are rejected, so a stolen token cannot stay valid forever. A missing
  token is anonymous; an invalid token is a `401` problem with
  `code: invalid_token`.

  Set `rls.auth.jwks_url` for asymmetric verification instead (RS256/384/512,
  ES256/384/512): keys are fetched from the JWKS endpoint, matched by `kid`,
  and cached for `rls.auth.jwks_cache_ttl` (default 1h; an unknown `kid`
  refreshes early, so key rotation just works). In JWKS mode the HMAC
  algorithms are off the allowlist entirely — no downgrade to
  HMAC-with-public-key.
- **`headers`**: for deployments behind a trusted gateway that has already
  authenticated the caller. Headers matching `rls.auth.header_prefix`
  (default `X-Pdbr-Claim-`) become claims: `X-Pdbr-Claim-User-Id: 42` →
  claim `user_id = "42"` → `pdbq.claims.user_id = '42'`. The role comes from
  the header named after `rls.role_claim` (`X-Pdbr-Claim-Role`).

  Set `rls.auth.trusted_proxies` to the gateway's CIDR blocks (or single
  IPs), e.g. `["10.0.0.0/8", "192.168.1.5"]`: claim headers arriving from
  any other peer are rejected. Left empty, **every** peer that can reach the
  port can forge claims and roles — pdbr logs a startup warning; only do
  this when the network layer guarantees that nothing but the gateway can
  connect.
- **`none`**: every request is anonymous. `pdbr request` (the CLI) forces
  this mode, so a request run from the shell always executes as
  `rls.anonymous_role` — see [cli.md](cli.md).

The verification code (`Authenticator`, JWKS cache, trusted-proxy check,
role resolution) lives in `pdbcore/auth` and is shared with pdbq; the
configuration keys are the same, only the header prefix default differs.

## Role resolution

| Precedence | Source |
|---|---|
| 1 | the `rls.role_claim` claim (default `role`) of a verified credential |
| 2 | `rls.default_role` — authenticated request without a role claim |
| 3 | `rls.anonymous_role` (default `anonymous`) — no credential |

The connection role needs `GRANT anonymous, app_user TO <connection role>`
(or be superuser) so `SET LOCAL ROLE` succeeds; a role that exists but was
never granted fails inside PostgreSQL and surfaces as a `403`
(`code: pg:42501`).

### Restricting assumable roles

By default any role named in the role claim may be assumed (the database
still rejects roles not granted to the connection role). Set
`rls.allowed_roles` to pin the set explicitly:

```yaml
rls:
  allowed_roles: [app_user, app_admin]
```

A request whose role claim resolves to anything else is rejected with a
`403` problem, `code: role_not_allowed` — the credential itself is valid, the
caller is just not permitted that role (pdbq answers `401` here; pdbr
deliberately uses `403`). `rls.default_role` and `rls.anonymous_role` are
always allowed — the operator configured those deliberately.

Leaving `allowed_roles` empty in `jwt` or `headers` mode means a client that
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
           OR author_id = NULLIF(current_setting('pdbq.claims.user_id', true), '')::integer);
CREATE POLICY posts_own_writes ON posts FOR ALL TO app_user
    USING (author_id = NULLIF(current_setting('pdbq.claims.user_id', true), '')::integer)
    WITH CHECK (author_id = NULLIF(current_setting('pdbq.claims.user_id', true), '')::integer);
```

Against this schema, with a token `{"role":"app_user","user_id":1,"exp":…}`:

```
GET /api/posts                       → published posts + user 1's drafts
GET /api/posts?author_id=eq.2        → only user 2's published posts
POST /api/posts {"author_id":2,…}    → 403 (WITH CHECK fails, SQLSTATE 42501)
```

Without a token the same requests run as `anonymous`: only published rows,
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
  column and the role needs `SELECT` on all of them.

- **ETags hash the whole row.** A single-row `GET`, and `PATCH`/`DELETE`
  with `If-Match`, compute `md5(to_jsonb(row)::text)` over **all** columns in
  SQL, independent of `select=`. Under column-level grants that expression
  fails with `42501` → `403` even when the selected columns are readable.
  Either grant `SELECT` on the full row or set `server.etags: false` for
  such deployments (`If-Match` is then ignored and no `ETag` header is sent).

- **`SELECT` policies apply to rows returned by the mutation itself.** This
  matters for nested creates (the `nested-mutations` plugin): all inserts run
  in one statement, and rows inserted by an earlier CTE are not visible to a
  policy's subquery in the same snapshot. A child-table `SELECT` policy that
  `EXISTS`-checks its parent row can never pass when the parent was created
  in the same statement — the insert succeeds but the `RETURNING` row is
  filtered, which PostgreSQL reports as an error. Write such policies against
  values that don't require same-statement visibility (e.g. compare the FK
  column to a claim setting directly), or split the creates into separate
  requests.

## Minting JWTs from a function (`rls.auth.jwt_type`)

Set `rls.auth.jwt_type` to a schema-qualified composite type (e.g.
`public.jwt`); any function returning it yields a signed HS256 token string
instead of the composite object. The composite's fields become the claims,
and an `exp` field becomes the token expiry. Requires `jwt_secret`;
`jwt_issuer` / `jwt_audience` are embedded when set. This is
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
rls:
  auth:
    mode: jwt
    jwt_secret: change-me
    jwt_type: public.jwt
```

```console
$ curl -s -X POST localhost:8080/api/rpc/authenticate \
    -H 'content-type: application/json' \
    -d '{"user_email":"ada@example.com"}'
"eyJhbGciOiJIUzI1NiIsInR5cCI6IkpXVCJ9.…"

$ curl -s localhost:8080/api/posts -H "Authorization: Bearer eyJ…"
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

## Turning it off

`rls.enabled: false` runs everything as the privileged connection role and
logs a loud warning at startup. Fine for local dev, never for production.

## Startup warnings

pdbr logs at `warn` on start when:

- `rls.enabled: false` (loud);
- `rls.auth.mode: headers` with empty `rls.auth.trusted_proxies`;
- `rls.auth.mode` is `jwt` or `headers` and `rls.allowed_roles` is empty.

`rls.auth.jwt_type` without `rls.auth.jwt_secret` is not a warning but a
configuration error (validation rule 5 in
[configuration.md](configuration.md#validation-rules)): minting signs with
the HMAC secret.

## Related

- [smart-comments.md](smart-comments.md) — `@omit create|update|delete` removes
  methods regardless of grants.
- [plugins.md](plugins.md) — `exec.Operation.Claims` / `.Role` let a
  `RequestHook` inspect or veto the resolved identity.
