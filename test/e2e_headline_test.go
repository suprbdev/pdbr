package e2e

import (
	"encoding/json"
	"fmt"
	"net/url"
	"os"
	"strconv"
	"strings"
	"testing"

	"github.com/suprbdev/pdbcore/confx"
	"github.com/suprbdev/pdbr/internal/config"
)

const headlineSchema = "pdbr_headline"

// headlineDDL creates writers and articles: search is a generated tsvector
// with a GIN index on both, body, meta (jsonb) and raw (json) are the
// documents the tests search directly (allowlisted by headlineServer), and
// secret is hidden by @omit read. body is hidden from the public contract.
const headlineDDL = `
CREATE TABLE pdbr_headline.writers (
  id     int PRIMARY KEY,
  name   text NOT NULL,
  bio    text,
  search tsvector GENERATED ALWAYS AS (to_tsvector('english', coalesce(bio, ''))) STORED
);
CREATE INDEX writers_search_idx ON pdbr_headline.writers USING gin (search);
CREATE TABLE pdbr_headline.articles (
  id        int PRIMARY KEY,
  title     text NOT NULL,
  body      text,
  meta      jsonb,
  raw       json,
  secret    text,
  writer_id int REFERENCES pdbr_headline.writers (id),
  search    tsvector GENERATED ALWAYS AS (to_tsvector('english', title || ' ' || coalesce(body, ''))) STORED
);
CREATE INDEX articles_search_idx ON pdbr_headline.articles USING gin (search);
CREATE INDEX articles_meta_idx ON pdbr_headline.articles USING gin (meta);
CREATE INDEX articles_writer_idx ON pdbr_headline.articles (writer_id);
COMMENT ON COLUMN pdbr_headline.articles.secret IS '@omit read';
COMMENT ON COLUMN pdbr_headline.articles.body IS '@behavior -contract:public';
INSERT INTO pdbr_headline.writers VALUES
  (1, 'Ada', 'A poet who writes about cats and the sea'),
  (2, 'Bob', 'A chemist');
INSERT INTO pdbr_headline.articles (id, title, body, meta, raw, secret, writer_id) VALUES
  (1, 'Cats at night', 'The fat cat sat on the mat while the dog slept', '{"tags": ["cats", "pets"], "summary": "A story about cats", "n": 7}', '{"note": "fat cats"}', 'cats', 1),
  (2, 'Rats in town', 'A fat rat ate the cheese', '{"summary": "rats"}', '{"note": "rats and cats"}', 'rats', 2),
  (3, 'Dogs', NULL, NULL, NULL, NULL, NULL);
`

func headlineServer(t *testing.T, mutate func(*config.Config)) string {
	t.Helper()
	return extensionServer(t, headlineSchema, headlineSchema+"_ext", nil, headlineDDL, func(cfg *config.Config) {
		cfg.Filters.AllowColumns = map[string][]string{headlineSchema + ".articles": {"body", "meta", "raw"}}
		if mutate != nil {
			mutate(cfg)
		}
	})
}

func hlURL(base, path string, params ...string) string {
	var sb strings.Builder
	sb.WriteString(base + "/" + path + "?")
	for i, p := range params {
		if i > 0 {
			sb.WriteByte('&')
		}
		k, v, _ := strings.Cut(p, "=")
		sb.WriteString(k + "=" + url.QueryEscape(v))
	}
	return sb.String()
}

