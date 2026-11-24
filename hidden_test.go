package pdbr

import (
	"context"
	"io"
	"log/slog"
	"strings"
	"testing"

	"github.com/suprbdev/pdbcore/introspect"
)

func hiddenRowsCatalog(t *testing.T) *introspect.Catalog {
	t.Helper()
	cat := contractCatalog(t)
	priv := introspect.Privileges{Select: true}
	cat.Tables = append(cat.Tables,
		&introspect.Table{Schema: "public", Name: "vault", Kind: introspect.RelTable, Comment: "@omit", Privileges: priv,
			Columns: []*introspect.Column{
				{Name: "id", PGType: "int4", TypeSchema: "pg_catalog", NotNull: true, Position: 1},
				{Name: "pin", PGType: "text", TypeSchema: "pg_catalog", Position: 2},
			},
			PrimaryKey: &introspect.Constraint{Name: "vault_pkey", Columns: []string{"id"}}},
		&introspect.Table{Schema: "public", Name: "secrets", Kind: introspect.RelTable, Comment: "@behavior -contract:public", Privileges: priv,
			Columns: []*introspect.Column{
				{Name: "id", PGType: "int4", TypeSchema: "pg_catalog", NotNull: true, Position: 1},
				{Name: "token", PGType: "text", TypeSchema: "pg_catalog", Position: 2},
			},
			PrimaryKey: &introspect.Constraint{Name: "secrets_pkey", Columns: []string{"id"}}},
	)
	users := cat.Table("public", "users")
	users.Columns = append(users.Columns,
		&introspect.Column{Name: "vault_copy", PGType: "vault", TypeSchema: "public", Position: len(users.Columns) + 1},
		&introspect.Column{Name: "vault_copies", PGType: "_vault", TypeSchema: "public", IsArray: true, Position: len(users.Columns) + 2},
	)
	usersArg := introspect.FuncArg{Name: "u", PGType: "users", TypeSchema: "public"}
	intArg := introspect.FuncArg{Name: "k", PGType: "int4", TypeSchema: "pg_catalog"}
	fn := func(name, ret, retSchema string, set bool, args ...introspect.FuncArg) *introspect.Function {
		return &introspect.Function{Schema: "public", Name: name, Args: args, ReturnType: ret, ReturnTypeSchema: retSchema,
			ReturnsSet: set, Volatility: introspect.VolatilityStable}
	}
	peek := fn("peek", "anyelement", "pg_catalog", false,
		introspect.FuncArg{Name: "probe", PGType: "anyelement", TypeSchema: "pg_catalog"}, intArg)
	peek.Comment = "@anyelement public.secrets"
	peekVault := fn("peek_vault", "anyelement", "pg_catalog", false,
		introspect.FuncArg{Name: "probe", PGType: "anyelement", TypeSchema: "pg_catalog"}, intArg)
	peekVault.Comment = "@anyelement public.vault"
	table := fn("vault_table", "record", "pg_catalog", true)
	table.ReturnColumns = []introspect.FuncArg{
		{Name: "id", PGType: "int4", TypeSchema: "pg_catalog", Mode: introspect.ArgModeTable},
		{Name: "v", PGType: "vault", TypeSchema: "public", Mode: introspect.ArgModeTable},
	}
	cat.Functions = append(cat.Functions,
		fn("get_vault", "vault", "public", false, intArg),
		fn("all_vault", "vault", "public", true),
		fn("vault_array", "_vault", "public", false),
		fn("vault_pin", "text", "pg_catalog", false, introspect.FuncArg{Name: "v", PGType: "vault", TypeSchema: "public"}),
		fn("users_vault", "vault", "public", false, usersArg),
		fn("users_vaults", "vault", "public", true, usersArg),
		fn("get_secret", "secrets", "public", false, intArg),
		fn("users_secret", "secrets", "public", false, usersArg),
		fn("get_stash", "stash", "priv", false, intArg),
		fn("users_stash", "stash", "priv", true, usersArg),
		table, peek, peekVault,
	)
	cat.Reindex()
	return cat
}

func TestHiddenRowTypesNotExposed(t *testing.T) {
	ctx := context.Background()
	app := New(contractConfig(), WithLogger(slog.New(slog.NewTextHandler(io.Discard, nil))))
	hiddenRPCs := []string{"get_vault", "all_vault", "vault_array", "vault_pin", "vault_table", "peek_vault", "get_stash"}
	hiddenComputed := []string{"vault", "vaults", "stash", "vault_copy", "vault_copies"}
	for _, name := range []string{"", "internal", "public"} {
		built, err := app.BuildContractAPI(ctx, hiddenRowsCatalog(t), name)
		if err != nil {
			t.Fatalf("%q: %v", name, err)
		}
		rpcs, computed := hiddenRPCs, hiddenComputed
		if name == "public" {
			rpcs = append(append([]string(nil), rpcs...), "get_secret", "peek")
			computed = append(append([]string(nil), computed...), "secret")
		}
		for _, fn := range rpcs {
			if built.RPCs["public."+fn] != nil {
				t.Errorf("%q: rpc %s is exposed", name, fn)
			}
		}
		users := built.ByPath["users"]
		for _, key := range computed {
			if users.Computed[key] != nil || users.Field(key) != nil || users.InputColumns[key] != nil {
				t.Errorf("%q: users.%s is exposed", name, key)
			}
		}
		_, raw := decodeOpenAPI(t, built)
		for _, word := range []string{"vault", "stash"} {
			if strings.Contains(raw, word) {
				t.Errorf("%q: OpenAPI mentions %q", name, word)
			}
		}
		if name == "public" && strings.Contains(raw, "secret") {
			t.Errorf("public: OpenAPI mentions the contract-hidden secrets table")
		}
		if name != "public" && (built.RPCs["public.get_secret"] == nil || built.ByPath["secrets"].Computed["peek"] == nil || users.Computed["secret"] == nil) {
			t.Errorf("%q: functions over the visible secrets table were dropped", name)
		}
	}
}

// TestContractKeepsRowFunctionsOfVisibleTables: a function returning rows of
// a table one contract hides stays in the contracts that show the table,
// whatever its return shape (one row, a set, an array of rows).
func TestContractKeepsRowFunctionsOfVisibleTables(t *testing.T) {
	ctx := context.Background()
	app := New(contractConfig(), WithLogger(slog.New(slog.NewTextHandler(io.Discard, nil))))
	cat := hiddenRowsCatalog(t)
	for _, f := range []struct {
		name, ret string
		set       bool
	}{{"secret_one", "secrets", false}, {"secret_set", "secrets", true}, {"secret_arr", "_secrets", false}} {
		cat.Functions = append(cat.Functions, &introspect.Function{Schema: "public", Name: f.name, ReturnType: f.ret,
			ReturnTypeSchema: "public", ReturnsSet: f.set, Volatility: introspect.VolatilityStable})
	}
	cat.Reindex()
	for _, name := range []string{"internal", "public", "internal"} {
		built, err := app.BuildContractAPI(ctx, cat, name)
		if err != nil {
			t.Fatalf("%q: %v", name, err)
		}
		for _, fn := range []string{"secret_one", "secret_set", "secret_arr"} {
			if got, want := built.RPCs["public."+fn] != nil, name == "internal"; got != want {
				t.Errorf("%q: rpc %s exposed = %v, want %v", name, fn, got, want)
			}
		}
	}
}
