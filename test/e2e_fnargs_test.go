package e2e

import (
	"context"
	"encoding/json"
	"net/url"
	"os"
	"testing"

	"github.com/jackc/pgx/v5"

	"github.com/suprbdev/pdbr/internal/config"
)

func TestRPCRowArguments(t *testing.T) {
	rpcFixtures(t)
	srv := testServer(t, nil)
	base := srv.URL + "/api/rpc/"

	if v := rpcOK(t, "POST", base+"user_greeting", `{"greeting": "Hello", "u": {"full_name": "Ada", "email": "a@x"}}`); v != "Hello, Ada!" {
		t.Errorf("user_greeting = %v", v)
	}
	if v := rpcOK(t, "POST", base+"user_greeting", `{"greeting": "Hi", "u": {"email": "a@x", "mood": "happy", "tags": ["a"], "settings": {"k": 1}, "address": {"city": "Paris"}, "balance": "9007199254740993"}}`); v != "Hi, a@x!" {
		t.Errorf("user_greeting with typed columns = %v", v)
	}
	if v := rpcOK(t, "GET", base+"user_greeting?greeting=Hey&u="+url.QueryEscape(`{"email":"g@x"}`), ""); v != "Hey, g@x!" {
		t.Errorf("GET user_greeting = %v", v)
	}
	if v := rpcOK(t, "POST", base+"user_greeting", `{"greeting": "Hi", "u": null}`); v != nil {
		t.Errorf("user_greeting(null row) = %v", v)
	}
	if v := rpcOK(t, "POST", base+"total_title_length", `{"ps": [{"title": "abc"}, null, {"id": 9, "title": "de"}]}`); v == nil || v.(interface{ String() string }).String() != "5" {
		t.Errorf("total_title_length = %v", v)
	}
	if v := rpcOK(t, "POST", base+"total_title_length", `{"ps": []}`); v == nil || v.(interface{ String() string }).String() != "0" {
		t.Errorf("total_title_length([]) = %v", v)
	}
	if body := rpcOK(t, "POST", base+"address_cities", `{"addrs": [{"city": "London"}, null, {"street": "x", "city": "Paris"}]}`); !sameJSONValue(body, `["London", null, "Paris"]`) {
		t.Errorf("address_cities = %v", body)
	}

	res, body, _ := rpcCall(t, "POST", base+"user_greeting", `{"greeting": "Hi", "u": {"nope": 1}}`, nil)
	wantProblem(t, res, body, 400, "invalid_argument")
	res, body, _ = rpcCall(t, "POST", base+"user_greeting", `{"greeting": "Hi", "u": "x"}`, nil)
	wantProblem(t, res, body, 400, "invalid_argument")
	res, body, _ = rpcCall(t, "GET", base+"user_greeting?greeting=Hi&u=nope", "", nil)
	wantProblem(t, res, body, 400, "invalid_argument")
	res, body, _ = rpcCall(t, "GET", base+"rename_user", "", nil)
	if res.StatusCode != 405 {
		t.Errorf("GET rename_user = %d %s", res.StatusCode, body)
	}
}

func TestRPCVolatileRowArgument(t *testing.T) {
	rpcFixtures(t)
	dbURL := os.Getenv("PDBR_TEST_DATABASE_URL")
	t.Cleanup(func() {
		ctx := context.Background()
		conn, err := pgx.Connect(ctx, dbURL)
		if err != nil {
			t.Errorf("cleanup connect: %v", err)
			return
		}
		defer conn.Close(ctx)
		if _, err := conn.Exec(ctx, `UPDATE users SET full_name = 'Grace Hopper' WHERE id = 2`); err != nil {
			t.Errorf("cleanup: %v", err)
		}
	})
	srv := testServer(t, nil)
	v := rpcOK(t, "POST", srv.URL+"/api/rpc/rename_user?select=id,full_name", `{"u": {"id": 2}, "new_name": "Rear Admiral Hopper"}`)
	if !sameJSONValue(v, `{"id": 2, "full_name": "Rear Admiral Hopper"}`) {
		t.Errorf("rename_user = %v", v)
	}
	if v := rpcOK(t, "POST", srv.URL+"/api/rpc/rename_user", `{"u": {"id": 999}, "new_name": "x"}`); v != nil {
		t.Errorf("rename_user(no row) = %v", v)
	}
}

func TestRPCPolymorphicArguments(t *testing.T) {
	rpcFixtures(t)
	srv := testServer(t, nil)
	base := srv.URL + "/api/rpc/"
	for path, want := range map[string]string{
		"pick_first?a=x&b=y":                        `"x"`,
		"larger_of?a=1&b=2.5":                       `"2.5"`,
		"range_lower?r=" + url.QueryEscape("[3,5)"): `3`,
	} {
		res, body, _ := rpcCall(t, "GET", base+path, "", nil)
		if res.StatusCode != 200 || !sameJSON(body, want) {
			t.Errorf("GET %s = %d %s, want %s", path, res.StatusCode, body, want)
		}
	}
	if v := rpcOK(t, "POST", base+"pick_first", `{"a": null, "b": "y"}`); v != "y" {
		t.Errorf("pick_first(null, y) = %v", v)
	}
	if v := rpcOK(t, "POST", base+"range_lower", `{"r": {"start": 7, "end": 9}}`); v == nil || v.(interface{ String() string }).String() != "7" {
		t.Errorf("POST range_lower = %v", v)
	}
	res, body, _ := rpcCall(t, "GET", base+"array_first?arr=%7B1%7D", "", nil)
	wantProblem(t, res, body, 404, "not_found")

	off := testServer(t, func(c *config.Config) { c.Plugins.Disabled = []string{"smart_comments"} })
	res, body, _ = rpcCall(t, "GET", off.URL+"/api/rpc/pick_first?a=x&b=y", "", nil)
	wantProblem(t, res, body, 404, "not_found")
}

func sameJSONValue(v any, want string) bool {
	b, err := json.Marshal(v)
	return err == nil && sameJSON(b, want)
}
