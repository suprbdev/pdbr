package parse

import (
	"reflect"
	"testing"
)

func TestPreferTx(t *testing.T) {
	b := buildFixture(t, true)
	cases := []struct {
		name, txEnd, method, url, prefer string
		rollback                         bool
		applied                          []string
		strictErr                        bool
	}{
		{"default commit", "", "POST", "/api/users", "", false, nil, false},
		{"commit ignores tx", "commit", "POST", "/api/users", "tx=rollback", false, nil, true},
		{"override honours rollback", "commit-allow-override", "POST", "/api/users", "tx=rollback", true, []string{"tx=rollback"}, false},
		{"override honours commit", "rollback-allow-override", "POST", "/api/users", "tx=commit", false, []string{"tx=commit"}, false},
		{"rollback default", "rollback", "POST", "/api/users", "", true, nil, false},
		{"rollback ignores commit", "rollback", "POST", "/api/users", "tx=commit", true, nil, true},
		{"rollback-allow-override default", "rollback-allow-override", "DELETE", "/api/users?id=eq.1", "", true, nil, false},
		{"read unaffected", "rollback", "GET", "/api/users", "", false, nil, false},
		{"read ignores tx", "commit-allow-override", "GET", "/api/users", "tx=rollback", false, nil, false},
		{"invalid value", "commit-allow-override", "POST", "/api/users", "tx=maybe", false, nil, true},
	}
	for _, tc := range cases {
		opts := allOn
		opts.TxEnd = tc.txEnd
		hdr := map[string]string{"Content-Type": "application/json"}
		if tc.prefer != "" {
			hdr["Prefer"] = tc.prefer
		}
		body := ""
		if tc.method == "POST" {
			body = `{"email":"a@b.c"}`
		}
		p, err := doParse(t, b, req{method: tc.method, url: tc.url, header: hdr, body: body}, opts)
		if err != nil {
			t.Fatalf("%s: %v", tc.name, err)
		}
		if p.Prefer.Rollback != tc.rollback || !reflect.DeepEqual(p.Prefer.Applied, tc.applied) {
			t.Errorf("%s: rollback=%v applied=%v, want %v %v", tc.name, p.Prefer.Rollback, p.Prefer.Applied, tc.rollback, tc.applied)
		}
		if tc.strictErr {
			hdr["Prefer"] = "handling=strict, " + tc.prefer
			_, err := doParse(t, b, req{method: tc.method, url: tc.url, header: hdr, body: body}, opts)
			if tc.method == "GET" {
				continue
			}
			wantCode(t, err, CodeInvalidPreference)
		}
	}
}

func TestPreferTimezone(t *testing.T) {
	b := buildFixture(t, true)
	p, err := doParse(t, b, req{url: "/api/users", header: map[string]string{"Prefer": `timezone="Asia/Tokyo"`}}, allOn)
	if err != nil || p.Prefer.Timezone != "Asia/Tokyo" {
		t.Fatalf("timezone = %+v %v", p.Prefer, err)
	}
	_, err = doParse(t, b, req{url: "/api/users", header: map[string]string{"Prefer": "handling=strict, timezone="}}, allOn)
	wantCode(t, err, CodeInvalidPreference)
}

func TestColumnsParam(t *testing.T) {
	b := buildFixture(t, true)
	post := func(url, body string) (map[string]any, error) {
		p, err := doParse(t, b, req{method: "POST", url: url, header: jsonCT, body: body}, allOn)
		if err != nil {
			return nil, err
		}
		return p.Body.Object, nil
	}
	obj, err := post("/api/users?columns=email", `{"email":"a@b.c","full_name":"x"}`)
	if err != nil || len(obj) != 1 || obj["email"] != "a@b.c" {
		t.Fatalf("columns = %v %v", obj, err)
	}
	_, err = post("/api/users?columns=email,nope", `{"email":"a@b.c"}`)
	wantCode(t, err, CodeInvalidBody)
	_, err = post("/api/users?columns=", `{"email":"a@b.c"}`)
	wantCode(t, err, CodeInvalidBody)
	_, err = post("/api/users?columns=full_name", `{"email":"a@b.c","full_name":"x"}`)
	wantCode(t, err, CodeInvalidBody)
}
