package exec

import (
	"bytes"
	"encoding/json"
	"net/http"
	"reflect"
	"slices"
	"testing"

	"github.com/suprbdev/pdbr/internal/compile"
	"github.com/suprbdev/pdbr/internal/plan"
)

func TestSplitEnvelope(t *testing.T) {
	for _, tc := range []struct {
		name, key, raw, meta, value string
		ok                          bool
	}{
		{"compact", "rows", `{"has_next":true,"n":2,"rows":[{"id":1},{"id":2}]}`, `{"has_next":true,"n":2}`, `[{"id":1},{"id":2}]`, true},
		{"postgres spacing", "rows", `{"has_next" : false, "first" : ["rows"], "rows" : [{"rows" : 1}]}`, `{"has_next" : false, "first" : ["rows"]}`, `[{"rows" : 1}]`, true},
		{"whitespace", "row", "{ \"etag\" : \"x\" ,\n \"row\" :\t{\"id\": 1} \n}", `{ "etag" : "x" }`, `{"id": 1}`, true},
		{"scalar payload", "row", `{"found":true,"row":null}`, `{"found":true}`, `null`, true},
		{"key in a string value", "row", `{"etag":"\"row\":","row":{}}`, `{"etag":"\"row\":"}`, `{}`, true},
		{"payload first", "rows", `{"rows":[1],"has_next":true}`, "", "", false},
		{"payload only", "rows", `{"rows":[1]}`, "", "", false},
		{"no payload", "rows", `{"n":1,"key":[7]}`, "", "", false},
		{"not an object", "rows", `[1,2]`, "", "", false},
		{"empty", "rows", ``, "", "", false},
		{"truncated", "rows", `{"n":1,"rows":`, "", "", false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			meta, value, ok := splitEnvelope([]byte(tc.raw), tc.key)
			if ok != tc.ok || string(meta) != tc.meta || string(value) != tc.value {
				t.Fatalf("splitEnvelope(%s) = %q, %q, %v; want %q, %q, %v", tc.raw, meta, value, ok, tc.meta, tc.value, tc.ok)
			}
		})
	}
}

// TestUnwrapPagePassthrough: the page body is a subslice of the scanned
// data (never copied or re-encoded), and both wrapper layouts yield the same
// body and headers.
func TestUnwrapPagePassthrough(t *testing.T) {
	b := built(t)
	op := listOp(b, "/posts")
	op.Plan.Page = plan.Page{Limit: 2, Offset: 4}
	op.RawQuery = "order=title.asc&limit=2&offset=4&count=true"
	stmt := &compile.Statement{Shape: compile.ShapePage, Cursors: true, Total: true}
	last := []byte(`{"has_next" : true, "has_prev" : true, "first" : [7], "last" : [2], "total" : 9, "n" : 2, "rows" : [{"id" : 7}, {"id" : 2}]}`)
	first := []byte(`{"rows" : [{"id" : 7}, {"id" : 2}], "has_next" : true, "has_prev" : true, "first" : [7], "last" : [2], "total" : 9}`)
	got, err := Unwrap(op, stmt, last, true, "/api")
	if err != nil {
		t.Fatal(err)
	}
	want, err := Unwrap(op, stmt, first, true, "/api")
	if err != nil {
		t.Fatal(err)
	}
	if string(got.Data) != `[{"id" : 7}, {"id" : 2}]` || string(want.Data) != string(got.Data) {
		t.Fatalf("data = %s / %s", got.Data, want.Data)
	}
	if !reflect.DeepEqual(got.Headers, want.Headers) || got.Headers.Get("Content-Range") != "4-5/9" || got.Headers.Get("X-Next-Cursor") == "" {
		t.Fatalf("headers differ:\n%v\n%v", got.Headers, want.Headers)
	}
	last[bytes.Index(last, []byte(`{"id" : 7}`))+2] = 'X'
	if string(got.Data) != `[{"Xd" : 7}, {"id" : 2}]` {
		t.Errorf("rows were copied out of the scanned data: %s", got.Data)
	}
}

func TestUnwrapMalformedEnvelope(t *testing.T) {
	b := built(t)
	op := listOp(b, "/posts")
	for shape, raw := range map[compile.Shape]string{
		compile.ShapePage:    `{"has_next":"yes","rows":[]}`,
		compile.ShapeRowEtag: `{"etag":1,"row":{}}`,
		compile.ShapeMutWrap: `{"found":"x","row":{}}`,
	} {
		_, err := Unwrap(op, &compile.Statement{Shape: shape}, []byte(raw), true, "/api")
		if e := asErr(err); e == nil || e.Status != http.StatusInternalServerError {
			t.Errorf("%s: %v", shape, err)
		}
	}
}

// FuzzSplitEnvelope: whenever the split succeeds on a valid object, meta
// plus the payload member decode to the same value as the whole wrapper
// (or the payload is not valid JSON on its own, which only a wrapper with
// members after the payload produces); a valid object whose last member is
// the payload, after another member, always splits.
func FuzzSplitEnvelope(f *testing.F) {
	for _, s := range []string{
		`{"has_next" : true, "n" : 2, "rows" : [{"id" : 1}, {"id" : 2}]}`,
		`{"etag":"\"row\":","row":{"row":"x"}}`,
		`{"rows":[1],"n":1}`,
		`{"a":{"rows":1},"rows":[]}`,
		`{"a":"\\","rows":null}`,
	} {
		f.Add(s)
	}
	f.Fuzz(func(t *testing.T, s string) {
		raw := []byte(s)
		meta, value, ok := splitEnvelope(raw, "rows")
		var whole map[string]any
		if json.Unmarshal(raw, &whole) != nil {
			return
		}
		if ok {
			var m map[string]any
			var v any
			if err := json.Unmarshal(meta, &m); err != nil {
				t.Fatalf("meta %q of valid %q: %v", meta, raw, err)
			}
			if json.Unmarshal(value, &v) != nil {
				return
			}
			m["rows"] = v
			if !reflect.DeepEqual(m, whole) {
				t.Fatalf("split of %q = %q + %q", raw, meta, value)
			}
			return
		}
		dec := json.NewDecoder(bytes.NewReader(raw))
		var keys []string
		if _, err := dec.Token(); err != nil {
			return
		}
		for dec.More() {
			k, _ := dec.Token()
			keys = append(keys, k.(string))
			var skip json.RawMessage
			_ = dec.Decode(&skip)
		}
		if len(keys) > 1 && keys[len(keys)-1] == "rows" && !slices.Contains(keys[:len(keys)-1], "rows") {
			t.Fatalf("%q not split", raw)
		}
	})
}
