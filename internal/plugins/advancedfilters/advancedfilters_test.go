package advancedfilters

import (
	"context"
	"io"
	"log/slog"
	"testing"

	"github.com/suprbdev/pdbcore/introspect"
	"github.com/suprbdev/pdbcore/testutil"

	"github.com/suprbdev/pdbr/internal/api"
	"github.com/suprbdev/pdbr/internal/inflect"
	"github.com/suprbdev/pdbr/internal/plugin"
)

func build(t *testing.T, p *Plugin, cat *introspect.Catalog, functions bool) *api.Built {
	t.Helper()
	b := api.New(cat, inflect.Default, api.Options{
		Schemas: []string{"public"}, BasePath: "/api", Functions: functions,
		Logger: slog.New(slog.NewTextHandler(io.Discard, nil)),
	})
	if p != nil {
		if err := p.TransformAPI(context.Background(), b); err != nil {
			t.Fatal(err)
		}
	}
	built, err := b.Build()
	if err != nil {
		t.Fatal(err)
	}
	return built
}

// flags are the four Surface flags this plugin owns.
type flags struct{ RelationFilters, RelationOrder, ComputedFilters, ComputedOrder bool }

func ours(s api.Surface) flags {
	return flags{s.RelationFilters, s.RelationOrder, s.ComputedFilters, s.ComputedOrder}
}

func rel(t *testing.T, built *api.Built, path, key string) *api.Relation {
	t.Helper()
	r, _ := built.ByPath[path].Resolve(key, "")
	if r == nil {
		t.Fatalf("relation %s.%s not found", path, key)
	}
	return r
}

func computed(t *testing.T, built *api.Built, path, key string) *api.Computed {
	t.Helper()
	c := built.ByPath[path].Computed[key]
	if c == nil {
		t.Fatalf("computed %s.%s not found", path, key)
	}
	return c
}

func TestMeta(t *testing.T) {
	p := New(DefaultSettings())
	if p.Name() != "advanced_filters" || p.Priority() != 150 {
		t.Fatalf("name/priority = %s/%d", p.Name(), p.Priority())
	}
	if got := plugin.Hooks(p); len(got) != 1 || got[0] != "api" {
		t.Fatalf("hooks = %v", got)
	}
}

func TestSettingsMatrix(t *testing.T) {
	with := func(f func(*Settings)) Settings {
		s := DefaultSettings()
		f(&s)
		return s
	}
	cases := []struct {
		name     string
		settings Settings
		want     flags
	}{
		{"defaults", DefaultSettings(), flags{RelationFilters: true, RelationOrder: true, ComputedFilters: true, ComputedOrder: true}},
		{"relations off", with(func(s *Settings) { s.Relations = false }), flags{ComputedFilters: true, ComputedOrder: true}},
		{"relation_order off", with(func(s *Settings) { s.RelationOrder = false }), flags{RelationFilters: true, ComputedFilters: true, ComputedOrder: true}},
		{"computed off", with(func(s *Settings) { s.Computed = false }), flags{RelationFilters: true, RelationOrder: true}},
		{"all off", with(func(s *Settings) { s.Relations, s.Computed = false, false }), flags{}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			built := build(t, New(tc.settings), testutil.FixtureCatalog(), true)
			if got := ours(built.Surface); got != tc.want {
				t.Fatalf("surface = %+v, want %+v", got, tc.want)
			}
			back := rel(t, built, "users", "posts")
			fwd := rel(t, built, "posts", "users")
			for _, r := range []*api.Relation{back, fwd} {
				if r.Filterable != tc.want.RelationFilters || r.Orderable != tc.want.RelationOrder {
					t.Errorf("%s.%s filterable=%v orderable=%v, want %v/%v", r.From.Path, r.Key, r.Filterable, r.Orderable, tc.want.RelationFilters, tc.want.RelationOrder)
				}
				if !r.Embeddable {
					t.Errorf("%s.%s must stay embeddable", r.From.Path, r.Key)
				}
			}
			c := computed(t, built, "users", "post_count")
			if c.Filterable != tc.want.ComputedFilters || c.Orderable != tc.want.ComputedOrder {
				t.Errorf("post_count filterable=%v orderable=%v", c.Filterable, c.Orderable)
			}
		})
	}
}

