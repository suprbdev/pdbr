package server

import (
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/suprbdev/pdbr/internal/api"
)

func TestTimeoutWriteBody(t *testing.T) {
	body := []byte(`[{"id" : 1}]`)
	for _, tc := range []struct {
		name  string
		write func(w http.ResponseWriter)
		want  string
		owned bool
	}{
		{"handed over", func(w http.ResponseWriter) { writeBody(w, body) }, `[{"id" : 1}]`, true},
		{"then written", func(w http.ResponseWriter) { writeBody(w, body); w.Write([]byte("!")) }, `[{"id" : 1}]!`, false},
		{"after a write", func(w http.ResponseWriter) { w.Write([]byte("<")); writeBody(w, body) }, `<[{"id" : 1}]`, false},
		{"twice", func(w http.ResponseWriter) { writeBody(w, body); writeBody(w, body) }, `[{"id" : 1}][{"id" : 1}]`, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			var tw *timeoutWriter
			h := withTimeout(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				tw = w.(*timeoutWriter)
				w.WriteHeader(http.StatusTeapot)
				tc.write(w)
			}), time.Second)
			rec := httptest.NewRecorder()
			h.ServeHTTP(rec, httptest.NewRequest("GET", "/", nil))
			if rec.Code != http.StatusTeapot || rec.Body.String() != tc.want {
				t.Fatalf("got %d %q, want %q", rec.Code, rec.Body.String(), tc.want)
			}
			if owned := tw.body != nil && &tw.body[0] == &body[0]; owned != tc.owned {
				t.Errorf("body kept without a copy = %v, want %v", owned, tc.owned)
			}
		})
	}
}

// TestRouteWriterTakesBodies: behind the full middleware chain a route
// handler can hand its body to the timeout buffer, unless the response is
// gzip-compressed.
func TestRouteWriterTakesBodies(t *testing.T) {
	var takes bool
	h := RouteHandlerFunc(func(w http.ResponseWriter, _ *http.Request, _ *api.Match) {
		_, takes = w.(bodyWriter)
		writeBody(w, []byte("[]"))
	})
	cfg := serverCfg()
	cfg.CORSOrigins = []string{"https://example.com"}
	cfg.Compression = true
	srv := testServer(t, cfg, nil, h)
	for enc, want := range map[string]bool{"identity": true, "gzip": false} {
		req, _ := http.NewRequest("GET", srv.URL+"/api/users", nil)
		req.Header.Set("Accept-Encoding", enc)
		res, err := http.DefaultTransport.RoundTrip(req)
		if err != nil {
			t.Fatal(err)
		}
		res.Body.Close()
		if res.StatusCode != 200 || takes != want {
			t.Errorf("Accept-Encoding %s: status %d, writer takes bodies = %v, want %v", enc, res.StatusCode, takes, want)
		}
	}
}
