package e2e

import (
	"net/url"
	"reflect"
	"slices"
	"strings"
	"testing"

	"github.com/suprbdev/pdbr/internal/config"
)

const (
	derivedSchema    = "pdbr_derived"
	derivedExtSchema = "pdbr_derived_ext"
)

// derivedDDL covers hstore value lookups, the ltree and intarray
// pseudo-columns, intarray's query_int and fuzzystrmatch. name has no index
// (filters.allow_columns admits it); tags has a gin__int_ops index, nums
// none. Row 5's name is longer than fuzzystrmatch's 255-character limit.
const derivedDDL = `
CREATE TABLE pdbr_derived.items (
  id    int PRIMARY KEY,
  name  text,
  attrs pdbr_derived_ext.hstore,
  path  pdbr_derived_ext.ltree,
  paths pdbr_derived_ext.ltree[],
  tags  int4[],
  nums  int4[]
);
CREATE INDEX items_attrs_idx ON pdbr_derived.items USING gin (attrs);
CREATE INDEX items_path_idx ON pdbr_derived.items USING gist (path);
CREATE INDEX items_tags_idx ON pdbr_derived.items USING gin (tags pdbr_derived_ext.gin__int_ops);
INSERT INTO pdbr_derived.items VALUES
  (1, 'Robert', 'color=>red, size=>L', 'top.science.astronomy', '{top.science.a,top.science.b}', '{1,2,3}', '{1}'),
  (2, 'Rupert', 'color=>blue, size=>NULL', 'top.science', '{top.hobbies}', '{2,4}', '{}'),
  (3, 'Smyth', 'size=>S', 'top', '{}', '{5}', NULL),
  (4, 'Knight', NULL, '', NULL, '{}', '{1,2}'),
  (5, repeat('x', 300), NULL, NULL, NULL, NULL, NULL);
`

func derivedServer(t *testing.T, mutate func(*config.Config)) string {
	t.Helper()
	return extensionServer(t, derivedSchema, derivedExtSchema, []string{"hstore", "ltree", "fuzzystrmatch", "intarray"}, derivedDDL,
		func(cfg *config.Config) {
			cfg.Filters.AllowColumns = map[string][]string{derivedSchema + ".items": {"name", "path", "tags"}}
			if mutate != nil {
				mutate(cfg)
			}
		})
}

func itemIDs(t *testing.T, srv, query string) []int {
	t.Helper()
	var rows []row
	getJSON(t, srv+"/api/items?select=id&"+query, &rows)
	return ids(rows, "id")
}

// TestContribDerivedFilters: hstore value paths, hk_def, the nlevel and
// icount pseudo-columns, query_int and the fuzzystrmatch operators against
// the live extensions, installed off the search_path.
func TestContribDerivedFilters(t *testing.T) {
	srv := derivedServer(t, nil)
	for q, want := range map[string][]int{
		"attrs->>color=eq.red&order=id":                                   {1},
		"attrs->color=in.(red,blue)&order=id":                             {1, 2},
		"attrs->>size=is.null&order=id":                                   {2, 4, 5},
		"attrs->size=like.*&order=id":                                     {1, 3},
		"order=attrs->color.desc,id&attrs->color=not.is.null":             {1, 2},
		"~path_nlevel=gte.2&order=id":                                     {1, 2},
		"or=(~path_nlevel.eq.0,~path_nlevel.eq.3)&order=id":               {1, 4},
		"order=~path_nlevel.desc,id":                                      {1, 2, 3, 4, 5},
		"path=descendant.top&~path_nlevel=lt.3&order=id":                  {2, 3},
		"tags=query_int." + url.QueryEscape("1&(2|4)") + "&order=id":      {1},
		"tags=query_int.!5&order=id":                                      {1, 2, 4},
		"tags=not.query_int.2&order=id":                                   {3, 4},
		"~tags_icount=gte.1&order=~tags_icount.desc,id":                   {1, 2, 3},
		"name=levenshtein_distance_lt.2&~levenshtein=Robert&order=id":     {1},
		"name=levenshtein_distance_lt.3&~levenshtein=Robert&order=id":     {1, 2},
		"name=not.levenshtein_distance_lt.3&~levenshtein=Robert&order=id": {3, 4},
		"name=soundex.Rupert&order=id":                                    {1, 2},
		"name=metaphone.night&order=id":                                   {4},
		"name=dmetaphone.Smith&order=id":                                  {3},
		"or=(name.soundex.Smith,name.dmetaphone.Nite)&order=id":           {3, 4},
		"order=name.levenshtein_distance,id&~levenshtein=Robert&limit=3":  {1, 2, 4},
	} {
		if got := itemIDs(t, srv, q); !eq(got, want) {
			t.Errorf("%s = %v, want %v", q, got, want)
		}
	}

	for q, code := range map[string]string{
		"attrs=hk_def.size":                             "column_not_filterable",
		"nums=query_int.1":                              "column_not_filterable",
		"order=~nums_icount":                            "column_not_orderable",
		"~path_parent=eq.top":                           "column_not_filterable",
		"name=levenshtein_distance_lt.2":                "invalid_parameter",
		"name=levenshtein_distance_lt.x&~levenshtein=a": "invalid_filter",
		"~levenshtein=Robert":                           "invalid_parameter",
		"tags=query_int." + url.QueryEscape("1 &"):      "pg:42601",
		"select=attrs->a->b":                            "invalid_select",
		"select=~path_subpath":                          "invalid_select",
		"select=~path_subpath(offset=70000)":            "invalid_select",
	} {
		res, out := get(t, srv+"/api/items?"+q)
		wantProblem(t, res, out, 400, code)
	}

	off := derivedServer(t, func(cfg *config.Config) { cfg.Filters.IndexedOnly = false })
	for q, want := range map[string][]int{
		"attrs=hk_def.size&order=id":      {1, 3},
		"attrs=not.hk_def.color&order=id": {3},
		"nums=query_int.1&order=id":       {1, 4},
		"order=~nums_icount.desc,id":      {4, 1, 2, 3, 5},
	} {
		if got := itemIDs(t, off, q); !eq(got, want) {
			t.Errorf("indexed_only off: %s = %v, want %v", q, got, want)
		}
	}
}

