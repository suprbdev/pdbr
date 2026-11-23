-- request: POST /api/users?on_conflict=email&select=id,email,full_name
WITH __mut AS (
  INSERT INTO "public"."users" ("email", "full_name")
  VALUES ($1, $2)
  ON CONFLICT ("email") DO UPDATE
  SET "full_name" = EXCLUDED."full_name"
  RETURNING "id", "email", "full_name"
)
SELECT json_build_object('row', json_build_object('id', t0."id", 'email', t0."email", 'full_name', t0."full_name"), 'key', jsonb_build_array(t0."id")) AS data
FROM __mut t0
-- args: ["a@b.c","Ada"]
