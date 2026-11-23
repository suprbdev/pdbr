package parse

import (
	"testing"

	"github.com/suprbdev/pdbcore/introspect"
	"github.com/suprbdev/pdbcore/testutil"

	"github.com/suprbdev/pdbr/internal/plan"
)

func typeName(t *plan.TypeRef) string {
	if t == nil {
		return ""
	}
	return t.Schema + "." + t.Name
}

var aggOn = func() Options {
	o := allOn
	o.Aggregates, o.SpatialAggregates = true, true
	return o
}()

func TestAggregateSelect(t *testing.T) {
	b := buildFixture(t, false)
	parse := func(t *testing.T, url string) *plan.Request {
		t.Helper()
		p, err := doParse(t, b, req{url: url}, aggOn)
		if err != nil {
			t.Fatalf("%s: %v", url, err)
		}
		return p
	}
	t.Run("functions, aliases and casts", func(t *testing.T) {
		p := parse(t, "/api/users?select=mood,count(),n:id.count(),total:balance::numeric.sum()::text,id.avg(),created_at.max(),email.min()")
		f := p.Select.Fields
		if len(f) != 7 || f[0].Agg != nil || f[0].Column.Name != "mood" || !p.Select.Aggregated() {
			t.Fatalf("fields = %+v", f)
		}
		want := []struct {
			fn         plan.AggFunc
			col, alias string
			in, out    string
		}{
			{plan.AggCount, "", "", "", ""},
			{plan.AggCount, "id", "n", "", ""},
			{plan.AggSum, "balance", "total", "pg_catalog.numeric", "pg_catalog.text"},
			{plan.AggAvg, "id", "", "", ""},
			{plan.AggMax, "created_at", "", "", ""},
			{plan.AggMin, "email", "", "", ""},
		}
		for i, w := range want {
			got := f[i+1]
			col := ""
			if got.Column != nil {
				col = got.Column.Name
			}
			if got.Agg == nil || got.Agg.Func != w.fn || col != w.col || got.Alias != w.alias || typeName(got.Agg.InputCast) != w.in || typeName(got.Agg.OutputCast) != w.out {
				t.Errorf("field %d = %+v agg %+v, want %+v", i+1, got, got.Agg, w)
			}
		}
		if f[1].Key(p.Resource) != "count" || f[2].Key(p.Resource) != "n" || f[4].Key(p.Resource) != "avg" {
			t.Errorf("keys = %q %q %q", f[1].Key(p.Resource), f[2].Key(p.Resource), f[4].Key(p.Resource))
		}
	})
	t.Run("legacy bare count and cast spellings", func(t *testing.T) {
		p := parse(t, "/api/users?select=id,posts(count)")
		if a := p.Select.Embeds[0].Node.Fields[0].Agg; a == nil || a.Func != plan.AggCount {
			t.Fatalf("legacy count = %+v", p.Select.Embeds[0].Node.Fields)
		}
		p = parse(t, "/api/users?select=id::INT8.sum(),balance.avg()::Float8,s:id.max()::integer,email::mood.max(),m:mood.min()::text")
		for i, want := range []string{"pg_catalog.int8", "pg_catalog.float8", "pg_catalog.int4", "public.mood", "pg_catalog.text"} {
			a := p.Select.Fields[i].Agg
			if got := typeName(a.InputCast) + typeName(a.OutputCast); got != want {
				t.Errorf("field %d cast = %q, want %q", i, got, want)
			}
		}
	})
	t.Run("embeds, order and paging", func(t *testing.T) {
		p := parse(t, "/api/users?select=id,posts(author_id,count())&posts.order=author_id.desc&posts.limit=2&limit=5")
		e := p.Select.Embeds[0]
		if p.Select.Aggregated() || !p.Select.Aggregates() || !e.Node.Aggregated() || e.Page.Limit != 2 || len(e.Order) != 1 {
			t.Fatalf("embed = %+v", e)
		}
		p = parse(t, "/api/posts?select=author_id,count()&order=author_id.desc&limit=2&offset=1")
		if !p.Select.Aggregated() || p.Page.Limit != 2 || p.Page.Offset != 1 || !p.Order[0].Desc {
			t.Fatalf("plan = %+v", p)
		}
		parse(t, "/api/events?select=*,count()&order=name")
		parse(t, "/api/rpc/search_posts?term=x&select=author_id,count()&order=author_id")
	})
	t.Run("spatial", func(t *testing.T) {
		p := parse(t, "/api/places?select=owner_id,location.extent(),u:location.union(),c:area.collect(),m:location.centroid(),location.count()")
		for i, fn := range []plan.AggFunc{plan.AggExtent, plan.AggUnion, plan.AggCollect, plan.AggCentroid, plan.AggCount} {
			if a := p.Select.Fields[i+1].Agg; a == nil || a.Func != fn {
				t.Errorf("field %d = %+v", i+1, p.Select.Fields[i+1])
			}
		}
		off := aggOn
		off.SpatialAggregates = false
		_, err := doParse(t, b, req{url: "/api/places?select=location.extent()"}, off)
		wantCode(t, err, CodeInvalidSelect)
		if _, err := doParse(t, b, req{url: "/api/places?select=location.count()"}, off); err != nil {
			t.Fatal(err)
		}
	})
	t.Run("disabled", func(t *testing.T) {
		for _, u := range []string{
			"/api/posts?select=count()",
			"/api/posts?select=id.sum()",
			"/api/users?select=id,posts(count)",
			"/api/places?select=location.extent()",
		} {
			_, err := doParse(t, b, req{url: u}, allOn)
			wantCode(t, err, CodeAggregatesDisabled)
		}
	})
	t.Run("errors", func(t *testing.T) {
		cases := []struct{ url, code string }{
			{"/api/users?select=email.sum()", CodeInvalidSelect},
			{"/api/users?select=settings.max()", CodeInvalidSelect},
			{"/api/users?select=address.max()", CodeInvalidSelect},
			{"/api/users?select=tags.sum()", CodeInvalidSelect},
			{"/api/users?select=email::text.avg()", CodeInvalidSelect},
			{"/api/places?select=location.sum()", CodeInvalidSelect},
			{"/api/places?select=location.max()", CodeInvalidSelect},
			{"/api/users?select=id.extent()", CodeInvalidSelect},
			{"/api/places?select=location::geometry.extent()", CodeInvalidSelect},
			{"/api/users?select=nope.sum()", CodeUnknownColumn},
			{"/api/users?select=post_count.sum()", CodeInvalidSelect},
			{"/api/users?select=id.sum(x)", CodeInvalidSelect},
			{"/api/users?select=id::in-t.sum()", CodeInvalidSelect},
			{"/api/users?select=id.sum()::", CodeInvalidSelect},
			{"/api/users?select=id.sum()::1x", CodeInvalidSelect},
			{"/api/users?select=id.sum()!inner", CodeInvalidSelect},
			{"/api/users?select=posts(id)::text", CodeInvalidSelect},
			{"/api/users?select=id.sum(),balance.sum()", CodeInvalidSelect},
			{"/api/users?select=*,id.sum(),email.min(),id:count()", CodeInvalidSelect},
			{"/api/users/1?select=count()", CodeInvalidSelect},
			{"/api/posts?select=id,users(count())", CodeInvalidSelect},
			{"/api/posts?select=count(),users(mood)", CodeInvalidSelect},
			{"/api/users?select=count(),posts!inner(id)", CodeInvalidSelect},
			{"/api/users?select=id,posts(count(),comments(id))", CodeInvalidSelect},
			{"/api/users?select=tag_words,count()", CodeInvalidSelect},
			{"/api/users?select=id.max()::regrole", CodeInvalidSelect},
			{"/api/users?select=id.max()::regclass", CodeInvalidSelect},
			{"/api/users?select=id.max()::regproc", CodeInvalidSelect},
			{"/api/users?select=id.max()::regprocedure", CodeInvalidSelect},
			{"/api/users?select=id.max()::regoper", CodeInvalidSelect},
			{"/api/users?select=id.max()::regoperator", CodeInvalidSelect},
			{"/api/users?select=id.max()::regnamespace", CodeInvalidSelect},
			{"/api/users?select=id.max()::regtype", CodeInvalidSelect},
			{"/api/users?select=id.max()::regconfig", CodeInvalidSelect},
			{"/api/users?select=id.max()::regdictionary", CodeInvalidSelect},
			{"/api/users?select=id.max()::regcollation", CodeInvalidSelect},
			{"/api/users?select=id::regclass.max()", CodeInvalidSelect},
			{"/api/users?select=id.max()::oid", CodeInvalidSelect},
			{"/api/users?select=id.max()::record", CodeInvalidSelect},
			{"/api/users?select=id.max()::anyelement", CodeInvalidSelect},
			{"/api/users?select=id.max()::address", CodeInvalidSelect},
			{"/api/users?select=id.max()::users", CodeInvalidSelect},
			{"/api/users?select=id.max()::event_type", CodeInvalidSelect},
			{"/api/users?select=id.max()::nosuchtype", CodeInvalidSelect},
			{"/api/users?select=id.max()::pg_catalog.int4", CodeInvalidSelect},
			{"/api/posts?select=author_id,count()&order=id", CodeColumnNotOrderable},
			{"/api/posts?select=author_id,count()&order=users(email)", CodeColumnNotOrderable},
			{"/api/users?select=id,posts(author_id,count())&posts.order=id", CodeColumnNotOrderable},
			{"/api/posts?select=author_id,count()&distinct=author_id", CodeInvalidSelect},
			{"/api/posts?select=author_id,count()&after=" + cursor(1), CodeKeysetUnsupported},
		}
		for _, c := range cases {
			_, err := doParse(t, b, req{url: c.url}, aggOn)
			if err == nil {
				t.Errorf("%s: no error, want %s", c.url, c.code)
				continue
			}
			wantCode(t, err, c.code)
		}
		for _, m := range []string{"POST", "PATCH", "PUT"} {
			_, err := doParse(t, b, req{method: m, url: "/api/users?select=count()", header: map[string]string{"Content-Type": "application/json"}, body: `{"email":"x@y.z"}`}, aggOn)
			wantCode(t, err, CodeInvalidSelect)
		}
		_, err := doParse(t, b, req{method: "DELETE", url: "/api/users?id=eq.1&select=count()"}, aggOn)
		wantCode(t, err, CodeInvalidSelect)
	})
	t.Run("ungroupable keys", func(t *testing.T) {
		cat := testutil.FixtureCatalog()
		cat.Composites = append(cat.Composites, &introspect.Composite{Schema: "public", Name: "blob", Fields: []*introspect.Column{
			{Name: "doc", PGType: "json", TypeSchema: "pg_catalog"},
		}})
		cat.Tables = append(cat.Tables, &introspect.Table{
			Schema: "public", Name: "odd", Kind: introspect.RelTable,
			Columns: []*introspect.Column{
				{Name: "id", Position: 1, PGType: "int4", NotNull: true},
				{Name: "x", Position: 2, PGType: "xml"},
				{Name: "p", Position: 3, PGType: "point"},
				{Name: "b", Position: 4, PGType: "blob", TypeSchema: "public"},
				{Name: "j", Position: 5, PGType: "json"},
				{Name: "ps", Position: 6, PGType: "_polygon", IsArray: true},
			},
			PrimaryKey: &introspect.Constraint{Name: "odd_pkey", Columns: []string{"id"}},
			Privileges: introspect.Privileges{Select: true},
		})
		ob := buildCatalog(t, cat, false, []string{"public"}, nil)
		for _, u := range []string{"/api/odd?select=x,count()", "/api/odd?select=p,count()", "/api/odd?select=b,count()", "/api/odd?select=ps,count()", "/api/odd?select=*,count()"} {
			_, err := doParse(t, ob, req{url: u}, aggOn)
			wantCode(t, err, CodeInvalidSelect)
		}
		if _, err := doParse(t, ob, req{url: "/api/odd?select=j,id,count(),n:x.count()"}, aggOn); err != nil {
			t.Fatal(err)
		}
	})
	t.Run("count and csv", func(t *testing.T) {
		p, err := doParse(t, b, req{url: "/api/users?select=mood,count()", header: map[string]string{"Prefer": "count=planned", "Accept": "text/csv"}}, aggOn)
		if err != nil {
			t.Fatal(err)
		}
		if !p.Count || p.CountMode != plan.CountExact || p.Accept != plan.AcceptCSV {
			t.Fatalf("count = %v %q accept %q", p.Count, p.CountMode, p.Accept)
		}
	})
}
