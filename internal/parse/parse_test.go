package parse

import (
	"encoding/base64"
	"errors"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"reflect"
	"strings"
	"testing"

	"github.com/suprbdev/pdbcore/postgis"
	"github.com/suprbdev/pdbcore/testutil"

	"github.com/suprbdev/pdbr/internal/api"
	"github.com/suprbdev/pdbr/internal/compile"
	"github.com/suprbdev/pdbr/internal/inflect"
	"github.com/suprbdev/pdbr/internal/plan"
)

func quiet() *slog.Logger { return slog.New(slog.NewTextHandler(io.Discard, nil)) }

func buildFixture(t testing.TB, indexedOnly bool) *api.Built {
	t.Helper()
	b, err := api.New(testutil.FixtureCatalog(), inflect.Default, api.Options{
		FilterIndexedOnly: indexedOnly,
		AllowColumns:      map[string][]string{"public.posts": {"published"}},
		Functions:         true,
		BasePath:          "/api",
		Schemas:           []string{"public"},
		MaxPageSize:       100,
		Etags:             true,
		Version:           "test",
		Logger:            quiet(),
	}).Build()
	if err != nil {
		t.Fatal(err)
	}
	return b
}

var allOn = Options{
	MaxEmbedDepth: 5, MaxPageSize: 100, Etags: true, CSV: true,
	RelationFilters: true, ComputedFilters: true, RelationOrder: true, ComputedOrder: true, Spatial: true,
}

type req struct {
	method string
	url    string
	header map[string]string
	body   string
}

func doParse(t testing.TB, built *api.Built, r req, opts Options) (*plan.Request, error) {
	t.Helper()
	method := r.method
	if method == "" {
		method = http.MethodGet
	}
	var body io.Reader
	if r.body != "" {
		body = strings.NewReader(r.body)
	}
	hr, err := http.NewRequest(method, "http://test"+r.url, body)
	if err != nil {
		t.Fatalf("%s: %v", r.url, err)
	}
	for k, v := range r.header {
		hr.Header.Set(k, v)
	}
	m, ok := built.Match(strings.TrimPrefix(hr.URL.EscapedPath(), "/api"))
	if !ok {
		return nil, errf(CodeNotFound, "no such route %s", hr.URL.Path)
	}
	return Parse(built, m, hr, opts)
}

func mustParse(t *testing.T, built *api.Built, url string) *plan.Request {
	t.Helper()
	p, err := doParse(t, built, req{url: url}, allOn)
	if err != nil {
		t.Fatalf("%s: unexpected error: %v", url, err)
	}
	return p
}

func wantCode(t *testing.T, err error, code string) {
	t.Helper()
	var ue *compile.UserError
	if !errors.As(err, &ue) {
		t.Fatalf("want *compile.UserError %s, got %v", code, err)
	}
	if ue.Code != code {
		t.Fatalf("want code %s, got %s (%s)", code, ue.Code, ue.Detail)
	}
}

func cursor(parts ...any) string {
	b := "[" + `"public.posts"`
	for _, p := range parts {
		switch v := p.(type) {
		case string:
			b += `,"` + v + `"`
		case int:
			b += `,` + itoa(v)
		}
	}
	b += "]"
	return base64.RawURLEncoding.EncodeToString([]byte(b))
}

func itoa(i int) string {
	return strings.TrimSpace(strings.Repeat(" ", 0) + string(rune('0'+i)))
}

func TestListDefaults(t *testing.T) {
	b := buildFixture(t, true)
	p := mustParse(t, b, "/api/users")
	if p.Method != plan.GET || p.Resource == nil || p.Resource.Path != "users" || p.Key != nil {
		t.Fatalf("bad plan: %+v", p)
	}
	if p.Select != nil || p.Filter != nil || p.Order != nil {
		t.Fatalf("expected empty select/filter/order")
	}
	if p.Page.Limit != 100 || p.Page.LimitSet || p.Page.Offset != 0 || p.Page.After != nil {
		t.Fatalf("page = %+v", p.Page)
	}
	if p.Accept != plan.AcceptJSON || p.Prefer.Return != "representation" {
		t.Fatalf("accept/prefer = %v %v", p.Accept, p.Prefer)
	}
}

func TestSelect(t *testing.T) {
	b := buildFixture(t, true)
	t.Run("fields and alias", func(t *testing.T) {
		p := mustParse(t, b, "/api/users?select=id,n:email,post_count")
		if p.Select == nil || len(p.Select.Fields) != 3 || p.Select.Star {
			t.Fatalf("select = %+v", p.Select)
		}
		f := p.Select.Fields
		if f[0].Column.Name != "id" || f[1].Alias != "n" || f[1].Column.Name != "email" || f[2].Computed == nil || f[2].Computed.Key != "post_count" {
			t.Fatalf("fields = %+v", f)
		}
	})
	t.Run("star plus computed", func(t *testing.T) {
		p := mustParse(t, b, "/api/users?select=*,post_count")
		if !p.Select.Star || len(p.Select.Fields) != 1 {
			t.Fatalf("select = %+v", p.Select)
		}
	})
	t.Run("embed backward with alias and nested forward", func(t *testing.T) {
		p := mustParse(t, b, "/api/users?select=id,recent:posts(id,title,users(email))")
		if len(p.Select.Embeds) != 1 {
			t.Fatalf("embeds = %+v", p.Select.Embeds)
		}
		e := p.Select.Embeds[0]
		if e.Key != "posts" || e.Alias != "recent" || e.Relation.Forward || e.Relation.To.Path != "posts" || e.Node.Alias != "recent" {
			t.Fatalf("embed = %+v", e)
		}
		if e.Page.Limit != 100 {
			t.Fatalf("embed page = %+v", e.Page)
		}
		if len(e.Node.Embeds) != 1 || !e.Node.Embeds[0].Relation.Forward || e.Node.Embeds[0].Node.Alias != "users" {
			t.Fatalf("nested = %+v", e.Node.Embeds)
		}
	})
	t.Run("empty embed selection means star", func(t *testing.T) {
		p := mustParse(t, b, "/api/users?select=id,posts()")
		if !p.Select.Embeds[0].Node.Star {
			t.Fatal("expected star")
		}
	})
	t.Run("hints", func(t *testing.T) {
		p := mustParse(t, b, "/api/comments?select=id,parent:comments!parent_id(id),replies:comments!comments_parent_id_fkey(id),posts!inner(title)")
		e := p.Select.Embeds
		if len(e) != 3 || !e[0].Relation.Forward || e[1].Relation.Forward || !e[2].Inner {
			t.Fatalf("embeds = %+v", e)
		}
		p = mustParse(t, b, "/api/comments?select=comments!parent_id!inner(id)")
		if !p.Select.Embeds[0].Inner || !p.Select.Embeds[0].Relation.Forward {
			t.Fatalf("embed = %+v", p.Select.Embeds[0])
		}
	})
	t.Run("ambiguous", func(t *testing.T) {
		_, err := doParse(t, b, req{url: "/api/comments?select=comments(id)"}, allOn)
		wantCode(t, err, CodeAmbiguousRelation)
		if !strings.Contains(err.Error(), "comments!parent_id") {
			t.Fatalf("detail should list candidates: %v", err)
		}
		_, err = doParse(t, b, req{url: "/api/comments?select=comments!nope(id)"}, allOn)
		wantCode(t, err, CodeAmbiguousRelation)
	})
	t.Run("depth", func(t *testing.T) {
		opts := allOn
		opts.MaxEmbedDepth = 2
		_, err := doParse(t, b, req{url: "/api/users?select=posts(comments(posts(id)))"}, opts)
		wantCode(t, err, CodeEmbedDepthExceeded)
		if _, err := doParse(t, b, req{url: "/api/users?select=posts(comments(id))"}, opts); err != nil {
			t.Fatal(err)
		}
	})
	t.Run("errors", func(t *testing.T) {
		cases := map[string]string{
			"/api/users?select=id,emial":              CodeUnknownColumn,
			"/api/users?select=id,posts(id":           CodeInvalidSelect,
			"/api/users?select=id,,email":             CodeInvalidSelect,
			"/api/users?select=:email":                CodeInvalidSelect,
			"/api/users?select=email!inner":           CodeInvalidSelect,
			"/api/users?select=posts":                 CodeInvalidSelect,
			"/api/users?select=email(transform=3857)": CodeInvalidSelect,
			"/api/users?select=nope(id)":              CodeUnknownRelation,
			"/api/users?select=nope!inner(id)":        CodeUnknownRelation,
			"/api/users?select=nope(transform=1)":     CodeUnknownColumn,
			"/api/users?select=posts!a!b(id)":         CodeInvalidSelect,
		}
		for url, code := range cases {
			_, err := doParse(t, b, req{url: url}, allOn)
			wantCode(t, err, code)
		}
	})
	t.Run("spatial args", func(t *testing.T) {
		p := mustParse(t, b, "/api/places?select=name,web:location(transform=3857),coarse:area(simplify=0.01)")
		f := p.Select.Fields
		if f[1].Args["transform"] != 3857 || f[2].Args["simplify"] != 0.01 || f[1].Alias != "web" {
			t.Fatalf("args = %+v", f)
		}
		for _, u := range []string{
			"/api/places?select=location(transform=abc)",
			"/api/places?select=location(foo=1)",
			"/api/places?select=location(transform=1,transform=2)",
			"/api/places?select=location()",
		} {
			_, err := doParse(t, b, req{url: u}, allOn)
			wantCode(t, err, CodeInvalidSelect)
		}
		off := allOn
		off.Spatial = false
		_, err := doParse(t, b, req{url: "/api/places?select=location(transform=3857)"}, off)
		wantCode(t, err, CodeInvalidSelect)
	})
	t.Run("setof computed nested select", func(t *testing.T) {
		p := mustParse(t, b, "/api/users?select=id,tag_words")
		if p.Select.Fields[1].Computed == nil || p.Select.Fields[1].Node != nil {
			t.Fatalf("fields = %+v", p.Select.Fields)
		}
	})
}

