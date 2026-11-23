package e2e

import (
	"context"
	"os"
	"slices"
	"testing"

	"github.com/jackc/pgx/v5"

	"github.com/suprbdev/pdbr/internal/config"
)

const customRangeDDL = `
DROP SCHEMA IF EXISTS pdbr_ranges CASCADE;
CREATE SCHEMA pdbr_ranges;
CREATE TYPE pdbr_ranges.floatrange AS RANGE (subtype = float8, multirange_type_name = pdbr_ranges.floatmultirange);
CREATE TABLE pdbr_ranges.spans (
  id     int PRIMARY KEY,
  span   pdbr_ranges.floatrange,
  spans  pdbr_ranges.floatmultirange
);
CREATE INDEX spans_span_idx ON pdbr_ranges.spans USING gist (span);
CREATE INDEX spans_spans_idx ON pdbr_ranges.spans USING gist (spans);
INSERT INTO pdbr_ranges.spans VALUES
  (1, '[1.5,2.5)', '{[0,1),[2,3)}'),
  (2, '[10,20]', '{[10,11)}'),
  (3, 'empty', NULL);
`

// TestCustomRangeTypes: a CREATE TYPE ... AS RANGE column (and its
// multirange) outside the search_path gets the range operators, the
// structured body form and OpenAPI filter ops like the built-in ranges.
func TestCustomRangeTypes(t *testing.T) {
	dbURL := os.Getenv("PDBR_TEST_DATABASE_URL")
	if dbURL == "" {
		t.Skip("PDBR_TEST_DATABASE_URL not set")
	}
	ctx := context.Background()
	conn, err := pgx.Connect(ctx, dbURL)
	if err != nil {
		t.Fatal(err)
	}
	defer conn.Close(ctx)
	t.Cleanup(func() {
		c, err := pgx.Connect(ctx, dbURL)
		if err != nil {
			t.Errorf("cleanup: %v", err)
			return
		}
		defer c.Close(ctx)
		_, _ = c.Exec(ctx, `DROP SCHEMA IF EXISTS pdbr_ranges CASCADE`)
	})
	if _, err := conn.Exec(ctx, customRangeDDL); err != nil {
		t.Fatal(err)
	}
	srv := testServer(t, func(cfg *config.Config) { cfg.Schema.Schemas = []string{"pdbr_ranges"} }).URL

	cases := []struct {
		query string
		want  []int
	}{
		{"span=ov.[2,3)", []int{1}},
		{"span=cs.2", []int{1}},
		{"span=cs.[12,13)", []int{2}},
		{"span=cd.[0,100)", []int{1, 2, 3}},
		{"span=sl.[5,6)", []int{1}},
		{"span=nxl.[5,6)", []int{2}},
		{"span=adj.[2.5,4)", []int{1}},
		{"span=not.ov.[2,3)", []int{2, 3}},
		{"spans=ov.[2.5,2.6)", []int{1}},
		{"spans=cs.10.5", []int{2}},
	}
	for _, tc := range cases {
		var rows []row
		res := getJSON(t, srv+"/api/spans?select=id&order=id&"+tc.query, &rows)
		if res.StatusCode != 200 || !eq(ids(rows, "id"), tc.want) {
			t.Errorf("%s = %d %v, want %v", tc.query, res.StatusCode, ids(rows, "id"), tc.want)
		}
	}

	var created row
	sendJSON(t, "POST", srv+"/api/spans?select=id,span,spans", `{"id": 4, "span": {"start": 0.5, "end": 1, "endInclusive": true}, "spans": [{"start": 1, "end": 2}, "[5,6)"]}`, 201, &created)
	if created["span"] != "[0.5,1]" || created["spans"] != "{[1,2),[5,6)}" {
		t.Errorf("created = %v", created)
	}

	body, doc := fetchOpenAPI(t, srv)
	validateOpenAPI(t, oasSchema(t), body)
	params := opParams(pathOp(doc, "/spans", "get"))
	for _, col := range []string{"span", "spans"} {
		ops, _ := params["query:"+col]["x-pdbr-filter-ops"].([]any)
		if !slices.Contains(ops, any("ov")) || !slices.Contains(ops, any("adj")) {
			t.Errorf("%s filter ops = %v", col, ops)
		}
	}
}
