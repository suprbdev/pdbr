package server

import (
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/suprbdev/pdbcore/testutil"

	"github.com/suprbdev/pdbr/internal/api"
	"github.com/suprbdev/pdbr/internal/compile"
	"github.com/suprbdev/pdbr/internal/exec"
	"github.com/suprbdev/pdbr/internal/inflect"
)

// pageData renders a page wrapper of n rows in the layout the compiler
// emits (PostgreSQL json spacing, rows last).
func pageData(n int) string {
	rows := make([]string, n)
	for i := range rows {
		rows[i] = fmt.Sprintf(`{"id" : %d, "email" : "user%06d@example.com", "full_name" : "User number %d with a longer name", "mood" : "happy", "balance" : "%d", "created_at" : "2026-01-02T03:04:05.123456+00:00", "tags" : ["a","b"]}`, i+1, i, i, i*100)
	}
	return fmt.Sprintf(`{"has_next" : true, "has_prev" : false, "first" : [1], "last" : [%d], "n" : %d, "rows" : [%s]}`, n, n, strings.Join(rows, ", "))
}

// BenchmarkHandlerPage serves GET /api/users through the full middleware
// chain (timeout buffer included) over a scripted database returning a
// page of rows/op rows.
func BenchmarkHandlerPage(b *testing.B) {
	for _, n := range []int{1, 100, 1000} {
		b.Run(fmt.Sprintf("rows=%d", n), func(b *testing.B) {
			db := (&scriptedDB{}).on(`FROM "public"."users"`, pageData(n), nil)
			cfg := serverCfg()
			built, err := api.New(testutil.FixtureCatalog(), inflect.Default, api.Options{
				FilterIndexedOnly: true, Functions: true, BasePath: cfg.BasePath, Schemas: []string{"public"},
				MaxPageSize: 1000, Etags: true, Version: "test", Logger: quiet(),
			}).Build()
			if err != nil {
				b.Fatal(err)
			}
			ex := exec.New(db, built, compile.New(built).Compile, nil, exec.Options{BasePath: cfg.BasePath, Logger: quiet(), MaxPageSize: 1000, MaxEmbedDepth: 5, MaxCost: 1000000})
			rt, err := NewRuntime(built, NewHandler(built, ex, ParseOptions(built, 5, 1000, true, true), cfg.BasePath, quiet()))
			if err != nil {
				b.Fatal(err)
			}
			h := New(cfg, nil, rt, quiet()).Handler()
			req := httptest.NewRequest(http.MethodGet, "/api/users?select=id,email,full_name,mood,balance,created_at,tags&limit=1000", nil)
			b.SetBytes(int64(len(pageData(n))))
			b.ReportAllocs()
			b.ResetTimer()
			for b.Loop() {
				db.seen = db.seen[:0]
				rec := httptest.NewRecorder()
				h.ServeHTTP(rec, req)
				if rec.Code != http.StatusOK {
					b.Fatalf("status %d: %s", rec.Code, rec.Body.String())
				}
			}
		})
	}
}