func TestFilters(t *testing.T) {
	b := buildFixture(t, true)
	cond := func(t *testing.T, url string) plan.Cond {
		t.Helper()
		p := mustParse(t, b, url)
		c, ok := p.Filter.(plan.Cond)
		if !ok {
			t.Fatalf("%s: filter = %#v", url, p.Filter)
		}
		return c
	}
	t.Run("scalar coercion", func(t *testing.T) {
		c := cond(t, "/api/users?id=eq.5")
		if c.Column.Name != "id" || c.Op != plan.OpEq || c.Value != int64(5) || c.Negate {
			t.Fatalf("cond = %+v", c)
		}
		c = cond(t, "/api/users?email=not.eq.a@b.c")
		if !c.Negate || c.Value != "a@b.c" {
			t.Fatalf("cond = %+v", c)
		}
		c = cond(t, "/api/posts?published=is.true")
		if c.Op != plan.OpIs || c.Value != true {
			t.Fatalf("cond = %+v", c)
		}
		c = cond(t, "/api/users?mood=is.null")
		if c.Op != plan.OpIs || c.Value != nil {
			t.Fatalf("cond = %+v", c)
		}
		c = cond(t, "/api/users?mood=eq.happy")
		if c.Value != "happy" {
			t.Fatalf("cond = %+v", c)
		}
		c = cond(t, "/api/events?type_code=eq.meetup")
		if c.Value != "meetup" {
			t.Fatalf("cond = %+v", c)
		}
		c = cond(t, "/api/users?id=gte.9007199254740993")
		if c.Value != int64(9007199254740993) {
			t.Fatalf("cond = %+v", c)
		}
		c = cond(t, "/api/posts?title=lt.2024-01-01T00:00:00Z")
		if c.Value != "2024-01-01T00:00:00Z" {
			t.Fatalf("cond = %+v", c)
		}
	})
	t.Run("in", func(t *testing.T) {
		c := cond(t, "/api/users?id=in.(1,2,3)")
		if !reflect.DeepEqual(c.Value, []any{int64(1), int64(2), int64(3)}) {
			t.Fatalf("value = %#v", c.Value)
		}
		c = cond(t, `/api/users?email=in.(a@b.c,"x,y@z.io","q\"uote")`)
		if !reflect.DeepEqual(c.Value, []any{"a@b.c", "x,y@z.io", `q"uote`}) {
			t.Fatalf("value = %#v", c.Value)
		}
		c = cond(t, "/api/users?mood=not.in.(sad,ok)")
		if !c.Negate || !reflect.DeepEqual(c.Value, []any{"sad", "ok"}) {
			t.Fatalf("cond = %+v", c)
		}
		c = cond(t, "/api/users?id=in.()")
		if v, ok := c.Value.([]any); !ok || len(v) != 0 {
			t.Fatalf("value = %#v", c.Value)
		}
	})
	t.Run("text patterns keep raw operand", func(t *testing.T) {
		c := cond(t, "/api/users?email=ilike.*@example.com")
		if c.Op != plan.OpILike || c.Value != "*@example.com" {
			t.Fatalf("cond = %+v", c)
		}
		c = cond(t, "/api/posts?title=sw.Draft%25_")
		if c.Op != plan.OpSw || c.Value != "Draft%_" {
			t.Fatalf("cond = %+v", c)
		}
	})
	t.Run("arrays jsonb", func(t *testing.T) {
		c := cond(t, `/api/users?tags=cs.{admin,ops,"x,y",NULL}`)
		if c.Op != plan.OpCs || !reflect.DeepEqual(c.Value, []any{"admin", "ops", "x,y", nil}) {
			t.Fatalf("value = %#v", c.Value)
		}
		c = cond(t, "/api/users?tags=ov.{}")
		if v, ok := c.Value.([]any); !ok || len(v) != 0 {
			t.Fatalf("value = %#v", c.Value)
		}
		c = cond(t, "/api/users?moods=cs.{happy}")
		if !reflect.DeepEqual(c.Value, []any{"happy"}) {
			t.Fatalf("value = %#v", c.Value)
		}
		c = cond(t, `/api/users?settings=cs.{"theme":"dark"}`)
		if c.Value != `{"theme":"dark"}` {
			t.Fatalf("value = %#v", c.Value)
		}
		c = cond(t, "/api/users?settings=hk.theme")
		if c.Op != plan.OpHk || c.Value != "theme" {
			t.Fatalf("cond = %+v", c)
		}
		c = cond(t, `/api/users?settings=jpm.$.theme=="dark"`)
		if c.Op != plan.OpJpm || c.Value != `$.theme=="dark"` {
			t.Fatalf("cond = %+v", c)
		}
		c = cond(t, `/api/users?settings=eq.{"a":1}`)
		if c.Op != plan.OpEq || c.Value != `{"a":1}` {
			t.Fatalf("cond = %+v", c)
		}
	})
	t.Run("logic", func(t *testing.T) {
		p := mustParse(t, b, `/api/users?and=(mood.in.(happy,ok),or(email.ew.@example.com,tags.cs.{admin}),not.settings.hk.banned)`)
		a, ok := p.Filter.(plan.And)
		if !ok || len(a.Terms) != 3 {
			t.Fatalf("filter = %#v", p.Filter)
		}
		if c := a.Terms[0].(plan.Cond); c.Op != plan.OpIn || !reflect.DeepEqual(c.Value, []any{"happy", "ok"}) {
			t.Fatalf("t0 = %+v", c)
		}
		o, ok := a.Terms[1].(plan.Or)
		if !ok || len(o.Terms) != 2 || o.Terms[0].(plan.Cond).Value != "@example.com" {
			t.Fatalf("t1 = %#v", a.Terms[1])
		}
		if c := a.Terms[2].(plan.Cond); !c.Negate || c.Op != plan.OpHk {
			t.Fatalf("t2 = %+v", c)
		}
		p = mustParse(t, b, `/api/posts?not.or=(published.is.false,title.sw.Draft)`)
		n, ok := p.Filter.(plan.Not)
		if !ok {
			t.Fatalf("filter = %#v", p.Filter)
		}
		if _, ok := n.Term.(plan.Or); !ok {
			t.Fatalf("filter = %#v", p.Filter)
		}
		p = mustParse(t, b, `/api/posts?or=(title.eq."Hello, world (draft)",title.eq."a\"b\\c")`)
		o = p.Filter.(plan.Or)
		if o.Terms[0].(plan.Cond).Value != "Hello, world (draft)" || o.Terms[1].(plan.Cond).Value != `a"b\c` {
			t.Fatalf("or = %#v", o)
		}
		p = mustParse(t, b, `/api/users?id=eq.1&or=(mood.eq.happy,mood.is.null)&email=eq.x`)
		a = p.Filter.(plan.And)
		if len(a.Terms) != 3 {
			t.Fatalf("filter = %#v", p.Filter)
		}
	})
	t.Run("errors", func(t *testing.T) {
		cases := map[string]string{
			"/api/users?id=eq.abc":                  CodeInvalidFilter,
			"/api/users?id=foo.1":                   CodeInvalidFilter,
			"/api/users?id=eq":                      CodeInvalidFilter,
			"/api/users?id=":                        CodeInvalidFilter,
			"/api/users?id=like.1":                  CodeInvalidFilter,
			"/api/users?email=cs.{a}":               CodeInvalidFilter,
			"/api/users?tags=in.(a)":                CodeInvalidFilter,
			"/api/users?tags=cs.a":                  CodeInvalidFilter,
			"/api/users?tags=cs.{a,{b}}":            CodeInvalidFilter,
			"/api/users?email=is.true":              CodeInvalidFilter,
			"/api/users?email=is.maybe":             CodeInvalidFilter,
			"/api/users?email=hk.x":                 CodeInvalidFilter,
			"/api/users?settings=cs.{notjson":       CodeInvalidFilter,
			"/api/users?mood=eq.angry":              CodeInvalidFilter,
			"/api/users?id=in.(1,2":                 CodeInvalidFilter,
			"/api/users?id=in.1,2":                  CodeInvalidFilter,
			"/api/users?and=(mood.eq.happy":         CodeInvalidFilter,
			"/api/users?and=()":                     CodeInvalidFilter,
			"/api/users?and=(mood)":                 CodeInvalidFilter,
			"/api/users?and=(xor(mood.eq.happy))":   CodeUnknownColumn,
			"/api/users?or=(nope.eq.1)":             CodeUnknownColumn,
			"/api/users?emial=eq.1":                 CodeUnknownColumn,
			"/api/users?full_name=eq.x":             CodeColumnNotFilterable,
			"/api/users?address=is.null":            CodeColumnNotFilterable,
			"/api/users?id=eq.1&id=eq.%zz":          CodeInvalidFilter,
			"/api/places?location=eq.POINT(1%202)":  CodeInvalidFilter,
			"/api/places?location=st_intersects.x":  CodeInvalidFilter,
			"/api/users?email=st_intersects.POINT":  CodeInvalidFilter,
			"/api/users?id=exists.true":             CodeInvalidFilter,
			"/api/users?tag_words=eq.x":             CodeColumnNotFilterable,
			"/api/users?tags=eq.{":                  CodeInvalidFilter,
			"/api/users?tags=cs.{\"unterminated}":   CodeInvalidFilter,
			"/api/users?tags=cs.{a,}":               CodeInvalidFilter,
			"/api/users?id=in.(\"unterminated)":     CodeInvalidFilter,
			"/api/users?id=in.(1,\"a\"b\")":         CodeInvalidFilter,
			"/api/users?settings=jpe.$.a&select=1(": CodeInvalidSelect,
		}
		for url, code := range cases {
			_, err := doParse(t, b, req{url: url}, allOn)
			if err == nil {
				t.Errorf("%s: expected %s, got nil", url, code)
				continue
			}
			var ue *compile.UserError
			if !errors.As(err, &ue) || ue.Code != code {
				t.Errorf("%s: want %s, got %v", url, code, err)
			}
		}
	})
	t.Run("policy off exposes everything", func(t *testing.T) {
		open := buildFixture(t, false)
		p, err := doParse(t, open, req{url: "/api/users?full_name=eq.x"}, allOn)
		if err != nil || p.Filter.(plan.Cond).Column.Name != "full_name" {
			t.Fatalf("plan=%+v err=%v", p, err)
		}
		_, err = doParse(t, open, req{url: "/api/users?address=is.null"}, allOn)
		wantCode(t, err, CodeColumnNotFilterable)
	})
	t.Run("computed", func(t *testing.T) {
		c := cond(t, "/api/users?post_count=gt.3")
		if c.Computed == nil || c.Value != int64(3) {
			t.Fatalf("cond = %+v", c)
		}
		off := allOn
		off.ComputedFilters = false
		_, err := doParse(t, b, req{url: "/api/users?post_count=gt.3"}, off)
		wantCode(t, err, CodeColumnNotFilterable)
	})
	t.Run("reserved name only in logic", func(t *testing.T) {
		_, err := doParse(t, b, req{url: "/api/users?select=id&limit=eq.5"}, allOn)
		wantCode(t, err, CodeInvalidFilter)
	})
	t.Run("column with same name as relation wins", func(t *testing.T) {
		c := cond(t, "/api/comments?post_id=eq.1")
		if c.Column.Name != "post_id" {
			t.Fatalf("cond = %+v", c)
		}
	})
}

