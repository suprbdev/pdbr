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
__vis_3 AS (
  SELECT * FROM __c_1 UNION ALL SELECT * FROM __c_2
)
SELECT jsonb_build_object('row', jsonb_build_object('id', t0."id", 'email', t0."email", 'posts', l3.data), 'key', jsonb_build_array(t0."id")) AS data
FROM __mut t0
LEFT JOIN LATERAL (
  SELECT coalesce(jsonb_agg(s2.data), '[]'::jsonb) AS data
  FROM (
    SELECT jsonb_build_object('id', t1."id", 'title', t1."title") AS data
    FROM __vis_3 t1
    WHERE t1."author_id" = t0."id"
    ORDER BY t1."id" ASC
    LIMIT $5
  ) s2
) l3 ON true
-- args: ["new@b.c","One","Two","b",100]
