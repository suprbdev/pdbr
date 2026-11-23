package parse

import (
	"errors"
	"net/http"
	"strings"
	"testing"

	"github.com/suprbdev/pdbr/internal/compile"
)

// FuzzParse feeds random paths, query strings, headers (the accept input
// doubles as the Prefer header) and RPC bodies
// through Parse: it must never panic, and every error must be a
// *compile.UserError with a code and a status.
func FuzzParse(f *testing.F) {
	seeds := []struct{ method, path, query, accept, body string }{
		{"GET", "/users", "select=id,email,posts(id,title,comments(id))&mood=eq.happy&posts.published=is.true&order=id.desc&limit=20&count=true", "", ""},
		{"GET", "/users", `and=(mood.in.(happy,ok),or(email.ew.@example.com,tags.cs.{admin}),not.settings.hk.banned)`, "text/csv", ""},
		{"GET", "/users", "posts!none.title=eq.x&posts=exists.true&order=post_count.desc,posts(title).asc", "", ""},
		{"GET", "/comments", "select=id,parent:comments!parent_id(id),replies:comments!comments_parent_id_fkey!inner(id)&replies.limit=2", "", ""},
		{"GET", "/places", "select=name,web:location(transform=3857)&location=st_dwithin.(\"POINT(1 2)\",100)&order=location.dist&near=POINT(1 2)", "", ""},
		{"GET", "/posts", "after=WyJwdWJsaWMucG9zdHMiLDdd&distinct=title&before=x&offset=1", "application/json;q=0.5, text/csv", ""},
		{"GET", "/users/1", "select=id&mood=eq.happy", "*/*", ""},
		{"GET", "/users/by/email/a%40b.c", "", "", ""},
		{"GET", "/rpc/search_posts", "term=hello&published=is.true&order=id.desc&limit=2", "text/csv", ""},
		{"POST", "/rpc/add_numbers", "", "", `{"a": 1, "b": "2"}`},
		{"POST", "/rpc/user_greeting", "", "", `{"greeting": "x", "u": {"id": 1, "mood": "ok", "address": {"city": "y"}}}`},
		{"GET", "/rpc/user_greeting", `greeting=x&u={"email":"a","tags":["b"]}`, "", ""},
		{"POST", "/rpc/total_title_length", "", "", `{"ps": [{"title": "a"}, null]}`},
		{"POST", "/users", "select=id,posts(id)", "", `{"email":"x"}`},
		{"PUT", "/users", "on_conflict=email", "", `{}`},
		{"DELETE", "/users/1", "", "", ""},
		{"HEAD", "/metrics", "limit=5&offset=5", "", ""},
		{"PATCH", "/users", "id=gt.1&select=id", "handling=strict, max-affected=2", `{"mood":"ok"}`},
		{"DELETE", "/posts", "or=(id.eq.1,title.like.x*)", "return=headers-only, tx=rollback", ""},
		{"POST", "/users", "on_conflict=email", "resolution=ignore-duplicates, missing=default", `[{"email":"x"},{}]`},
		{"PUT", "/users/by/email/a%40b.c", "select=id", "count=exact", `{"email":"a@b.c","mood":"ok"}`},
		{"GET", "/kb/docs", "search=fts(english).fat%20%26%20cat&body=not.wfts.x&order=search.rank.desc,title.trgm_dist&trgm=pg&title=trgm_sim.pg", "", ""},
		{"GET", "/kb/docs", `or=(attrs.hk_all.{a,b},attrs.cs.{"a":"1"},path.lquery.*.b,paths.lquery_any.{a,*.b})&pages=cs.3&valid=adj.[2020-01-01,)&net=cd_strict.10.0.0.0/8&email=ilike.*A*`, "", ""},
		{"PATCH", "/kb/docs/1", "", "", `{"attrs":{"a":null},"pages":{"start":1,"isEmpty":false},"slots":["[1,2)",{"end":9}]}`},
		{"GET", "/users", "select=mood,count(),total:balance::numeric.sum()::text,posts(published,id.max())&posts.order=published&order=mood.desc&limit=2&offset=1", "count=exact", ""},
		{"GET", "/places", "select=owner_id,location.extent(),u:area.union(),location.centroid(),location.collect(),n:location.count()", "text/csv", ""},
		{"GET", "/places", "select=id,location,area&geometry=area&limit=2&order=id", "application/geo+json", ""},
		{"GET", "/places/1", "select=id,name,location&geometry=location", "application/geo+json", ""},
		{"GET", "/posts", "select=count,id.sum(x),title::text,users(count())&after=x&distinct=id&order=id", "", ""},
		{"GET", "/rpc/search_posts", "term=a&select=author_id,count()&order=author_id", "", ""},
		{"GET", "/users", `select=id,x:settings->a->>b,settings->"a.b"->-1::int,tags->0,address->>city::text&settings->>theme=eq.dark&settings->n=in.(1,2)&order=settings->>theme.desc,settings->0`, "", ""},
		{"GET", "/users", `select=settings->>theme,count(),settings->n::numeric.sum()&or=(settings->>x.eq.1,not.settings->"y".cs.[1])&order=settings->>theme&posts.settings->>a=eq.1`, "", ""},
		{"GET", "/users", "email=match.^a&id=gt(any).{1,2}&mood=eq(all).{ok}&or=(email.like(any).{a*,*b},email.not.imatch(all).{x})&settings->>t=ilike(any).{A*}", "", ""},
		{"GET", "/posts", "select=id,...users!inner(a:email,...places(name,...users(id)))&users.mood=eq.ok&users.places.order=id.desc&users.places.limit=2", "text/csv", ""},
		{"GET", "/posts", "select=published,n:count(),...users(mood,m:id.max(),settings->>t)&order=published&users.email=sw.a", "count=exact", ""},
		{"GET", "/users", "select=x:...posts(id),...nope(a),...posts(count()),...comments", "", ""},
		{"GET", "/kb/docs", "email=imatch.^a&email=match(any).{^b,^c}&title=like(all).{P*}", "", ""},
		{"GET", "/kb/docs", "body=unaccent_ilike.*cr%C3%A8me*&or=(body.not.unaccent_eq.Caf%C3%A9,body.unaccent_sw.Zo,body.unaccent_includes.50%25)", "", ""},
		{"GET", "/kb/embeddings", "select=id,emb,chunks(id)&order=emb.vec_cos,half.vec_l2.desc&chunks.order=emb.vec_ip&vec=%5B0.1%2C2%2C-3e-2%5D&emb=not.is.null&after=x", "", ""},
		{"GET", "/kb/embeddings", "order=sparse.vec_l1&vec=%7B1%3A1.5%7D%2F3&limit=2", "text/csv", ""},
		{"POST", "/kb/embeddings", "select=id,emb", "", `{"emb":[1,2,3],"half":"[1,2]","sparse":[0,1.5]}`},
		{"GET", "/places", "location=bbox_intersects2d.POINT(1%202)&or=(area.not.bbox_intersects2d.POINT(0%200))", "", ""},
		{"GET", "/posts", "select=id,users(settings->>theme)&users.settings->>theme=like.d*&users.order=settings->n.desc&users.settings->>a->b=eq.x", "", ""},
	}
	for _, s := range seeds {
		f.Add(s.method, s.path, s.query, s.accept, s.body)
	}
	built := buildContrib(f, true)
	aggAll := allOn
	aggAll.Aggregates, aggAll.SpatialAggregates = true, true
	optsList := []Options{allOn, {}, {Spatial: true, CSV: true}, {RelationFilters: true, ComputedOrder: true, MaxPageSize: 3, MaxEmbedDepth: 1}, aggAll, {Aggregates: true}}
	f.Fuzz(func(t *testing.T, method, path, query, accept, body string) {
		if strings.ContainsAny(method, " \r\n\t") || strings.ContainsAny(path+query, " \r\n\t#") {
			t.Skip()
		}
		if !strings.HasPrefix(path, "/") {
			path = "/" + path
		}
		var rd *strings.Reader
		if body != "" {
			rd = strings.NewReader(body)
		}
		var r *http.Request
		var err error
		if rd != nil {
			r, err = http.NewRequest(method, "http://x/api"+path+"?"+query, rd)
		} else {
			r, err = http.NewRequest(method, "http://x/api"+path+"?"+query, nil)
		}
		if err != nil {
			t.Skip()
		}
		if accept != "" {
			r.Header.Set("Accept", accept)
			r.Header.Set("Prefer", accept)
		}
		r.Header.Set("Content-Type", "application/json")
		m, ok := built.Match(strings.TrimPrefix(r.URL.EscapedPath(), "/api"))
		if !ok {
			t.Skip()
		}
		for _, opts := range optsList {
			if rd != nil {
				_, _ = rd.Seek(0, 0)
				r.Body = readCloser{rd}
				r.ContentLength = int64(rd.Len())
			}
			p, err := Parse(built, m, r, opts)
			if err == nil {
				if p == nil || p.Page.Limit <= 0 && m.RPC == nil {
					t.Fatalf("plan without page limit: %+v", p)
				}
				continue
			}
			var ue *compile.UserError
			if !errors.As(err, &ue) {
				t.Fatalf("non-UserError: %T %v", err, err)
			}
			if ue.Code == "" || ue.Status < 400 || ue.Status > 599 {
				t.Fatalf("bad UserError: %+v", ue)
			}
		}
	})
}

type readCloser struct{ *strings.Reader }

func (readCloser) Close() error { return nil }
