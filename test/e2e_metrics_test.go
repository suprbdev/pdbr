package e2e

import (
	"strconv"
	"strings"
	"testing"

	"github.com/suprbdev/pdbr/internal/config"
)

// metricValue returns the value of the exposition line for series (name
// plus labels exactly as rendered), or -1 when the series is absent.
func metricValue(body, series string) float64 {
	for _, line := range strings.Split(body, "\n") {
		if v, ok := strings.CutPrefix(line, series+" "); ok {
			f, err := strconv.ParseFloat(v, 64)
			if err != nil {
				return -1
			}
			return f
		}
	}
	return -1
}

func scrapeMetrics(t *testing.T, url string) string {
	t.Helper()
	res, body := get(t, url)
	if res.StatusCode != 200 {
		t.Fatalf("GET %s = %d %s", url, res.StatusCode, body)
	}
	return string(body)
}

// TestMetrics: with metrics.enabled the real handler serves /metrics outside
// the base path, and the HTTP, database, pool, compile and product series
// reflect the requests served.
func TestMetrics(t *testing.T) {
	srv := testServer(t, func(c *config.Config) {
		c.Metrics.Enabled = true
		c.Server.Aggregates = true
	})

	var rows []row
	getJSON(t, srv.URL+"/api/users?select=id&limit=2", &rows)
	getJSON(t, srv.URL+"/api/posts?select=count()", &rows)
	rpcOK(t, "GET", srv.URL+"/api/rpc/best_mood", "")
	rpcOK(t, "POST", srv.URL+"/api/rpc/add_numbers", `{"a": 1, "b": 2}`)
	res, body := get(t, srv.URL+"/api/users?select=id,posts(id,title,comments(id))")
	wantProblem(t, res, body, 400, "cost_exceeded")
	res, body = get(t, srv.URL+"/api/nope")
	wantProblem(t, res, body, 404, "not_found")
	res, body = get(t, srv.URL+"/nope")
	wantProblem(t, res, body, 404, "not_found")

	out := scrapeMetrics(t, srv.URL+"/metrics")
	for series, want := range map[string]float64{
		`pdbr_http_requests_total{method="GET",route="resource",status_class="2xx"}`: 2,
		`pdbr_http_requests_total{method="GET",route="resource",status_class="4xx"}`: 1,
		`pdbr_http_requests_total{method="GET",route="rpc",status_class="2xx"}`:      1,
		`pdbr_http_requests_total{method="POST",route="rpc",status_class="2xx"}`:     1,
		`pdbr_http_requests_total{method="GET",route="other",status_class="4xx"}`:    2,
		`pdbr_db_duration_seconds_count{op="query"}`:                                 3,
		`pdbr_db_duration_seconds_count{op="query_tx"}`:                              1,
		`pdbr_db_pool_max_conns`:                       10,
		`pdbr_cost_limit_rejections_total`:             1,
		`pdbr_aggregate_requests_total`:                1,
		`pdbr_compile_duration_seconds_count`:          4,
		`pdbr_schema_rebuilds_total{result="success"}`: 0,
		`pdbr_http_requests_in_flight`:                 1,
	} {
		if got := metricValue(out, series); got != want {
			t.Errorf("%s = %v, want %v", series, got, want)
		}
	}
	if got := metricValue(out, `pdbr_db_pool_acquires_total`); got < 4 {
		t.Errorf("pdbr_db_pool_acquires_total = %v, want >= 4", got)
	}

	out = scrapeMetrics(t, srv.URL+"/metrics")
	if got := metricValue(out, `pdbr_http_requests_total{method="GET",route="metrics",status_class="2xx"}`); got != 1 {
		t.Errorf("metrics scrapes counted = %v, want 1", got)
	}
}

func TestMetricsDisabled(t *testing.T) {
	srv := testServer(t, nil)
	res, body := get(t, srv.URL+"/metrics")
	wantProblem(t, res, body, 404, "not_found")
}