// TestContribDerivedSelect: the pseudo-columns, derived values with their
// arguments, hstore values by key and the Levenshtein score in responses.
func TestContribDerivedSelect(t *testing.T) {
	srv := derivedServer(t, nil)
	var rows []row
	q := "select=id,~path_nlevel,~path_parent,~paths_lca,anc:~path_lca(path=top.science.zoology),~path_subpath(offset=1,length=1)," +
		"last:~path_subpath(offset=-1),~tags_icount,attrs->color,size:attrs->>size,~name_levenshtein_distance&~levenshtein=Robert&order=id"
	getJSON(t, srv+"/api/items?"+q, &rows)
	want := []row{
		{"id": 1.0, "~path_nlevel": 3.0, "~path_parent": "top.science", "~paths_lca": "top.science", "anc": "top.science",
			"~path_subpath": "science", "last": "astronomy", "~tags_icount": 3.0, "color": "red", "size": "L", "~name_levenshtein_distance": 0.0},
		{"id": 2.0, "~path_nlevel": 2.0, "~path_parent": "top", "~paths_lca": "top", "anc": "top",
			"~path_subpath": "science", "last": "science", "~tags_icount": 2.0, "color": "blue", "size": nil, "~name_levenshtein_distance": 2.0},
		{"id": 3.0, "~path_nlevel": 1.0, "~path_parent": "", "~paths_lca": nil, "anc": "",
			"~path_subpath": nil, "last": "top", "~tags_icount": 1.0, "color": nil, "size": "S", "~name_levenshtein_distance": 6.0},
		{"id": 4.0, "~path_nlevel": 0.0, "~path_parent": nil, "~paths_lca": nil, "anc": nil,
			"~path_subpath": nil, "last": nil, "~tags_icount": 0.0, "color": nil, "size": nil, "~name_levenshtein_distance": 5.0},
		{"id": 5.0, "~path_nlevel": nil, "~path_parent": nil, "~paths_lca": nil, "anc": nil,
			"~path_subpath": nil, "last": nil, "~tags_icount": nil, "color": nil, "size": nil, "~name_levenshtein_distance": nil},
	}
	if !reflect.DeepEqual(rows, want) {
		t.Errorf("rows =\n%v\nwant\n%v", rows, want)
	}

	res, out := get(t, srv+"/api/items?select=~name_levenshtein_distance")
	wantProblem(t, res, out, 400, "invalid_parameter")

	body, doc := fetchOpenAPI(t, srv)
	validateOpenAPI(t, oasSchema(t), body)
	params := opParams(pathOp(doc, "/items", "get"))
	ops := func(col string) []string {
		var out []string
		raw, _ := params["query:"+col]["x-pdbr-filter-ops"].([]any)
		for _, o := range raw {
			out = append(out, o.(string))
		}
		return out
	}
	if got := ops("tags"); !slices.Contains(got, "query_int") {
		t.Errorf("tags ops = %v", got)
	}
	if got := ops("name"); !slices.Contains(got, "levenshtein_distance_lt") || !slices.Contains(got, "dmetaphone") {
		t.Errorf("name ops = %v", got)
	}
	if got := ops("~path_nlevel"); !slices.Contains(got, "gte") {
		t.Errorf("~path_nlevel ops = %v", got)
	}
	if params["query:~levenshtein"] == nil {
		t.Error("levenshtein parameter not documented")
	}
	if params["query:~nums_icount"] != nil {
		t.Error("~nums_icount documented as a filter although the policy refuses it")
	}
	if order, _ := params["query:order"]["description"].(string); !strings.Contains(order, "name.levenshtein_distance") || !strings.Contains(order, "~path_nlevel") {
		t.Errorf("order description: %s", order)
	}
}
