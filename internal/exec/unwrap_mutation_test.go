package exec

import (
	"errors"
	"testing"

	"github.com/suprbdev/pdbr/internal/compile"
	"github.com/suprbdev/pdbr/internal/plan"
)

func TestUnwrapMutation(t *testing.T) {
	b := built(t)
	users := b.ByPath["users"]
	op := func(method, prefer string) *Operation {
		return &Operation{Method: method, Path: "/users", Resource: users, Plan: &plan.Request{Method: plan.Method(method), Prefer: plan.Prefer{Return: prefer}}}
	}
	stmt := func(sh compile.Shape) *compile.Statement { return &compile.Statement{Shape: sh, Mutation: true} }
	type want struct {
		status int
		body   string
		loc    string
		code   string
	}
	cases := []struct {
		name  string
		op    *Operation
		shape compile.Shape
		raw   string
		found bool
		want  want
	}{
		{"patch row", op("PATCH", ""), compile.ShapeMutRow, `{"id":1}`, true, want{200, `{"id":1}`, "", ""}},
		{"patch miss", op("PATCH", ""), compile.ShapeMutRow, ``, false, want{404, "", "", CodeNotFound}},
		{"delete row", op("DELETE", ""), compile.ShapeMutRow, `{"id":1}`, true, want{200, `{"id":1}`, "", ""}},
		{"bulk post", op("POST", ""), compile.ShapeMutList, `[{"id":1}]`, true, want{201, `[{"id":1}]`, "", ""}},
		{"bulk put", op("PUT", ""), compile.ShapeMutList, `[{"id":1}]`, true, want{200, `[{"id":1}]`, "", ""}},
		{"bulk empty", op("PUT", ""), compile.ShapeMutList, ``, false, want{200, `[]`, "", ""}},
		{"post single", op("POST", ""), compile.ShapeMutWrap, `{"row":{"id":42},"key":[42]}`, true, want{201, `{"id":42}`, "/api/users/42", ""}},
		{"post text key", op("POST", ""), compile.ShapeMutWrap, `{"row":{"id":"a b"},"key":["a b"]}`, true, want{201, `{"id":"a b"}`, "/api/users/a%20b", ""}},
		{"post no key", op("POST", ""), compile.ShapeMutWrap, `{"row":{"x":1}}`, true, want{201, `{"x":1}`, "", ""}},
		{"post minimal", op("POST", "minimal"), compile.ShapeMutWrap, `{"n":1,"key":[7]}`, true, want{201, "", "/api/users/7", ""}},
		{"put inserted", op("PUT", ""), compile.ShapeMutWrap, `{"row":{"id":1},"inserted":true,"key":[1]}`, true, want{201, `{"id":1}`, "/api/users/1", ""}},
		{"put updated", op("PUT", ""), compile.ShapeMutWrap, `{"row":{"id":1},"inserted":false,"key":[1]}`, true, want{200, `{"id":1}`, "", ""}},
		{"put minimal inserted", op("PUT", "minimal"), compile.ShapeMutWrap, `{"n":1,"key":[1],"inserted":true}`, true, want{201, "", "/api/users/1", ""}},
		{"put minimal updated", op("PUT", "minimal"), compile.ShapeMutWrap, `{"n":1,"key":[1],"inserted":false}`, true, want{204, "", "", ""}},
		{"patch minimal", op("PATCH", "minimal"), compile.ShapeMutWrap, `{"n":1}`, true, want{204, "", "", ""}},
		{"patch minimal miss", op("PATCH", "minimal"), compile.ShapeMutWrap, `{"n":0}`, true, want{404, "", "", CodeNotFound}},
		{"ifmatch ok", op("PATCH", ""), compile.ShapeMutWrap, `{"found":true,"row":{"id":1}}`, true, want{200, `{"id":1}`, "", ""}},
		{"ifmatch stale", op("PATCH", ""), compile.ShapeMutWrap, `{"found":true,"row":null}`, true, want{412, "", "", CodeEtagMismatch}},
		{"ifmatch missing", op("DELETE", ""), compile.ShapeMutWrap, `{"found":false,"row":null}`, true, want{404, "", "", CodeNotFound}},
		{"ifmatch minimal", op("DELETE", "minimal"), compile.ShapeMutWrap, `{"found":true,"row":true}`, true, want{204, "", "", ""}},
		{"wrap no rows", op("PATCH", ""), compile.ShapeMutWrap, ``, false, want{404, "", "", CodeNotFound}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			var raw []byte
			if tc.raw != "" {
				raw = []byte(tc.raw)
			}
			res, err := Unwrap(tc.op, stmt(tc.shape), raw, tc.found, "/api")
			if tc.want.code != "" {
				var xe *Error
				if !errors.As(err, &xe) || xe.Status != tc.want.status || xe.Code != tc.want.code {
					t.Fatalf("got %v %v, want %d %s", res, err, tc.want.status, tc.want.code)
				}
				return
			}
			if err != nil {
				t.Fatal(err)
			}
			if res.Status != tc.want.status || string(res.Data) != tc.want.body || res.Headers.Get("Location") != tc.want.loc {
				t.Fatalf("got %d %q loc=%q, want %d %q loc=%q", res.Status, res.Data, res.Headers.Get("Location"), tc.want.status, tc.want.body, tc.want.loc)
			}
		})
	}
	// schema-qualified Location for non-bare resources
	res, err := Unwrap(&Operation{Method: "POST", Resource: users, Plan: &plan.Request{Method: plan.POST}}, stmt(compile.ShapeMutWrap), []byte(`{"row":{},"key":[3]}`), true, "")
	if err != nil || res.Headers.Get("Location") != "/users/3" {
		t.Fatalf("root base path: %v %v", res, err)
	}
}
