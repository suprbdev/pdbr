package e2e

import (
	"bytes"
	"encoding/csv"
	"net/http"
	"os"
	"os/exec"
	"strings"
	"testing"

	"github.com/suprbdev/pdbr/internal/config"
)

const csvType = "text/csv; charset=utf-8"

func csvRows(t *testing.T, body []byte) [][]string {
	t.Helper()
	rows, err := csv.NewReader(bytes.NewReader(body)).ReadAll()
	if err != nil {
		t.Fatalf("csv parse: %v\n%s", err, body)
	}
	return rows
}

func TestCSVLists(t *testing.T) {
	srv := testServer(t, nil).URL
	accept := map[string]string{"Accept": "text/csv"}

	res, body := do(t, "GET", srv+"/api/users?select=id,email,tags,settings,full_name&order=id&limit=2", accept)
	if res.StatusCode != 200 || res.Header.Get("Content-Type") != csvType {
		t.Fatalf("csv = %d %q %s", res.StatusCode, res.Header.Get("Content-Type"), body)
	}
	if !strings.HasSuffix(string(body), "\r\n") || !strings.HasPrefix(string(body), "id,email,tags,settings,full_name\r\n") {
		t.Errorf("csv framing: %q", body)
	}
	rows := csvRows(t, body)
	if len(rows) != 3 || rows[1][0] != "1" || rows[1][1] != "ada@example.com" {
		t.Fatalf("rows = %v", rows)
	}
	// Arrays / json travel as compact JSON text, NULL as an empty field.
	var jsonRows []row
	getJSON(t, srv+"/api/users?select=id,email,tags,settings,full_name&order=id&limit=2", &jsonRows)
	for i, jr := range jsonRows {
		for j, col := range []string{"tags", "settings", "full_name"} {
			got := rows[i+1][2+j]
			switch v := jr[col].(type) {
			case nil:
				if got != "" {
					t.Errorf("row %d %s: NULL rendered as %q", i, col, got)
				}
			case string:
				if got != v {
					t.Errorf("row %d %s = %q, want %q", i, col, got, v)
				}
			default:
				if !strings.HasPrefix(got, "[") && !strings.HasPrefix(got, "{") {
					t.Errorf("row %d %s = %q, want JSON text", i, col, got)
				}
			}
		}
	}
	// select=* → catalog order; alias renames the column; the paging
	// headers are unchanged.
	res, body = do(t, "GET", srv+"/api/users?limit=1&order=id&count=true", accept)
	if hdr := csvRows(t, body)[0]; strings.Join(hdr, ",") != "id,email,full_name,mood,settings,tags,balance,created_at,address,prev_addresses,moods" {
		t.Errorf("select=* header = %v", hdr)
	}
	if res.Header.Get("X-Total-Count") != "3" || res.Header.Get("X-Next-Cursor") == "" || !strings.Contains(res.Header.Get("Link"), `rel="next"`) {
		t.Errorf("csv page headers = %v", res.Header)
	}
	_, aliased := do(t, "GET", srv+"/api/users?select=mail:email,id&limit=1", accept)
	if hdr := csvRows(t, aliased)[0]; strings.Join(hdr, ",") != "mail,id" {
		t.Errorf("alias header = %v", hdr)
	}
	// HEAD parity: same status, headers and media type, no body.
	jsonRes, _ := do(t, "GET", srv+"/api/users?limit=1&order=id&count=true", nil)
	head, hb := do(t, "HEAD", srv+"/api/users?limit=1&order=id&count=true", accept)
	if head.StatusCode != jsonRes.StatusCode || len(hb) != 0 || head.Header.Get("Content-Type") != csvType {
		t.Errorf("HEAD csv = %d %q %v", head.StatusCode, hb, head.Header)
	}
	for _, h := range []string{"X-Total-Count", "X-Next-Cursor", "Link"} {
		if head.Header.Get(h) != jsonRes.Header.Get(h) || head.Header.Get(h) != res.Header.Get(h) {
			t.Errorf("HEAD/GET/JSON %s differ: %q %q %q", h, head.Header.Get(h), res.Header.Get(h), jsonRes.Header.Get(h))
		}
	}
	// Empty result: header row only.
	_, body = do(t, "GET", srv+"/api/users?select=id&email=eq.nobody", accept)
	if string(body) != "id\r\n" {
		t.Errorf("empty csv = %q", body)
	}
	// Negotiation: text/* picks CSV, a lower q than JSON keeps JSON.
	res, _ = do(t, "GET", srv+"/api/users?limit=1", map[string]string{"Accept": "text/*"})
	if res.Header.Get("Content-Type") != csvType {
		t.Errorf("text/* = %q", res.Header.Get("Content-Type"))
	}
	res, _ = do(t, "GET", srv+"/api/users?limit=1", map[string]string{"Accept": "text/csv;q=0.5, application/json"})
	if !strings.HasPrefix(res.Header.Get("Content-Type"), "application/json") {
		t.Errorf("q-ordering = %q", res.Header.Get("Content-Type"))
	}
	// Errors stay problem+json even when only text/csv is acceptable.
	res, body = do(t, "GET", srv+"/api/users?select=emial", accept)
	wantProblem(t, res, body, 400, "unknown_column")
}