func TestSpatialFilters(t *testing.T) {
	b := buildFixture(t, true)
	p := mustParse(t, b, "/api/places?location=st_intersects.SRID%3D4326%3BPOINT(1%202)")
	c := p.Filter.(plan.Cond)
	v, ok := c.Value.(postgis.Value)
	if !ok || c.Op != plan.OpStIntersects || v.Param != "SRID=4326;POINT(1 2)" || v.Flavour != postgis.Geometry {
		t.Fatalf("cond = %+v", c)
	}
	p = mustParse(t, b, `/api/places?area=st_within.{"type":"Point","coordinates":[1,2]}`)
	v = p.Filter.(plan.Cond).Value.(postgis.Value)
	if v.Flavour != postgis.Geography || !strings.Contains(v.Param, `"Point"`) {
		t.Fatalf("value = %+v", v)
	}
	p = mustParse(t, b, `/api/places?location=st_dwithin.("POINT(1 2)",100,true)`)
	d := p.Filter.(plan.Cond).Value.(plan.DWithin)
	if d.Distance != 100 || d.Spheroid == nil || !*d.Spheroid || d.Geom.(postgis.Value).Param != "POINT(1 2)" {
		t.Fatalf("dwithin = %+v", d)
	}
	p = mustParse(t, b, `/api/places?location=not.bbox_intersects.(-1,-2,3,4,4326)`)
	c = p.Filter.(plan.Cond)
	bb := c.Value.(plan.BBox)
	if !c.Negate || bb.MinX != -1 || bb.MaxY != 4 || bb.SRID != 4326 {
		t.Fatalf("bbox = %+v", bb)
	}
	p = mustParse(t, b, `/api/places?location=is.null`)
	if p.Filter.(plan.Cond).Value != nil {
		t.Fatal("is.null on spatial column")
	}
	for _, u := range []string{
		"/api/places?location=st_dwithin.(POINT(1 2))",
		"/api/places?location=st_dwithin.(\"POINT(1 2)\",abc)",
		"/api/places?location=bbox_contains.(1,2,3)",
		"/api/places?location=bbox_contains.(1,2,3,x)",
		"/api/places?location=st_intersects.{\"type\":\"Nope\"}",
		"/api/places?location=in.(POINT(1 2))",
		"/api/places?location=st_dwithin.(\"POINT(1 2)\",1,maybe)",
	} {
		_, err := doParse(t, b, req{url: u}, allOn)
		wantCode(t, err, CodeInvalidFilter)
	}
	off := allOn
	off.Spatial = false
	_, err := doParse(t, b, req{url: "/api/places?location=st_intersects.POINT(1%202)"}, off)
	wantCode(t, err, CodeInvalidFilter)
	for k := range plan.SpatialOps {
		if _, ok := postgis.OpByName(plan.SpatialOps[k]); !ok {
			t.Errorf("SpatialOps[%s] = %q is not a postgis op", k, plan.SpatialOps[k])
		}
	}
}