func TestJSONFullTextSearch(t *testing.T) {
	srv := headlineServer(t, nil)
	cases := []struct {
		params []string
		want   []int
	}{
		{[]string{"meta=wfts(english).story"}, []int{1}},
		{[]string{"meta=fts(english).cat"}, []int{1}},
		{[]string{"meta=fts.summary"}, nil},
		{[]string{"meta=fts.7"}, nil},
		{[]string{"meta=not.plfts(english).rats"}, []int{1}},
		{[]string{"raw=plfts(english).cats"}, []int{1, 2}},
		{[]string{"raw=phfts(english).fat cats"}, []int{1}},
		{[]string{"or=(raw.fts.rats,meta.wfts(english).story)"}, []int{1, 2}},
	}
	for _, tc := range cases {
		var rows []row
		getJSON(t, hlURL(srv+"/api", "articles", append([]string{"select=id", "order=id"}, tc.params...)...), &rows)
		if got := ids(rows, "id"); !eq(got, tc.want) {
			t.Errorf("%v: ids %v, want %v", tc.params, got, tc.want)
		}
	}
	var rows []row
	getJSON(t, hlURL(srv+"/api", "articles", "select=id", "raw=plfts(english).cats", "order=raw.rank.desc,id"), &rows)
	if got := ids(rows, "id"); !eq(got, []int{1, 2}) {
		t.Errorf("rank order: ids %v", got)
	}

	strict := headlineServer(t, func(cfg *config.Config) { cfg.Filters.AllowColumns = nil })
	res, body := get(t, hlURL(strict+"/api", "articles", "select=id", "meta=fts.cat"))
	wantProblem(t, res, body, 400, "column_not_filterable")
}

func TestHeadlines(t *testing.T) {
	srv := headlineServer(t, nil)
	cases := []struct {
		name   string
		path   string
		params []string
		key    string
		want   []any
	}{
		{"tsvector over title", "articles",
			[]string{"select=id,h:search_headline(document=title,start_sel=[,stop_sel=])", "search=fts(english).cat"}, "h",
			[]any{"[Cats] at night"}},
		{"tsvector over body with options", "articles",
			[]string{`select=id,h:search_headline(document=body,max_words=4,min_words=2,start_sel="<mark>",stop_sel="</mark>")`, "search=plfts(english).fat cat"}, "h",
			[]any{"<mark>fat</mark> <mark>cat</mark>"}},
		{"text column itself", "articles",
			[]string{"select=id,body_headline", "body=wfts(english).cheese"}, "body_headline",
			[]any{"A fat rat ate the <b>cheese</b>"}},
		{"jsonb document", "articles",
			[]string{`select=id,meta_headline(start_sel="*",stop_sel="*")`, "meta=fts(english).story"}, "meta_headline",
			[]any{map[string]any{"summary": "A *story* about cats", "tags": []any{"cats", "pets"}, "n": float64(7)}}},
		{"json document", "articles",
			[]string{"select=id,raw_headline(highlight_all=true)", "raw=fts.rats"}, "raw_headline",
			[]any{map[string]any{"note": "<b>rats</b> and cats"}}},
		{"null document", "articles",
			[]string{"select=id,search_headline(document=body)", "search=fts.dog", "order=id"}, "search_headline",
			[]any{"The fat cat sat on the mat while the <b>dog</b> slept", nil}},
	}
	for _, tc := range cases {
		var rows []row
		getJSON(t, hlURL(srv+"/api", tc.path, tc.params...), &rows)
		if len(rows) != len(tc.want) {
			t.Errorf("%s: %d rows %v, want %d", tc.name, len(rows), rows, len(tc.want))
			continue
		}
		for i, w := range tc.want {
			got, _ := json.Marshal(rows[i][tc.key])
			want, _ := json.Marshal(w)
			if !sameJSON(got, string(want)) {
				t.Errorf("%s row %d: %s = %s, want %s", tc.name, i, tc.key, got, want)
			}
		}
	}

	var writers []row
	getJSON(t, hlURL(srv+"/api", "writers", "select=name,articles(id,search_headline(document=title))", "articles.search=fts(english).cats",
		"articles.order=id", "articles.limit=5", "order=id"), &writers)
	if len(writers) != 2 || !sameJSON(mustJSON(t, writers[0]["articles"]), `[{"id":1,"search_headline":"<b>Cats</b> at night"}]`) ||
		!sameJSON(mustJSON(t, writers[1]["articles"]), `[]`) {
		t.Errorf("embedded headlines: %v", writers)
	}
	var arts []row
	getJSON(t, hlURL(srv+"/api", "articles", "select=id,writers(name,search_headline(document=bio))", "writers.search=fts(english).poet", "order=id"), &arts)
	if len(arts) != 3 || !sameJSON(mustJSON(t, arts[0]["writers"]), `{"name":"Ada","search_headline":"A <b>poet</b> who writes about cats and the sea"}`) ||
		arts[1]["writers"] != nil {
		t.Errorf("forward embed headlines: %v", arts)
	}

	for _, tc := range []struct {
		params []string
		code   string
	}{
		{[]string{"select=id,search_headline(document=title)"}, "invalid_filter"},
		{[]string{"select=id,search_headline(document=title)", "or=(search.fts.cat,id.eq.1)"}, "invalid_filter"},
		{[]string{"select=id,search_headline(document=title)", "search=fts.cat", "and=(search.fts.fat)"}, "invalid_filter"},
		{[]string{"select=id,search_headline", "search=fts.cat"}, "invalid_select"},
		{[]string{"select=id,search_headline(document=secret)", "search=fts.cat"}, "invalid_select"},
		{[]string{"select=id,search_headline(document=writer_id)", "search=fts.cat"}, "invalid_select"},
		{[]string{"select=id,search_headline(document=title,max_words=0)", "search=fts.cat"}, "invalid_select"},
		{[]string{"select=id,search_headline(document=title,max_words=10)", "search=fts.cat"}, "invalid_select"},
		{[]string{`select=id,search_headline(document=title,start_sel="a\"b")`, "search=fts.cat"}, "invalid_select"},
		{[]string{"select=id,search_headline(document=title,colour=red)", "search=fts.cat"}, "unknown_argument"},
		{[]string{"select=id,body_headline(document=title)", "body=fts.cat"}, "invalid_select"},
		{[]string{"select=id,secret_headline", "search=fts.cat"}, "unknown_column"},
	} {
		res, body := get(t, hlURL(srv+"/api", "articles", tc.params...))
		wantProblem(t, res, body, 400, tc.code)
	}

	var patched []row
	sendJSON(t, "PATCH", hlURL(srv+"/api", "articles", "search=fts(english).rat", "select=id,search_headline(document=title)"),
		`{"title": "Rats in town"}`, 200, &patched)
	if len(patched) != 1 || patched[0]["search_headline"] != "<b>Rats</b> in town" {
		t.Errorf("PATCH response headline: %v", patched)
	}
}

