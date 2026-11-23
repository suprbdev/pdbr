package e2e

import (
	"context"
	"os"
	"testing"

	"github.com/jackc/pgx/v5"

	"github.com/suprbdev/pdbr/internal/config"
)

// partitionFixtures creates a range-partitioned e2e_orders referencing
// e2e_customers (the FK carries smart-comment relation names, which every
// per-partition clone used to repeat) and e2e_items referencing the
// partitioned table; dropped again after the test.
func partitionFixtures(t *testing.T) {
	t.Helper()
	url := os.Getenv("PDBR_TEST_DATABASE_URL")
	if url == "" {
		t.Skip("PDBR_TEST_DATABASE_URL not set; run `make test-e2e`")
	}
	ctx := context.Background()
	run := func(ddl ...string) error {
		conn, err := pgx.Connect(ctx, url)
		if err != nil {
			return err
		}
		defer conn.Close(ctx)
		for _, d := range ddl {
			if _, err := conn.Exec(ctx, d); err != nil {
				return err
			}
		}
		return nil
	}
	drop := []string{`DROP TABLE IF EXISTS public.e2e_items, public.e2e_orders, public.e2e_customers`}
	if err := run(drop...); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if err := run(drop...); err != nil {
			t.Errorf("partition fixture cleanup: %v", err)
		}
	})
	err := run(
		`CREATE TABLE public.e2e_customers (id int PRIMARY KEY, name text NOT NULL)`,
		`CREATE TABLE public.e2e_orders (id int NOT NULL, customer_id int NOT NULL REFERENCES public.e2e_customers (id),
		    placed_at date NOT NULL, PRIMARY KEY (id, placed_at)) PARTITION BY RANGE (placed_at)`,
		`CREATE TABLE public.e2e_orders_2025 PARTITION OF public.e2e_orders FOR VALUES FROM ('2025-01-01') TO ('2026-01-01')`,
		`CREATE TABLE public.e2e_orders_2026 PARTITION OF public.e2e_orders FOR VALUES FROM ('2026-01-01') TO ('2027-01-01')`,
		`CREATE INDEX ON public.e2e_orders (customer_id)`,
		`COMMENT ON CONSTRAINT e2e_orders_customer_id_fkey ON public.e2e_orders IS E'@fieldName buyer\n@foreignFieldName purchases'`,
		`CREATE TABLE public.e2e_items (id int PRIMARY KEY, order_id int NOT NULL, placed_at date NOT NULL,
		    FOREIGN KEY (order_id, placed_at) REFERENCES public.e2e_orders (id, placed_at))`,
		`INSERT INTO public.e2e_customers VALUES (1, 'ada')`,
		`INSERT INTO public.e2e_orders VALUES (1, 1, '2025-05-01'), (2, 1, '2026-05-01')`,
		`INSERT INTO public.e2e_items VALUES (1, 2, '2026-05-01')`,
		`GRANT SELECT, INSERT, UPDATE, DELETE ON public.e2e_customers, public.e2e_orders, public.e2e_orders_2025,
		    public.e2e_orders_2026, public.e2e_items TO anonymous, app_user`,
	)
	if err != nil {
		t.Fatalf("partition fixture: %v", err)
	}
}

func TestPartitions(t *testing.T) {
	partitionFixtures(t)
	srv := testServer(t, nil)
	var rows []row
	getJSON(t, srv.URL+"/api/e2e_customers?select=id,purchases(id,e2e_items(id))&purchases.order=id&limit=5&purchases.limit=5", &rows)
	if len(rows) != 1 || len(rows[0]["purchases"].([]any)) != 2 {
		t.Fatalf("customers with purchases = %v", rows)
	}
	if items := rows[0]["purchases"].([]any)[1].(map[string]any)["e2e_items"].([]any); len(items) != 1 {
		t.Errorf("items of order 2 = %v", items)
	}
	getJSON(t, srv.URL+"/api/e2e_orders?select=id,buyer(name)&order=id", &rows)
	if len(rows) != 2 || rows[1]["buyer"].(map[string]any)["name"] != "ada" {
		t.Errorf("orders with buyer = %v", rows)
	}
	getJSON(t, srv.URL+"/api/e2e_items?select=id,e2e_orders(id)", &rows)
	if len(rows) != 1 || rows[0]["e2e_orders"].(map[string]any)["id"] != float64(2) {
		t.Errorf("items with order = %v", rows)
	}
	for _, path := range []string{"/api/e2e_orders_2025", "/api/public/e2e_orders_2026"} {
		res, body := get(t, srv.URL+path)
		wantProblem(t, res, body, 404, "not_found")
	}
	var created row
	sendJSON(t, "POST", srv.URL+"/api/e2e_orders", `{"id": 3, "customer_id": 1, "placed_at": "2025-07-01"}`, 200, &created)
	if created["id"] != float64(3) {
		t.Errorf("insert through the parent = %v", created)
	}
	doc := openAPI(t, srv.URL)
	paths := doc["paths"].(map[string]any)
	if paths["/e2e_orders"] == nil || paths["/e2e_orders_2025"] != nil {
		t.Errorf("OpenAPI paths: parent %v, partition %v", paths["/e2e_orders"] != nil, paths["/e2e_orders_2025"] != nil)
	}

	exposed := testServer(t, func(c *config.Config) { c.Schema.ExposePartitions = true })
	getJSON(t, exposed.URL+"/api/e2e_orders_2025?select=id,customer_id", &rows)
	if len(rows) != 2 {
		t.Errorf("exposed partition rows = %v", rows)
	}
	getJSON(t, exposed.URL+"/api/e2e_customers?select=id,purchases(id)", &rows)
	if len(rows) != 1 || len(rows[0]["purchases"].([]any)) != 3 {
		t.Errorf("purchases with partitions exposed = %v", rows)
	}
}
