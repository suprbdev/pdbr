package parse

import (
	"testing"

	"github.com/suprbdev/pdbr/internal/plan"
)

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
			{plan.AggSum, "balance", "total", "numeric", "text"},
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
			if got.Agg == nil || got.Agg.Func != w.fn || col != w.col || got.Alias != w.alias || got.Agg.InputCast != w.in || got.Agg.OutputCast != w.out {
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
		p = parse(t, "/api/users?select=id::INT8.sum(),balance.avg()::Float8")
		if a := p.Select.Fields[0].Agg; a.InputCast != "int8" {
			t.Errorf("input cast = %q", a.InputCast)
		}
		if a := p.Select.Fields[1].Agg; a.OutputCast != "float8" {
			t.Errorf("output cast = %q", a.OutputCast)
		}
	})
	t.Run("embeds, order and paging", func(t *testing.T) {
		p := parse(t, "/api/users?select=id,posts(author_id,count())&posts.order=author_id.desc&posts.limit=2&limit=5")
		e := p.Select.Embeds[0]
		if p.Select.Aggregated() || !p.Select.Aggregates() || !e.Node.Aggregated() || e.Page.Limit != 2 || len(e.Order) != 1 {
			t.Fatalf("embed = %+v", e)
		}
		p = parse(t, "/api/posts?select=author_id,count(),users(mood)&order=author_id.desc&limit=2&offset=1")
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
			{"/api/users?select=id::text", CodeInvalidSelect},
			{"/api/users?select=posts(id)::text", CodeInvalidSelect},
			{"/api/users?select=id.sum(),balance.sum()", CodeInvalidSelect},
			{"/api/users?select=*,id.sum(),email.min(),id:count()", CodeInvalidSelect},
			{"/api/users/1?select=count()", CodeInvalidSelect},
			{"/api/posts?select=id,users(count())", CodeInvalidSelect},
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
