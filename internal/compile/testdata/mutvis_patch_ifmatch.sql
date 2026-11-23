-- request: PATCH /api/users/1?select=id,posts(id,users(full_name))
WITH cur AS (
  SELECT md5(to_jsonb(__cur)::text) AS h
  FROM "public"."users" __cur
  WHERE __cur."id" = $2
),
__mut AS (
  UPDATE "public"."users"
  SET "full_name" = $1
  WHERE "public"."users"."id" = $2
    AND (SELECT h FROM cur) = $3
  RETURNING "id", "full_name"
),
__vis AS NOT MATERIALIZED (
  SELECT "id", "full_name" FROM "public"."users" WHERE ("public"."users"."id" = $2) IS NOT TRUE
  UNION ALL SELECT __mut."id", __mut."full_name" FROM __mut
)
SELECT json_build_object('found', EXISTS (SELECT 1 FROM cur), 'row', (
  SELECT json_build_object('id', t0."id", 'posts', l5.data)
  FROM __mut t0
  LEFT JOIN LATERAL (
    SELECT coalesce(json_agg(s4.data), '[]'::json) AS data
    FROM (
      SELECT json_build_object('id', t1."id", 'users', l3.data) AS data
      FROM "public"."posts" t1
      LEFT JOIN LATERAL (
        SELECT json_build_object('full_name', t2."full_name") AS data
        FROM __vis t2
        WHERE t2."id" = t1."author_id"
        LIMIT 1
      ) l3 ON true
      WHERE t1."author_id" = t0."id"
      ORDER BY t1."id" ASC
      LIMIT $4
    ) s4
  ) l5 ON true
)) AS data
-- args: ["Grace",1,"5d41402abc4b2a76b9719d911017c592",100]
