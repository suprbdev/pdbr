package api

import (
	"bytes"
	"encoding/json"
	"reflect"
	"strings"
	"testing"

	"github.com/suprbdev/pdbcore/introspect"
	"github.com/suprbdev/pdbcore/testutil"
)

// domainCatalog is the fixture catalog plus domains (email over text with
// a regex CHECK, percent over positive_int with bounds, int_list over
// int[], one in another schema, one over a range) and columns and a
// function using them.
func domainCatalog() *introspect.Catalog {
	cat := testutil.FixtureCatalog()
	cat.Domains = []*introspect.Domain{
		{Schema: "public", Name: "email", BaseType: "text", BaseTypeSchema: "pg_catalog",
			Checks: []string{"CHECK ((VALUE ~ '^[^@]+@[^@]+$'::text))"}, Comment: "@name ignored\nAn email address."},
		{Schema: "public", Name: "positive_int", BaseType: "int4", BaseTypeSchema: "pg_catalog", NotNull: true,
			Checks: []string{"CHECK ((VALUE > 0))"}},
		{Schema: "public", Name: "percent", BaseType: "int4", BaseTypeSchema: "pg_catalog", Parent: "public.positive_int",
			Checks: []string{"CHECK ((((VALUE)::integer >= 1) AND ((VALUE)::integer <= 100)))", "CHECK (((VALUE)::integer <= 90)) NOT VALID"}},
		{Schema: "public", Name: "int_list", BaseType: "_int4", BaseTypeSchema: "pg_catalog", IsArrayBase: true},
		{Schema: "audit", Name: "code", BaseType: "varchar", BaseTypeSchema: "pg_catalog"},
		{Schema: "public", Name: "span", BaseType: "int4range", BaseTypeSchema: "pg_catalog"},
	}
	users := cat.Table("public", "users")
	users.Columns = append(users.Columns,
		&introspect.Column{Name: "work_email", Position: 12, PGType: "text", TypeSchema: "public", BaseTypeSchema: "pg_catalog", Domain: "public.email"},
		&introspect.Column{Name: "aliases", Position: 13, PGType: "_text", TypeSchema: "public", BaseTypeSchema: "pg_catalog", IsArray: true, Domain: "public.email"},
		&introspect.Column{Name: "score", Position: 14, PGType: "int4", TypeSchema: "public", BaseTypeSchema: "pg_catalog", Domain: "public.percent"},
		&introspect.Column{Name: "lucky", Position: 15, PGType: "_int4", TypeSchema: "public", BaseTypeSchema: "pg_catalog", IsArray: true, Domain: "public.int_list"},
		&introspect.Column{Name: "code", Position: 16, PGType: "varchar", TypeSchema: "audit", BaseTypeSchema: "pg_catalog", Domain: "audit.code"},
		&introspect.Column{Name: "span", Position: 17, PGType: "int4range", TypeSchema: "public", BaseTypeSchema: "pg_catalog", Domain: "public.span"},
	)
	cat.Functions = append(cat.Functions, &introspect.Function{
		Schema: "public", Name: "rate",
		Args: []introspect.FuncArg{
			{Name: "who", PGType: "email", TypeSchema: "public", Mode: introspect.ArgModeIn, Domain: "public.email"},
			{Name: "cc", PGType: "_email", TypeSchema: "public", Mode: introspect.ArgModeIn, Domain: "public.email"},
		},
		ReturnType: "percent", ReturnTypeSchema: "public", ReturnDomain: "public.percent",
		Volatility: introspect.VolatilityStable,
	})
	return cat
}

func jsonOf(t *testing.T, v any) string {
	t.Helper()
	var buf bytes.Buffer
	enc := json.NewEncoder(&buf)
	enc.SetEscapeHTML(false)
	if err := enc.Encode(v); err != nil {
		t.Fatal(err)
	}
	return strings.TrimSuffix(buf.String(), "\n")
}