func TestEmbedParams(t *testing.T) {
	b := buildFixture(t, true)
	p := mustParse(t, b, "/api/users?select=id,posts(id,title,comments(id))&posts.order=title.desc&posts.limit=5&posts.offset=5&posts.published=is.true&posts.comments.limit=2&posts.comments.post_id=eq.1&posts.or=(title.eq.a,title.eq.b)")
	e := p.Select.Embeds[0]
	if e.Page.Limit != 5 || !e.Page.LimitSet || e.Page.Offset != 5 || len(e.Order) != 1 || !e.Order[0].Desc || e.Order[0].Column.Name != "title" {
		t.Fatalf("embed = %+v", e)
	}
	a, ok := e.Filter.(plan.And)
	if !ok || len(a.Terms) != 2 {
		t.Fatalf("embed filter = %#v", e.Filter)
	}
	if a.Terms[0].(plan.Cond).Column.Name != "published" {
		t.Fatalf("embed filter = %#v", e.Filter)
	}
	if _, ok := a.Terms[1].(plan.Or); !ok {
		t.Fatalf("embed filter = %#v", e.Filter)
	}
	c := e.Node.Embeds[0]
	if c.Page.Limit != 2 || c.Filter.(plan.Cond).Column.Name != "post_id" {
		t.Fatalf("nested embed = %+v", c)
	}
	if p.Filter != nil {
		t.Fatalf("root filter should be empty, got %#v", p.Filter)
	}
	// alias addressing
	p = mustParse(t, b, "/api/users?select=id,recent:posts(id)&recent.limit=3")
	if p.Select.Embeds[0].Page.Limit != 3 {
		t.Fatalf("embed = %+v", p.Select.Embeds[0])
	}
	// aliased embed no longer addressable by key -> relation filter on root
	p = mustParse(t, b, "/api/users?select=id,recent:posts(id)&posts.title=eq.x")
	if p.Filter == nil || p.Filter.(plan.Cond).Relation == nil {
		t.Fatalf("expected relation filter, got %#v", p.Filter)
	}
	for u, code := range map[string]string{
		"/api/users?select=id,posts(id)&posts.limit=0":         CodeInvalidFilter,
		"/api/users?select=id,posts(id)&posts.limit=101":       CodePageSizeExceeded,
		"/api/users?select=id,posts(id)&posts.offset=-1":       CodeInvalidFilter,
		"/api/users?select=id,posts(id)&posts.after=x":         CodeInvalidFilter,
		"/api/users?select=id,posts(id)&posts.body=eq.x":       CodeColumnNotFilterable,
		"/api/users?select=id,posts(id)&posts.nope=eq.x":       CodeUnknownColumn,
		"/api/users?select=id,posts(id)&posts.order=body.desc": CodeColumnNotOrderable,
	} {
		_, err := doParse(t, b, req{url: u}, allOn)
		wantCode(t, err, code)
	}
}

