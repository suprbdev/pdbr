package compile_test

import (
	"bytes"
	"context"
	"encoding/json"
	"os"
	"regexp"
	"strings"
	"testing"

	"github.com/jackc/pgx/v5"

	"github.com/suprbdev/pdbcore/introspect"
	"github.com/suprbdev/pdbcore/postgis"

	"github.com/suprbdev/pdbr/internal/api"
	"github.com/suprbdev/pdbr/internal/compile"
	"github.com/suprbdev/pdbr/internal/plan"
)

// TestAgainstDatabase executes representative statements against a live
// fixture database (PDBR_TEST_DATABASE_URL; skipped otherwise) so the SQL
// shapes are known to be valid PostgreSQL, not just byte-exact.
func TestAgainstDatabase(t *testing.T) {
	url := os.Getenv("PDBR_TEST_DATABASE_URL")
	if url == "" {
		t.Skip("PDBR_TEST_DATABASE_URL not set")
	}
	ctx := context.Background()
	conn, err := pgx.Connect(ctx, url)
	if err != nil {
		t.Fatalf("connect: %v", err)
	}
	defer conn.Close(ctx)
	cat, err := introspect.Introspect(ctx, conn, []string{"public"})
	if err != nil {
		t.Fatalf("introspect: %v", err)
	}
	built := buildCatalog(t, cat, true)
	c := compile.New(built)
	f := &fixture{t: t, built: built, c: c}
	users, posts, places, comments := f.res("users"), f.res("posts"), f.res("places"), f.res("comments")

	type page struct {
		Rows    json.RawMessage `json:"rows"`
		HasNext bool            `json:"has_next"`
		HasPrev bool            `json:"has_prev"`
		First   []any           `json:"first"`
		Last    []any           `json:"last"`
		Total   *int            `json:"total"`
	}
	run := func(name string, p *plan.Request, check func(t *testing.T, data []byte)) {
		t.Run(name, func(t *testing.T) {
			stmt, err := c.Compile(ctx, &compile.Request{Plan: p, Built: built})
			if err != nil {
				t.Fatalf("compile: %v", err)
			}
			rows, err := conn.Query(ctx, stmt.SQL, stmt.Args...)
			if err != nil {
				t.Fatalf("query: %v\n%s\n%v", err, stmt.SQL, stmt.Args)
			}
			defer rows.Close()
			var data []byte
			n := 0
			for rows.Next() {
				n++
				if err := rows.Scan(&data); err != nil {
					t.Fatalf("scan: %v", err)
				}
			}
			if err := rows.Err(); err != nil {
				t.Fatalf("rows: %v\n%s", err, stmt.SQL)
			}
			if stmt.Shape == compile.ShapePage && n != 1 {
				t.Fatalf("page statement returned %d rows", n)
			}
			if check != nil {
				check(t, data)
			}
		})
	}
	decodePage := func(t *testing.T, data []byte) page {
		var pg page
		dec := json.NewDecoder(bytes.NewReader(data))
		dec.UseNumber()
		if err := dec.Decode(&pg); err != nil {
			t.Fatalf("decode page: %v\n%s", err, data)
		}
		return pg
	}
	rowCount := func(t *testing.T, raw json.RawMessage) int {
		var arr []json.RawMessage
		if err := json.Unmarshal(raw, &arr); err != nil {
			t.Fatalf("rows not an array: %s", raw)
		}
		return len(arr)
	}

	run("list_all_users", list(users), func(t *testing.T, data []byte) {
		pg := decodePage(t, data)
		if rowCount(t, pg.Rows) != 3 || pg.HasNext || pg.HasPrev || len(pg.First) != 1 || len(pg.Last) != 1 {
			t.Errorf("page: %s", data)
		}
	})
	p := list(users)
	p.Page = plan.Page{Limit: 2}
	p.Count = true
	p.Select = &plan.Node{Resource: users, Fields: []plan.FieldSel{{Column: f.col("users", "id")}, {Column: f.col("users", "address")}, {Column: f.col("users", "balance")}, {Computed: f.computed("users", "post_count")}}, Embeds: []plan.Embed{{
		Key: "posts", Relation: f.rel("users", "posts", ""), Node: fields(posts, "id", "title"),
		Order: []plan.OrderTerm{{Column: f.col("posts", "title"), Desc: true}}, Page: plan.Page{Limit: 1, LimitSet: true},
	}}}
	run("list_embed_count_keyset", p, func(t *testing.T, data []byte) {
		pg := decodePage(t, data)
		if rowCount(t, pg.Rows) != 2 || !pg.HasNext || pg.HasPrev || pg.Total == nil || *pg.Total != 3 {
			t.Errorf("page: %s", data)
		}
		// Follow the next cursor.
		cur := plan.EncodeCursor(users, pg.Last)
		name, keys, err := plan.DecodeCursor(cur)
		if err != nil || name != "public.users" {
			t.Fatalf("cursor: %v", err)
		}
		p2 := *p
		p2.Page = plan.Page{Limit: 2, After: []any{keys[0].(json.Number).String()}}
		stmt, err := c.Compile(ctx, &compile.Request{Plan: &p2, Built: built})
		if err != nil {
			t.Fatal(err)
		}
		var d2 []byte
		if err := conn.QueryRow(ctx, stmt.SQL, stmt.Args...).Scan(&d2); err != nil {
			t.Fatalf("page 2: %v\n%s", err, stmt.SQL)
		}
		pg2 := decodePage(t, d2)
		if rowCount(t, pg2.Rows) != 1 || pg2.HasNext || !pg2.HasPrev {
			t.Errorf("page 2: %s", d2)
		}
		// And back with before=.
		p3 := *p
		p3.Page = plan.Page{Limit: 2, Before: []any{pg2.First[0].(json.Number).String()}}
		stmt, err = c.Compile(ctx, &compile.Request{Plan: &p3, Built: built})
		if err != nil {
			t.Fatal(err)
		}
		var d3 []byte
		if err := conn.QueryRow(ctx, stmt.SQL, stmt.Args...).Scan(&d3); err != nil {
			t.Fatalf("page back: %v\n%s", err, stmt.SQL)
		}
		if string(decodePage(t, d3).Rows) != string(pg.Rows) {
			t.Errorf("before page differs from page 1:\n%s\n%s", d3, pg.Rows)
		}
	})
	p = list(posts)
	p.Order = []plan.OrderTerm{{Column: f.col("posts", "body"), Desc: true}, {Column: f.col("posts", "title")}}
	p.Page = plan.Page{Limit: 2, After: []any{int64(1)}}
	p.Filter = plan.Or{Terms: []plan.Expr{
		cond(f.col("posts", "title"), plan.OpILike, "*o*"),
		cond(f.col("posts", "published"), plan.OpIs, true),
		plan.Not{Term: cond(f.col("posts", "id"), plan.OpIn, []any{int64(99)})},
	}}
	run("keyset_nullable_mixed", p, nil)
	p = list(users)
	p.Filter = plan.And{Terms: []plan.Expr{
		cond(f.col("users", "mood"), plan.OpIn, []any{"happy", "ok"}),
		cond(f.col("users", "mood"), plan.OpEq, "happy"),
		plan.Cond{Column: f.col("users", "mood"), Op: plan.OpIn, Value: []any{"sad"}, Negate: true},
		cond(f.col("users", "moods"), plan.OpCs, []any{"happy"}),
		cond(f.col("users", "moods"), plan.OpEq, []any{"happy", "sad"}),
		cond(f.col("users", "tags"), plan.OpOv, []any{"admin", "x"}),
		cond(f.col("users", "settings"), plan.OpCs, `{"theme":"dark"}`),
		cond(f.col("users", "settings"), plan.OpHk, "theme"),
		cond(f.col("users", "settings"), plan.OpJpe, `$.theme`),
		cond(f.col("users", "settings"), plan.OpJpm, `$.theme == "dark"`),
		cond(f.col("users", "email"), plan.OpSw, "a"),
		cond(f.col("users", "email"), plan.OpEw, ".com"),
		cond(f.col("users", "balance"), plan.OpGte, "0"),
		cond(f.col("users", "created_at"), plan.OpLt, "2999-01-01T00:00:00Z"),
		cond(f.col("users", "full_name"), plan.OpIs, nil),
		plan.Cond{Computed: f.computed("users", "post_count"), Op: plan.OpGte, Value: int64(0)},
		plan.Cond{Relation: &plan.RelationPath{Steps: []*api.Relation{f.rel("users", "posts", "")}, Quantifier: plan.Every, Leaf: &plan.Cond{Column: f.col("posts", "id"), Op: plan.OpGt, Value: int64(0)}}},
	}}
	p.Order = []plan.OrderTerm{{Relation: &plan.RelationOrder{Relation: f.rel("users", "posts", ""), Column: f.col("posts", "title")}, Desc: true}, {Computed: f.computed("users", "post_count")}}
	run("filters_everything", p, nil)
	p = list(f.res("events"))
	p.Filter = plan.And{Terms: []plan.Expr{
		cond(f.col("events", "type_code"), plan.OpEq, "meeting"),
		cond(f.col("events", "type_code"), plan.OpIn, []any{"meeting", "party"}),
	}}
	run("table_backed_enum", p, nil)
	p = list(users)
	p.Distinct = []*introspect.Column{f.col("users", "mood")}
	p.Order = []plan.OrderTerm{{Column: f.col("users", "mood"), NullsFirst: true}}
	p.Count = true
	p.Select = fields(users, "mood")
	run("distinct", p, func(t *testing.T, data []byte) {
		pg := decodePage(t, data)
		if pg.Total == nil || *pg.Total != rowCount(t, pg.Rows) {
			t.Errorf("distinct total: %s", data)
		}
	})
	geom, _ := postgis.Parse("SRID=4326;POINT(-0.1281 51.508)", postgis.Geometry)
	p = list(places)
	p.Select = &plan.Node{Resource: places, Fields: []plan.FieldSel{
		{Column: f.col("places", "name")}, {Column: f.col("places", "location")},
		{Alias: "web", Column: f.col("places", "location"), Args: map[string]any{"transform": 3857, "simplify": 0.001}},
		{Pseudo: &api.PseudoColumn{Key: "~location_srid", Column: f.col("places", "location"), Accessor: "srid"}},
		{Pseudo: &api.PseudoColumn{Key: "~area_centroid", Column: f.col("places", "area"), Accessor: "centroid"}},
	}}
	sp := true
	p.Filter = plan.And{Terms: []plan.Expr{
		cond(f.col("places", "location"), plan.OpStDWithin, plan.DWithin{Geom: geom, Distance: 1e6, Spheroid: &sp}),
		cond(f.col("places", "location"), plan.OpBBoxIntersects, plan.BBox{MinX: -180, MinY: -90, MaxX: 180, MaxY: 90}),
		cond(f.col("places", "area"), plan.OpStIntersects, geom),
		plan.Not{Term: cond(f.col("places", "location"), plan.OpStEquals, "SRID=4326;POINT(0 0)")},
	}}
	p.Order = []plan.OrderTerm{{Distance: f.col("places", "location")}}
	p.Near = geom
	run("spatial", p, nil)
	rp := &plan.Request{Method: plan.GET, Resource: users, Key: &plan.Key{Constraint: users.Uniques[0], Values: []any{"ada@example.com"}}}
	rp.Select = &plan.Node{Resource: users, Star: true, Fields: []plan.FieldSel{{Computed: f.computed("users", "tag_words")}}, Embeds: []plan.Embed{{
		Key: "posts", Relation: f.rel("users", "posts", ""), Inner: true, Filter: cond(f.col("posts", "published"), plan.OpIs, true),
		Node: &plan.Node{Resource: posts, Star: true, Embeds: []plan.Embed{{Key: "comments", Relation: f.rel("posts", "comments", ""), Node: &plan.Node{Resource: comments, Star: true, Embeds: []plan.Embed{
			{Key: "comments", Alias: "parent", Relation: f.rel("comments", "comments", "parent_id")},
			{Key: "comments", Alias: "replies", Relation: f.rel("comments", "comments", "comments_parent_id_fkey")},
		}}}}},
	}}}
	run("row_unique_etag_nested", rp, func(t *testing.T, data []byte) {
		var w struct {
			Etag string          `json:"etag"`
			Row  json.RawMessage `json:"row"`
		}
		if err := json.Unmarshal(data, &w); err != nil || len(w.Etag) != 65 || w.Etag[32] != '.' || len(w.Row) == 0 {
			t.Errorf("etag wrapper: %s", data)
		}
		var row map[string]any
		if err := json.Unmarshal(w.Row, &row); err != nil {
			t.Fatal(err)
		}
		if _, ok := row["balance"].(string); !ok {
			t.Errorf("int8 must be a string: %v", row["balance"])
		}
		if posts, ok := row["posts"].([]any); !ok || len(posts) != 1 {
			t.Errorf("inner embed with filter: %v", row["posts"])
		}
	})
	rp2 := &plan.Request{Method: plan.GET, Resource: places, Key: &plan.Key{Constraint: places.Table.PrimaryKey, Values: []any{int64(1)}}}
	run("row_pk_spatial_star", rp2, nil)
}

