package api

import (
	"reflect"
	"strings"
	"testing"

	"github.com/suprbdev/pdbcore/introspect"
	"github.com/suprbdev/pdbcore/testutil"

	"github.com/suprbdev/pdbr/internal/inflect"
	"github.com/suprbdev/pdbr/internal/testcatalog"
)

// scoreKeys lists r's score pseudo-columns as "key=param:expr", sorted.
func scoreKeys(r *Resource) []string {
	var out []string
	for _, k := range sortedKeys(r.Pseudo) {
		if pc := r.Pseudo[k]; pc.Score != nil {
			out = append(out, k+"="+pc.Score.Param+":"+pc.Score.Expr.Name)
		}
	}
	return out
}

func TestContribScores(t *testing.T) {
	b := build(t, testcatalog.Contrib(testutil.FixtureCatalog()), contribOptions(true))
	docs := b.ByPath["kb.docs"]
	want := []string{
		"~body_levenshtein_distance=~levenshtein:levenshteinDistance",
		"~body_trgm_similarity=~trgm:similarity", "~body_trgm_strict_word_similarity=~trgm:strictWordSimilarity", "~body_trgm_word_similarity=~trgm:wordSimilarity",
		"~title_trgm_similarity=~trgm:similarity", "~title_trgm_strict_word_similarity=~trgm:strictWordSimilarity", "~title_trgm_word_similarity=~trgm:wordSimilarity",
	}
	if got := scoreKeys(docs); !reflect.DeepEqual(got, want) {
		t.Errorf("kb.docs scores = %v\nwant %v", got, want)
	}
	docsWant := want
	emb := b.ByPath["kb.embeddings"]
	want = []string{
		"~code_hamming_distance=~vec:hammingDistance", "~code_jaccard_distance=~vec:jaccardDistance",
		"~emb_cosine_distance=~vec:cosineDistance", "~emb_l1_distance=~vec:l1Distance", "~emb_l2_distance=~vec:l2Distance", "~emb_negative_inner_product=~vec:negativeInnerProduct",
		"~half_cosine_distance=~vec:cosineDistance", "~half_l1_distance=~vec:l1Distance", "~half_l2_distance=~vec:l2Distance", "~half_negative_inner_product=~vec:negativeInnerProduct",
		"~sparse_cosine_distance=~vec:cosineDistance", "~sparse_l1_distance=~vec:l1Distance", "~sparse_l2_distance=~vec:l2Distance", "~sparse_negative_inner_product=~vec:negativeInnerProduct",
	}
	if got := scoreKeys(emb); !reflect.DeepEqual(got, want) {
		t.Errorf("kb.embeddings scores = %v\nwant %v", got, want)
	}
	if len(scoreKeys(b.ByPath["users"])) != 0 {
		t.Errorf("users scores = %v", scoreKeys(b.ByPath["users"]))
	}

	props := b.OpenAPI.Components.Schemas["kb.embeddings"].Properties
	if p := props["~emb_cosine_distance"]; p == nil || !reflect.DeepEqual(p.Type, []string{"number", "null"}) || !p.ReadOnly || !strings.Contains(p.Description, "~vec=") {
		t.Errorf("~emb_cosine_distance property = %+v", p)
	}
	get := b.OpenAPI.Paths["/kb/docs"].Get
	if sel := paramByName(get.Parameters, "select").Description; !strings.Contains(sel, "Scores against ~near=, ~trgm=, ~vec=, ~latlon= or ~levenshtein=") || !strings.Contains(sel, "~title_trgm_similarity") {
		t.Errorf("select description: %s", sel)
	}
	if paramByName(get.Parameters, "~trgm") == nil {
		t.Error("kb.docs lacks trgm")
	}

	cat := testcatalog.Contrib(testutil.FixtureCatalog())
	cat.Table(testcatalog.ContribSchema, "embeddings").Indexes = nil
	cat.Table(testcatalog.ContribSchema, "docs").Columns = append(cat.Table(testcatalog.ContribSchema, "docs").Columns,
		&introspect.Column{Name: "trgm", Position: 20, PGType: "int4", TypeSchema: "pg_catalog"})
	b = build(t, cat, contribOptions(true))
	if got := scoreKeys(b.ByPath["kb.docs"]); !reflect.DeepEqual(got, docsWant) {
		t.Errorf("a column named trgm changed the scores: %v", got)
	}
	got := scoreKeys(b.ByPath["kb.embeddings"])
	for _, k := range got {
		if strings.HasPrefix(k, "code_") {
			t.Errorf("unindexed bit column scored under indexed_only: %v", got)
		}
	}
	if len(got) != 12 {
		t.Errorf("vector scores without indexes = %v", got)
	}
	b = build(t, cat, contribOptions(false))
	if got := scoreKeys(b.ByPath["kb.embeddings"]); len(got) != 18 || !strings.HasPrefix(got[0], "~code_hamming_distance") {
		t.Errorf("indexed_only off: %v", got)
	}
}

func TestScoresRemovedWithField(t *testing.T) {
	b := New(testcatalog.Contrib(testutil.FixtureCatalog()), inflect.Default, contribOptions(true))
	emb := b.Resources["kb.embeddings"]
	b.RemoveField(emb, "emb", true)
	built, err := b.Build()
	if err != nil {
		t.Fatal(err)
	}
	for _, k := range scoreKeys(built.ByPath["kb.embeddings"]) {
		if strings.HasPrefix(k, "emb_") {
			t.Errorf("score of a removed column: %s", k)
		}
	}
}

func TestScoreKey(t *testing.T) {
	for in, want := range map[string]string{"similarity": "similarity", "l2Distance": "l2_distance", "negativeInnerProduct": "negative_inner_product", "strictWordSimilarity": "strict_word_similarity"} {
		if got := ScoreKey(in); got != want {
			t.Errorf("ScoreKey(%s) = %s, want %s", in, got, want)
		}
	}
}
