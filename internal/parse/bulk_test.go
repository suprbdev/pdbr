package parse

import (
	"reflect"
	"testing"

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
		{req{method: "DELETE", url: "/api/users?id=eq.1&limit=1"}, CodeInvalidFilter},
		{req{method: "DELETE", url: "/api/users?id=eq.1&order=id"}, CodeInvalidFilter},
		{req{method: "DELETE", url: "/api/users?id=eq.1&on_conflict=email"}, CodeInvalidFilter},
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
	for _, bad := range []string{"tx=commit", "return=nope", "max-affected=x", "max-affected=-2", "handling=maybe", "timezone=UTC"} {
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
