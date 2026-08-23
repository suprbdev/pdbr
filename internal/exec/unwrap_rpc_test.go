package exec

import (
	"context"
	"encoding/json"
	"fmt"
	"testing"
	"time"

	"github.com/golang-jwt/jwt/v5"

	"github.com/suprbdev/pdbcore/jwtmint"

	"github.com/suprbdev/pdbr/internal/compile"
	"github.com/suprbdev/pdbr/internal/plan"
)

func TestUnwrapRPCShapes(t *testing.T) {
	b := built(t)
	op := &Operation{Method: "POST", Path: "/rpc/best_mood", RPC: b.RPCByPath["best_mood"]}
	res, err := Unwrap(op, &compile.Statement{Shape: compile.ShapeVoid}, []byte("null"), true, "/api")
	if err != nil || res.Status != 204 || res.Data != nil {
		t.Fatalf("void: %v %+v", err, res)
	}
	res, err = Unwrap(op, &compile.Statement{Shape: compile.ShapeScalar}, []byte(`"happy"`), true, "/api")
	if err != nil || res.Status != 200 || string(res.Data) != `"happy"` {
		t.Fatalf("scalar: %v %+v", err, res)
	}
	res, err = Unwrap(op, &compile.Statement{Shape: compile.ShapeScalar}, nil, false, "/api")
	if err != nil || res.Status != 200 || string(res.Data) != "null" {
		t.Fatalf("scalar missing row: %v %+v", err, res)
	}
	// SETOF <table> pages carry offset links only, never cursors.
	setof := &Operation{Method: "GET", Path: "/rpc/search_posts", RawQuery: "term=x&limit=2", RPC: b.RPCByPath["search_posts"],
		Plan: &plan.Request{Method: plan.GET, RPC: b.RPCByPath["search_posts"], Page: plan.Page{Limit: 2}}}
	res, err = Unwrap(setof, &compile.Statement{Shape: compile.ShapePage, Total: true},
		[]byte(`{"rows":[{"id":1},{"id":2}],"has_next":true,"has_prev":false,"total":5}`), true, "/api")
	if err != nil || string(res.Data) != `[{"id":1},{"id":2}]` || res.Headers.Get("X-Total-Count") != "5" {
		t.Fatalf("setof page: %v %+v", err, res)
	}
	if got := res.Headers.Get("Link"); got != `</api/rpc/search_posts?term=x&limit=2&offset=2>; rel="next"` {
		t.Errorf("Link = %q", got)
	}
	if res.Headers.Get("X-Next-Cursor") != "" {
		t.Error("setof rpc must not mint cursors")
	}
}

func TestExecuteMintsJWT(t *testing.T) {
	b := built(t)
	exp := time.Now().Add(time.Hour).Unix()
	claims := fmt.Sprintf(`{"exp": %d, "user_id": 7, "role": "app_user"}`, exp)
	db := &fakeDB{row: fakeRow{data: []byte(claims)}}
	mint := jwtmint.Options{Schema: "public", Type: "jwt", Secret: "s3cret"}
	e := New(db, b, func(context.Context, *compile.Request) (*compile.Statement, error) {
		return &compile.Statement{SQL: "SELECT 1", Shape: compile.ShapeJWT}, nil
	}, nil, Options{Mint: mint})
	rpc := b.RPCByPath["authenticate"]
	res := e.Execute(context.Background(), &Operation{Method: "POST", Path: "/rpc/authenticate", RPC: rpc,
		Plan: &plan.Request{Method: plan.POST, RPC: rpc, Body: &plan.Body{Object: map[string]any{}}}})
	if res.Err != nil {
		t.Fatal(res.Err)
	}
	var tok string
	if err := json.Unmarshal(res.Data, &tok); err != nil {
		t.Fatalf("minted body must be a JSON string: %s", res.Data)
	}
	parsed := jwt.MapClaims{}
	if _, err := jwt.ParseWithClaims(tok, parsed, func(*jwt.Token) (any, error) { return []byte("s3cret"), nil }); err != nil {
		t.Fatalf("token does not verify: %v", err)
	}
	if parsed["role"] != "app_user" || parsed["user_id"] != float64(7) {
		t.Errorf("claims = %v", parsed)
	}
	// Null claims (unknown user) stay null; a set mints each element.
	db.row = fakeRow{data: []byte("null")}
	if res := e.Execute(context.Background(), &Operation{Method: "POST", Path: "/rpc/authenticate", RPC: rpc,
		Plan: &plan.Request{Method: plan.POST, RPC: rpc}}); res.Err != nil || string(res.Data) != "null" {
		t.Fatalf("null claims: %v %s", res.Err, res.Data)
	}
	e.Compile = func(context.Context, *compile.Request) (*compile.Statement, error) {
		return &compile.Statement{SQL: "SELECT 1", Shape: compile.ShapeJWTSet}, nil
	}
	db.row = fakeRow{data: []byte(`[` + claims + `, null]`)}
	res = e.Execute(context.Background(), &Operation{Method: "POST", Path: "/rpc/authenticate", RPC: rpc,
		Plan: &plan.Request{Method: plan.POST, RPC: rpc}})
	var toks []*string
	if res.Err != nil || json.Unmarshal(res.Data, &toks) != nil || len(toks) != 2 || toks[0] == nil || toks[1] != nil {
		t.Fatalf("set: %v %s", res.Err, res.Data)
	}
	// Minting without configuration is a server error, not a leaked claims object.
	e.Opts.Mint = jwtmint.Options{}
	if res := e.Execute(context.Background(), &Operation{Method: "POST", Path: "/rpc/authenticate", RPC: rpc,
		Plan: &plan.Request{Method: plan.POST, RPC: rpc}}); errOf(t, res).Status != 500 {
		t.Fatalf("unconfigured mint: %+v", res)
	}
}
