package parse

import (
	"encoding/json"
	"reflect"
	"strings"
	"testing"

	"github.com/suprbdev/pdbcore/introspect"
	"github.com/suprbdev/pdbcore/postgis"

	"github.com/suprbdev/pdbr/internal/api"
	"github.com/suprbdev/pdbr/internal/plan"
)

func parseBodyReq(t *testing.T, b *api.Built, method, url, body string, hdr map[string]string) (*plan.Request, error) {
	t.Helper()
	h := map[string]string{"Content-Type": "application/json"}
	for k, v := range hdr {
		h[k] = v
	}
	return doParse(t, b, req{method: method, url: url, body: body, header: h}, allOn)
}

func TestBodyRules(t *testing.T) {
	b := buildFixture(t, true)
	cases := []struct {
		name, method, url, body string
		code                    string
		detail                  string
	}{
		{"post ok", "POST", "/api/users", `{"email":"a@b.c"}`, "", ""},
		{"post required missing", "POST", "/api/users", `{"full_name":"x"}`, CodeInvalidBody, "email"},
		{"post empty object required", "POST", "/api/users", `{}`, CodeInvalidBody, "missing required"},
		{"post empty array", "POST", "/api/users", `[]`, CodeInvalidBody, "empty array"},
		{"post scalar", "POST", "/api/users", `1`, CodeInvalidBody, "must be a JSON object"},
		{"post no body", "POST", "/api/users", ``, CodeInvalidBody, "required"},
		{"post bad json", "POST", "/api/users", `{`, CodeInvalidBody, "not valid JSON"},
		{"post trailing", "POST", "/api/users", `{"email":"a"} x`, CodeInvalidBody, "trailing"},
		{"post element not object", "POST", "/api/users", `[{"email":"a"}, 2]`, CodeInvalidBody, "element 1"},
		{"post element missing required", "POST", "/api/users", `[{"email":"a"}, {"full_name":"b"}]`, CodeInvalidBody, "element 1: missing required column(s): email"},
		{"unknown key", "POST", "/api/users", `{"email":"a","nope":1}`, CodeInvalidBody, `unknown key "nope"`},
		{"relation key not claimed", "POST", "/api/users", `{"email":"a","posts":{}}`, CodeInvalidBody, `unknown key "posts"`},
		{"generated column", "POST", "/api/users", `{"email":"a","id":1}`, CodeColumnReadOnly, `"id"`},
		{"generated in bulk", "POST", "/api/users", `[{"email":"a","id":1}]`, CodeColumnReadOnly, `"id"`},
		{"object for text", "POST", "/api/users", `{"email":{"x":1}}`, CodeInvalidBody, `"email"`},
		{"string for int", "POST", "/api/posts", `{"author_id":"x","title":"t"}`, CodeInvalidBody, `"author_id"`},
		{"unknown enum label", "POST", "/api/users", `{"email":"a","mood":"angry"}`, CodeInvalidBody, `not a value of enum`},
		{"scalar for array", "POST", "/api/users", `{"email":"a","tags":"x"}`, CodeInvalidBody, `"tags"`},
		{"array for scalar", "POST", "/api/users", `{"email":["a"]}`, CodeInvalidBody, `"email"`},
		{"composite string", "POST", "/api/users", `{"email":"a","address":"x"}`, CodeInvalidBody, `composite type address`},
		{"composite unknown attribute", "POST", "/api/users", `{"email":"a","address":{"zip":1}}`, CodeInvalidBody, `unknown attribute "zip"`},
		{"composite array element", "POST", "/api/users", `{"email":"a","prev_addresses":[{"street":"x"},"y"]}`, CodeInvalidBody, `element 1`},
		{"spatial bad", "POST", "/api/places", `{"name":"p","location":"garbage"}`, CodeInvalidBody, ""},
		{"patch ok", "PATCH", "/api/users/1", `{"full_name":"x"}`, "", ""},
		{"patch empty", "PATCH", "/api/users/1", `{}`, CodeInvalidBody, "at least one column"},
		{"patch array", "PATCH", "/api/users/1", `[{"full_name":"x"}]`, CodeInvalidBody, "PATCH body must be a JSON object"},
		{"patch key column allowed", "PATCH", "/api/users/1", `{"email":"new"}`, "", ""},
		{"patch by unique", "PATCH", "/api/users/by/email/a@b.c", `{"full_name":"x"}`, "", ""},
		{"put ok", "PUT", "/api/users?on_conflict=email", `{"email":"a"}`, "", ""},
		{"put bulk ok", "PUT", "/api/users?on_conflict=email", `[{"email":"a"},{"email":"b","mood":"ok"}]`, "", ""},
		{"put target missing", "PUT", "/api/users?on_conflict=email", `{"full_name":"x"}`, CodeInvalidBody, "conflict target"},
		{"put target missing in element", "PUT", "/api/users?on_conflict=email", `[{"email":"a"},{"full_name":"x"}]`, CodeInvalidBody, "element 1"},
		{"put default target is pk (generated)", "PUT", "/api/users", `{"email":"a"}`, CodeInvalidBody, "conflict target"},
		{"put required still applies", "PUT", "/api/users?on_conflict=email", `{"email":"a","full_name":null}`, "", ""},
		{"put required on other table", "PUT", "/api/places?on_conflict=id", `{"id":1}`, CodeColumnReadOnly, "id"},
		{"delete ignores body", "DELETE", "/api/users/1", `{"junk": true}`, "", ""},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			p, err := parseBodyReq(t, b, tc.method, tc.url, tc.body, nil)
			if tc.code == "" {
				if err != nil {
					t.Fatalf("unexpected error: %v", err)
				}
				if tc.method != "DELETE" && p.Body == nil {
					t.Fatal("no body")
				}
				return
			}
			wantCode(t, err, tc.code)
			if tc.detail != "" && !strings.Contains(err.Error(), tc.detail) {
				t.Fatalf("detail %q does not contain %q", err.Error(), tc.detail)
			}
		})
	}
}