func TestRelationFilters(t *testing.T) {
	b := buildFixture(t, true)
	p := mustParse(t, b, "/api/users?posts.title=eq.x")
	c := p.Filter.(plan.Cond)
	if c.Relation == nil || c.Relation.Quantifier != plan.Some || c.Relation.Leaf == nil || c.Relation.Leaf.Column.Name != "title" || c.Relation.Steps[0].Forward {
		t.Fatalf("cond = %+v", c)
	}
	p = mustParse(t, b, "/api/users?posts!none.title=not.eq.x")
	c = p.Filter.(plan.Cond)
	if c.Relation.Quantifier != plan.None || !c.Relation.Leaf.Negate || c.Negate {
		t.Fatalf("cond = %+v rel=%+v", c, c.Relation)
	}
	p = mustParse(t, b, "/api/users?posts!every.published=is.true")
	if p.Filter.(plan.Cond).Relation.Quantifier != plan.Every {
		t.Fatal("every")
	}
	p = mustParse(t, b, "/api/posts?users.email=eq.x")
	if !p.Filter.(plan.Cond).Relation.Steps[0].Forward {
		t.Fatal("forward")
	}
	p = mustParse(t, b, "/api/users?posts=exists.true")
	c = p.Filter.(plan.Cond)
	if c.Op != plan.OpExists || c.Relation.Quantifier != plan.Exists || c.Relation.Exists == nil || !*c.Relation.Exists || c.Relation.Leaf != nil {
		t.Fatalf("cond = %+v rel=%+v", c, c.Relation)
	}
	p = mustParse(t, b, "/api/posts?users=not.exists.true")
	if *p.Filter.(plan.Cond).Relation.Exists {
		t.Fatal("not.exists.true should be exists=false")
	}
	p = mustParse(t, b, "/api/comments?comments!parent_id.post_id=eq.1")
	if !p.Filter.(plan.Cond).Relation.Steps[0].Forward {
		t.Fatal("hinted forward")
	}
	p = mustParse(t, b, "/api/comments?comments!comments_parent_id_fkey!none.post_id=eq.1")
	if p.Filter.(plan.Cond).Relation.Steps[0].Forward || p.Filter.(plan.Cond).Relation.Quantifier != plan.None {
		t.Fatal("hinted backward none")
	}
	p = mustParse(t, b, "/api/users?and=(posts.title.eq.x,mood.eq.happy)")
	if p.Filter.(plan.And).Terms[0].(plan.Cond).Relation == nil {
		t.Fatal("relation filter inside logic")
	}
	// bare conditions on the same relation and quantifier share one EXISTS
	p = mustParse(t, b, "/api/users?posts.published=is.true&posts.title=sw.A&posts!none.title=eq.x&mood=eq.happy&posts.author_id=eq.1")
	and := p.Filter.(plan.And)
	if len(and.Terms) != 3 {
		t.Fatalf("terms = %d, want 3 (grouped some, none, mood): %#v", len(and.Terms), and.Terms)
	}
	grouped := and.Terms[0].(plan.Cond).Relation
	if grouped.Quantifier != plan.Some || grouped.Filter == nil {
		t.Fatalf("grouped = %+v", grouped)
	}
	if leaves := grouped.Filter.(plan.And).Terms; len(leaves) != 3 || leaves[0].(plan.Cond).Column.Name != "published" || leaves[1].(plan.Cond).Column.Name != "title" || leaves[2].(plan.Cond).Column.Name != "author_id" {
		t.Fatalf("leaves = %#v", leaves)
	}
	if none := and.Terms[1].(plan.Cond).Relation; none.Quantifier != plan.None || none.Filter != nil || none.Leaf == nil {
		t.Fatalf("none = %+v", none)
	}
	// the first condition of a group keeps its Leaf until a second arrives
	p = mustParse(t, b, "/api/users?posts.title=eq.x&posts.title=eq.y")
	if rp := p.Filter.(plan.Cond).Relation; rp.Filter == nil || len(rp.Filter.(plan.And).Terms) != 2 {
		t.Fatalf("pair = %+v", rp)
	}
	// inner embed + same filter narrows both
	p = mustParse(t, b, "/api/users?select=id,posts!inner(id)&posts.published=is.true")
	if p.Filter != nil || p.Select.Embeds[0].Filter == nil {
		t.Fatalf("filter=%#v embed=%#v", p.Filter, p.Select.Embeds[0].Filter)
	}
	// quantified form never addresses an embed
	p = mustParse(t, b, "/api/users?select=id,posts(id)&posts!none.published=is.true")
	if p.Filter == nil || p.Select.Embeds[0].Filter != nil {
		t.Fatalf("filter=%#v embed=%#v", p.Filter, p.Select.Embeds[0].Filter)
	}
	for u, code := range map[string]string{
		"/api/users?nope.title=eq.x":             CodeUnknownRelation,
		"/api/users?nope=exists.true":            CodeUnknownColumn,
		"/api/users?posts.nope=eq.x":             CodeUnknownColumn,
		"/api/users?posts.comments.body=eq.x":    CodeInvalidFilter,
		"/api/posts?users!none.email=eq.x":       CodeInvalidFilter,
		"/api/users?posts=eq.1":                  CodeInvalidFilter,
		"/api/users?posts=exists.maybe":          CodeInvalidFilter,
		"/api/users?posts!none=exists.true":      CodeInvalidFilter,
		"/api/users?posts!inner.title=eq.x":      CodeInvalidFilter,
		"/api/users?posts!none!every.title=eq.x": CodeInvalidFilter,
		"/api/users?posts.title=exists.true":     CodeInvalidFilter,
		"/api/comments?comments.post_id=eq.1":    CodeAmbiguousRelation,
		"/api/users?posts.body=eq.x":             CodeColumnNotFilterable,
		"/api/users?posts!.title=eq.x":           CodeInvalidFilter,
		"/api/users?posts!a!b.title=eq.x":        CodeInvalidFilter,
		"/api/users?posts!a.title=eq.x":          CodeAmbiguousRelation,
	} {
		_, err := doParse(t, b, req{url: u}, allOn)
		if err == nil {
			t.Errorf("%s: expected %s", u, code)
			continue
		}
		var ue *compile.UserError
		if !errors.As(err, &ue) || ue.Code != code {
			t.Errorf("%s: want %s, got %v", u, code, err)
		}
	}
	off := allOn
	off.RelationFilters = false
	_, err := doParse(t, b, req{url: "/api/users?posts.title=eq.x"}, off)
	wantCode(t, err, CodeInvalidFilter)
	_, err = doParse(t, b, req{url: "/api/users?nope.title=eq.x"}, off)
	wantCode(t, err, CodeUnknownRelation)
}

func TestOrder(t *testing.T) {
	b := buildFixture(t, true)
	p := mustParse(t, b, "/api/posts?order=published.desc,title.asc.nullsfirst,id.nullslast")
	o := p.Order
	if len(o) != 3 || !o[0].Desc || o[0].NullsFirst || o[1].Desc || !o[1].NullsFirst || o[2].Desc || o[2].NullsFirst || o[2].Column.Name != "id" {
		t.Fatalf("order = %+v", o)
	}
	p = mustParse(t, b, "/api/posts?order=users(email).asc")
	if p.Order[0].Relation == nil || !p.Order[0].Relation.Relation.Forward || p.Order[0].Relation.Column.Name != "email" {
		t.Fatalf("order = %+v", p.Order)
	}
	p = mustParse(t, b, "/api/comments?order=comments!parent_id(post_id).desc")
	if p.Order[0].Relation == nil || !p.Order[0].Desc {
		t.Fatalf("order = %+v", p.Order)
	}
	p = mustParse(t, b, "/api/users?order=post_count.desc")
	if p.Order[0].Computed == nil {
		t.Fatalf("order = %+v", p.Order)
	}
	p = mustParse(t, b, "/api/places?order=location.dist.asc,id&near=SRID%3D4326%3BPOINT(-0.1281%2051.508)")
	if p.Order[0].Distance == nil || p.Order[0].Distance.Name != "location" || p.Near == nil {
		t.Fatalf("order = %+v near=%v", p.Order, p.Near)
	}
	if p.Near.(postgis.Value).Param != "SRID=4326;POINT(-0.1281 51.508)" {
		t.Fatalf("near = %+v", p.Near)
	}
	_, err := doParse(t, b, req{url: "/api/places?order=location.dist"}, allOn)
	wantCode(t, err, CodeInvalidFilter)
	p = mustParse(t, b, "/api/users?select=id,places(id)&places.order=location.dist&near=POINT(1%202)")
	if p.Select.Embeds[0].Order[0].Distance == nil || p.Near == nil {
		t.Fatalf("embed dist order = %+v near=%v", p.Select.Embeds[0].Order, p.Near)
	}
	for u, code := range map[string]string{
		"/api/posts?order=":                                CodeInvalidFilter,
		"/api/posts?order=title.sideways":                  CodeInvalidFilter,
		"/api/posts?order=title.asc.desc":                  CodeInvalidFilter,
		"/api/posts?order=title.nullsfirst.asc":            CodeInvalidFilter,
		"/api/posts?order=nope":                            CodeUnknownColumn,
		"/api/posts?order=body":                            CodeColumnNotOrderable,
		"/api/posts?order=nope(email)":                     CodeUnknownRelation,
		"/api/posts?order=users(nope)":                     CodeUnknownColumn,
		"/api/posts?order=users(full_name)":                CodeColumnNotOrderable,
		"/api/posts?order=users(email":                     CodeInvalidFilter,
		"/api/comments?order=comments(post_id)":            CodeAmbiguousRelation,
		"/api/users?order=tag_words":                       CodeColumnNotOrderable,
		"/api/places?order=location":                       CodeColumnNotOrderable,
		"/api/places?order=name.dist&near=POINT(1%202)":    CodeInvalidFilter,
		"/api/places?order=location.dist.asc&near=garbage": CodeInvalidFilter,
	} {
		_, err := doParse(t, b, req{url: u}, allOn)
		if err == nil {
			t.Errorf("%s: expected %s", u, code)
			continue
		}
		var ue *compile.UserError
		if !errors.As(err, &ue) || ue.Code != code {
			t.Errorf("%s: want %s, got %v", u, code, err)
		}
	}
	off := allOn
	off.RelationOrder, off.ComputedOrder, off.Spatial = false, false, false
	_, err = doParse(t, b, req{url: "/api/posts?order=users(email)"}, off)
	wantCode(t, err, CodeInvalidFilter)
	_, err = doParse(t, b, req{url: "/api/users?order=post_count"}, off)
	wantCode(t, err, CodeColumnNotOrderable)
	_, err = doParse(t, b, req{url: "/api/places?order=location.dist&near=POINT(1%202)"}, off)
	wantCode(t, err, CodeInvalidFilter)
}