func TestOpenAPIDomains(t *testing.T) {
	opts := defaultOptions()
	opts.Domains = true
	doc := build(t, domainCatalog(), opts).OpenAPI
	schemas := doc.Components.Schemas
	for name, want := range map[string]string{
		"email": `{"type":"string","pattern":"^[^@]+@[^@]+$","description":"An email address.\n\nDomain public.email over text.\n\nConstraints: ` + "`CHECK ((VALUE ~ '^[^@]+@[^@]+$'::text))`" + `."}`,
		"percent": `{"type":"integer","format":"int32","description":"Domain public.percent over int4.\n\nConstraints: ` +
			"`NOT NULL`, `CHECK ((((VALUE)::integer >= 1) AND ((VALUE)::integer <= 100)))`, `CHECK (((VALUE)::integer <= 90)) NOT VALID`, `CHECK ((VALUE > 0))`" +
			`.","minimum":1,"maximum":100,"exclusiveMinimum":0}`,
		"int_list":   `{"type":"array","description":"Domain public.int_list over _int4.","items":{"type":"integer","format":"int32"}}`,
		"audit.code": `{"type":"string","description":"Domain audit.code over varchar."}`,
	} {
		if got := jsonOf(t, schemas[name]); got != want {
			t.Errorf("components.schemas[%s] =\n %s\nwant\n %s", name, got, want)
		}
	}
	if schemas["span"] != nil {
		t.Error("a domain over a range must keep the range's own schemas")
	}
	users := schemas["users"].Properties
	for prop, want := range map[string]string{
		"work_email": `{"anyOf":[{"$ref":"#/components/schemas/email"},{"type":"null"}]}`,
		"aliases":    `{"type":["array","null"],"items":{"$ref":"#/components/schemas/email"}}`,
		"score":      `{"anyOf":[{"$ref":"#/components/schemas/percent"},{"type":"null"}]}`,
		"lucky":      `{"anyOf":[{"$ref":"#/components/schemas/int_list"},{"type":"null"}]}`,
		"code":       `{"anyOf":[{"$ref":"#/components/schemas/audit.code"},{"type":"null"}]}`,
		"span":       `{"type":["string","null"]}`,
	} {
		if got := jsonOf(t, users[prop]); got != want {
			t.Errorf("users.%s = %s, want %s", prop, got, want)
		}
	}
	if got := jsonOf(t, schemas["users.insert"].Properties["work_email"]); got != `{"anyOf":[{"$ref":"#/components/schemas/email"},{"type":"null"}]}` {
		t.Errorf("users.insert.work_email = %s", got)
	}
	post := doc.Paths["/rpc/rate"].Post
	body := post.RequestBody.Content["application/json"].Schema.Properties
	if got := jsonOf(t, body["who"]); got != `{"anyOf":[{"$ref":"#/components/schemas/email"},{"type":"null"}]}` {
		t.Errorf("rate body who = %s", got)
	}
	if got := jsonOf(t, body["cc"]); got != `{"type":["array","null"],"items":{"$ref":"#/components/schemas/email"}}` {
		t.Errorf("rate body cc = %s", got)
	}
	if p := paramByName(doc.Paths["/rpc/rate"].Get.Parameters, "who"); p == nil || p.Schema.Ref != "#/components/schemas/email" {
		t.Errorf("rate GET who = %+v", p)
	}
	ok := post.Responses["200"].Content["application/json"].Schema
	if got := jsonOf(t, ok); got != `{"anyOf":[{"$ref":"#/components/schemas/percent"},{"type":"null"}]}` {
		t.Errorf("rate result = %s", got)
	}
}

func TestOpenAPIDomainsOff(t *testing.T) {
	doc := build(t, domainCatalog(), defaultOptions()).OpenAPI
	if doc.Components.Schemas["email"] != nil {
		t.Fatal("domain component emitted with Domains off")
	}
	users := doc.Components.Schemas["users"].Properties
	if got := jsonOf(t, users["score"]); got != `{"type":["integer","null"],"format":"int32"}` {
		t.Errorf("users.score = %s", got)
	}
	body := doc.Paths["/rpc/rate"].Post.RequestBody.Content["application/json"].Schema.Properties
	if got := jsonOf(t, body["cc"]); got != `{"type":["array","null"],"items":{"type":"string"}}` {
		t.Errorf("rate body cc = %s", got)
	}
	if got := jsonOf(t, doc.Paths["/rpc/rate"].Post.Responses["200"].Content["application/json"].Schema); got != `{"type":["integer","null"],"format":"int32"}` {
		t.Errorf("rate result = %s", got)
	}
}

func TestApplyChecks(t *testing.T) {
	for _, tc := range []struct {
		base   string
		checks []string
		want   Schema
	}{
		{"int4", []string{"CHECK ((VALUE >= 0))", "CHECK ((VALUE < 10))"}, Schema{Minimum: intPtr(0), ExclusiveMaximum: intPtr(10)}},
		{"int8", []string{"CHECK ((VALUE >= 0))"}, Schema{}},
		{"float8", []string{"CHECK ((VALUE >= (0)::double precision))", "CHECK ((VALUE >= 0.5))"}, Schema{Minimum: intPtr(0)}},
		{"int4", []string{"CHECK ((VALUE >= '-5'::integer))", "CHECK ((VALUE >= 3))", "CHECK ((VALUE <= 9))", "CHECK ((VALUE <= 7))"}, Schema{Minimum: intPtr(3), Maximum: intPtr(7)}},
		{"int4", []string{"CHECK (((VALUE > 0) OR (VALUE < -10)))"}, Schema{}},
		{"text", []string{"CHECK ((VALUE ~ 'it''s'::text))"}, Schema{Pattern: "it's"}},
		{"text", []string{"CHECK ((VALUE ~ 'a'::text))", "CHECK ((VALUE ~ 'b'::text))"}, Schema{}},
		{"text", []string{"CHECK ((VALUE ~* 'a'::text))", "CHECK ((length(VALUE) < 5))"}, Schema{}},
		{"varchar", []string{"CHECK (((VALUE)::text ~ '^[A-Z]{3}$'::text))"}, Schema{Pattern: "^[A-Z]{3}$"}},
		{"text", []string{"CHECK ((VALUE ~ '(a AND b)'::text))"}, Schema{Pattern: "(a AND b)"}},
	} {
		var got Schema
		applyChecks(&got, tc.base, tc.checks)
		if !reflect.DeepEqual(got, tc.want) {
			t.Errorf("applyChecks(%s, %v) = %s, want %s", tc.base, tc.checks, jsonOf(t, got), jsonOf(t, tc.want))
		}
	}
}
