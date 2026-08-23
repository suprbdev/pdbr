package e2e

import (
	"context"
	"encoding/json"
	"os"
	"strings"
	"testing"

	"github.com/jackc/pgx/v5"

	"github.com/suprbdev/pdbr/internal/config"
)

func wantIDs(t *testing.T, base, q string, want []int) {
	t.Helper()
	res, body := get(t, base+"/api/"+q)
	if res.StatusCode != 200 {
		t.Errorf("%s = %d %s", q, res.StatusCode, body)
		return
	}
	var rows []row
	if err := json.Unmarshal(body, &rows); err != nil {
		t.Fatalf("%s: %v", q, err)
	}
	if got := ids(rows, "id"); !eq(got, want) {
		t.Errorf("%s = %v, want %v", q, got, want)
	}
}

func wantCode(t *testing.T, base, q string, status int, code string) {
	t.Helper()
	res, body := get(t, base+"/api/"+q)
	if res.StatusCode != status {
		t.Errorf("%s = %d %s, want %d %s", q, res.StatusCode, body, status, code)
		return
	}
	var p problem
	if err := json.Unmarshal(body, &p); err != nil || p.Code != code {
		t.Errorf("%s = %s, want code %s", q, body, code)
	}
}

func TestAdvancedFiltersRelationFilters(t *testing.T) {
	srv := testServer(t, nil)
	for _, tc := range []struct {
		q    string
		want []int
	}{
		// backward: some (default), none, every, exists
		{"users?posts.title=eq.Compilers%20101", []int{2}},
		{"users?posts.title=sw.Un&posts.author_id=eq.1", []int{1}},
		{"users?posts.title=sw.Un&posts.title=ew.draft", []int{1}},
		{"users?posts!none.title=eq.Compilers%20101&order=id", []int{1, 3}},
		{"users?posts!every.title=sw.C&order=id", []int{2}},
		{"users?places!every.owner_id=eq.1&order=id", []int{1}},
		{"users?places!none.id=eq.3&order=id", []int{1, 3}},
		{"users?posts=exists.true&order=id", []int{1, 2, 3}},
		{"users?posts=exists.false", []int{}},
		{"users?posts=not.exists.true", []int{}},
		{"posts?comments=exists.false&order=id", []int{1, 2, 3, 4}},
		{"posts?comments=exists.true", []int{}},
		{"posts?comments!none.id=eq.1&order=id", []int{1, 2, 3, 4}},
		// forward
		{"places?users.email=eq.grace@example.com", []int{3}},
		{"posts?users.email=eq.ada@example.com&order=id", []int{1, 2}},
		{"posts?users.mood=eq.happy&order=id", []int{1, 2, 4}},
		{"posts?users=exists.true&order=id", []int{1, 2, 3, 4}},
		{"posts?users.post_count=gte.2&order=id", []int{1, 2}},
		{"places?users=exists.false", []int{}},
		// negation and logic
		{"users?posts.title=not.eq.Compilers%20101&order=id", []int{1, 3}},
		{"users?or=(posts.title.eq.Compilers%20101,id.eq.3)&order=id", []int{2, 3}},
		// self-reference with hint
		{"comments?comments!parent_id.id=eq.1", []int{}},
	} {
		wantIDs(t, srv.URL, tc.q, tc.want)
	}
	for q, code := range map[string]string{
		"posts?users!none.email=eq.x":    "invalid_filter",
		"posts?users!every.email=eq.x":   "invalid_filter",
		"users?posts!none=exists.true":   "invalid_filter",
		"users?posts=exists.maybe":       "invalid_filter",
		"users?posts=eq.1":               "invalid_filter",
		"users?posts.comments.body=eq.x": "invalid_filter",
		"users?posts.published=is.true":  "column_not_filterable",
		"users?posts.nope=eq.1":          "unknown_column",
		"users?nope.title=eq.x":          "unknown_relation",
		"users?posts!bad.title=eq.x":     "ambiguous_relation",
		"comments?comments.body=eq.x":    "ambiguous_relation",
	} {
		wantCode(t, srv.URL, q, 400, code)
	}
}