func TestPaging(t *testing.T) {
	b := buildFixture(t, true)
	p := mustParse(t, b, "/api/posts?limit=20&offset=40&count=true")
	if p.Page.Limit != 20 || p.Page.Offset != 40 || !p.Count || !p.Page.LimitSet {
		t.Fatalf("page = %+v count=%v", p.Page, p.Count)
	}
	p = mustParse(t, b, "/api/posts?count=false")
	if p.Count {
		t.Fatal("count=false")
	}
	after := base64.RawURLEncoding.EncodeToString([]byte(`["public.posts", 7]`))
	p = mustParse(t, b, "/api/posts?after="+after)
	if !reflect.DeepEqual(p.Page.After, []any{int64(7)}) {
		t.Fatalf("after = %#v", p.Page.After)
	}
	before := base64.URLEncoding.EncodeToString([]byte(`["public.posts","13"]`))
	p = mustParse(t, b, "/api/posts?before="+before)
	if !reflect.DeepEqual(p.Page.Before, []any{int64(13)}) {
		t.Fatalf("before = %#v", p.Page.Before)
	}
	p = mustParse(t, b, "/api/posts?after="+after+"&offset=3")
	if p.Page.Offset != 3 {
		t.Fatal("after+offset allowed")
	}
	p = mustParse(t, b, "/api/users?distinct=mood,id&order=mood.asc")
	if len(p.Distinct) != 2 || p.Distinct[0].Name != "mood" {
		t.Fatalf("distinct = %+v", p.Distinct)
	}
	other := base64.RawURLEncoding.EncodeToString([]byte(`["public.users", 7]`))
	for u, code := range map[string]string{
		"/api/posts?limit=0":         CodeInvalidFilter,
		"/api/posts?limit=-1":        CodeInvalidFilter,
		"/api/posts?limit=abc":       CodeInvalidFilter,
		"/api/posts?limit=101":       CodePageSizeExceeded,
		"/api/posts?offset=-1":       CodeInvalidFilter,
		"/api/posts?offset=x":        CodeInvalidFilter,
		"/api/posts?count=yes":       CodeInvalidFilter,
		"/api/posts?limit=1&limit=2": CodeInvalidFilter,
		"/api/posts?after=!!!":       CodeInvalidCursor,
		"/api/posts?after=" + base64.RawURLEncoding.EncodeToString([]byte(`{"a":1}`)):                CodeInvalidCursor,
		"/api/posts?after=" + base64.RawURLEncoding.EncodeToString([]byte(`["public.posts"]`)):       CodeInvalidCursor,
		"/api/posts?after=" + base64.RawURLEncoding.EncodeToString([]byte(`["public.posts",1,2]`)):   CodeInvalidCursor,
		"/api/posts?after=" + base64.RawURLEncoding.EncodeToString([]byte(`["public.posts","abc"]`)): CodeInvalidCursor,
		"/api/posts?after=" + base64.RawURLEncoding.EncodeToString([]byte(`["public.posts",[1]]`)):   CodeInvalidCursor,
		"/api/posts?after=" + base64.RawURLEncoding.EncodeToString([]byte(`[1,2]`)):                  CodeInvalidCursor,
		"/api/posts?after=" + other:                     CodeInvalidCursor,
		"/api/posts?before=" + after + "&offset=1":      CodeKeysetUnsupported,
		"/api/posts?after=" + after + "&distinct=title": CodeKeysetUnsupported,
		"/api/metrics?after=" + after:                   CodeKeysetUnsupported,
		"/api/users?distinct=nope":                      CodeUnknownColumn,
		"/api/users?distinct=full_name":                 CodeColumnNotOrderable,
		"/api/users?distinct=":                          CodeInvalidFilter,
		"/api/places?distinct=location":                 CodeInvalidFilter,
		"/api/posts/1?limit=1":                          CodeInvalidFilter,
		"/api/posts/1?count=true":                       CodeInvalidFilter,
	} {
		_, err := doParse(t, b, req{url: u}, allOn)
		if err == nil {
			t.Errorf("%s: expected %s", u, code)
			continue
		}
		var ue *compile.UserError
		if !errors.As(err, &ue) || ue.Code != code {
			t.Errorf("%s: want %s, got %v", u, code, err)
		}
	}
	// zero options fall back to defaults
	p, err := doParse(t, b, req{url: "/api/posts"}, Options{})
	if err != nil || p.Page.Limit != 100 {
		t.Fatalf("plan=%+v err=%v", p, err)
	}
	// distinct + offset ok on a view
	if _, err := doParse(t, b, req{url: "/api/metrics?limit=5&offset=5"}, allOn); err != nil {
		t.Fatal(err)
	}
}

func TestKeys(t *testing.T) {
	b := buildFixture(t, true)
	p := mustParse(t, b, "/api/users/42")
	if p.Key == nil || p.Key.Constraint.Name != "users_pkey" || !reflect.DeepEqual(p.Key.Values, []any{int64(42)}) {
		t.Fatalf("key = %+v", p.Key)
	}
	p = mustParse(t, b, "/api/users/by/email/a%40b.c?select=id,post_count")
	if p.Key.Constraint.Name != "users_email_key" || !reflect.DeepEqual(p.Key.Values, []any{"a@b.c"}) {
		t.Fatalf("key = %+v", p.Key)
	}
	// filters combine with keys; single-row row route
	p = mustParse(t, b, "/api/users/1?mood=eq.happy")
	if p.Filter == nil || p.Key == nil {
		t.Fatalf("plan = %+v", p)
	}
	_, err := doParse(t, b, req{url: "/api/users/abc"}, allOn)
	wantCode(t, err, CodeInvalidFilter)
	_, err = doParse(t, b, req{url: "/api/users/1,2"}, allOn)
	wantCode(t, err, CodeInvalidFilter)
	// composite key path splitting
	got, err := splitKeySegment("3,x%2Cy", 2)
	if err != nil || !reflect.DeepEqual(got, []string{"3", "x,y"}) {
		t.Fatalf("split = %v %v", got, err)
	}
	if _, err := splitKeySegment("3", 2); err == nil {
		t.Fatal("arity")
	}
	if _, err := splitKeySegment("%zz", 1); err == nil {
		t.Fatal("bad escape")
	}
}

