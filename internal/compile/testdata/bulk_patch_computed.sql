-- request: PATCH /api/users?post_count=gt.5&select=id
WITH __mut AS (
  UPDATE "public"."users" m0
  SET "mood" = $2
  WHERE "public"."users_post_count"(ROW(m0."id", m0."email", m0."full_name", m0."mood", m0."settings", m0."tags", m0."balance", m0."created_at", m0."address", m0."prev_addresses", m0."moods")::"public"."users") > $1
  RETURNING "id"
)
SELECT jsonb_build_object('n', count(*), 'rows', coalesce(jsonb_agg(s2.data), '[]'::jsonb)) AS data
FROM (
  SELECT jsonb_build_object('id', t1."id") AS data
  FROM __mut t1
) s2
-- args: [5,"ok"]