func TestCSVRPC(t *testing.T) {
	srv := testServer(t, nil).URL
	accept := map[string]string{"Accept": "text/csv"}
	res, body := do(t, "GET", srv+"/api/rpc/search_posts?term=e&select=id,title&order=id&limit=2", accept)
	if res.StatusCode != 200 || res.Header.Get("Content-Type") != csvType {
		t.Fatalf("rpc csv = %d %q %s", res.StatusCode, res.Header.Get("Content-Type"), body)
	}
	if rows := csvRows(t, body); strings.Join(rows[0], ",") != "id,title" || len(rows) < 2 {
		t.Errorf("rpc rows = %v", rows)
	}
	res, body = do(t, "GET", srv+"/api/rpc/search_posts?term=e&limit=1", accept)
	if rows := csvRows(t, body); strings.Join(rows[0], ",") != "id,author_id,title,body,published" {
		t.Errorf("rpc select=* header = %v", rows[0])
	}
	if res.Header.Get("Link") == "" {
		t.Errorf("rpc csv lost offset Link: %v", res.Header)
	}
	// SETOF scalar: a single "value" column.
	_, body = do(t, "GET", srv+"/api/rpc/mood_moods", accept)
	if string(body) != "value\r\nok\r\nhappy\r\n" {
		t.Errorf("scalar set csv = %q", body)
	}
	// Non-set RPC and POST: 406.
	res, body = do(t, "GET", srv+"/api/rpc/best_mood", accept)
	wantProblem(t, res, body, 406, "not_acceptable")
	res, body = send(t, "POST", srv+"/api/rpc/search_posts", `{"term":"e"}`, accept)
	wantProblem(t, res, body, 406, "not_acceptable")
}

func TestCSVRejections(t *testing.T) {
	srv := testServer(t, nil).URL
	accept := map[string]string{"Accept": "text/csv"}
	for name, r := range map[string]struct{ method, path, body string }{
		"embeds":     {"GET", "/api/users?select=id,posts(id)", ""},
		"single row": {"GET", "/api/users/1", ""},
		"by unique":  {"GET", "/api/users/by/email/ada%40example.com", ""},
		"mutation":   {"POST", "/api/users", `{"email":"csv@e2e"}`},
	} {
		res, body := send(t, r.method, srv+r.path, r.body, accept)
		if p := wantProblem(t, res, body, 406, "not_acceptable"); name == "embeds" && !strings.Contains(p.Detail, "embed") {
			t.Errorf("%s: detail %q", name, p.Detail)
		}
	}
	off := testServer(t, func(c *config.Config) { c.Server.CSV = false }).URL
	res, body := do(t, "GET", off+"/api/users?limit=1", accept)
	if p := wantProblem(t, res, body, 406, "not_acceptable"); !strings.Contains(p.Detail, "server.csv") {
		t.Errorf("server.csv off detail = %q", p.Detail)
	}
	if res, _ = do(t, "GET", off+"/api/users?limit=1", map[string]string{"Accept": "text/csv, */*;q=0.1"}); res.StatusCode != 200 || !strings.HasPrefix(res.Header.Get("Content-Type"), "application/json") {
		t.Errorf("csv off with */* fallback = %d %q", res.StatusCode, res.Header.Get("Content-Type"))
	}
	if res, _ = do(t, "GET", off+"/api/openapi.json", nil); res.StatusCode != http.StatusOK {
		t.Errorf("openapi with csv off = %d", res.StatusCode)
	}
}

// TestCSVRequestCLI: pdbr request --accept text/csv streams the same CSV.
func TestCSVRequestCLI(t *testing.T) {
	url := os.Getenv("PDBR_TEST_DATABASE_URL")
	if url == "" {
		t.Skip("PDBR_TEST_DATABASE_URL not set")
	}
	if _, err := exec.LookPath("go"); err != nil {
		t.Skip("go toolchain not on PATH")
	}
	cmd := exec.Command("go", "run", "./cmd/pdbr", "request", "GET", "/users?select=id,email&order=id&limit=1",
		"--accept", "text/csv", "-i", "--database.url", url)
	cmd.Dir = ".."
	var stderr bytes.Buffer
	cmd.Stderr = &stderr
	out, err := cmd.Output()
	if err != nil {
		t.Fatalf("pdbr request: %v\n%s", err, stderr.String())
	}
	if !strings.Contains(string(out), "Content-Type: "+csvType+"\n") || !strings.HasSuffix(string(out), "id,email\r\n1,ada@example.com\r\n") {
		t.Errorf("cli csv output = %q", out)
	}
	if !strings.Contains(stderr.String(), "HTTP/1.1 200 OK") {
		t.Errorf("status line missing on stderr: %q", stderr.String())
	}
}
