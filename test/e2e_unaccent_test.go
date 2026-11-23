package e2e

import (
	"net/url"
	"slices"
	"strings"
	"testing"

	"github.com/suprbdev/pdbr/internal/config"
)

const (
	unaccentSchema    = "pdbr_unaccent"
	unaccentExtSchema = "pdbr_unaccent_ext"
)

// unaccentDDL: title has no index (filters.allow_columns admits it), name a
// B-tree index, which serves none of the unaccent operators.
const unaccentDDL = `
CREATE TABLE pdbr_unaccent.dishes (
  id    int PRIMARY KEY,
  title text,
  name  varchar(40)
);
CREATE INDEX dishes_name_idx ON pdbr_unaccent.dishes (name);
INSERT INTO pdbr_unaccent.dishes VALUES
  (1, 'Crème Brûlée', 'a'),
  (2, 'São Paulo', 'b'),
  (3, 'Zoë Müller', 'c'),
  (4, 'creme fraiche', 'd'),
  (5, '50% off_sale', 'e');
`

func unaccentServer(t *testing.T, mutate func(*config.Config)) string {
	t.Helper()
	return extensionServer(t, unaccentSchema, unaccentExtSchema, []string{"unaccent"}, unaccentDDL, func(cfg *config.Config) {
		cfg.Filters.AllowColumns = map[string][]string{unaccentSchema + ".dishes": {"title"}}
		if mutate != nil {
			mutate(cfg)
		}
	})
}

// TestUnaccentFilters: the unaccent operators ignore accents and case with
// the extension off the search_path, take not. and logic lists, match
// unaccent_sw/unaccent_includes values literally, and need
// filters.allow_columns under filters.indexed_only.
func TestUnaccentFilters(t *testing.T) {
	srv := unaccentServer(t, nil)
	for q, want := range map[string][]int{
		"title=unaccent_eq." + url.QueryEscape("CREME brulee"):                              {1},
		"title=unaccent_ilike." + url.QueryEscape("*crème*"):                                {1, 4},
		"title=unaccent_ilike." + url.QueryEscape("z_e%"):                                   {3},
		"title=unaccent_sw.sao":                                                             {2},
		"title=unaccent_includes.MULLER":                                                    {3},
		"title=not.unaccent_includes.creme":                                                 {2, 3, 5},
		"title=unaccent_includes." + url.QueryEscape("%"):                                   {5},
		"title=unaccent_includes._":                                                         {5},
		"title=unaccent_sw." + url.QueryEscape("_"):                                         {},
		"or=(title.unaccent_sw.zoe,title.unaccent_eq." + url.QueryEscape("SAO PAULO") + ")": {2, 3},
		"and=(title.unaccent_includes.e,title.not.unaccent_sw.c)":                           {3, 5},
	} {
		var rows []row
		getJSON(t, srv+"/api/dishes?select=id&order=id&"+q, &rows)
		if got := ids(rows, "id"); !eq(got, want) {
			t.Errorf("%s = %v, want %v", q, got, want)
		}
	}
	res, out := get(t, srv+"/api/dishes?name=unaccent_eq.a")
	wantProblem(t, res, out, 400, "column_not_filterable")

	off := unaccentServer(t, func(cfg *config.Config) { cfg.Filters.IndexedOnly = false })
	var rows []row
	getJSON(t, off+"/api/dishes?select=id&name=unaccent_eq.A", &rows)
	if !eq(ids(rows, "id"), []int{1}) {
		t.Errorf("name=unaccent_eq.A without the policy = %v", rows)
	}

	body, doc := fetchOpenAPI(t, srv)
	validateOpenAPI(t, oasSchema(t), body)
	params := opParams(pathOp(doc, "/dishes", "get"))
	ops := func(col string) []string {
		var out []string
		raw, _ := params["query:"+col]["x-pdbr-filter-ops"].([]any)
		for _, o := range raw {
			out = append(out, o.(string))
		}
		return out
	}
	if got := strings.Join(ops("title"), ","); !strings.HasSuffix(got, "unaccent_eq,unaccent_ilike,unaccent_sw,unaccent_includes") {
		t.Errorf("title ops = %s", got)
	}
	if slices.Contains(ops("name"), "unaccent_eq") {
		t.Errorf("indexed name documents unaccent ops: %v", ops("name"))
	}
}