func TestHeaders(t *testing.T) {
	b := buildFixture(t, true)
	p, err := doParse(t, b, req{url: "/api/users/1", header: map[string]string{
		"If-None-Match": `W/"abc"`, "If-Match": `W/"def"`, "Prefer": "return=minimal, count=exact", "Accept": "application/json",
	}}, allOn)
	if err != nil || p.IfNoneMatch != `W/"abc"` || p.IfMatch != `W/"def"` || p.Prefer.Return != "minimal" || p.Accept != plan.AcceptJSON {
		t.Fatalf("plan=%+v err=%v", p, err)
	}
	off := allOn
	off.Etags = false
	p, _ = doParse(t, b, req{url: "/api/users/1", header: map[string]string{"If-Match": `W/"def"`}}, off)
	if p.IfMatch != "" {
		t.Fatal("etags off")
	}
	// Accept negotiation
	for _, tc := range []struct {
		url, accept string
		want        plan.Accept
		code        string
		opts        Options
	}{
		{"/api/users", "text/csv", plan.AcceptCSV, "", allOn},
		{"/api/users", "text/*", plan.AcceptCSV, "", allOn},
		{"/api/users", "*/*", plan.AcceptJSON, "", allOn},
		{"/api/users", "application/*", plan.AcceptJSON, "", allOn},
		{"/api/users", "text/csv;q=0.5, application/json", plan.AcceptJSON, "", allOn},
		{"/api/users", "application/json;q=0.5, text/csv", plan.AcceptCSV, "", allOn},
		{"/api/users?select=id,posts(id)", "text/csv", "", CodeNotAcceptable, allOn},
		{"/api/users?select=id,posts(id)", "text/csv, application/json;q=0.1", plan.AcceptJSON, "", allOn},
		{"/api/users/1", "text/csv", "", CodeNotAcceptable, allOn},
		{"/api/users", "text/html", "", CodeNotAcceptable, allOn},
		{"/api/users", "application/json;q=0", "", CodeNotAcceptable, allOn},
		{"/api/users", "garbage", "", CodeNotAcceptable, allOn},
		{"/api/users", "text/csv", "", CodeNotAcceptable, Options{CSV: false}},
		{"/api/rpc/search_posts?term=x", "text/csv", plan.AcceptCSV, "", allOn},
		{"/api/rpc/add_numbers?a=1", "text/csv", "", CodeNotAcceptable, allOn},
	} {
		p, err := doParse(t, b, req{url: tc.url, header: map[string]string{"Accept": tc.accept}}, tc.opts)
		if tc.code != "" {
			if err == nil {
				t.Errorf("%s %s: expected %s", tc.url, tc.accept, tc.code)
				continue
			}
			var ue *compile.UserError
			if !errors.As(err, &ue) || ue.Code != tc.code || ue.Status != 406 {
				t.Errorf("%s %s: got %v", tc.url, tc.accept, err)
			}
			continue
		}
		if err != nil || p.Accept != tc.want {
			t.Errorf("%s %s: accept=%v err=%v", tc.url, tc.accept, p, err)
		}
	}
	// Content-Type
	_, err = doParse(t, b, req{method: "POST", url: "/api/users", body: `{"email":"x"}`, header: map[string]string{"Content-Type": "text/plain"}}, allOn)
	wantCode(t, err, CodeUnsupportedMediaType)
	_, err = doParse(t, b, req{method: "POST", url: "/api/users", body: `{"email":"x"}`}, allOn)
	wantCode(t, err, CodeUnsupportedMediaType)
	p, err = doParse(t, b, req{method: "POST", url: "/api/users?select=id,posts(id)&posts.limit=2", body: `{"email":"x"}`, header: map[string]string{"Content-Type": "application/json; charset=utf-8"}}, allOn)
	if err != nil || p.Method != plan.POST || p.Body == nil || p.Body.Object["email"] != "x" || p.Select == nil || p.Select.Embeds[0].Page.Limit != 2 {
		t.Fatalf("plan=%+v err=%v", p, err)
	}
	if _, err := doParse(t, b, req{method: "DELETE", url: "/api/users/1"}, allOn); err != nil {
		t.Fatal(err)
	}
	// mutation routes reject read params
	for _, u := range []string{"/api/users?limit=1", "/api/users?mood=eq.happy", "/api/users?order=id", "/api/users?on_conflict=email"} {
		_, err := doParse(t, b, req{method: "POST", url: u, body: "{}", header: map[string]string{"Content-Type": "application/json"}}, allOn)
		wantCode(t, err, CodeInvalidFilter)
	}
	jsonHdr := map[string]string{"Content-Type": "application/json"}
	p, err = doParse(t, b, req{method: "PUT", url: "/api/users?on_conflict=email", body: `{"email":"x"}`, header: jsonHdr}, allOn)
	if err != nil || p.OnConflict == nil || p.OnConflict.Name != "users_email_key" {
		t.Fatalf("plan=%+v err=%v", p, err)
	}
	// on_conflict=id resolves to the PK; the body then fails the conflict-column rule (id is generated).
	_, err = doParse(t, b, req{method: "PUT", url: "/api/users?on_conflict=id", body: `{"email":"x"}`, header: jsonHdr}, allOn)
	wantCode(t, err, CodeInvalidBody)
	_, err = doParse(t, b, req{method: "PUT", url: "/api/users?on_conflict=mood", body: `{"email":"x"}`, header: jsonHdr}, allOn)
	wantCode(t, err, CodeInvalidBody)
	_, err = doParse(t, b, req{method: "PUT", url: "/api/users?on_conflict=nope", body: `{"email":"x"}`, header: jsonHdr}, allOn)
	wantCode(t, err, CodeUnknownColumn)
	// HEAD parses like GET
	p, err = doParse(t, b, req{method: "HEAD", url: "/api/users?limit=2&count=true"}, allOn)
	if err != nil || p.Method != plan.HEAD || p.Page.Limit != 2 || !p.Count {
		t.Fatalf("plan=%+v err=%v", p, err)
	}
}

func TestRPC(t *testing.T) {
	b := buildFixture(t, true)
	p := mustParse(t, b, "/api/rpc/add_numbers?a=2&b=40")
	if p.RPC == nil || p.Body == nil || !reflect.DeepEqual(p.Body.Object, map[string]any{"a": int64(2), "b": int64(40)}) {
		t.Fatalf("plan=%+v", p)
	}
	p = mustParse(t, b, "/api/rpc/search_posts?term=rest&published=eq.true&order=id.desc&limit=20&count=true&select=id,title,users(email)")
	if p.Body.Object["term"] != "rest" || p.Filter.(plan.Cond).Column.Name != "published" || p.Page.Limit != 20 || !p.Count || len(p.Select.Embeds) != 1 {
		t.Fatalf("plan=%+v", p)
	}
	p = mustParse(t, b, "/api/rpc/word_lengths?words={a,bb}")
	if !reflect.DeepEqual(p.Body.Object["words"], []any{"a", "bb"}) {
		t.Fatalf("words = %#v", p.Body.Object["words"])
	}
	for u, code := range map[string]string{
		"/api/rpc/add_numbers?c=1":                 CodeUnknownArgument,
		"/api/rpc/add_numbers?a=x":                 CodeInvalidFilter,
		"/api/rpc/add_numbers?select=id":           CodeInvalidSelect,
		"/api/rpc/add_numbers?order=id":            CodeInvalidFilter,
		"/api/rpc/search_posts?nope=eq.1":          CodeUnknownColumn,
		"/api/rpc/search_posts?after=" + cursor(1): CodeKeysetUnsupported,
	} {
		_, err := doParse(t, b, req{url: u}, allOn)
		if err == nil {
			t.Errorf("%s: expected %s", u, code)
			continue
		}
		var ue *compile.UserError
		if !errors.As(err, &ue) || ue.Code != code {
			t.Errorf("%s: want %s, got %v", u, code, err)
		}
	}
	// POST body
	hdr := map[string]string{"Content-Type": "application/json"}
	p, err := doParse(t, b, req{method: "POST", url: "/api/rpc/publish_post?select=title,users(email)", body: `{"post_id": 7}`, header: hdr}, allOn)
	if err != nil || p.Body.Object["post_id"] != int64(7) || len(p.Select.Embeds) != 1 {
		t.Fatalf("plan=%+v err=%v", p, err)
	}
	p, err = doParse(t, b, req{method: "POST", url: "/api/rpc/add_numbers", body: `{"a": "3", "b": null}`, header: hdr}, allOn)
	if err != nil || p.Body.Object["a"] != int64(3) || p.Body.Object["b"] != nil {
		t.Fatalf("plan=%+v err=%v", p, err)
	}
	p, err = doParse(t, b, req{method: "POST", url: "/api/rpc/word_lengths", body: `{"words": ["a","b"]}`, header: hdr}, allOn)
	if err != nil || !reflect.DeepEqual(p.Body.Object["words"], []any{"a", "b"}) {
		t.Fatalf("plan=%+v err=%v", p, err)
	}
	p, err = doParse(t, b, req{method: "POST", url: "/api/rpc/add_numbers"}, allOn)
	if err != nil || len(p.Body.Object) != 0 {
		t.Fatalf("plan=%+v err=%v", p, err)
	}
	p, err = doParse(t, b, req{method: "POST", url: "/api/rpc/search_posts?published=eq.true", body: `{"term":"x"}`, header: hdr}, allOn)
	if err != nil || p.Filter == nil {
		t.Fatalf("plan=%+v err=%v", p, err)
	}
	for body, code := range map[string]string{
		`[1]`:              CodeInvalidBody,
		`{"nope": 1}`:      CodeUnknownArgument,
		`{"a": "x"}`:       CodeInvalidBody,
		`{"a": 1} {"b":2}`: CodeInvalidBody,
		`{"a": [1]}`:       CodeInvalidBody,
		`nope`:             CodeInvalidBody,
	} {
		_, err := doParse(t, b, req{method: "POST", url: "/api/rpc/add_numbers", body: body, header: hdr}, allOn)
		if err == nil {
			t.Errorf("%s: expected %s", body, code)
			continue
		}
		var ue *compile.UserError
		if !errors.As(err, &ue) || ue.Code != code {
			t.Errorf("%s: want %s, got %v", body, code, err)
		}
	}
	_, err = doParse(t, b, req{method: "POST", url: "/api/rpc/add_numbers?a=1", body: `{}`, header: hdr}, allOn)
	wantCode(t, err, CodeUnknownArgument)
}

