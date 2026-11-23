package exec

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/jackc/pgx/v5/pgconn"

	"github.com/suprbdev/pdbcore/metrics"

	"github.com/suprbdev/pdbr/internal/plan"
)

func TestExecuteMetrics(t *testing.T) {
	b := built(t)
	m, err := metrics.New(metrics.Options{Namespace: "pdbr"})
	if err != nil {
		t.Fatal(err)
	}
	costs, err := m.NewCounter("cost_limit_rejections_total", "test")
	if err != nil {
		t.Fatal(err)
	}
	aggs, err := m.NewCounter("aggregate_requests_total", "test")
	if err != nil {
		t.Fatal(err)
	}
	opts := Options{Metrics: m, CostRejections: costs, AggregateRequests: aggs}
	db := &fakeDB{row: fakeRow{data: []byte(`{"rows":[],"has_next":false,"has_prev":false}`)}}
	e := newExec(db, b, nil, opts)
	ctx := context.Background()

	if res := e.Execute(ctx, listOp(b, "/users")); res.Err != nil {
		t.Fatalf("list: %v", res.Err)
	}
	db.row = fakeRow{err: &pgconn.PgError{Code: "23505"}}
	if res := e.Execute(ctx, listOp(b, "/users")); res.Status != http.StatusConflict {
		t.Fatalf("unique violation = %d", res.Status)
	}
	db.row = fakeRow{data: []byte(`{"rows":[],"has_next":false,"has_prev":false}`)}
	agg := listOp(b, "/users")
	agg.Plan.Select = &plan.Node{Resource: agg.Resource, Fields: []plan.FieldSel{{Agg: &plan.Aggregate{Func: plan.AggCount}}}}
	e.Execute(ctx, agg)

	cheap := newExec(db, b, nil, Options{Metrics: m, CostRejections: costs, AggregateRequests: aggs, MaxCost: 5})
	if res := cheap.Execute(ctx, listOp(b, "/users")); res.Status != http.StatusBadRequest {
		t.Fatalf("cost gate = %d", res.Status)
	}

	op := listOp(b, "/users")
	op.ForceTx = true
	if res := e.Execute(ctx, op); res.Err != nil {
		t.Fatalf("list in a transaction: %v", res.Err)
	}
	if len(db.txs) != 1 || db.txs[0].opts.Observer == nil {
		t.Fatalf("transaction observer not set: %+v", db.txs)
	}

	rec := httptest.NewRecorder()
	m.Handler().ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/metrics", nil))
	body := rec.Body.String()
	for _, s := range []string{
		`pdbr_cost_limit_rejections_total 1`,
		`pdbr_aggregate_requests_total 1`,
		`pdbr_db_errors_total{op="query",sqlstate_class="23"} 1`,
		`pdbr_compile_duration_seconds_count 4`,
	} {
		if !strings.Contains(body, "\n"+s+"\n") {
			t.Errorf("missing %s", s)
		}
	}
	if !strings.Contains(body, "\npdbr_db_duration_seconds_count{op=\"query\"} ") {
		t.Errorf("missing pdbr_db_duration_seconds_count for op=query")
	}
}

func TestExecuteNilMetrics(t *testing.T) {
	b := built(t)
	db := &fakeDB{row: fakeRow{data: []byte(`{"rows":[],"has_next":false,"has_prev":false}`)}}
	e := newExec(db, b, nil, Options{MaxCost: 5})
	if res := e.Execute(context.Background(), listOp(b, "/users")); res.Status != http.StatusBadRequest {
		t.Fatalf("cost gate = %d", res.Status)
	}
	e = newExec(db, b, nil, Options{})
	if res := e.Execute(context.Background(), listOp(b, "/users")); res.Err != nil {
		t.Fatalf("list: %v", res.Err)
	}
}
