-- request: POST /api/users?select=id,mood,moods,tags,settings
WITH __mut AS (
  INSERT INTO "public"."users" ("email", "mood", "settings", "tags", "moods")
  VALUES ($1, $2, $3::jsonb, $4::"text"[], $5::text[]::"public"."mood"[])
  RETURNING "id", "mood", "settings", "tags", "moods"
)
SELECT json_build_object('row', json_build_object('id', t0."id", 'mood', t0."mood", 'moods', to_json(t0."moods"), 'tags', to_json(t0."tags"), 'settings', t0."settings"), 'key', jsonb_build_array(t0."id")) AS data
FROM __mut t0
-- args: ["a@b.c","happy","{\"theme\":\"dark\"}",["admin","founder"],["sad","happy"]]
