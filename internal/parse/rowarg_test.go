package parse

import (
	"encoding/json"
	"net/url"
	"testing"

	"github.com/suprbdev/pdbcore/testutil"

	"github.com/suprbdev/pdbr/internal/api"
	"github.com/suprbdev/pdbr/internal/inflect"
)

func TestRowArgument(t *testing.T) {
	b := buildFixture(t, false)
	post := func(body string) (map[string]any, error) {
		r, err := parseBodyReq(t, b, "POST", "/api/rpc/user_greeting", body, nil)
		if err != nil {
			return nil, err
		}
		return r.Body.Object, nil
	}
	args, err := post(`{"greeting":"Hi","u":{"id":1,"mood":"happy","settings":{"a":[1]},"address":{"city":"Paris"},"balance":"12"}}`)
	if err != nil {
		t.Fatal(err)
	}
	doc, _ := json.Marshal(args["u"])
	if string(doc) != `{"address":{"city":"Paris"},"balance":12,"id":1,"mood":"happy","settings":{"a":[1]}}` {
		t.Fatalf("row document = %s", doc)
	}
	for body, code := range map[string]string{
		`{"greeting":"Hi","u":{"nope":1}}`:       CodeInvalidBody,
		`{"greeting":"Hi","u":[{"id":1}]}`:       CodeInvalidBody,
		`{"greeting":"Hi","u":{"id":"x"}}`:       CodeInvalidBody,
		`{"greeting":"Hi","u":{"mood":"angry"}}`: CodeInvalidBody,
	} {
		_, err := post(body)
		wantCode(t, err, code)
	}

	r, err := doParse(t, b, req{method: "GET", url: "/api/rpc/user_greeting?greeting=Hi&u=" + url.QueryEscape(`{"email":"a@b"}`)}, allOn)
	if err != nil {
		t.Fatal(err)
	}
	if doc, _ := json.Marshal(r.Body.Object["u"]); string(doc) != `{"email":"a@b"}` {
		t.Fatalf("GET row document = %s", doc)
	}
	_, err = doParse(t, b, req{method: "GET", url: "/api/rpc/user_greeting?greeting=Hi&u=nope"}, allOn)
	wantCode(t, err, CodeInvalidFilter)

	r, err = parseBodyReq(t, b, "POST", "/api/rpc/total_title_length", `{"ps":[{"title":"a"},null]}`, nil)
	if err != nil {
		t.Fatal(err)
	}
	if doc, _ := json.Marshal(r.Body.Object["ps"]); string(doc) != `[{"title":"a"},null]` {
		t.Fatalf("array document = %s", doc)
	}
	_, err = parseBodyReq(t, b, "POST", "/api/rpc/total_title_length", `{"ps":{"title":"a"}}`, nil)
	wantCode(t, err, CodeInvalidBody)
}

func TestRowArgumentHiddenColumn(t *testing.T) {
	bl := api.New(testutil.FixtureCatalog(), inflect.Default, api.Options{Functions: true, BasePath: "/api", Schemas: []string{"public"}, MaxPageSize: 100, Logger: quiet()})
	users := bl.Resource("public", "users")
	bl.RemoveField(users, "settings", false)
	b, err := bl.Build()
	if err != nil {
		t.Fatal(err)
	}
	_, err = parseBodyReq(t, b, "POST", "/api/rpc/user_greeting", `{"greeting":"Hi","u":{"settings":{}}}`, nil)
	wantCode(t, err, CodeInvalidBody)
	if _, err := parseBodyReq(t, b, "POST", "/api/rpc/user_greeting", `{"greeting":"Hi","u":{"id":1}}`, nil); err != nil {
		t.Fatal(err)
	}
}
