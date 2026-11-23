-- request: POST /api/users?select=id,posts(id,users(email,posts(id)))
WITH __mut AS (
  INSERT INTO "public"."users" ("email")
  VALUES ($1)
  RETURNING "id", "email"
),
__c_1 AS (
  INSERT INTO "public"."posts" ("title", "author_id")
  SELECT $2, __mut."id"
  FROM __mut
  RETURNING *
),
__vis_1 AS NOT MATERIALIZED (
  SELECT "id", "author_id" FROM "public"."posts"
  UNION ALL SELECT "id", "author_id" FROM __c_1
),
__vis AS NOT MATERIALIZED (
  SELECT "id", "email" FROM "public"."users"
  UNION ALL SELECT __mut."id", __mut."email" FROM __mut
)
SELECT json_build_object('key', jsonb_build_array(t0."id"), 'row', json_build_object('id', t0."id", 'posts', l8.data)) AS data
FROM __mut t0
LEFT JOIN LATERAL (
  SELECT coalesce(json_agg(s7.data), '[]'::json) AS data
  FROM (
    SELECT json_build_object('id', t1."id", 'users', l6.data) AS data
    FROM __vis_1 t1
    LEFT JOIN LATERAL (
      SELECT json_build_object('email', t2."email", 'posts', l5.data) AS data
      FROM __vis t2
      LEFT JOIN LATERAL (
        SELECT coalesce(json_agg(s4.data), '[]'::json) AS data
        FROM (
          SELECT json_build_object('id', t3."id") AS data
          FROM __vis_1 t3
          WHERE t3."author_id" = t2."id"
          ORDER BY t3."id" ASC
          LIMIT 100
        ) s4
      ) l5 ON true
      WHERE t2."id" = t1."author_id"
      LIMIT 1
    ) l6 ON true
    WHERE t1."author_id" = t0."id"
    ORDER BY t1."id" ASC
    LIMIT 100
  ) s7
) l8 ON true
-- args: ["n@b.c","One"]