func mustJSON(t *testing.T, v any) []byte {
	t.Helper()
	b, err := json.Marshal(v)
	if err != nil {
		t.Fatal(err)
	}
	return b
}

func TestHeadlineOpenAPIAndContracts(t *testing.T) {
	srv := headlineServer(t, nil)
	var doc struct {
		Components struct {
			Schemas map[string]struct {
				Properties map[string]map[string]any `json:"properties"`
			} `json:"schemas"`
		} `json:"components"`
	}
	getJSON(t, srv+"/api/openapi.json", &doc)
	props := doc.Components.Schemas["articles"].Properties
	for _, k := range []string{"search_headline", "body_headline", "meta_headline", "raw_headline"} {
		if props[k] == nil || props[k]["readOnly"] != true {
			t.Errorf("articles.%s: %v", k, props[k])
		}
	}
	if props["title_headline"] != nil || props["secret_headline"] != nil {
		t.Error("headline on a column without a full-text filter")
	}
	if d, _ := props["search_headline"]["description"].(string); !strings.Contains(d, "document=<body|meta|raw|title>") {
		t.Errorf("search_headline description: %s", d)
	}
	var raw any
	getJSON(t, srv+"/api/openapi.json", &raw)
	ops := map[string]string{}
	var walk func(v any)
	walk = func(v any) {
		switch x := v.(type) {
		case map[string]any:
			if name, ok := x["name"].(string); ok && x["in"] == "query" {
				if l, ok := x["x-pdbr-filter-ops"].([]any); ok {
					for _, o := range l {
						ops[name] += o.(string) + ","
					}
				}
			}
			for _, e := range x {
				walk(e)
			}
		case []any:
			for _, e := range x {
				walk(e)
			}
		}
	}
	walk(raw)
	for _, k := range []string{"meta", "raw"} {
		if !strings.Contains(ops[k], "wfts") {
			t.Errorf("%s filter ops %q lack fts", k, ops[k])
		}
	}

	if os.Getenv("PDBR_TEST_DATABASE_URL") == "" {
		return
	}
	pub := headlineServer(t, func(c *config.Config) {
		c.Contracts = []confx.Contract{{Name: "internal"}, {Name: "public"}}
	})
	res, body := get(t, hlURL(pub+"/api/public", "articles", "select=id,search_headline(document=body)", "search=fts.cat"))
	wantProblem(t, res, body, 400, "invalid_select")
	var rows []row
	getJSON(t, hlURL(pub+"/api/public", "articles", "select=id,search_headline(document=title)", "search=fts(english).cat"), &rows)
	if len(rows) != 1 {
		t.Errorf("public contract headline: %v", rows)
	}
	getJSON(t, hlURL(pub+"/api/internal", "articles", "select=id,search_headline(document=body)", "search=fts(english).cat"), &rows)
	if len(rows) != 1 {
		t.Errorf("internal contract headline: %v", rows)
	}
}