func TestWithoutPluginSurfaceIsClosed(t *testing.T) {
	built := build(t, nil, testutil.FixtureCatalog(), true)
	if got := ours(built.Surface); got != (flags{}) {
		t.Fatalf("surface without the plugin = %+v", got)
	}
}

func TestSchemaFunctionsOffClosesComputed(t *testing.T) {
	built := build(t, New(DefaultSettings()), testutil.FixtureCatalog(), false)
	if built.Surface.ComputedFilters || built.Surface.ComputedOrder {
		t.Fatalf("computed surface must follow schema.functions: %+v", built.Surface)
	}
	if !built.Surface.RelationFilters || !built.Surface.RelationOrder {
		t.Fatalf("relations must not depend on schema.functions: %+v", built.Surface)
	}
}

func TestSetReturningComputedNeverFilterable(t *testing.T) {
	built := build(t, New(DefaultSettings()), testutil.FixtureCatalog(), true)
	c := computed(t, built, "users", "tag_words")
	if c.Filterable || c.Orderable {
		t.Fatalf("SETOF computed column must not be filterable/orderable: %+v", c)
	}
}

func fkComment(cat *introspect.Catalog, table, fk, comment string) {
	for _, f := range cat.Table("public", table).ForeignKeys {
		if f.Name == fk {
			f.Comment = comment
		}
	}
}

func fnComment(cat *introspect.Catalog, name, comment string) {
	for _, f := range cat.Functions {
		if f.Schema == "public" && f.Name == name {
			f.Comment = comment
		}
	}
}

func TestOmitFilterOrderOnForeignKey(t *testing.T) {
	cases := []struct {
		comment               string
		filterable, orderable bool
	}{
		{"", true, true},
		{"@omit filter", false, true},
		{"@omit order", true, false},
		{"@omit filter,order\nJoins posts to their author.", false, false},
		{"@omit", false, false},
		{"@behavior -filter", false, true},
		{"@costMultiplier 3", true, true},
	}
	for _, tc := range cases {
		t.Run(tc.comment, func(t *testing.T) {
			cat := testutil.FixtureCatalog()
			fkComment(cat, "posts", "posts_author_id_fkey", tc.comment)
			built := build(t, New(DefaultSettings()), cat, true)
			for _, r := range []*api.Relation{rel(t, built, "users", "posts"), rel(t, built, "posts", "users")} {
				if r.Filterable != tc.filterable || r.Orderable != tc.orderable {
					t.Errorf("%s.%s filterable=%v orderable=%v, want %v/%v", r.From.Path, r.Key, r.Filterable, r.Orderable, tc.filterable, tc.orderable)
				}
				if !r.Embeddable {
					t.Errorf("%s.%s must stay embeddable", r.From.Path, r.Key)
				}
			}
			// other relations are untouched
			if o := rel(t, built, "posts", "comments"); !o.Filterable || !o.Orderable {
				t.Errorf("unrelated relation affected: %+v", o)
			}
		})
	}
}

func TestOmitFilterOrderOnComputed(t *testing.T) {
	cases := []struct {
		comment               string
		filterable, orderable bool
	}{
		{"", true, true},
		{"@omit filter", false, true},
		{"@omit order", true, false},
		{"@omit filter order", false, false},
		{"@omit", false, false},
		{"@behavior -order\nNumber of posts.", true, false},
		{"Number of posts.\n@omit filter", true, true},
	}
	for _, tc := range cases {
		t.Run(tc.comment, func(t *testing.T) {
			cat := testutil.FixtureCatalog()
			fnComment(cat, "users_post_count", tc.comment)
			built := build(t, New(DefaultSettings()), cat, true)
			c := computed(t, built, "users", "post_count")
			if c.Filterable != tc.filterable || c.Orderable != tc.orderable {
				t.Errorf("post_count filterable=%v orderable=%v, want %v/%v", c.Filterable, c.Orderable, tc.filterable, tc.orderable)
			}
		})
	}
}

