package e2e

import (
	"context"
	"math"
	"net/url"
	"os"
	"strings"
	"testing"

	"github.com/jackc/pgx/v5"

	"github.com/suprbdev/pdbr/internal/config"
)

// sqlScores runs q (id, value...) and returns id -> values (NaN for NULL).
func sqlScores(t *testing.T, q string, args ...any) map[int][]float64 {
	t.Helper()
	ctx := context.Background()
	conn, err := pgx.Connect(ctx, os.Getenv("PDBR_TEST_DATABASE_URL"))
	if err != nil {
		t.Fatal(err)
	}
	defer conn.Close(ctx)
	rows, err := conn.Query(ctx, q, args...)
	if err != nil {
		t.Fatalf("%s: %v", q, err)
	}
	defer rows.Close()
	out := map[int][]float64{}
	for rows.Next() {
		vals, err := rows.Values()
		if err != nil {
			t.Fatal(err)
		}
		id := int(vals[0].(int32))
		for _, v := range vals[1:] {
			f := math.NaN()
			switch x := v.(type) {
			case float64:
				f = x
			case float32:
				f = float64(x)
			}
			out[id] = append(out[id], f)
		}
	}
	if err := rows.Err(); err != nil {
		t.Fatal(err)
	}
	return out
}

// scoreOf reads a numeric response value, NaN for null.
func scoreOf(v any) float64 {
	if f, ok := v.(float64); ok {
		return f
	}
	return math.NaN()
}

func approxEqual(a, b float64) bool {
	if math.IsNaN(a) || math.IsNaN(b) {
		return math.IsNaN(a) && math.IsNaN(b)
	}
	return math.Abs(a-b) <= 1e-6*math.Max(1, math.Abs(b))
}

// checkScores compares the score keys of rows with the SQL values per id.
func checkScores(t *testing.T, rows []row, keys []string, want map[int][]float64) {
	t.Helper()
	if len(rows) == 0 {
		t.Fatal("no rows")
	}
	for _, r := range rows {
		id := int(r["id"].(float64))
		for i, k := range keys {
			if got := scoreOf(r[k]); !approxEqual(got, want[id][i]) {
				t.Errorf("row %d %s = %v, want %v", id, k, got, want[id][i])
			}
		}
	}
}

// TestDistanceScores selects ~<col>_st_distance against ~near= on the fixture's
// places (root, row path and embed) and compares it with PostGIS: SRID units
// on geometry, metres on geography and with spheroid=true.
func TestDistanceScores(t *testing.T) {
	srv := testServer(t, nil)
	const ref = "SRID=4326;POINT(-0.1 51.5)"
	want := sqlScores(t, `SELECT id, ST_Distance(location, ST_GeomFromEWKT($1)),
		ST_Distance(location::geography, ST_GeomFromEWKT($1)::geography),
		ST_Distance(area, ST_GeomFromEWKT($1)::geography) FROM places`, ref)
	var rows []row
	getJSON(t, srv.URL+"/api/places?select=id,~location_st_distance,m:~location_st_distance(spheroid=true),~area_st_distance&order=location.st_distance&~near="+url.QueryEscape(ref), &rows)
	checkScores(t, rows, []string{"~location_st_distance", "m", "~area_st_distance"}, want)

	var one row
	getJSON(t, srv.URL+"/api/places/1?select=id,~area_st_distance&~near="+url.QueryEscape(ref), &one)
	checkScores(t, []row{one}, []string{"~area_st_distance"}, map[int][]float64{1: {want[1][2]}})

	var users []struct {
		Places []row `json:"places"`
	}
	getJSON(t, srv.URL+"/api/users?select=id,places(id,~location_st_distance)&~near="+url.QueryEscape(ref), &users)
	var embedded []row
	for _, u := range users {
		embedded = append(embedded, u.Places...)
	}
	checkScores(t, embedded, []string{"~location_st_distance"}, want)

	for _, q := range []string{
		"/api/places?select=id,~location_st_distance",
		"/api/places?select=id,~area_st_distance(spheroid=true)&~near=POINT(0%200)",
	} {
		if res, body := get(t, srv.URL+q); res.StatusCode != 400 {
			t.Errorf("%s = %d %s", q, res.StatusCode, body)
		}
	}
}

const (
	scoresSchema    = "pdbr_scores"
	scoresExtSchema = "pdbr_scores_ext"
)

const scoresDDL = `
CREATE TABLE pdbr_scores.items (
  id   int PRIMARY KEY,
  name text NOT NULL,
  emb  pdbr_scores_ext.vector(3),
  code bit(3)
);
CREATE INDEX items_name_idx ON pdbr_scores.items USING gist (name pdbr_scores_ext.gist_trgm_ops);
CREATE INDEX items_emb_idx ON pdbr_scores.items USING hnsw (emb pdbr_scores_ext.vector_cosine_ops);
CREATE INDEX items_code_idx ON pdbr_scores.items USING hnsw (code pdbr_scores_ext.bit_hamming_ops);
INSERT INTO pdbr_scores.items VALUES
  (1, 'postgres database', '[1,0,0]', '110'),
  (2, 'postgis spatial', '[0,1,0]', '011'),
  (3, 'graph queries', '[1,1,0]', '000'),
  (4, 'unrelated', NULL, NULL);
`

