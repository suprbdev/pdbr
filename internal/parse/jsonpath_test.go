package parse

import (
	"net/url"
	"testing"

	"github.com/suprbdev/pdbr/internal/plan"
)

func TestSplitJSONPath(t *testing.T) {
	key := func(k string) plan.JSONStep { return plan.JSONStep{Key: k} }
	text := func(k string) plan.JSONStep { return plan.JSONStep{Key: k, Text: true} }
	idx := func(i int, txt bool) plan.JSONStep { return plan.JSONStep{Array: true, Index: i, Text: txt} }
	cases := []struct {
		in   string
		col  string
		path plan.JSONPath
	}{
		{"data->a", "data", plan.JSONPath{key("a")}},
		{"data->a->>b", "data", plan.JSONPath{key("a"), text("b")}},
		{"data->0->-1->>2", "data", plan.JSONPath{idx(0, false), idx(-1, false), idx(2, true)}},
		{`data->"a.b"->>"c:d->e"`, "data", plan.JSONPath{key("a.b"), text("c:d->e")}},
		{`data->""`, "data", plan.JSONPath{key("")}},
		{"data->first-name->$x_1", "data", plan.JSONPath{key("first-name"), key("$x_1")}},
		{"data->1a", "data", plan.JSONPath{key("1a")}},
	}
	for _, c := range cases {
		col, path, has, err := splitJSONPath(c.in)
		if err != nil || !has || col != c.col || !path.Equal(c.path) {
			t.Errorf("%s: col=%q path=%#v has=%v err=%v", c.in, col, path, has, err)
		}
	}
	if _, _, has, err := splitJSONPath("data"); has || err != nil {
		t.Errorf("plain column: has=%v err=%v", has, err)
	}
	for _, bad := range []string{"->a", "data->", "data->>a->b", "data->a.b", "data->a b", `data->"a`, `data->"a"b`, "data->99999999999", "data->a->"} {
		if _, _, _, err := splitJSONPath(bad); err == nil {
			t.Errorf("%s: no error", bad)
		}
	}
}

func TestJSONPathKeys(t *testing.T) {
	p := plan.JSONPath{{Key: "a"}, {Array: true, Index: 0}}
	if p.LastKey() != "a" || p.Shape() != "->k->i" || p.Text() || p.String() != "->a->0" {
		t.Fatalf("key=%q shape=%q text=%v string=%q", p.LastKey(), p.Shape(), p.Text(), p.String())
	}
	if (plan.JSONPath{{Array: true, Index: 1, Text: true}}).LastKey() != "" {
		t.Fatal("index-only path has a key")
	}
	if s := (plan.JSONPath{{Key: "a.b", Text: true}}).String(); s != `->>"a.b"` {
		t.Fatalf("quoted string = %s", s)
	}
}

func TestJSONPathSelect(t *testing.T) {
	b := buildFixture(t, false)
	p := mustParse(t, b, "/api/users?select=id,settings->a->>b,x:settings->0,tags->1,address->>city::text,n:settings->n::int")
	f := p.Select.Fields
	if len(f) != 6 {
		t.Fatalf("fields = %+v", f)
	}
	keys := []string{"id", "b", "x", "tags", "city", "n"}
	for i, want := range keys {
		if got := f[i].Key(p.Resource); got != want {
			t.Errorf("field %d key = %q, want %q", i, got, want)
		}
	}
	if !f[1].PathKeyed() || f[2].PathKeyed() || f[3].PathKeyed() || f[1].Column.Name != "settings" || !f[1].Path.Text() {
		t.Fatalf("path fields = %+v", f[1:4])
	}
	if typeName(f[4].Cast) != "pg_catalog.text" || typeName(f[5].Cast) != "pg_catalog.int4" || len(f[5].Path) != 1 {
		t.Fatalf("casts = %+v %+v", f[4], f[5])
	}
	for _, c := range []struct{ url, code string }{
		{"/api/users?select=email->a", CodeInvalidSelect},
		{"/api/users?select=nope->a", CodeUnknownColumn},
		{"/api/users?select=post_count->a", CodeInvalidSelect},
		{"/api/users?select=settings->>a->b", CodeInvalidSelect},
		{"/api/users?select=settings->a::regclass", CodeInvalidSelect},
		{"/api/users?select=settings->a::", CodeInvalidSelect},
		{"/api/users?select=post_count::text", CodeInvalidSelect},
		{"/api/users?select=posts(id)::text", CodeInvalidSelect},
		{"/api/users?select=settings->a(x)", CodeInvalidSelect},
		{"/api/places?select=location->a", CodeInvalidSelect},
	} {
		_, err := doParse(t, b, req{url: c.url}, allOn)
		wantCode(t, err, c.code)
	}
}

