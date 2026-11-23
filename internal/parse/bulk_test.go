package parse

import (
	"reflect"
	"testing"

	"github.com/suprbdev/pdbcore/testutil"

	"github.com/suprbdev/pdbr/internal/api"
	"github.com/suprbdev/pdbr/internal/inflect"
	"github.com/suprbdev/pdbr/internal/plan"
)

var jsonCT = map[string]string{"Content-Type": "application/json"}

func withHeader(h map[string]string, k, v string) map[string]string {
	out := map[string]string{k: v}
	for hk, hv := range h {
		out[hk] = hv
	}
	return out
}

func TestBulkWrites(t *testing.T) {
	b := buildFixture(t, true)
	p, err := doParse(t, b, req{method: "PATCH", url: "/api/users?mood=eq.sad&id=gt.1", header: jsonCT, body: `{"full_name":"x"}`}, allOn)
	if err != nil {
		t.Fatal(err)
	}
	if p.Key != nil || p.Filter == nil || p.Body == nil || p.Body.Object["full_name"] != "x" {
		t.Fatalf("bulk PATCH plan = %+v", p)
	}
	p, err = doParse(t, b, req{method: "DELETE", url: "/api/posts?users.email=eq.a@b.c&select=id"}, allOn)
	if err != nil || p.Filter == nil || p.Select == nil {
		t.Fatalf("bulk DELETE = %+v %v", p, err)
	}

	for _, r := range []req{
		{method: "DELETE", url: "/api/posts"},
		{method: "DELETE", url: "/api/posts?select=id"},
		{method: "PATCH", url: "/api/users", header: jsonCT, body: `{"full_name":"x"}`},
	} {
		_, err := doParse(t, b, r, allOn)
		wantCode(t, err, CodeUnfilteredMutation)
	}
	if p, err := doParse(t, b, req{method: "DELETE", url: "/api/posts?select=id,users!inner(id)&users.email=eq.a@b.c"}, allOn); err != nil || p.Filter != nil {
		t.Fatalf("!inner embed restricts like a filter: %+v %v", p, err)
	}
	_, err = doParse(t, b, req{method: "DELETE", url: "/api/posts?id=eq.1", header: map[string]string{"If-Match": `W/"x"`}}, allOn)
	wantCode(t, err, CodeIfMatchUnsupported)
	noEtags := allOn
	noEtags.Etags = false
	_, err = doParse(t, b, req{method: "DELETE", url: "/api/posts?id=eq.1", header: map[string]string{"If-Match": `W/"x"`}}, noEtags)
	wantCode(t, err, CodeIfMatchUnsupported)
	open := allOn
	open.AllowUnfilteredMutations = true
	if p, err := doParse(t, b, req{method: "DELETE", url: "/api/posts"}, open); err != nil || p.Filter != nil {
		t.Fatalf("allowed unfiltered = %+v %v", p, err)
	}

	cases := []struct {
		r    req
		code string
	}{
		{req{method: "PATCH", url: "/api/users?full_name=eq.x", header: jsonCT, body: `{"mood":"ok"}`}, CodeColumnNotFilterable},
		{req{method: "PATCH", url: "/api/users?id=eq.1", header: jsonCT, body: `[{"mood":"ok"}]`}, CodeInvalidBody},
		{req{method: "PATCH", url: "/api/users?id=eq.1", header: jsonCT, body: `{}`}, CodeInvalidBody},
		{req{method: "DELETE", url: "/api/users?id=eq.1&limit=1"}, CodeInvalidParameter},
		{req{method: "DELETE", url: "/api/users?id=eq.1&order=id"}, CodeInvalidParameter},
		{req{method: "DELETE", url: "/api/users?id=eq.1&on_conflict=email"}, CodeInvalidParameter},
		{req{method: "DELETE", url: "/api/users/1?id=eq.1"}, CodeInvalidFilter},
		{req{method: "PATCH", url: "/api/users/1?id=eq.1", header: jsonCT, body: `{"mood":"ok"}`}, CodeInvalidFilter},
	}
	for _, tc := range cases {
		_, err := doParse(t, b, tc.r, allOn)
		wantCode(t, err, tc.code)
	}
}