func TestBodyShape(t *testing.T) {
	b := buildFixture(t, true)
	p, err := parseBodyReq(t, b, "POST", "/api/users", `{"full_name":"Ada","email":"a@b.c"}`, nil)
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(p.Body.Object, map[string]any{"email": "a@b.c", "full_name": "Ada"}) || p.Body.Objects != nil {
		t.Errorf("object = %#v", p.Body.Object)
	}
	if names(p.Body.Columns) != "email,full_name" {
		t.Errorf("columns = %s", names(p.Body.Columns))
	}
	p, err = parseBodyReq(t, b, "POST", "/api/users", `[{"email":"a","full_name":"A"},{"email":"b","mood":"ok"}]`, nil)
	if err != nil {
		t.Fatal(err)
	}
	if len(p.Body.Objects) != 2 || p.Body.Object != nil || names(p.Body.Columns) != "email,full_name,mood" {
		t.Errorf("bulk = %+v cols=%s", p.Body, names(p.Body.Columns))
	}
	if p.Body.Objects[1]["mood"] != "ok" {
		t.Errorf("enum label verbatim: %v", p.Body.Objects[1])
	}
	// PUT resolves the default conflict target
	p, err = parseBodyReq(t, b, "PUT", "/api/posts?on_conflict=id", `{"author_id":1,"title":"t"}`, nil)
	if err == nil || p != nil {
		t.Fatalf("id is generated: %v", err)
	}
	// keys are optional in PATCH; Prefer/If-Match ride along
	p, err = parseBodyReq(t, b, "PATCH", "/api/users/1", `{"full_name":"x"}`, map[string]string{"Prefer": "return=minimal", "If-Match": `W/"abc"`})
	if err != nil || p.Prefer.Return != "minimal" || p.IfMatch != `W/"abc"` || names(p.Body.Columns) != "full_name" {
		t.Fatalf("plan=%+v err=%v", p, err)
	}
}

func names(cols []*introspect.Column) string {
	out := make([]string, len(cols))
	for i, c := range cols {
		out[i] = c.Name
	}
	return strings.Join(out, ",")
}

func TestBodyCoercion(t *testing.T) {
	b := buildFixture(t, true)
	body := `{"email":"a@b.c","balance":"9007199254740993","mood":"happy","moods":["sad",null],"tags":["x","y"],
		"settings":{"a":[1,2]},"address":{"street":"s","city":"c","mood":"ok"},"prev_addresses":[{"street":"p"}],
		"created_at":"2026-01-01T00:00:00Z","full_name":null}`
	p, err := parseBodyReq(t, b, "POST", "/api/users", body, nil)
	if err != nil {
		t.Fatal(err)
	}
	o := p.Body.Object
	if o["balance"] != int64(9007199254740993) || o["mood"] != "happy" || o["created_at"] != "2026-01-01T00:00:00Z" {
		t.Errorf("scalars = %#v", o)
	}
	if v, ok := o["full_name"]; !ok || v != nil {
		t.Errorf("explicit null must be kept as nil: %#v", o)
	}
	if !reflect.DeepEqual(o["moods"], []any{"sad", nil}) || !reflect.DeepEqual(o["tags"], []any{"x", "y"}) {
		t.Errorf("arrays = %#v %#v", o["moods"], o["tags"])
	}
	if s, ok := o["settings"].(string); !ok || !json.Valid([]byte(s)) || s != `{"a":[1,2]}` {
		t.Errorf("jsonb = %#v", o["settings"])
	}
	if s, ok := o["address"].(string); !ok || s != `{"city":"c","mood":"ok","street":"s"}` {
		t.Errorf("composite = %#v", o["address"])
	}
	if s, ok := o["prev_addresses"].(string); !ok || s != `[{"street":"p"}]` {
		t.Errorf("composite array = %#v", o["prev_addresses"])
	}
	// numbers for int8 / numeric are accepted too; strings for numbers
	p, err = parseBodyReq(t, b, "POST", "/api/users", `{"email":"a","balance":5}`, nil)
	if err != nil || p.Body.Object["balance"] != int64(5) {
		t.Errorf("balance number: %v %v", p, err)
	}
	// spatial
	p, err = parseBodyReq(t, b, "POST", "/api/places", `{"name":"p","location":{"type":"Point","coordinates":[1,2]},"area":"SRID=4326;POLYGON((0 0,1 0,1 1,0 0))"}`, nil)
	if err != nil {
		t.Fatal(err)
	}
	if _, ok := p.Body.Object["location"].(postgis.Value); !ok {
		t.Errorf("location = %#v", p.Body.Object["location"])
	}
	if _, ok := p.Body.Object["area"].(postgis.Value); !ok {
		t.Errorf("area = %#v", p.Body.Object["area"])
	}
}

