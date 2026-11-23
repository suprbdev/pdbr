-- request: PATCH /api/memberships?user_id=eq.1&select=user_id,users(memberships(team,role))
WITH __mut AS (
  UPDATE "public"."memberships" m0
  SET "user_id" = $2, "team" = $3
  WHERE m0."user_id" = $1
  RETURNING "user_id", "team", "role"
),
__vis AS NOT MATERIALIZED (
  SELECT "user_id", "team", "role" FROM "public"."memberships" m0 WHERE (m0."user_id" = $1) IS NOT TRUE
  UNION ALL SELECT __mut."user_id", __mut."team", __mut."role" FROM __mut
)
SELECT json_build_object('n', count(*), 'rows', coalesce(json_agg(s7.data), '[]'::json)) AS data
FROM (
  SELECT json_build_object('user_id', t1."user_id", 'users', l6.data) AS data
  FROM __mut t1
  LEFT JOIN LATERAL (
    SELECT json_build_object('memberships', l5.data) AS data
    FROM "public"."users" t2
    LEFT JOIN LATERAL (
      SELECT coalesce(json_agg(s4.data), '[]'::json) AS data
      FROM (
        SELECT json_build_object('team', t3."team", 'role', t3."role") AS data
        FROM __vis t3
        WHERE t3."user_id" = t2."id"
        ORDER BY t3."user_id" ASC, t3."team" ASC
        LIMIT 100
      ) s4
    ) l5 ON true
    WHERE t2."id" = t1."user_id"
    LIMIT 1
  ) l6 ON true
) s7
-- args: [1,2,"ops"]
