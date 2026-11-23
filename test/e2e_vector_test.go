package e2e

import (
	"context"
	"encoding/json"
	"net/url"
	"os"
	"reflect"
	"strconv"
	"strings"
	"testing"

	"github.com/jackc/pgx/v5"

	"github.com/suprbdev/pdbr/internal/config"
)

// The pgvector suite installs the vector extension into vectorExtSchema
// (off the search_path) and skips when the server does not have pgvector.
const (
	vectorSchema    = "pdbr_vector"
	vectorExtSchema = "pdbr_vector_ext"
)

// vectorDDL: items.emb and items.half carry HNSW indexes (the vector
// distance terms are orderable under filters.indexed_only), items.sparse has
// none; chunks hang off items for embed ordering.
const vectorDDL = `
CREATE TABLE pdbr_vector.items (
  id     int PRIMARY KEY,
  title  text,
  emb    pdbr_vector_ext.vector(3),
  half   pdbr_vector_ext.halfvec(3),
  sparse pdbr_vector_ext.sparsevec(5)
);
CREATE INDEX items_emb_idx ON pdbr_vector.items USING hnsw (emb pdbr_vector_ext.vector_l2_ops);
CREATE INDEX items_half_idx ON pdbr_vector.items USING hnsw (half pdbr_vector_ext.halfvec_cosine_ops);
INSERT INTO pdbr_vector.items VALUES
  (1, 'x',  '[1,0,0]', '[1,0,0]', '{1:1}/5'),
  (2, 'y',  '[0,1,0]', '[0,1,0]', '{2:1}/5'),
  (3, 'z',  '[0,0,1]', '[0,0,1]', '{3:1}/5'),
  (4, 'xy', '[1,1,0]', '[1,1,0]', '{1:1,2:1}/5'),
  (5, 'none', NULL, NULL, NULL);
CREATE TABLE pdbr_vector.chunks (
  id      int PRIMARY KEY,
  item_id int REFERENCES pdbr_vector.items (id),
  emb     pdbr_vector_ext.vector(3)
);
CREATE INDEX chunks_item_idx ON pdbr_vector.chunks (item_id);
CREATE INDEX chunks_emb_idx ON pdbr_vector.chunks USING hnsw (emb pdbr_vector_ext.vector_cosine_ops);
INSERT INTO pdbr_vector.chunks VALUES (1, 1, '[0,1,0]'), (2, 1, '[1,0,0]'), (3, 1, '[0,0,1]');
CREATE FUNCTION pdbr_vector.twice(v pdbr_vector_ext.vector) RETURNS pdbr_vector_ext.vector
LANGUAGE sql IMMUTABLE SET search_path = pdbr_vector_ext, pg_catalog AS $$ SELECT v + v $$;
CREATE FUNCTION pdbr_vector.ndims(v pdbr_vector_ext.sparsevec) RETURNS int
LANGUAGE sql IMMUTABLE SET search_path = pdbr_vector_ext, pg_catalog AS $$ SELECT (regexp_match(v::text, '/(\d+)$'))[1]::int $$;
`

func vectorServer(t *testing.T, mutate func(*config.Config)) string {
	t.Helper()
	return extensionServer(t, vectorSchema, vectorExtSchema, []string{"vector"}, vectorDDL, mutate)
}

// vectorAtLeast reports whether the installed pgvector version is at least
// major.minor.
func vectorAtLeast(t *testing.T, major, minor int) bool {
	t.Helper()
	ctx := context.Background()
	conn, err := pgx.Connect(ctx, os.Getenv("PDBR_TEST_DATABASE_URL"))
	if err != nil {
		t.Fatal(err)
	}
	defer conn.Close(ctx)
	var v string
	if err := conn.QueryRow(ctx, `SELECT extversion FROM pg_extension WHERE extname = 'vector'`).Scan(&v); err != nil {
		t.Fatal(err)
	}
	parts := strings.SplitN(v, ".", 3)
	if len(parts) < 2 {
		return true
	}
	maj, _ := strconv.Atoi(parts[0])
	mnr, _ := strconv.Atoi(parts[1])
	return maj > major || maj == major && mnr >= minor
}