func TestAdvancedFiltersRelatedOrder(t *testing.T) {
	srv := testServer(t, nil)
	for _, tc := range []struct {
		q    string
		want []int
	}{
		{"posts?order=users(email).desc,id", []int{3, 4, 1, 2}},
		{"posts?order=users(email).asc,id", []int{1, 2, 4, 3}},
		{"users?order=posts(title).desc", []int{1, 3, 2}},
		{"users?order=posts(title).asc", []int{2, 1, 3}},
		{"users?order=places(id).desc,id", []int{3, 2, 1}},
		{"comments?order=comments!parent_id(id).asc", []int{}},
	} {
		wantIDs(t, srv.URL, tc.q, tc.want)
	}
	// keyset paging under a related ordering
	var rows []row
	res := getJSON(t, srv.URL+"/api/posts?order=users(email).desc,id&limit=2", &rows)
	if !eq(ids(rows, "id"), []int{3, 4}) {
		t.Fatalf("page 1 = %v", rows)
	}
	next := res.Header.Get("X-Next-Cursor")
	if next == "" {
		t.Fatal("missing X-Next-Cursor")
	}
	getJSON(t, srv.URL+"/api/posts?order=users(email).desc,id&limit=2&after="+next, &rows)
	if !eq(ids(rows, "id"), []int{1, 2}) {
		t.Fatalf("page 2 = %v", rows)
	}
	for q, code := range map[string]string{
		"posts?order=users(published).asc": "unknown_column",
		"users?order=posts(published).asc": "column_not_orderable",
		"users?order=nope(id).asc":         "unknown_relation",
		"users?order=posts().asc":          "invalid_filter",
	} {
		wantCode(t, srv.URL, q, 400, code)
	}
}

func TestAdvancedFiltersComputed(t *testing.T) {
	srv := testServer(t, nil)
	for _, tc := range []struct {
		q    string
		want []int
	}{
		{"users?post_count=gte.2", []int{1}},
		{"users?post_count=eq.1&order=id", []int{2, 3}},
		{"users?post_count=in.(0,1)&order=id", []int{2, 3}},
		{"users?post_count=not.gt.1&order=id", []int{2, 3}},
		{"users?order=post_count.desc,id", []int{1, 2, 3}},
		{"users?order=post_count.asc,id.desc", []int{3, 2, 1}},
		{"users?select=id,post_count&post_count=gt.1&order=post_count.desc", []int{1}},
	} {
		wantIDs(t, srv.URL, tc.q, tc.want)
	}
	for q, code := range map[string]string{
		"users?tag_words=eq.x":         "column_not_filterable",
		"users?order=tag_words.asc":    "column_not_orderable",
		"users?post_count=eq.abc":      "invalid_filter",
		"users?post_count=exists.true": "invalid_filter",
	} {
		wantCode(t, srv.URL, q, 400, code)
	}
}

