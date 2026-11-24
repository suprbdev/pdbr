package e2e

import (
	"context"
	"encoding/json"
	"os"
	"testing"

	"github.com/jackc/pgx/v5"

	"github.com/suprbdev/pdbr/internal/config"
)

func TestVolatileSetReturningRunsOnce(t *testing.T) {
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
		_, _ = c.Exec(ctx, `DROP FUNCTION IF EXISTS public.e2e_once_rows(), public.e2e_once_scalar(), public.e2e_once_composite(), public.e2e_once_stable_rows();
DROP TYPE IF EXISTS public.e2e_once_pair; DROP TABLE IF EXISTS public.e2e_once_calls`)
	})
	setup := `
DROP TABLE IF EXISTS public.e2e_once_calls CASCADE;
CREATE TABLE public.e2e_once_calls (fn text NOT NULL);
GRANT ALL ON public.e2e_once_calls TO PUBLIC;
CREATE TYPE public.e2e_once_pair AS (a int, b text);
CREATE FUNCTION public.e2e_once_rows() RETURNS SETOF public.posts LANGUAGE plpgsql VOLATILE AS $$
BEGIN
  INSERT INTO public.e2e_once_calls VALUES ('rows');
  RETURN QUERY SELECT * FROM public.posts ORDER BY id LIMIT 4;
END $$;
CREATE FUNCTION public.e2e_once_scalar() RETURNS SETOF int LANGUAGE plpgsql VOLATILE AS $$
BEGIN
  INSERT INTO public.e2e_once_calls VALUES ('scalar');
  RETURN QUERY SELECT g FROM generate_series(1, 4) g;
END $$;
CREATE FUNCTION public.e2e_once_composite() RETURNS SETOF public.e2e_once_pair LANGUAGE plpgsql VOLATILE AS $$
BEGIN
  INSERT INTO public.e2e_once_calls VALUES ('composite');
  RETURN QUERY SELECT g, 'x' || g FROM generate_series(1, 4) g;
END $$`
	if _, err := conn.Exec(ctx, setup); err != nil {
		t.Fatal(err)
	}
	if n := dbInt(t, `SELECT count(*) FROM posts`); n < 4 {
		t.Skipf("fixture has %d posts, need 4", n)
	}
	srv := testServer(t, nil).URL
	rb := testServer(t, func(c *config.Config) { c.TX.End = "rollback" }).URL

	type result struct {
		rows  int
		total string
	}
	reqs := []struct {
		name string
		path string
		hdr  map[string]string
		want result
	}{
		{"plain", "", nil, result{4, ""}},
		{"count", "?count=true", nil, result{4, "4"}},
		{"limit=0", "?limit=0", nil, result{0, ""}},
		{"limit=0 count", "?limit=0&count=true", nil, result{0, "4"}},
		{"limit offset count", "?limit=2&offset=1&count=true", nil, result{2, "4"}},
		{"offset", "?offset=3", nil, result{1, ""}},
		{"offset at end count", "?offset=4&count=true", nil, result{0, "4"}},
		{"range", "", map[string]string{"Range": "1-2", "Range-Unit": "items"}, result{-1, ""}},
		{"range count", "", map[string]string{"Range": "1-2", "Range-Unit": "items", "Prefer": "count=exact"}, result{-1, ""}},
		{"count planned", "?count=true", map[string]string{"Prefer": "count=planned"}, result{4, "4"}},
		{"count estimated", "?count=true", map[string]string{"Prefer": "count=estimated"}, result{4, "4"}},
	}
	for _, fn := range []struct {
		name, tag string
		counted   bool
	}{
		{"e2e_once_rows", "rows", true},
		{"e2e_once_scalar", "scalar", false},
		{"e2e_once_composite", "composite", false},
	} {
		for _, rq := range reqs {
			if _, err := conn.Exec(ctx, `TRUNCATE public.e2e_once_calls`); err != nil {
				t.Fatal(err)
			}
			res, body := send(t, "POST", srv+"/api/rpc/"+fn.name+rq.path, "{}", rq.hdr)
			if res.StatusCode != 200 && res.StatusCode != 206 {
				t.Errorf("%s %s: status %d %s", fn.name, rq.name, res.StatusCode, body)
				continue
			}
			calls := dbInt(t, `SELECT count(*) FROM public.e2e_once_calls WHERE fn = '`+fn.tag+`'`)
			if calls != 1 {
				t.Errorf("%s %s: function ran %d times, want 1", fn.name, rq.name, calls)
			}
			if n := len(decodeRows(t, body)); rq.want.rows >= 0 && n != rq.want.rows {
				t.Errorf("%s %s: %d rows, want %d (%s)", fn.name, rq.name, n, rq.want.rows, body)
			}
			if fn.counted && rq.want.total != "" {
				if got := res.Header.Get("X-Total-Count"); got != rq.want.total {
					t.Errorf("%s %s: X-Total-Count %q, want %q", fn.name, rq.name, got, rq.want.total)
				}
			}
		}
		if _, err := conn.Exec(ctx, `TRUNCATE public.e2e_once_calls`); err != nil {
			t.Fatal(err)
		}
		res, body := send(t, "POST", rb+"/api/rpc/"+fn.name+"?count=true", "{}", nil)
		if res.StatusCode != 200 || len(decodeRows(t, body)) != 4 {
			t.Errorf("%s rollback: %d %s", fn.name, res.StatusCode, body)
		}
		if calls := dbInt(t, `SELECT count(*) FROM public.e2e_once_calls`); calls != 0 {
			t.Errorf("%s rollback: %d calls survived", fn.name, calls)
		}
	}
}

func decodeRows(t *testing.T, body []byte) []json.RawMessage {
	t.Helper()
	var rows []json.RawMessage
	if err := json.Unmarshal(body, &rows); err != nil {
		t.Fatalf("body %s: %v", body, err)
	}
	return rows
}