// TestVectorOrder: every distance term orders nearest first against vec=
// (JSON array, raw or URL-encoded, or the text form), in the root and in
// embeds; keyset cursors walk the same order; a missing or stray vec= and a
// malformed vector are 400s. An HNSW index scan skips rows whose vector is
// NULL, so the NULL row (5) is only expected where no index serves the
// order (sparse); the indexed cases filter it out.
func TestVectorOrder(t *testing.T) {
	srv := vectorServer(t, func(cfg *config.Config) { cfg.Filters.IndexedOnly = false })
	q := url.QueryEscape("[1,0.1,0]")
	sq := url.QueryEscape("[1,0.1,0,0,0]")
	cases := map[string][]int{
		"emb.vec_l2":      {1, 4, 2, 3},
		"emb.vec_cos":     {1, 4, 2, 3},
		"emb.vec_ip":      {4, 1, 2, 3},
		"half.vec_l2":     {1, 4, 2, 3},
		"half.vec_cos":    {1, 4, 2, 3},
		"emb.vec_l2.desc": {3, 2, 4, 1},
		"sparse.vec_l2":   {1, 4, 2, 3, 5},
		"sparse.vec_ip":   {4, 1, 2, 3, 5},
		"sparse.vec_cos":  {1, 4, 2, 3, 5},
	}
	if vectorAtLeast(t, 0, 7) {
		cases["emb.vec_l1"] = []int{1, 4, 2, 3}
		cases["sparse.vec_l1"] = []int{1, 4, 2, 3, 5}
	}
	query := func(order string) string {
		if strings.HasPrefix(order, "sparse") {
			return "/api/items?select=id&order=" + order + "&vec=" + sq
		}
		return "/api/items?select=id&id=lt.5&order=" + order + "&vec=" + q
	}
	var rows []row
	for order, want := range cases {
		getJSON(t, srv+query(order), &rows)
		if got := ids(rows, "id"); !eq(got, want) {
			t.Errorf("order=%s = %v, want %v", order, got, want)
		}
		var walked []int
		next := srv + query(order) + "&limit=2"
		for range 5 {
			res := getJSON(t, next, &rows)
			walked = append(walked, ids(rows, "id")...)
			cursor := res.Header.Get("X-Next-Cursor")
			if cursor == "" {
				break
			}
			next = srv + query(order) + "&limit=2&after=" + cursor
		}
		if !eq(walked, want) {
			t.Errorf("keyset walk by %s = %v, want %v", order, walked, want)
		}
	}
	for _, vec := range []string{"[1,0.1,0]", url.QueryEscape("[1, 0.1, 0]")} {
		getJSON(t, srv+"/api/items?select=id&id=lt.5&order=emb.vec_cos&vec="+vec, &rows)
		if !eq(ids(rows, "id"), []int{1, 4, 2, 3}) {
			t.Errorf("vec=%s = %v", vec, ids(rows, "id"))
		}
	}
	getJSON(t, srv+"/api/items?select=id&order=sparse.vec_ip&vec="+url.QueryEscape("{2:1}/5"), &rows)
	if got := ids(rows, "id"); !eq(got, []int{2, 4, 1, 3, 5}) {
		t.Errorf("sparse text vec order = %v", got)
	}

	getJSON(t, srv+"/api/items?select=id,chunks(id)&id=eq.1&chunks.order=emb.vec_l2&vec="+url.QueryEscape("[0,0,1]"), &rows)
	if ch, _ := rows[0]["chunks"].([]any); len(ch) != 3 || ch[0].(map[string]any)["id"] != float64(3) {
		t.Errorf("embed vector order = %v", rows)
	}

	for path, code := range map[string]string{
		"/api/items?order=emb.vec_l2":                                     "invalid_filter",
		"/api/items?order=id&vec=" + q:                                    "invalid_filter",
		"/api/items?order=emb.vec_l2&vec=" + url.QueryEscape(`[1,"a",0]`): "invalid_filter",
		"/api/items?order=emb.vec_l2&vec=" + url.QueryEscape("[1,0]"):     "pg:22000",
		"/api/items?order=emb.vec_l2&vec=" + url.QueryEscape("nope"):      "pg:22P02",
		"/api/items?order=emb":                                            "column_not_orderable",
		"/api/items?emb=eq." + q:                                          "invalid_filter",
	} {
		res, out := get(t, srv+path)
		wantProblem(t, res, out, 400, code)
	}
	getJSON(t, srv+"/api/items?select=id&emb=is.null", &rows)
	if !eq(ids(rows, "id"), []int{5}) {
		t.Errorf("emb=is.null = %v", ids(rows, "id"))
	}

	indexed := vectorServer(t, nil)
	getJSON(t, indexed+"/api/items?select=id&id=lt.5&order=emb.vec_l2&vec="+q, &rows)
	if !eq(ids(rows, "id"), []int{1, 4, 2, 3}) {
		t.Errorf("indexed emb.vec_l2 = %v", ids(rows, "id"))
	}
	for _, order := range []string{"emb.vec_cos", "sparse.vec_l2"} {
		vec := q
		if strings.HasPrefix(order, "sparse") {
			vec = sq
		}
		res, out := get(t, indexed+"/api/items?order="+order+"&vec="+vec)
		wantProblem(t, res, out, 400, "column_not_orderable")
	}
}