func TestAdvancedFiltersSettings(t *testing.T) {
	t.Run("relations off", func(t *testing.T) {
		srv := testServer(t, func(c *config.Config) {
			c.Plugins.Settings = map[string]map[string]any{"advanced-filters": {"relations": false}}
		})
		wantCode(t, srv.URL, "users?posts.title=eq.x", 400, "invalid_filter")
		wantCode(t, srv.URL, "users?posts=exists.true", 400, "invalid_filter")
		wantCode(t, srv.URL, "posts?order=users(email).asc", 400, "invalid_filter")
		wantIDs(t, srv.URL, "users?post_count=gte.2", []int{1})
		wantIDs(t, srv.URL, "users?order=post_count.desc,id", []int{1, 2, 3})
		// embeds and embed filters are core, untouched
		wantIDs(t, srv.URL, "users?select=id,posts!inner(id)&posts.title=eq.Compilers%20101", []int{2})
	})
	t.Run("relation_order off", func(t *testing.T) {
		srv := testServer(t, func(c *config.Config) {
			c.Plugins.Settings = map[string]map[string]any{"advanced-filters": {"relation_order": false}}
		})
		wantCode(t, srv.URL, "posts?order=users(email).asc", 400, "invalid_filter")
		wantIDs(t, srv.URL, "users?posts.title=eq.Compilers%20101", []int{2})
	})
	t.Run("computed off", func(t *testing.T) {
		srv := testServer(t, func(c *config.Config) {
			c.Plugins.Settings = map[string]map[string]any{"advanced-filters": {"computed": false}}
		})
		wantCode(t, srv.URL, "users?post_count=gte.2", 400, "column_not_filterable")
		wantCode(t, srv.URL, "users?order=post_count.desc", 400, "column_not_orderable")
		wantIDs(t, srv.URL, "users?posts.title=eq.Compilers%20101", []int{2})
		var rows []row
		getJSON(t, srv.URL+"/api/users?select=id,post_count&id=eq.1", &rows)
		if len(rows) != 1 || rows[0]["post_count"] != "2" {
			t.Fatalf("select= of a computed column must survive computed=false: %v", rows)
		}
	})
	t.Run("opt-in", func(t *testing.T) {
		tagged(t, map[string]string{
			"CONSTRAINT comments_post_id_fkey ON comments": "@filterable",
			"FUNCTION users_post_count(users)":             "@sortable",
		})
		srv := testServer(t, func(c *config.Config) {
			c.Plugins.Disabled = []string{"smart-comments"}
			c.Plugins.Settings = map[string]map[string]any{"advanced-filters": {"relations_opt_in": true, "computed_opt_in": true}}
		})
		wantIDs(t, srv.URL, "posts?comments=exists.false&order=id", []int{1, 2, 3, 4})
		wantIDs(t, srv.URL, "comments?posts.title=eq.x", []int{})
		wantIDs(t, srv.URL, "comments?order=posts(title).asc", []int{})
		wantCode(t, srv.URL, "users?posts.title=eq.x", 400, "column_not_filterable")
		wantCode(t, srv.URL, "users?posts=exists.true", 400, "column_not_filterable")
		wantCode(t, srv.URL, "posts?order=users(email).asc", 400, "column_not_orderable")
		wantIDs(t, srv.URL, "users?order=post_count.desc,id", []int{1, 2, 3})
		wantIDs(t, srv.URL, "users?post_count=gte.2", []int{1})
	})
}

// tagged sets catalog comments for the test and clears them on cleanup so
// the shared fixture stays pristine for the other suites.
func tagged(t *testing.T, comments map[string]string) {
	t.Helper()
	url := os.Getenv("PDBR_TEST_DATABASE_URL")
	if url == "" {
		t.Skip("PDBR_TEST_DATABASE_URL not set")
	}
	ctx := context.Background()
	conn, err := pgx.Connect(ctx, url)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		for obj := range comments {
			_, _ = conn.Exec(ctx, "COMMENT ON "+obj+" IS NULL")
		}
		conn.Close(ctx)
	})
	for obj, c := range comments {
		if _, err := conn.Exec(ctx, "COMMENT ON "+obj+" IS '"+strings.ReplaceAll(c, "'", "''")+"'"); err != nil {
			t.Fatal(err)
		}
	}
}