func TestPrefer(t *testing.T) {
	b := buildFixture(t, true)
	patch := func(url, prefer string) (*plan.Request, error) {
		return doParse(t, b, req{method: "PATCH", url: url, header: withHeader(jsonCT, "Prefer", prefer), body: `{"full_name":"x"}`}, allOn)
	}
	cases := []struct {
		url, prefer string
		ret         string
		strict      bool
		max         int64
		applied     []string
	}{
		{"/api/users/1", "", "representation", false, -1, nil},
		{"/api/users/1", "return=headers-only", "headers-only", false, -1, []string{"return=headers-only"}},
		{"/api/users/1", "RETURN=Minimal; x=y", "minimal", false, -1, []string{"return=minimal"}},
		{"/api/users/1", "return=minimal, return=representation", "representation", false, -1, []string{"return=representation"}},
		{"/api/users?id=gt.1", "max-affected=3", "representation", false, -1, nil},
		{"/api/users?id=gt.1", "max-affected=3, handling=strict", "representation", true, 3, []string{"max-affected=3", "handling=strict"}},
		{"/api/users?id=gt.1", "handling=lenient, tx=rollback, return=nope, max-affected=-1", "representation", false, -1, []string{"handling=lenient"}},
		{"/api/users?id=gt.1", "handling=strict, max-affected=2, handling=lenient", "representation", false, -1, []string{"handling=lenient"}},
		{"/api/users?id=gt.1", "handling=lenient, handling=strict, max-affected=2", "representation", true, 2, []string{"handling=strict", "max-affected=2"}},
	}
	for _, tc := range cases {
		p, err := patch(tc.url, tc.prefer)
		if err != nil {
			t.Fatalf("%q: %v", tc.prefer, err)
		}
		pf := p.Prefer
		var max int64 = -1
		if pf.MaxAffected != nil {
			max = *pf.MaxAffected
		}
		if pf.Return != tc.ret || pf.Strict != tc.strict || max != tc.max || !reflect.DeepEqual(pf.Applied, tc.applied) {
			t.Errorf("%q: prefer = %+v (max %d), want return=%s strict=%v max=%d applied=%v", tc.prefer, pf, max, tc.ret, tc.strict, tc.max, tc.applied)
		}
	}
	if p, _ := patch("/api/users/1", "return=headers-only"); !p.Prefer.Minimal() {
		t.Error("headers-only is not minimal")
	}
	for _, bad := range []string{"tx=commit", "return=nope", "max-affected=x", "max-affected=-2", "handling=maybe", "timezone="} {
		_, err := patch("/api/users/1", "handling=strict, "+bad)
		wantCode(t, err, CodeInvalidPreference)
	}
	p, err := doParse(t, b, req{url: "/api/users?id=eq.1", header: map[string]string{"Prefer": "return=minimal, max-affected=1, handling=strict"}}, allOn)
	if err != nil || p.Prefer.MaxAffected != nil || !reflect.DeepEqual(p.Prefer.Applied, []string{"handling=strict"}) {
		t.Fatalf("GET prefer = %+v %v", p, err)
	}
	p, err = doParse(t, b, req{method: "POST", url: "/api/users", header: withHeader(jsonCT, "Prefer", "handling=strict, max-affected=1"), body: `{"email":"a@b.c"}`}, allOn)
	if err != nil || p.Prefer.MaxAffected != nil {
		t.Fatalf("POST max-affected = %+v %v", p, err)
	}
}

