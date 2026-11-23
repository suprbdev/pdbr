-- request: POST /api/users?on_conflict=email
WITH __mut AS (
  INSERT INTO "public"."users" ("email", "mood")
  VALUES ($1, DEFAULT), ($2, $3)
  ON CONFLICT ("email") DO NOTHING
  RETURNING 1
)
SELECT json_build_object('n', (SELECT count(*) FROM __mut)) AS data
-- args: ["a@b.c","d@e.f","ok"]
