package server

import (
	"bytes"
	"reflect"
	"testing"

	"github.com/suprbdev/pdbcore/testutil"

	"github.com/suprbdev/pdbr/internal/api"
	"github.com/suprbdev/pdbr/internal/exec"
	"github.com/suprbdev/pdbr/internal/inflect"
	"github.com/suprbdev/pdbr/internal/plan"
)

func TestWriteCSV(t *testing.T) {
	var buf bytes.Buffer
	data := []byte(`[{"id":1,"email":"a@b.c","tags":["x","y"],"settings":{"a": 1},"ok":true,"note":null,"q":"say \"hi\", now"},` +
		`{"id":2,"email":"d@e.f","tags":null,"settings":null,"ok":false,"note":"line\nbreak"}]`)
	if err := writeCSV(&buf, []string{"id", "email", "tags", "settings", "ok", "note", "q", "missing"}, data, true); err != nil {
		t.Fatal(err)
	}
	want := "id,email,tags,settings,ok,note,q,missing\r\n" +
		`1,a@b.c,"[""x"",""y""]","{""a"":1}",true,,"say ""hi"", now",` + "\r\n" +
		"2,d@e.f,,,false,\"line\nbreak\",,\r\n"
	if buf.String() != want {
		t.Errorf("csv =\n%q\nwant\n%q", buf.String(), want)
	}
	buf.Reset()
	if err := writeCSV(&buf, []string{"value"}, []byte(`["ok","happy",null,3]`), true); err != nil {
		t.Fatal(err)
	}
	if got := buf.String(); got != "value\r\nok\r\nhappy\r\n\r\n3\r\n" {
		t.Errorf("scalar csv = %q", got)
	}
	buf.Reset()
	if err := writeCSV(&buf, []string{"a"}, []byte(`[]`), true); err != nil || buf.String() != "a\r\n" {
		t.Errorf("empty csv = %q %v", buf.String(), err)
	}
	if err := writeCSV(&buf, []string{"a"}, []byte(`{"a":1}`), true); err == nil {
		t.Error("object accepted as csv rows")
	}
}

func TestWriteCSVLineBreaks(t *testing.T) {
	data := []byte(`[{"v":"\r=1"},{"v":"a\rb"},{"v":"line1\nline2"},{"v":"crlf\r\nend"},{"v":" lead"},{"v":"plain"}]`)
	var buf bytes.Buffer
	if err := writeCSV(&buf, []string{"v"}, data, false); err != nil {
		t.Fatal(err)
	}
	want := "v\r\n\"\r=1\"\r\n\"a\rb\"\r\n\"line1\nline2\"\r\n\"crlf\r\nend\"\r\n\" lead\"\r\nplain\r\n"
	if buf.String() != want {
		t.Errorf("csv =\n%q\nwant\n%q", buf.String(), want)
	}
	buf.Reset()
	if err := writeCSV(&buf, []string{"v"}, []byte(`[{"v":"\r=1"}]`), true); err != nil {
		t.Fatal(err)
	}
	if got := buf.String(); got != "v\r\n\"'\r=1\"\r\n" {
		t.Errorf("escaped csv = %q", got)
	}
}

func TestWriteCSVFormulas(t *testing.T) {
	data := []byte(`[{"=h":"=HYPERLINK(\"https://x.invalid\")","n":-5,"s":"-5"},` +
		`{"=h":"+1","n":1.5,"s":"@SUM(A1)"},{"=h":"\tx","n":0,"s":"\rx"},{"=h":"a=b","n":null,"s":""}]`)
	var buf bytes.Buffer
	if err := writeCSV(&buf, []string{"=h", "n", "s"}, data, true); err != nil {
		t.Fatal(err)
	}
	want := "'=h,n,s\r\n" +
		`"'=HYPERLINK(""https://x.invalid"")",-5,'-5` + "\r\n" +
		"'+1,1.5,'@SUM(A1)\r\n" +
		"'\tx,0,\"'\rx\"\r\n" +
		"a=b,,\r\n"
	if buf.String() != want {
		t.Errorf("escaped csv =\n%q\nwant\n%q", buf.String(), want)
	}
	buf.Reset()
	if err := writeCSV(&buf, []string{"value"}, []byte(`["=1+1",-1,"-1"]`), true); err != nil {
		t.Fatal(err)
	}
	if got := buf.String(); got != "value\r\n'=1+1\r\n-1\r\n'-1\r\n" {
		t.Errorf("escaped scalar csv = %q", got)
	}
	buf.Reset()
	if err := writeCSV(&buf, []string{"=h", "s"}, []byte(`[{"=h":"=HYPERLINK(\"https://x.invalid\")","s":-5}]`), false); err != nil {
		t.Fatal(err)
	}
	if got, want := buf.String(), "=h,s\r\n"+`"=HYPERLINK(""https://x.invalid"")",-5`+"\r\n"; got != want {
		t.Errorf("raw csv =\n%q\nwant\n%q", got, want)
	}
}

func TestCSVColumns(t *testing.T) {
	built, err := api.New(testutil.FixtureCatalog(), inflect.Default, api.Options{Functions: true, Schemas: []string{"public"}, MaxPageSize: 100}).Build()
	if err != nil {
		t.Fatal(err)
	}
	users := built.ByPath["users"]
	all := []string{"id", "email", "full_name", "mood", "settings", "tags", "balance", "created_at", "address", "prev_addresses", "moods"}
	if got := csvColumns(&exec.Operation{Resource: users, Plan: &plan.Request{}}); !reflect.DeepEqual(got, all) {
		t.Errorf("select=* columns = %v", got)
	}
	email := users.Field("email").Column
	node := &plan.Node{Resource: users, Star: true, Fields: []plan.FieldSel{
		{Column: email},
		{Alias: "mail", Column: email},
		{Computed: users.Computed["post_count"]},
	}}
	want := append(append([]string(nil), all...), "mail", "post_count")
	if got := csvColumns(&exec.Operation{Resource: users, Plan: &plan.Request{Select: node}}); !reflect.DeepEqual(got, want) {
		t.Errorf("star+fields columns = %v", got)
	}
	node = &plan.Node{Resource: users, Fields: []plan.FieldSel{{Column: users.Field("mood").Column}, {Column: email}}}
	if got := csvColumns(&exec.Operation{Resource: users, Plan: &plan.Request{Select: node}}); !reflect.DeepEqual(got, []string{"mood", "email"}) {
		t.Errorf("select order columns = %v", got)
	}
	node = &plan.Node{Resource: users, Fields: []plan.FieldSel{
		{Column: users.Field("mood").Column},
		{Agg: &plan.Aggregate{Func: plan.AggCount}},
		{Alias: "total", Column: users.Field("balance").Column, Agg: &plan.Aggregate{Func: plan.AggSum}},
		{Column: email, Agg: &plan.Aggregate{Func: plan.AggMax}},
	}}
	if got := csvColumns(&exec.Operation{Resource: users, Plan: &plan.Request{Select: node}}); !reflect.DeepEqual(got, []string{"mood", "count", "total", "max"}) {
		t.Errorf("aggregate columns = %v", got)
	}
	if got := csvColumns(&exec.Operation{RPC: built.RPCByPath["mood_moods"], Plan: &plan.Request{}}); !reflect.DeepEqual(got, []string{"value"}) {
		t.Errorf("scalar set columns = %v", got)
	}
	if got := csvColumns(&exec.Operation{RPC: built.RPCByPath["search_posts"], Plan: &plan.Request{}}); !reflect.DeepEqual(got, []string{"id", "author_id", "title", "body", "published"}) {
		t.Errorf("setof table columns = %v", got)
	}
}
