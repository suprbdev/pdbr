package e2e

import (
	"net/http"
	"os"
	"slices"
	"testing"

	"github.com/suprbdev/pdbr/internal/config"
)

// TestBareAliasesFollowFirstSchema boots live with a primary schema that
// sorts after the second one: the bare paths must serve schema.schemas[0],
// and the configured slice must keep its order.
func TestBareAliasesFollowFirstSchema(t *testing.T) {
	url := os.Getenv("PDBR_TEST_DATABASE_URL")
	if url == "" {
		t.Skip("PDBR_TEST_DATABASE_URL not set; run `make test-e2e`")
	}
	drop := `DROP SCHEMA IF EXISTS b15_order CASCADE`
	execDDL(t, url, drop,
		`CREATE SCHEMA b15_order`,
		`CREATE TABLE b15_order.users (id int PRIMARY KEY, tag text NOT NULL)`,
		`INSERT INTO b15_order.users VALUES (1, 'secondary')`,
	)
	t.Cleanup(func() { execDDL(t, url, drop) })
	schemas := []string{"public", "b15_order"}
	srv := testServer(t, func(c *config.Config) { c.Schema.Schemas = schemas })
	if !slices.Equal(schemas, []string{"public", "b15_order"}) {
		t.Fatalf("schema.schemas reordered: %v", schemas)
	}
	base := srv.URL + "/api"
	res, body := send(t, http.MethodGet, base+"/users?select=tag", "", nil)
	wantProblem(t, res, body, http.StatusBadRequest, "unknown_column")
	var rows []row
	getJSON(t, base+"/b15_order/users?select=tag", &rows)
	if len(rows) != 1 || rows[0]["tag"] != "secondary" {
		t.Fatalf("b15_order rows = %v", rows)
	}
}
