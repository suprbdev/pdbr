package e2e

import (
	"context"
	"os"
	"reflect"
	"testing"

	"github.com/jackc/pgx/v5"

	"github.com/suprbdev/pdbr/internal/config"
)

// TestTransactionBatch: requests run as one pipelined batch, so the
// statement must see the request identity and transactions.isolation,
// and the identity must end with the batch (pooled connections are reused
// by the next request).
func TestTransactionBatch(t *testing.T) {
	dbURL := os.Getenv("PDBR_TEST_DATABASE_URL")
	if dbURL == "" {
		t.Skip("PDBR_TEST_DATABASE_URL not set")
	}
	ctx := context.Background()
	conn, err := pgx.Connect(ctx, dbURL)
	if err != nil {
		t.Fatal(err)
	}
	defer conn.Close(ctx)
	t.Cleanup(func() {
		c, err := pgx.Connect(ctx, dbURL)
		if err != nil {
			t.Errorf("cleanup: %v", err)
			return
		}
		defer c.Close(ctx)
		_, _ = c.Exec(ctx, `DROP FUNCTION IF EXISTS public.e2e_tx_probe()`)
	})
	if _, err := conn.Exec(ctx, `CREATE OR REPLACE FUNCTION public.e2e_tx_probe() RETURNS jsonb LANGUAGE sql VOLATILE AS $$
  SELECT jsonb_build_object('iso', current_setting('transaction_isolation'), 'user', current_user::text,
    'uid', nullif(current_setting('jwt.claims.user_id', true), ''))
$$`); err != nil {
		t.Fatal(err)
	}
	var login string
	if err := conn.QueryRow(ctx, `SELECT current_user::text`).Scan(&login); err != nil {
		t.Fatal(err)
	}

	for _, iso := range []struct{ config, want string }{
		{"read_committed", "read committed"},
		{"repeatable_read", "repeatable read"},
		{"serializable", "serializable"},
	} {
		srv := testServer(t, func(c *config.Config) {
			headersAuth(c)
			c.TX.Isolation = iso.config
		})
		for range 3 {
			_, body, got := rpcCall(t, "POST", srv.URL+"/api/rpc/e2e_tx_probe", "{}", map[string]string{"X-Pdbr-Claim-Role": "app_user", "X-Pdbr-Claim-User-Id": "7"})
			want := map[string]any{"iso": iso.want, "user": "app_user", "uid": "7"}
			if !reflect.DeepEqual(got, want) {
				t.Errorf("%s app_user = %s, want %v", iso.config, body, want)
			}
			_, body, got = rpcCall(t, "POST", srv.URL+"/api/rpc/e2e_tx_probe", "{}", nil)
			want = map[string]any{"iso": iso.want, "user": "anonymous", "uid": nil}
			if !reflect.DeepEqual(got, want) {
				t.Errorf("%s anonymous = %s, want %v (identity leaked across requests?)", iso.config, body, want)
			}
		}
	}

	off := testServer(t, func(c *config.Config) { c.TX.Mutations = false })
	_, body, got := rpcCall(t, "POST", off.URL+"/api/rpc/e2e_tx_probe", "{}", map[string]string{"X-Pdbr-Claim-Role": "app_user"})
	if m, _ := got.(map[string]any); m["user"] != login || m["uid"] != nil {
		t.Errorf("no tx, rls off = %s, want the login role %s", body, login)
	}
}