func TestAdvancedFiltersOmitTags(t *testing.T) {
	tagged(t, map[string]string{
		"CONSTRAINT posts_author_id_fkey ON posts":  "@omit filter",
		"CONSTRAINT places_owner_id_fkey ON places": "@omit order",
		"FUNCTION users_post_count(users)":          "@omit order\nHow many posts.",
	})
	// smart-comments disabled: the tags must still be honoured, straight
	// from the catalog.
	srv := testServer(t, func(c *config.Config) { c.Plugins.Disabled = []string{"smart-comments"} })
	wantCode(t, srv.URL, "users?posts.title=eq.x", 400, "column_not_filterable")
	wantCode(t, srv.URL, "users?posts=exists.true", 400, "column_not_filterable")
	wantCode(t, srv.URL, "posts?users.email=eq.x", 400, "column_not_filterable")
	wantIDs(t, srv.URL, "posts?order=users(email).desc,id", []int{3, 4, 1, 2})
	wantIDs(t, srv.URL, "users?order=posts(title).desc", []int{1, 3, 2})
	wantIDs(t, srv.URL, "users?select=id,posts!inner(id)&posts.title=eq.Compilers%20101", []int{2})

	wantCode(t, srv.URL, "users?order=places(name).asc", 400, "column_not_orderable")
	wantCode(t, srv.URL, "places?order=users(email).asc", 400, "column_not_orderable")
	wantIDs(t, srv.URL, "users?places.id=eq.3", []int{2})

	wantCode(t, srv.URL, "users?order=post_count.desc", 400, "column_not_orderable")
	wantIDs(t, srv.URL, "users?post_count=gte.2", []int{1})
}

func TestAdvancedFiltersPluginDisabled(t *testing.T) {
	srv := testServer(t, func(c *config.Config) { c.Plugins.Disabled = []string{"advanced-filters"} })
	for q, code := range map[string]string{
		"users?posts.title=eq.x":       "invalid_filter",
		"users?posts!none.title=eq.x":  "invalid_filter",
		"users?posts=exists.true":      "invalid_filter",
		"posts?users.email=eq.x":       "invalid_filter",
		"posts?order=users(email).asc": "invalid_filter",
		"users?post_count=gte.2":       "column_not_filterable",
		"users?order=post_count.desc":  "column_not_orderable",
		"users?nope.title=eq.x":        "unknown_relation",
		"users?nope=eq.1":              "unknown_column",
		"users?or=(posts.title.eq.x)":  "invalid_filter",
	} {
		wantCode(t, srv.URL, q, 400, code)
	}
	// core surface stays: embeds, embed filters, computed in select=
	wantIDs(t, srv.URL, "users?select=id,posts!inner(id)&posts.title=eq.Compilers%20101", []int{2})
	var rows []row
	getJSON(t, srv.URL+"/api/users?select=id,post_count&id=eq.1", &rows)
	if len(rows) != 1 || rows[0]["post_count"] != "2" {
		t.Fatalf("select=post_count = %v", rows)
	}

	// OpenAPI never lists a relation/computed query parameter while the
	// plugin is off (and the document is otherwise the same as with it on).
	off := openAPIParams(t, srv.URL)
	on := openAPIParams(t, testServer(t, nil).URL)
	for _, name := range []string{"posts.title", "posts!none.title", "posts", "post_count", "users.email"} {
		if off[name] {
			t.Errorf("OpenAPI lists %q with the plugin disabled", name)
		}
	}
	for name := range on {
		if !off[name] && !strings.Contains(name, ".") {
			t.Errorf("core parameter %q disappeared with the plugin disabled", name)
		}
	}
}

// openAPIParams collects every query parameter name in the document.
func openAPIParams(t *testing.T, base string) map[string]bool {
	t.Helper()
	var doc struct {
		Paths map[string]map[string]json.RawMessage `json:"paths"`
	}
	getJSON(t, base+"/api/openapi.json", &doc)
	out := map[string]bool{}
	for _, item := range doc.Paths {
		for _, raw := range item {
			var op struct {
				Parameters []struct {
					Name string `json:"name"`
					In   string `json:"in"`
				} `json:"parameters"`
			}
			if json.Unmarshal(raw, &op) != nil {
				continue
			}
			for _, p := range op.Parameters {
				if p.In == "query" {
					out[p.Name] = true
				}
			}
		}
	}
	return out
}