// TestKeysetIndexBoundAgainstDatabase EXPLAINs a deep keyset page under a
// mixed-direction order on a few thousand posts (inserted and analysed in a
// rolled-back transaction) and requires the leading order column to be an
// index condition rather than a filter over every row before the cursor.
func TestKeysetIndexBoundAgainstDatabase(t *testing.T) {
	url := os.Getenv("PDBR_TEST_DATABASE_URL")
	if url == "" {
		t.Skip("PDBR_TEST_DATABASE_URL not set")
	}
	ctx := context.Background()
	conn, err := pgx.Connect(ctx, url)
	if err != nil {
		t.Fatalf("connect: %v", err)
	}
	defer conn.Close(ctx)
	cat, err := introspect.Introspect(ctx, conn, []string{"public"})
	if err != nil {
		t.Fatalf("introspect: %v", err)
	}
	built := buildCatalog(t, cat, false)
	c := compile.New(built)
	f := &fixture{t: t, built: built, c: c}
	posts := f.res("posts")

	tx, err := conn.Begin(ctx)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = tx.Rollback(ctx) }()
	var anchor int64
	err = tx.QueryRow(ctx, `WITH ins AS (
		INSERT INTO posts (author_id, title, published)
		SELECT 1 + g % 3, 'KB ' || lpad(g::text, 5, '0'), g % 2 = 0 FROM generate_series(1, 5000) g
		RETURNING id, title)
		SELECT id FROM ins WHERE title = 'KB 04900'`).Scan(&anchor)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := tx.Exec(ctx, `ANALYZE posts`); err != nil {
		t.Fatal(err)
	}
	for _, tc := range []struct {
		name  string
		order []plan.OrderTerm
		page  plan.Page
		cond  string
	}{
		{"after asc,desc", []plan.OrderTerm{{Column: f.col("posts", "title")}, {Column: f.col("posts", "author_id"), Desc: true}}, plan.Page{Limit: 20, After: []any{anchor}}, `title >= `},
		{"before desc,asc", []plan.OrderTerm{{Column: f.col("posts", "title"), Desc: true}, {Column: f.col("posts", "author_id")}}, plan.Page{Limit: 20, Before: []any{anchor}}, `title >= `},
		{"after desc,asc", []plan.OrderTerm{{Column: f.col("posts", "title"), Desc: true}, {Column: f.col("posts", "author_id")}}, plan.Page{Limit: 20, After: []any{anchor}}, `title <= `},
	} {
		t.Run(tc.name, func(t *testing.T) {
			p := list(posts)
			p.Select = fields(posts, "id", "title")
			p.Order = tc.order
			p.Page = tc.page
			stmt, err := c.Compile(ctx, &compile.Request{Plan: p, Built: built})
			if err != nil {
				t.Fatalf("compile: %v", err)
			}
			rows, err := tx.Query(ctx, "EXPLAIN (ANALYZE, COSTS OFF, TIMING OFF) "+stmt.SQL, stmt.Args...)
			if err != nil {
				t.Fatalf("explain: %v\n%s", err, stmt.SQL)
			}
			var lines []string
			for rows.Next() {
				var l string
				if err := rows.Scan(&l); err != nil {
					t.Fatal(err)
				}
				lines = append(lines, l)
			}
			if err := rows.Err(); err != nil {
				t.Fatal(err)
			}
			out := strings.Join(lines, "\n")
			if !strings.Contains(out, "Index Cond: ("+tc.cond) {
				t.Errorf("leading order column is not an index condition:\n%s", out)
			}
			for _, m := range regexp.MustCompile(`Rows Removed by Filter: (\d+)`).FindAllStringSubmatch(out, -1) {
				if len(m[1]) > 2 {
					t.Errorf("deep page filtered %s rows:\n%s", m[1], out)
				}
			}
		})
	}
}