func TestCoerceJSON(t *testing.T) {
	b := buildFixture(t, true)
	p := &parser{built: b, opts: allOn}
	users := b.ByPath["users"]
	col := func(name string) colType { return p.typeOf(users.Table.Column(name)) }
	if v, err := p.coerceJSON(col("settings"), map[string]any{"a": 1.0}); err != nil || v != `{"a":1}` {
		t.Fatalf("jsonb: %v %v", v, err)
	}
	if v, err := p.coerceJSON(col("address"), map[string]any{"city": "x"}); err != nil || v != `{"city":"x"}` {
		t.Fatalf("composite: %v %v", v, err)
	}
	if v, err := p.coerceJSON(col("mood"), "happy"); err != nil || v != "happy" {
		t.Fatalf("enum: %v %v", v, err)
	}
	if _, err := p.coerceJSON(col("mood"), "angry"); err == nil {
		t.Fatal("bad enum")
	}
	if v, err := p.coerceJSON(col("tags"), "{a,b}"); err != nil || !reflect.DeepEqual(v, []any{"a", "b"}) {
		t.Fatalf("array literal string: %v %v", v, err)
	}
	if v, err := p.coerceJSON(col("email"), true); err != nil || v != "true" {
		t.Fatalf("bool to text: %v %v", v, err)
	}
	if _, err := p.coerceJSON(col("id"), map[string]any{}); err == nil {
		t.Fatal("object to int")
	}
	places := b.ByPath["places"]
	if v, err := p.coerceJSON(p.typeOf(places.Table.Column("location")), map[string]any{"type": "Point", "coordinates": []any{1.0, 2.0}}); err != nil || v.(postgis.Value).Flavour != postgis.Geometry {
		t.Fatalf("spatial: %v %v", v, err)
	}
	if _, err := p.coerceJSON(p.typeOf(places.Table.Column("location")), 5.0); err == nil {
		t.Fatal("spatial number")
	}
	for _, tc := range []struct {
		typ, in string
		ok      bool
	}{
		{"uuid", "123e4567-e89b-12d3-a456-426614174000", true},
		{"uuid", "nope", false},
		{"numeric", "12.5e3", true},
		{"numeric", "NaN", true},
		{"numeric", "12abc", false},
		{"float8", "1.5", true},
		{"float8", "x", false},
		{"bool", "yes", true},
		{"bool", "maybe", false},
		{"int2", "1.5", false},
		{"timestamptz", "anything", true},
	} {
		_, err := p.coerceElem(colType{base: tc.typ, kind: scalarKind(tc.typ)}, tc.in)
		if (err == nil) != tc.ok {
			t.Errorf("%s %q: err=%v", tc.typ, tc.in, err)
		}
	}
	if _, err := p.coerceElem(colType{base: "address", kind: kindComposite}, "x"); err == nil {
		t.Fatal("composite scalar")
	}
	if v, err := p.coerceElem(colType{base: "address", kind: kindComposite}, `{"city":"x"}`); err != nil || v != `{"city":"x"}` {
		t.Fatalf("composite json text: %v %v", v, err)
	}
}

func TestParseArrayLiteral(t *testing.T) {
	deref := func(items []*string) []any {
		out := make([]any, len(items))
		for i, it := range items {
			if it != nil {
				out[i] = *it
			}
		}
		return out
	}
	for in, want := range map[string][]any{
		`{a,b}`:           {"a", "b"},
		`{ a , b }`:       {"a", "b"},
		`{"a b","c\"d"}`:  {"a b", `c"d`},
		`{NULL,"NULL",x}`: {nil, "NULL", "x"},
		`{}`:              {},
		`{"a\\b"}`:        {`a\b`},
		`{"",x}`:          {"", "x"},
	} {
		got, err := parseArrayLiteral(in)
		if err != nil {
			t.Errorf("%s: %v", in, err)
			continue
		}
		if !reflect.DeepEqual(deref(got), want) {
			t.Errorf("%s: got %#v want %#v", in, deref(got), want)
		}
	}
	for _, in := range []string{`a,b`, `{a`, `{a,{b}}`, `{"a}`, `{a,}`, `{"a"b}`, ``} {
		if _, err := parseArrayLiteral(in); err == nil {
			t.Errorf("%s: expected error", in)
		}
	}
}

func TestSplitQuery(t *testing.T) {
	ps, err := splitQuery("a=1&b=x%20y&&c&d=1=2&e=%2B+")
	if err != nil {
		t.Fatal(err)
	}
	want := []param{{"a", "1"}, {"b", "x y"}, {"c", ""}, {"d", "1=2"}, {"e", "+ "}}
	if !reflect.DeepEqual(ps, want) {
		t.Fatalf("got %+v", ps)
	}
	if _, err := splitQuery("a=%zz"); err == nil {
		t.Fatal("bad escape")
	}
}

func TestErrorsAreUserErrors(t *testing.T) {
	b := buildFixture(t, true)
	_, err := doParse(t, b, req{url: "/api/users?select=("}, allOn)
	var ue *compile.UserError
	if !errors.As(err, &ue) || ue.Status != 400 {
		t.Fatalf("got %v", err)
	}
	m, _ := b.Match("/users")
	if _, err := Parse(b, m, httptest.NewRequest("OPTIONS", "/api/users", nil), allOn); err == nil {
		t.Fatal("OPTIONS")
	}
	if _, err := Parse(nil, m, httptest.NewRequest("GET", "/api/users", nil), allOn); err == nil {
		t.Fatal("nil built")
	}
}

func BenchmarkParse(b *testing.B) {
	built := buildFixture(b, true)
	hr := httptest.NewRequest("GET", "/api/users?select=id,email,posts(id,title,comments(id))&mood=eq.happy&posts.published=is.true&order=id.desc&limit=20&count=true", nil)
	m, _ := built.Match("/users")
	b.ReportAllocs()
	for b.Loop() {
		if _, err := Parse(built, m, hr, allOn); err != nil {
			b.Fatal(err)
		}
	}
}