func TestPreferCount(t *testing.T) {
	b := buildFixture(t, true)
	cases := []struct {
		r       req
		count   bool
		mode    plan.CountMode
		applied []string
	}{
		{req{url: "/api/users", header: map[string]string{"Prefer": "count=exact"}}, true, plan.CountExact, []string{"count=exact"}},
		{req{url: "/api/users", header: map[string]string{"Prefer": "count=planned"}}, true, plan.CountPlanned, []string{"count=planned"}},
		{req{method: "HEAD", url: "/api/users?select=id", header: map[string]string{"Prefer": "count=estimated"}}, true, plan.CountEstimated, []string{"count=estimated"}},
		{req{method: "HEAD", url: "/api/posts?select=id", header: map[string]string{"Prefer": "count=estimated"}}, true, plan.CountExact, []string{"count=exact"}},
		{req{url: "/api/users?mood=eq.ok", header: map[string]string{"Prefer": "count=planned"}}, true, plan.CountExact, []string{"count=exact"}},
		{req{url: "/api/users?distinct=mood", header: map[string]string{"Prefer": "count=estimated"}}, true, plan.CountExact, []string{"count=exact"}},
		{req{url: "/api/users?select=id,posts!inner(id)&limit=1", header: map[string]string{"Prefer": "count=planned"}}, true, plan.CountExact, []string{"count=exact"}},
		{req{url: "/api/metrics", header: map[string]string{"Prefer": "count=planned"}}, true, plan.CountExact, []string{"count=exact"}},
		{req{url: "/api/rpc/search_posts?term=x", header: map[string]string{"Prefer": "count=planned"}}, true, plan.CountExact, []string{"count=exact"}},
		{req{url: "/api/users?count=true"}, true, "", nil},
		{req{url: "/api/users/1", header: map[string]string{"Prefer": "count=exact"}}, false, "", nil},
		{req{method: "DELETE", url: "/api/users?id=eq.1", header: map[string]string{"Prefer": "count=planned"}}, true, plan.CountExact, []string{"count=exact"}},
		{req{method: "DELETE", url: "/api/users/1", header: map[string]string{"Prefer": "count=exact"}}, false, "", nil},
		{req{url: "/api/users", header: map[string]string{"Prefer": "count=nope"}}, false, "", nil},
	}
	for _, tc := range cases {
		p, err := doParse(t, b, tc.r, allOn)
		if err != nil {
			t.Fatalf("%s %s: %v", tc.r.method, tc.r.url, err)
		}
		if p.Count != tc.count || p.CountMode != tc.mode || !reflect.DeepEqual(p.Prefer.Applied, tc.applied) {
			t.Errorf("%s %s %v: count=%v mode=%q applied=%v", tc.r.method, tc.r.url, tc.r.header, p.Count, p.CountMode, p.Prefer.Applied)
		}
	}
	_, err := doParse(t, b, req{url: "/api/users", header: map[string]string{"Prefer": "handling=strict, count=nope"}}, allOn)
	wantCode(t, err, CodeInvalidPreference)
}

func TestPreferResolution(t *testing.T) {
	b := buildFixture(t, true)
	post := func(url, prefer, body string) (*plan.Request, error) {
		return doParse(t, b, req{method: "POST", url: url, header: withHeader(jsonCT, "Prefer", prefer), body: body}, allOn)
	}
	p, err := post("/api/users?on_conflict=email", "resolution=merge-duplicates, missing=default", `[{"email":"a@b.c"},{"email":"d@e.f","mood":"ok"}]`)
	if err != nil {
		t.Fatal(err)
	}
	if p.Prefer.Resolution != plan.MergeDuplicates || !p.Prefer.MissingDefault || p.OnConflict == nil || p.OnConflict.Columns[0] != "email" ||
		!reflect.DeepEqual(p.Prefer.Applied, []string{"resolution=merge-duplicates", "missing=default"}) {
		t.Fatalf("merge plan = %+v / %+v", p.Prefer, p.OnConflict)
	}
	p, err = post("/api/posts", "resolution=ignore-duplicates", `{"author_id":1,"title":"x"}`)
	if err != nil || p.OnConflict == nil || p.OnConflict.Columns[0] != "id" {
		t.Fatalf("ignore defaults to the PK: %+v %v", p, err)
	}
	p, err = post("/api/posts", "resolution=merge-duplicates", `{"author_id":1,"title":"x"}`)
	if err != nil || p.OnConflict == nil {
		t.Fatalf("merge without target columns in the body: %+v %v", p, err)
	}
	for _, tc := range []struct {
		url, prefer, body, code string
	}{
		{"/api/users?on_conflict=email", "", `{"email":"a@b.c"}`, CodeInvalidParameter},
		{"/api/users?on_conflict=email", "resolution=nope", `{"email":"a@b.c"}`, CodeInvalidParameter},
		{"/api/users?on_conflict=full_name", "resolution=merge-duplicates", `{"email":"a@b.c"}`, CodeInvalidBody},
		{"/api/users", "handling=strict, resolution=nope", `{"email":"a@b.c"}`, CodeInvalidPreference},
		{"/api/users", "handling=strict, missing=null", `{"email":"a@b.c"}`, CodeInvalidPreference},
	} {
		_, err := post(tc.url, tc.prefer, tc.body)
		wantCode(t, err, tc.code)
	}
	locked := buildFixture(t, true)
	locked.ByPath["users"].NoUpdate = map[string]bool{"full_name": true}
	_, err = doParse(t, locked, req{method: "POST", url: "/api/users?on_conflict=email", header: withHeader(jsonCT, "Prefer", "resolution=merge-duplicates"), body: `{"email":"a@b.c","full_name":"x"}`}, allOn)
	wantCode(t, err, CodeInvalidBody)
	for _, prefer := range []string{"", "resolution=ignore-duplicates"} {
		if _, err := doParse(t, locked, req{method: "POST", url: "/api/users", header: withHeader(jsonCT, "Prefer", prefer), body: `{"email":"a@b.c","full_name":"x"}`}, allOn); err != nil {
			t.Fatalf("create may still set a no-update column (%q): %v", prefer, err)
		}
	}
	cat := testutil.FixtureCatalog()
	for _, tb := range cat.Tables {
		if tb.Name == "posts" {
			tb.Privileges.Update = false
		}
	}
	noUpdate, err := api.New(cat, inflect.Default, api.Options{Schemas: []string{"public"}, BasePath: "/api", Logger: quiet()}).Build()
	if err != nil {
		t.Fatal(err)
	}
	_, err = doParse(t, noUpdate, req{method: "POST", url: "/api/posts", header: withHeader(jsonCT, "Prefer", "resolution=merge-duplicates"), body: `{"author_id":1,"title":"x"}`}, allOn)
	wantCode(t, err, CodeInvalidPreference)
	if _, err := doParse(t, noUpdate, req{method: "POST", url: "/api/posts", header: withHeader(jsonCT, "Prefer", "resolution=ignore-duplicates"), body: `{"author_id":1,"title":"x"}`}, allOn); err != nil {
		t.Fatalf("ignore-duplicates needs no UPDATE: %v", err)
	}
	p, err = doParse(t, b, req{method: "PATCH", url: "/api/users/1", header: withHeader(jsonCT, "Prefer", "resolution=merge-duplicates, missing=default"), body: `{"mood":"ok"}`}, allOn)
	if err != nil || p.Prefer.Resolution != "" || p.Prefer.MissingDefault || len(p.Prefer.Applied) != 0 {
		t.Fatalf("resolution/missing on PATCH must be ignored: %+v %v", p.Prefer, err)
	}
}