// TestVectorValues: vector and halfvec values come out as JSON arrays of
// numbers and sparsevec as text, in JSON and CSV; bodies and RPC arguments
// take a list or the text form; bad vectors are 400s.
func TestVectorValues(t *testing.T) {
	srv := vectorServer(t, nil)
	var created row
	sendJSON(t, "POST", srv+"/api/items?select=id,title,emb,half,sparse", `{"id": 10, "title": "new", "emb": [0.5, -0.25, 3], "half": "[1,2,3]", "sparse": [0, 1.5, 0, 0, 2]}`, 201, &created)
	want := row{"id": float64(10), "title": "new", "emb": []any{0.5, -0.25, float64(3)}, "half": []any{float64(1), float64(2), float64(3)}, "sparse": "{2:1.5,5:2}/5"}
	if !reflect.DeepEqual(created, want) {
		t.Errorf("created = %v, want %v", created, want)
	}
	var rows []row
	getJSON(t, srv+"/api/items?id=eq.10", &rows)
	if len(rows) != 1 || !reflect.DeepEqual(rows[0], want) {
		t.Errorf("GET = %v, want %v", rows, want)
	}
	sendJSON(t, "POST", srv+"/api/items", `[{"id": 11, "emb": "[1,1,1]"}, {"id": 12, "emb": [2, 2, 2], "sparse": "{1:3}/5"}]`, 201, nil)
	var one row
	sendJSON(t, "PATCH", srv+"/api/items/11", `{"emb": null, "half": [0, 0, 1]}`, 200, &one)
	if one["emb"] != nil || !reflect.DeepEqual(one["half"], []any{float64(0), float64(0), float64(1)}) {
		t.Errorf("PATCH = %v", one)
	}

	res, out := do(t, "GET", srv+"/api/items?select=id,emb,sparse&id=eq.1", map[string]string{"Accept": "text/csv"})
	if res.StatusCode != 200 || !strings.Contains(string(out), `1,"[1,0,0]",{1:1}/5`) {
		t.Errorf("CSV = %d %q", res.StatusCode, out)
	}

	for body, code := range map[string]string{
		`{"id": 20, "emb": [1, "x", 0]}`: "invalid_body",
		`{"id": 20, "emb": []}`:          "invalid_body",
		`{"id": 20, "emb": {"a": 1}}`:    "invalid_body",
		`{"id": 20, "emb": [1, 2]}`:      "pg:22000",
		`{"id": 20, "emb": "nope"}`:      "pg:22P02",
	} {
		res, out := send(t, "POST", srv+"/api/items", body, nil)
		wantProblem(t, res, out, 400, code)
	}

	var got any
	sendJSON(t, "POST", srv+"/api/rpc/twice", `{"v": [1, 2.5, -1]}`, 200, &got)
	if !reflect.DeepEqual(got, []any{float64(2), float64(5), float64(-2)}) {
		t.Errorf("POST rpc twice = %v", got)
	}
	getJSON(t, srv+"/api/rpc/twice?v="+url.QueryEscape("[1,2,3]"), &got)
	if !reflect.DeepEqual(got, []any{float64(2), float64(4), float64(6)}) {
		t.Errorf("GET rpc twice = %v", got)
	}
	sendJSON(t, "POST", srv+"/api/rpc/ndims", `{"v": [0, 0, 1, 0]}`, 200, &got)
	if got != float64(4) {
		t.Errorf("rpc ndims = %v", got)
	}
}

// TestVectorOpenAPI: the document validates, vectors are arrays of numbers
// (sparsevec text), bodies take either form, and vec= plus the distance
// terms are documented.
func TestVectorOpenAPI(t *testing.T) {
	srv := vectorServer(t, nil)
	body, doc := fetchOpenAPI(t, srv)
	validateOpenAPI(t, oasSchema(t), body)
	props := componentSchema(doc, "items")["properties"].(map[string]any)
	emb, _ := json.Marshal(props["emb"])
	if !strings.Contains(string(emb), `"array"`) || !strings.Contains(string(emb), `"number"`) {
		t.Errorf("items.emb = %s", emb)
	}
	sparse, _ := json.Marshal(props["sparse"])
	if strings.Contains(string(sparse), `"array"`) {
		t.Errorf("items.sparse = %s", sparse)
	}
	in, _ := json.Marshal(componentSchema(doc, "items.insert")["properties"].(map[string]any)["emb"])
	if !strings.Contains(string(in), "oneOf") {
		t.Errorf("items.insert emb = %s", in)
	}
	params := opParams(pathOp(doc, "/items", "get"))
	if params["query:vec"] == nil {
		t.Error("vec param missing")
	}
	order, _ := params["query:order"]["description"].(string)
	if !strings.Contains(order, "emb.vec_l2") || !strings.Contains(order, "half.vec_cos") || strings.Contains(order, "sparse.vec") {
		t.Errorf("order description = %s", order)
	}
	if ops, _ := params["query:emb"]["x-pdbr-filter-ops"].([]any); len(ops) != 1 || ops[0] != "is" {
		t.Errorf("emb ops = %v", ops)
	}
}
