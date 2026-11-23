package api

import (
	"reflect"
	"strings"
	"testing"

	"github.com/suprbdev/pdbcore/introspect"
	"github.com/suprbdev/pdbcore/testutil"

	"github.com/suprbdev/pdbr/internal/testcatalog"
)

// headlineKeys lists r's headline pseudo-columns as "key=documents", sorted
// ("self" for a document column).
func headlineKeys(r *Resource) []string {
	var out []string
	for _, k := range sortedKeys(r.Pseudo) {
		pc := r.Pseudo[k]
		if pc.Headline == nil {
			continue
		}
		docs := "self"
		if pc.Headline.Documents != nil {
			docs = strings.Join(sortedKeys(pc.Headline.Documents), "|")
		}
		out = append(out, k+"="+docs)
	}
	return out
}

func TestContribHeadlines(t *testing.T) {
	b := build(t, testcatalog.Contrib(testutil.FixtureCatalog()), contribOptions(true))
	if got, want := headlineKeys(b.ByPath["kb.docs"]), []string{"body_headline=self", "search_headline=body|email|title"}; !reflect.DeepEqual(got, want) {
		t.Errorf("kb.docs headlines = %v, want %v", got, want)
	}
	if got, want := headlineKeys(b.ByPath["kb.articles"]), []string{"search_headline=meta|raw|title"}; !reflect.DeepEqual(got, want) {
		t.Errorf("kb.articles headlines = %v, want %v", got, want)
	}
	if got := headlineKeys(b.ByPath["users"]); len(got) != 0 {
		t.Errorf("users headlines = %v", got)
	}
	props := b.OpenAPI.Components.Schemas["kb.docs"].Properties
	if p := props["search_headline"]; p == nil || !p.ReadOnly || !reflect.DeepEqual(p.Type, []string{"string", "null"}) ||
		!strings.Contains(p.Description, "select=search_headline(document=<body|email|title>[,<max_words|") {
		t.Errorf("search_headline property = %+v", p)
	}
	if sel := paramByName(b.OpenAPI.Paths["/kb/docs"].Get.Parameters, "select").Description; !strings.Contains(sel, "body_headline[(<max_words") {
		t.Errorf("select description: %s", sel)
	}

	off := build(t, testcatalog.Contrib(testutil.FixtureCatalog()), contribOptions(false))
	if got, want := headlineKeys(off.ByPath["kb.articles"]), []string{"meta_headline=self", "raw_headline=self", "search_headline=meta|raw|title", "title_headline=self"}; !reflect.DeepEqual(got, want) {
		t.Errorf("kb.articles headlines without the policy = %v, want %v", got, want)
	}
	if p := off.OpenAPI.Components.Schemas["kb.articles"].Properties["meta_headline"]; p == nil || p.Type != nil || !p.ReadOnly {
		t.Errorf("meta_headline property = %+v", p)
	}
	ops := strings.Join(paramByName(off.OpenAPI.Paths["/kb/articles"].Get.Parameters, "meta").XFilterOps, ",")
	if !strings.Contains(ops, "fts,plfts,phfts,wfts") || !strings.Contains(ops, "cs") {
		t.Errorf("meta filter ops = %s", ops)
	}
}

func TestContribHeadlinesGating(t *testing.T) {
	cat := testcatalog.Contrib(testutil.FixtureCatalog())
	cat.Table(testcatalog.ContribSchema, "labels").Columns = append(cat.Table(testcatalog.ContribSchema, "labels").Columns,
		&introspect.Column{Name: "search", Position: 4, PGType: "tsvector", TypeSchema: "pg_catalog"},
		&introspect.Column{Name: "search_headline", Position: 5, PGType: "text", TypeSchema: "pg_catalog"})
	opts := contribOptions(false)
	b := New(cat, nil, opts)
	arts := b.Resources["kb.articles"]
	b.RemoveField(arts, "title", true)
	b.RemoveField(arts, "meta", true)
	b.RemoveFilterColumn(b.Resources["kb.docs"], "search")
	built, err := b.Build()
	if err != nil {
		t.Fatal(err)
	}
	if got, want := headlineKeys(built.ByPath["kb.articles"]), []string{"raw_headline=self", "search_headline=raw"}; !reflect.DeepEqual(got, want) {
		t.Errorf("hidden documents: %v, want %v", got, want)
	}
	if got, want := headlineKeys(built.ByPath["kb.docs"]), []string{"body_headline=self", "email_headline=self", "title_headline=self"}; !reflect.DeepEqual(got, want) {
		t.Errorf("unfilterable tsvector: %v, want %v", got, want)
	}
	if got := headlineKeys(built.ByPath["kb.labels"]); len(got) != 1 || got[0] != "search_headline_headline=self" {
		t.Errorf("a real column owning the key must win: %v", got)
	}
}