func TestBodyNestedKeys(t *testing.T) {
	b := buildFixture(t, true)
	opts := allOn
	opts.NestedKeys = func(res *api.Resource) map[string]*api.Relation {
		out := map[string]*api.Relation{}
		for key, rels := range res.Relations {
			if len(rels) == 1 {
				out[key] = rels[0]
			}
		}
		return out
	}
	hdr := map[string]string{"Content-Type": "application/json"}
	// backward relation key is kept raw
	p, err := doParse(t, b, req{method: "POST", url: "/api/users", body: `{"email":"a","posts":{"create":[{"title":"x"}]}}`, header: hdr}, opts)
	if err != nil {
		t.Fatal(err)
	}
	n := p.Body.Nested["posts"]
	if n == nil || n.Relation == nil || n.Relation.Forward || n.Raw == nil || names(p.Body.Columns) != "email" {
		t.Fatalf("nested = %+v body=%+v", n, p.Body)
	}
	// forward relation makes the FK optional...
	p, err = doParse(t, b, req{method: "POST", url: "/api/posts", body: `{"title":"t","users":{"connect":{"id":1}}}`, header: hdr}, opts)
	if err != nil || p.Body.Nested["users"] == nil || !p.Body.Nested["users"].Relation.Forward {
		t.Fatalf("forward nested: %+v %v", p.Body, err)
	}
	// ...but not both
	_, err = doParse(t, b, req{method: "POST", url: "/api/posts", body: `{"title":"t","author_id":1,"users":{"connect":{"id":1}}}`, header: hdr}, opts)
	wantCode(t, err, CodeInvalidBody)
	// bulk bodies and bulk PATCH never take nested keys
	_, err = doParse(t, b, req{method: "POST", url: "/api/users", body: `[{"email":"a","posts":{}}]`, header: hdr}, opts)
	wantCode(t, err, CodeInvalidBody)
	_, err = doParse(t, b, req{method: "PATCH", url: "/api/users?id=eq.1", body: `{"posts":{}}`, header: hdr}, opts)
	wantCode(t, err, CodeInvalidBody)
	p, err = doParse(t, b, req{method: "PATCH", url: "/api/users/1", body: `{"posts":{"delete":[{"id":1}]}}`, header: hdr}, opts)
	if err != nil || p.Body.Nested["posts"] == nil || len(p.Body.Object) != 0 {
		t.Fatalf("PATCH nested: %+v %v", p.Body, err)
	}
	_, err = doParse(t, b, req{method: "PATCH", url: "/api/users/1", body: `{"posts":{"delete":[{"id":1}]}}`, header: withHeader(hdr, "If-Match", `W/"x"`)}, opts)
	wantCode(t, err, CodeIfMatchUnsupported)
	_, err = doParse(t, b, req{method: "PATCH", url: "/api/users/1", body: `{}`, header: hdr}, opts)
	wantCode(t, err, CodeInvalidBody)
	// CoerceObject mirrors the body rules for plugins
	obj, err := CoerceObject(b, b.ByPath["users"], map[string]any{"email": "a", "balance": json.Number("7")})
	if err != nil || obj["balance"] != int64(7) {
		t.Fatalf("CoerceObject = %v %v", obj, err)
	}
	if _, err := CoerceObject(b, b.ByPath["users"], map[string]any{"id": 1}); err == nil {
		t.Fatal("CoerceObject must reject generated columns")
	}
}
