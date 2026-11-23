-- request: PATCH /api/posts/1?select=id,title,users(id,full_name)
WITH __p_1 AS (
  UPDATE "public"."users"
  SET "full_name" = $3
  WHERE EXISTS (SELECT 1 FROM "public"."posts" __o1 WHERE __o1."id" = $1 AND __o1."author_id" = "public"."users"."id")
  RETURNING "public"."users".*
),
__mut AS (
  UPDATE "public"."posts"
  SET "title" = $2
  WHERE "public"."posts"."id" = $1
  RETURNING "id", "author_id", "title"
),
__vis_1 AS NOT MATERIALIZED (
  SELECT "id", "full_name" FROM "public"."users" WHERE NOT EXISTS (SELECT 1 FROM __p_1 __m WHERE __m."id" = "public"."users"."id")
  UNION ALL SELECT "id", "full_name" FROM __p_1
),
__guard AS MATERIALIZED (
  SELECT (SELECT CASE WHEN count(*) > 0 THEN true ELSE concat('pdbr guard: row of "posts": matched ', count(*), ' rows')::boolean END FROM __mut)
    AND (SELECT CASE WHEN count(*) > 0 THEN true ELSE concat('pdbr guard: nested "users.update": matched ', count(*), ' rows')::boolean END FROM __p_1) AS ok
)
SELECT __guarded.data
FROM __guard
LEFT JOIN LATERAL (
  SELECT json_build_object('id', t0."id", 'title', t0."title", 'users', l2.data) AS data
  FROM __mut t0
  LEFT JOIN LATERAL (
    SELECT json_build_object('id', t1."id", 'full_name', t1."full_name") AS data
    FROM __vis_1 t1
    WHERE t1."id" = t0."author_id"
    LIMIT 1
  ) l2 ON true
) __guarded ON true
WHERE __guard.ok
-- args: [1,"Engine notes","Ada L."]
