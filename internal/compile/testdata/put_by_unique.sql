-- request: PUT /api/users/by/email/a%40b.c?select=id,full_name
WITH __mut AS (
  INSERT INTO "public"."users" ("email", "full_name")
  VALUES ($1, $2)
  ON CONFLICT ("email") DO UPDATE
  SET "full_name" = EXCLUDED."full_name"
  RETURNING "id", "full_name", (xmax = 0) AS __inserted
)
SELECT json_build_object('inserted', t0.__inserted, 'key', jsonb_build_array(t0."id"), 'row', json_build_object('id', t0."id", 'full_name', t0."full_name")) AS data
FROM __mut t0
-- args: ["a@b.c","Ada"]
