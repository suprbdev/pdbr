-- request: PUT /api/memberships/7,eng
WITH __mut AS (
  INSERT INTO "public"."memberships" ("user_id", "team", "role")
  VALUES ($1, $2, $3)
  ON CONFLICT ("user_id", "team") DO UPDATE
  SET "role" = EXCLUDED."role"
  RETURNING "user_id", "team", "role", (xmax = 0) AS __inserted
)
SELECT json_build_object('row', json_build_object('user_id', t0."user_id", 'team', t0."team", 'role', t0."role"), 'inserted', t0.__inserted) AS data
FROM __mut t0
-- args: [7,"eng","admin"]