func TestJSONPathFilterOrder(t *testing.T) {
	b := buildFixture(t, false)
	q := url.Values{}
	q.Set("settings->>theme", "eq.dark")
	q.Set("settings->n", "in.(1,2)")
	q.Set("order", `settings->"a.b".desc.nullsfirst,settings->>x`)
	p := mustParse(t, b, "/api/users?"+q.Encode())
	terms := p.Filter.(plan.And).Terms
	c0 := terms[0].(plan.Cond)
	c1 := terms[1].(plan.Cond)
	if c0.Op != plan.OpEq || c0.Value != "dark" || c0.Path.String() != "->>theme" {
		t.Fatalf("cond 0 = %+v", c0)
	}
	if vals, ok := c1.Value.([]any); !ok || len(vals) != 2 || vals[0] != "1" {
		t.Fatalf("cond 1 = %+v", c1)
	}
	if len(p.Order) != 2 || !p.Order[0].Desc || !p.Order[0].NullsFirst || p.Order[0].Path.LastKey() != "a.b" || !p.Order[1].Path.Text() {
		t.Fatalf("order = %+v", p.Order)
	}
	p = mustParse(t, b, "/api/users?or=(settings->>theme.eq.dark,not.settings->n.gt.2)")
	if or, ok := p.Filter.(plan.Or); !ok || len(or.Terms) != 2 {
		t.Fatalf("logic = %+v", p.Filter)
	}
	for _, c := range []struct{ url, code string }{
		{"/api/users?settings->n=eq.dark", CodeInvalidFilter},
		{"/api/users?settings->n=like.x", CodeInvalidFilter},
		{"/api/users?settings->>n=cs.{a}", CodeInvalidFilter},
		{"/api/users?settings->n=in.(x)", CodeInvalidFilter},
		{"/api/users?settings->n=in.(\"x)", CodeInvalidFilter},
		{"/api/users?email->a=eq.x", CodeInvalidFilter},
		{"/api/users?address->>city=eq.x", CodeColumnNotFilterable},
		{"/api/users?settings->>a=fts.x", CodeInvalidFilter},
		{"/api/users?order=email->a", CodeInvalidFilter},
		{"/api/users?order=address->>city", CodeColumnNotOrderable},
	} {
		_, err := doParse(t, b, req{url: c.url}, allOn)
		wantCode(t, err, c.code)
	}
	ib := buildFixture(t, true)
	if _, err := doParse(t, ib, req{url: "/api/users?full_name->a=eq.1"}, allOn); err == nil {
		t.Fatal("non-json column accepted")
	}
	p, err := doParse(t, b, req{url: "/api/posts?select=id,users(settings->>theme)&users.settings->>theme=eq.dark"}, allOn)
	if err != nil || p.Select.Embeds[0].Filter == nil {
		t.Fatalf("embed filter: %v %+v", err, p)
	}
	p, err = doParse(t, b, req{url: "/api/posts?users.settings->>theme=eq.dark"}, allOn)
	if err != nil || p.Filter.(plan.Cond).Relation.Leaf.Path.LastKey() != "theme" {
		t.Fatalf("relation filter: %v %+v", err, p)
	}
}

func TestJSONPathAggregates(t *testing.T) {
	b := buildFixture(t, false)
	p, err := doParse(t, b, req{url: "/api/users?select=settings->>theme,count(),settings->n::numeric.sum()&order=settings->>theme.desc"}, aggOn)
	if err != nil {
		t.Fatal(err)
	}
	if f := p.Select.Fields[2]; f.Agg == nil || typeName(f.Agg.InputCast) != "pg_catalog.numeric" || f.Path.LastKey() != "n" {
		t.Fatalf("aggregate = %+v", f)
	}
	for _, c := range []struct{ url, code string }{
		{"/api/users?select=settings->n.sum()", CodeInvalidSelect},
		{"/api/users?select=settings->n.max()", CodeInvalidSelect},
		{"/api/users?select=settings->>theme,count()&order=settings->>mode", CodeColumnNotOrderable},
		{"/api/users?select=settings->>theme::text,count()&order=settings->>theme", CodeColumnNotOrderable},
		{"/api/users?select=mood::json,count()", CodeInvalidSelect},
		{"/api/users?select=settings->>theme,settings->>theme,count()", CodeInvalidSelect},
	} {
		_, err := doParse(t, b, req{url: c.url}, aggOn)
		wantCode(t, err, c.code)
	}
	if _, err := doParse(t, b, req{url: "/api/users?select=settings->>n.max(),settings->>theme::text,count()"}, aggOn); err != nil {
		t.Fatal(err)
	}
}