func TestOptInModes(t *testing.T) {
	cat := testutil.FixtureCatalog()
	fkComment(cat, "posts", "posts_author_id_fkey", "@filterable")
	fkComment(cat, "comments", "comments_post_id_fkey", "@sortable\n@omit filter")
	fnComment(cat, "users_post_count", "@filterable")
	built := build(t, New(Settings{Relations: true, Computed: true, RelationOrder: true, RelationsOptIn: true, ComputedOptIn: true}), cat, true)
	if got := ours(built.Surface); got != (flags{RelationFilters: true, RelationOrder: true, ComputedFilters: true, ComputedOrder: true}) {
		t.Fatalf("surface = %+v", got)
	}
	check := func(r *api.Relation, filterable, orderable bool) {
		t.Helper()
		if r.Filterable != filterable || r.Orderable != orderable {
			t.Errorf("%s.%s filterable=%v orderable=%v, want %v/%v", r.From.Path, r.Key, r.Filterable, r.Orderable, filterable, orderable)
		}
	}
	check(rel(t, built, "users", "posts"), true, true)
	check(rel(t, built, "posts", "users"), true, true)
	check(rel(t, built, "posts", "comments"), false, true)
	check(rel(t, built, "comments", "posts"), false, true)
	check(rel(t, built, "users", "places"), false, false)
	check(rel(t, built, "places", "users"), false, false)
	if c := computed(t, built, "users", "post_count"); !c.Filterable || !c.Orderable {
		t.Errorf("tagged computed must be exposed: %+v", c)
	}

	untagged := build(t, New(Settings{Relations: true, Computed: true, RelationOrder: true, ComputedOptIn: true}), testutil.FixtureCatalog(), true)
	if c := computed(t, untagged, "users", "post_count"); c.Filterable || c.Orderable {
		t.Errorf("untagged computed must be closed under computed_opt_in: %+v", c)
	}
	if r := rel(t, untagged, "users", "posts"); !r.Filterable || !r.Orderable {
		t.Errorf("relations_opt_in off must keep relations open: %+v", r)
	}
}

// omitMany models the smart_comments plugin (priority 50) having removed
// the backward embed for @omit many: the backward filter/order go with it,
// the forward side stays.
type omitMany struct{}

func (omitMany) Name() string  { return "omit-many" }
func (omitMany) Priority() int { return 50 }
func (omitMany) TransformAPI(_ context.Context, b *api.Builder) error {
	for _, r := range b.Resource("public", "users").Relations["posts"] {
		r.Embeddable = false
	}
	return nil
}

func TestBackwardOmittedDropsFilters(t *testing.T) {
	cat := testutil.FixtureCatalog()
	b := api.New(cat, inflect.Default, api.Options{Schemas: []string{"public"}, BasePath: "/api", Functions: true, Logger: slog.New(slog.NewTextHandler(io.Discard, nil))})
	reg := plugin.NewRegistry(omitMany{}, New(DefaultSettings()))
	if err := reg.TransformAPI(context.Background(), nil, b); err != nil {
		t.Fatal(err)
	}
	built, err := b.Build()
	if err != nil {
		t.Fatal(err)
	}
	if r := rel(t, built, "users", "posts"); r.Filterable || r.Orderable || r.Embeddable {
		t.Errorf("backward relation must be closed: %+v", r)
	}
	if r := rel(t, built, "posts", "users"); !r.Filterable || !r.Orderable {
		t.Errorf("forward relation must stay open: %+v", r)
	}
}

func TestEarlierClearedFlagsStayCleared(t *testing.T) {
	cat := testutil.FixtureCatalog()
	b := api.New(cat, inflect.Default, api.Options{Schemas: []string{"public"}, BasePath: "/api", Functions: true, Logger: slog.New(slog.NewTextHandler(io.Discard, nil))})
	b.Resource("public", "posts").Relations["users"][0].Filterable = false
	b.Resource("public", "users").Computed["post_count"].Orderable = false
	if err := New(DefaultSettings()).TransformAPI(context.Background(), b); err != nil {
		t.Fatal(err)
	}
	if b.Resource("public", "posts").Relations["users"][0].Filterable {
		t.Error("relation Filterable turned back on")
	}
	if b.Resource("public", "users").Computed["post_count"].Orderable {
		t.Error("computed Orderable turned back on")
	}
}
