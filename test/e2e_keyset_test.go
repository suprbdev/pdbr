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
