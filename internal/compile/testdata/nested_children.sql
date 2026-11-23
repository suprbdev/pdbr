-- request: POST /api/users?select=id,email,posts(id,title)
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
__c_2 AS (
  INSERT INTO "public"."posts" ("title", "body", "author_id")
  SELECT $3, $4, __mut."id"
  FROM __mut
  RETURNING *
),
__vis_1 AS NOT MATERIALIZED (
  SELECT "id", "author_id", "title" FROM "public"."posts"
  UNION ALL SELECT "id", "author_id", "title" FROM __c_1
  UNION ALL SELECT "id", "author_id", "title" FROM __c_2
)
SELECT json_build_object('key', jsonb_build_array(t0."id"), 'row', json_build_object('id', t0."id", 'email', t0."email", 'posts', l3.data)) AS data
FROM __mut t0
LEFT JOIN LATERAL (
  SELECT coalesce(json_agg(s2.data), '[]'::json) AS data
  FROM (
    SELECT json_build_object('id', t1."id", 'title', t1."title") AS data
    FROM __vis_1 t1
    WHERE t1."author_id" = t0."id"
    ORDER BY t1."id" ASC
    LIMIT 100
  ) s2
) l3 ON true
-- args: ["new@b.c","One","Two","b"]