func TestKeyedPut(t *testing.T) {
	b := buildFixture(t, true)
	p, err := doParse(t, b, req{method: "PUT", url: "/api/users/by/email/a%40b.c", header: jsonCT, body: `{"full_name":"Ada"}`}, allOn)
	if err != nil {
		t.Fatal(err)
	}
	if p.Body.Object["email"] != "a@b.c" || p.OnConflict == nil || p.OnConflict.Columns[0] != "email" || len(p.Body.Columns) != 2 {
		t.Fatalf("keyed PUT plan = %+v %+v", p.Body, p.OnConflict)
	}
	if _, err := doParse(t, b, req{method: "PUT", url: "/api/users/by/email/a%40b.c", header: jsonCT, body: `{"email":"a@b.c","mood":"ok"}`}, allOn); err != nil {
		t.Fatalf("matching key in body: %v", err)
	}
	for _, tc := range []struct {
		url, body, code string
	}{
		{"/api/users/by/email/a%40b.c", `{"email":"x@y.z"}`, CodeInvalidBody},
		{"/api/users/by/email/a%40b.c", `[{"full_name":"x"}]`, CodeInvalidBody},
		{"/api/users/by/email/a%40b.c?on_conflict=email", `{"full_name":"x"}`, CodeInvalidParameter},
		{"/api/users/by/email/a%40b.c?id=eq.1", `{"full_name":"x"}`, CodeInvalidFilter},
	} {
		_, err := doParse(t, b, req{method: "PUT", url: tc.url, header: jsonCT, body: tc.body}, allOn)
		wantCode(t, err, tc.code)
	}
	_, err = doParse(t, b, req{method: "PUT", url: "/api/users/by/email/a%40b.c", header: withHeader(jsonCT, "If-Match", `W/"x"`), body: `{"full_name":"x"}`}, allOn)
	wantCode(t, err, CodeIfMatchUnsupported)
	if m, ok := b.Match("/users/1"); !ok || m.Route.Allows("PUT") {
		t.Error("PUT offered on an identity primary key")
	}
}
