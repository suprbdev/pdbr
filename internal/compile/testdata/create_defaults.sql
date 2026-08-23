-- request: POST /api/audit/log
WITH __mut AS (
  INSERT INTO "audit"."log" ("action")
  VALUES ($1)
  RETURNING "id", "action", "at"
)
SELECT jsonb_build_object('row', jsonb_build_object('id', (t0."id")::text, 'action', t0."action", 'at', t0."at"), 'key', jsonb_build_array(t0."id")) AS data
FROM __mut t0
-- args: ["login"]