func TestHeadlineDocumentBound(t *testing.T) {
	long := strings.Repeat("lorem ipsum dolor ", 10) + "the fat cat"
	insert := `INSERT INTO ` + headlineSchema + `.articles (id, title, body, raw) VALUES (4, 'Long', '` + long + `', '{"note": "` + long + `"}')`
	headlines := func(srv string, id int) row {
		var rows []row
		getJSON(t, hlURL(srv+"/api", "articles", "select=id,h:body_headline,r:raw_headline", "body=fts(english).cat", "raw=fts(english).cat", "id=eq."+strconv.Itoa(id)), &rows)
		if len(rows) != 1 {
			t.Fatalf("article %d: rows %v", id, rows)
		}
		return rows[0]
	}

	bounded := headlineServer(t, func(cfg *config.Config) { cfg.Limits.MaxHeadlineDocument = 60 })
	execDDL(t, os.Getenv("PDBR_TEST_DATABASE_URL"), insert)
	if r := headlines(bounded, 4); strings.Contains(r["h"].(string), "<b>") || r["r"] != nil {
		t.Errorf("match past the bound: %v", r)
	}
	if r := headlines(bounded, 1); !strings.Contains(r["h"].(string), "<b>cat</b>") || !strings.Contains(fmt.Sprint(r["r"]), "<b>cats</b>") {
		t.Errorf("match inside the bound: %v", r)
	}

	full := headlineServer(t, nil)
	execDDL(t, os.Getenv("PDBR_TEST_DATABASE_URL"), insert)
	if r := headlines(full, 4); !strings.Contains(r["h"].(string), "<b>cat</b>") || !strings.Contains(fmt.Sprint(r["r"]), "<b>cat</b>") {
		t.Errorf("default bound: %v", r)
	}

	page := []string{"select=id,a:body_headline,b:body_headline(max_words=20)", "body=fts(english).cat", "limit=10"}
	costly := headlineServer(t, func(cfg *config.Config) { cfg.Limits.MaxCost = 1000 })
	res, body := get(t, hlURL(costly+"/api", "articles", page...))
	wantProblem(t, res, body, 400, "cost_exceeded")
	cheap := headlineServer(t, func(cfg *config.Config) {
		cfg.Limits.MaxCost = 1000
		cfg.Limits.MaxHeadlineDocument = 8192
	})
	if res, body := get(t, hlURL(cheap+"/api", "articles", page...)); res.StatusCode != 200 {
		t.Errorf("headlines over an 8192-character bound = %d %s", res.StatusCode, body)
	}
}
