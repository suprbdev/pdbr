-- request: POST /api/posts?select=id,title,users(id,email)
WITH __p_1 AS (
  INSERT INTO "public"."users" ("email", "full_name")
  VALUES ($1, $2)
  RETURNING *
),
__mut AS (
  INSERT INTO "public"."posts" ("title", "author_id")
  SELECT $3, __p_1."id"
  FROM __p_1
  RETURNING "id", "author_id", "title"
),
__vis_2 AS (
  SELECT * FROM __p_1
)
SELECT jsonb_build_object('row', jsonb_build_object('id', t0."id", 'title', t0."title", 'users', l2.data), 'key', jsonb_build_array(t0."id")) AS data
FROM __mut t0
LEFT JOIN LATERAL (
  SELECT jsonb_build_object('id', t1."id", 'email', t1."email") AS data
  FROM __vis_2 t1
  WHERE t1."id" = t0."author_id"
  LIMIT 1
) l2 ON true
-- args: ["new@b.c","New","Hello"]