// TestContribScores selects the trigram similarity and pgvector distance
// pseudo-columns and applies the distance-threshold filters, comparing every
// value with the extension functions. It skips without pgvector.
func TestContribScores(t *testing.T) {
	srv := extensionServer(t, scoresSchema, scoresExtSchema, []string{"pg_trgm", "vector"}, scoresDDL, func(cfg *config.Config) {
		cfg.Filters.AllowColumns = map[string][]string{scoresSchema + ".items": {"emb", "code"}}
	})
	const g, v = "pdbr_scores_ext", "pdbr_scores_ext"
	var rows []row

	getJSON(t, srv+"/api/items?select=id,~name_trgm_similarity,~name_trgm_word_similarity,~name_trgm_strict_word_similarity&order=name.trgm_distance&~trgm=postgres", &rows)
	checkScores(t, rows, []string{"~name_trgm_similarity", "~name_trgm_word_similarity", "~name_trgm_strict_word_similarity"}, sqlScores(t, `SELECT id,
		`+g+`.similarity(name, 'postgres'), `+g+`.word_similarity('postgres', name), `+g+`.strict_word_similarity('postgres', name)
		FROM pdbr_scores.items`))

	vec := url.QueryEscape("[1,0.5,0]")
	vecKeys := []string{"~emb_cosine_distance", "~emb_l2_distance", "~emb_negative_inner_product", "~emb_l1_distance"}
	getJSON(t, srv+"/api/items?select=id,"+strings.Join(vecKeys, ",")+"&order=id&~vec="+vec, &rows)
	checkScores(t, rows, vecKeys, sqlScores(t, `SELECT id,
		emb OPERATOR(`+v+`.<=>) '[1,0.5,0]'::`+v+`.vector, emb OPERATOR(`+v+`.<->) '[1,0.5,0]'::`+v+`.vector,
		emb OPERATOR(`+v+`.<#>) '[1,0.5,0]'::`+v+`.vector, emb OPERATOR(`+v+`.<+>) '[1,0.5,0]'::`+v+`.vector
		FROM pdbr_scores.items`))

	getJSON(t, srv+"/api/items?select=id,~code_hamming_distance,~code_jaccard_distance&order=id&~vec=100", &rows)
	checkScores(t, rows, []string{"~code_hamming_distance", "~code_jaccard_distance"}, sqlScores(t, `SELECT id,
		code OPERATOR(`+v+`.<~>) '100'::varbit, code OPERATOR(`+v+`.<%>) '100'::varbit FROM pdbr_scores.items`))

	for q, want := range map[string][]int{
		"select=id&emb=cosine_distance_lt.0.1&order=emb.cosine_distance&~vec=" + vec: {3},
		"select=id&emb=l2_distance_lt.1&order=id&~vec=" + vec:                        {1, 3},
		"select=id&emb=not.l2_distance_lt.1&order=id&~vec=" + vec:                    {2},
		"select=id&code=hamming_distance_lt.2&order=id&~vec=100":                     {1, 3},
		"select=id&code=not.is.null&order=code.hamming_distance,id&~vec=001":         {2, 3, 1},
		"select=id&or=(emb.cosine_distance_lt.0.1,id.eq.2)&order=id&~vec=" + vec:     {2, 3},
	} {
		getJSON(t, srv+"/api/items?"+q, &rows)
		if got := ids(rows, "id"); !eq(got, want) {
			t.Errorf("%s = %v, want %v", q, got, want)
		}
	}

	for _, q := range []string{
		"select=id,~name_trgm_similarity",
		"select=id&emb=cosine_distance_lt.0.1",
		"select=id,~emb_l2_distance&~vec=%5B1%2C0%5D",
		"select=id&code=hamming_distance_lt.1&~vec=102",
		"select=id&~trgm=x",
	} {
		if res, body := get(t, srv+"/api/items?"+q); res.StatusCode != 400 {
			t.Errorf("%s = %d %s", q, res.StatusCode, body)
		}
	}

	strict := testServer(t, func(cfg *config.Config) { cfg.Schema.Schemas = []string{scoresSchema} })
	if res, body := get(t, strict.URL+"/api/items?select=id&emb=l2_distance_lt.1&~vec="+vec); res.StatusCode != 400 || !strings.Contains(string(body), "column_not_filterable") {
		t.Errorf("threshold without allow_columns = %d %s", res.StatusCode, body)
	}
}
