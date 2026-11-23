package e2e

import (
	"context"
	"encoding/base64"
	"fmt"
	"os"
	"testing"

	"github.com/jackc/pgx/v5"
)

// TestKeysetDeletedAnchor: a cursor whose row has been deleted yields an
// empty page under a non-PK order (the anchor's order values cannot be
// reconstructed) but keeps paging under a PK-only order (the cursor carries
// the key values).
func TestKeysetDeletedAnchor(t *testing.T) {
	dbURL := os.Getenv("PDBR_TEST_DATABASE_URL")
	if dbURL == "" {
		t.Skip("PDBR_TEST_DATABASE_URL not set; run `make test-e2e`")
	}
	ctx := context.Background()
	conn, err := pgx.Connect(ctx, dbURL)
	if err != nil {
		t.Fatal(err)
	}
	defer conn.Close(ctx)
	var id int
	if err := conn.QueryRow(ctx, `INSERT INTO posts (author_id, title, published) VALUES (1, 'M7 anchor', true) RETURNING id`).Scan(&id); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		c, err := pgx.Connect(ctx, dbURL)
		if err == nil {
			defer c.Close(ctx)
			c.Exec(ctx, `DELETE FROM posts WHERE title = 'M7 anchor'`)
		}
	})
	cursor := base64.RawURLEncoding.EncodeToString([]byte(fmt.Sprintf(`["public.posts",%d]`, id)))

	srv := testServer(t, nil)
	var rows []row
	// 'M7 anchor' sorts before 'Notes…', 'On …' and 'Unpublished…' by title.
	getJSON(t, srv.URL+"/api/posts?select=id&order=title.asc&limit=10&after="+cursor, &rows)
	if len(rows) == 0 {
		t.Fatalf("live anchor, title order: no rows")
	}
	getJSON(t, srv.URL+"/api/posts?select=id&order=id.asc&limit=10&after="+cursor, &rows)
	if len(rows) != 0 {
		t.Fatalf("live anchor is the last id, expected no rows after it, got %v", rows)
	}

	if _, err := conn.Exec(ctx, `DELETE FROM posts WHERE id = $1`, id); err != nil {
		t.Fatal(err)
	}
	getJSON(t, srv.URL+"/api/posts?select=id&order=title.asc&limit=10&after="+cursor, &rows)
	if len(rows) != 0 {
		t.Fatalf("deleted anchor under title order should give an empty page, got %v", rows)
	}
	getJSON(t, srv.URL+"/api/posts?select=id&order=id.desc&limit=10&after="+cursor, &rows)
	if len(rows) != 4 {
		t.Fatalf("deleted anchor under PK order should keep paging (4 fixture posts), got %v", rows)
	}
}

// TestKeysetMixedWalk walks mixed-direction and nullable-leading orders
// page by page in both directions and compares every page with the order
// PostgreSQL produces for the same ORDER BY, so the redundant leading bound
// cannot drop or repeat rows at page boundaries.
func TestKeysetMixedWalk(t *testing.T) {
	dbURL := os.Getenv("PDBR_TEST_DATABASE_URL")
	if dbURL == "" {
		t.Skip("PDBR_TEST_DATABASE_URL not set; run `make test-e2e`")
	}
	ctx := context.Background()
	conn, err := pgx.Connect(ctx, dbURL)
	if err != nil {
		t.Fatal(err)
	}
	defer conn.Close(ctx)
	if _, err := conn.Exec(ctx, `INSERT INTO posts (author_id, title, published)
		SELECT 1 + g % 3, 'KS ' || (g % 5), g % 2 = 0 FROM generate_series(1, 23) g`); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		c, err := pgx.Connect(ctx, dbURL)
		if err == nil {
			defer c.Close(ctx)
			c.Exec(ctx, `DELETE FROM posts WHERE title LIKE 'KS %'`)
		}
	})
	srv := testServer(t, nil)

	cases := []struct{ order, sql string }{
		{"title.asc,author_id.desc", `title ASC, author_id DESC, id ASC`},
		{"title.desc,author_id.asc", `title DESC, author_id ASC, id ASC`},
		{"author_id.desc,title.asc,id.desc", `author_id DESC, title ASC, id DESC`},
	}
	for _, tc := range cases {
		t.Run(tc.order, func(t *testing.T) {
			var want []int
			rs, err := conn.Query(ctx, `SELECT id FROM posts WHERE title LIKE 'KS %' ORDER BY `+tc.sql)
			if err != nil {
				t.Fatal(err)
			}
			for rs.Next() {
				var id int
				if err := rs.Scan(&id); err != nil {
					t.Fatal(err)
				}
				want = append(want, id)
			}
			if rs.Err() != nil {
				t.Fatal(rs.Err())
			}
			base := srv.URL + "/api/posts?select=id&title=like.KS*&limit=4&order=" + tc.order
			var rows []row
			var got []int
			var pages [][]int
			url := base
			for range 20 {
				res := getJSON(t, url, &rows)
				pages = append(pages, ids(rows, "id"))
				got = append(got, ids(rows, "id")...)
				next := res.Header.Get("X-Next-Cursor")
				if next == "" {
					break
				}
				url = base + "&after=" + next
			}
			if !eq(got, want) {
				t.Fatalf("forward walk = %v\nwant %v", got, want)
			}
			res := getJSON(t, url, &rows)
			prev := res.Header.Get("X-Prev-Cursor")
			for i := len(pages) - 2; i >= 0; i-- {
				if prev == "" {
					t.Fatalf("page %d: no prev cursor", i+1)
				}
				res = getJSON(t, base+"&before="+prev, &rows)
				if !eq(ids(rows, "id"), pages[i]) {
					t.Fatalf("backward page %d = %v, want %v", i, ids(rows, "id"), pages[i])
				}
				prev = res.Header.Get("X-Prev-Cursor")
			}
			if prev != "" {
				t.Errorf("first page reached backwards still has a prev cursor")
			}
		})
	}
}
