package e2e

import (
	"encoding/json"
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/suprbdev/pdbr/internal/config"
)

func sendJSONWith(t *testing.T, method, url, body string, hdr map[string]string, status int, v any) {
	t.Helper()
	res, out := send(t, method, url, body, hdr)
	if res.StatusCode != status {
		t.Fatalf("%s %s = %d %s, want %d", method, url, res.StatusCode, out, status)
	}
	if err := json.Unmarshal(out, v); err != nil {
		t.Fatalf("%s %s: bad JSON %s: %v", method, url, out, err)
	}
}

func countByPrefix(t *testing.T, base, prefix string) int {
	t.Helper()
	var rows []row
	getJSON(t, base+"/api/users?"+byPrefix(prefix)+"&select=id", &rows)
	return len(rows)
}

func TestPreferTxOverride(t *testing.T) {
	sweep(t)
	srv := testServer(t, func(c *config.Config) { c.TX.End = "commit-allow-override" })
	prefix := fmt.Sprintf("m3-tx-%d", time.Now().UnixNano())
	hdr := map[string]string{"Prefer": "tx=rollback"}

	res, body := send(t, "POST", srv.URL+"/api/users", fmt.Sprintf(`{"email":"%s-a@example.com"}`, prefix), hdr)
	if res.StatusCode != 201 || !strings.Contains(string(body), prefix+"-a@example.com") || res.Header.Get("Preference-Applied") != "tx=rollback" {
		t.Fatalf("rolled-back POST = %d %s %v", res.StatusCode, body, res.Header)
	}
	if n := countByPrefix(t, srv.URL, prefix); n != 0 {
		t.Fatalf("tx=rollback left %d rows", n)
	}

	res, _ = send(t, "POST", srv.URL+"/api/users", fmt.Sprintf(`{"email":"%s-b@example.com"}`, prefix), map[string]string{"Prefer": "tx=commit"})
	if res.StatusCode != 201 || res.Header.Get("Preference-Applied") != "tx=commit" || countByPrefix(t, srv.URL, prefix) != 1 {
		t.Fatalf("tx=commit POST = %d %v", res.StatusCode, res.Header)
	}

	var rows []row
	sendJSONWith(t, "PATCH", srv.URL+"/api/users?"+byPrefix(prefix)+"&select=full_name", `{"full_name":"gone"}`, hdr, 200, &rows)
	if len(rows) != 1 || rows[0]["full_name"] != "gone" {
		t.Fatalf("rolled-back PATCH response = %v", rows)
	}
	rows = nil
	getJSON(t, srv.URL+"/api/users?"+byPrefix(prefix)+"&select=full_name", &rows)
	if len(rows) != 1 || rows[0]["full_name"] != nil {
		t.Fatalf("rolled-back PATCH persisted: %v", rows)
	}

	send(t, "DELETE", srv.URL+"/api/users?"+byPrefix(prefix), "", hdr)
	if n := countByPrefix(t, srv.URL, prefix); n != 1 {
		t.Fatalf("rolled-back DELETE removed rows: %d", n)
	}
}

func TestPreferTxDisallowed(t *testing.T) {
	sweep(t)
	srv := testServer(t, nil)
	prefix := fmt.Sprintf("m3-txd-%d", time.Now().UnixNano())
	body := fmt.Sprintf(`{"email":"%s-0@example.com"}`, prefix)

	res, _ := send(t, "POST", srv.URL+"/api/users", body, map[string]string{"Prefer": "tx=rollback"})
	if res.StatusCode != 201 || res.Header.Get("Preference-Applied") != "" || countByPrefix(t, srv.URL, prefix) != 1 {
		t.Fatalf("tx=rollback must be ignored under tx_end=commit: %d %v", res.StatusCode, res.Header)
	}
	res, out := send(t, "POST", srv.URL+"/api/users", fmt.Sprintf(`{"email":"%s-2@example.com"}`, prefix), map[string]string{"Prefer": "handling=strict, tx=rollback"})
	wantProblem(t, res, out, 400, "invalid_preference")
	if n := countByPrefix(t, srv.URL, prefix); n != 1 {
		t.Fatalf("rejected request wrote rows: %d", n)
	}
}

func TestTxEndRollbackConfig(t *testing.T) {
	sweep(t)
	srv := testServer(t, func(c *config.Config) { c.TX.End = "rollback-allow-override" })
	prefix := fmt.Sprintf("m3-txr-%d", time.Now().UnixNano())

	res, _ := send(t, "POST", srv.URL+"/api/users", fmt.Sprintf(`{"email":"%s-a@example.com"}`, prefix), nil)
	if res.StatusCode != 201 || countByPrefix(t, srv.URL, prefix) != 0 {
		t.Fatalf("default rollback persisted rows: %d", countByPrefix(t, srv.URL, prefix))
	}
	send(t, "POST", srv.URL+"/api/users", fmt.Sprintf(`{"email":"%s-b@example.com"}`, prefix), map[string]string{"Prefer": "tx=commit"})
	if n := countByPrefix(t, srv.URL, prefix); n != 1 {
		t.Fatalf("tx=commit override: %d rows", n)
	}

	fixed := testServer(t, func(c *config.Config) { c.TX.End = "rollback" })
	send(t, "POST", fixed.URL+"/api/users", fmt.Sprintf(`{"email":"%s-c@example.com"}`, prefix), map[string]string{"Prefer": "tx=commit"})
	if n := countByPrefix(t, fixed.URL, prefix); n != 1 {
		t.Fatalf("tx_end=rollback must ignore tx=commit: %d rows", n)
	}
}

func TestPreferTimezone(t *testing.T) {
	sweep(t)
	srv := testServer(t, nil)
	prefix := fmt.Sprintf("m3-tz-%d", time.Now().UnixNano())
	sendJSON(t, "POST", srv.URL+"/api/users", fmt.Sprintf(`{"email":"%s-0@example.com"}`, prefix), 201, nil)
	get := func(prefer string) ([]row, string) {
		res, body := send(t, "GET", srv.URL+"/api/users?"+byPrefix(prefix)+"&select=created_at", "", map[string]string{"Prefer": prefer})
		if res.StatusCode != 200 {
			t.Fatalf("GET = %d %s", res.StatusCode, body)
		}
		var rows []row
		if err := json.Unmarshal(body, &rows); err != nil || len(rows) != 1 {
			t.Fatalf("rows = %s %v", body, err)
		}
		return rows, res.Header.Get("Preference-Applied")
	}
	tokyo, applied := get("timezone=Asia/Tokyo")
	if applied != "timezone=Asia/Tokyo" || !strings.HasSuffix(tokyo[0]["created_at"].(string), "+09:00") {
		t.Fatalf("tokyo = %v applied %q", tokyo, applied)
	}
	utc, _ := get("timezone=utc")
	if s := utc[0]["created_at"].(string); !strings.HasSuffix(s, "+00:00") && !strings.HasSuffix(s, "Z") {
		t.Fatalf("utc = %v", utc)
	}
	_, applied = get("timezone=Mars/Base")
	if applied != "" {
		t.Fatalf("unknown zone applied: %q", applied)
	}
	res, body := send(t, "GET", srv.URL+"/api/users?"+byPrefix(prefix), "", map[string]string{"Prefer": "handling=strict, timezone=Mars/Base"})
	wantProblem(t, res, body, 400, "invalid_preference")

	var rows []row
	sendJSONWith(t, "PATCH", srv.URL+"/api/users?"+byPrefix(prefix)+"&select=created_at", `{"full_name":"tz"}`, map[string]string{"Prefer": "timezone=Pacific/Auckland"}, 200, &rows)
	if s, _ := rows[0]["created_at"].(string); !strings.HasSuffix(s, "+12:00") && !strings.HasSuffix(s, "+13:00") {
		t.Fatalf("PATCH under timezone: %v", rows)
	}
}

func TestColumnsParam(t *testing.T) {
	sweep(t)
	srv := testServer(t, nil)
	prefix := fmt.Sprintf("m3-col-%d", time.Now().UnixNano())

	body := fmt.Sprintf(`[{"email":"%[1]s-0@example.com","full_name":"keep","mood":"sad","balance":99},{"email":"%[1]s-1@example.com","mood":"ok"}]`, prefix)
	var rows []row
	sendJSON(t, "POST", srv.URL+"/api/users?columns=email,full_name&select=email,full_name,mood,balance", body, 201, &rows)
	if len(rows) != 2 || rows[0]["full_name"] != "keep" || rows[0]["mood"] != nil || fmt.Sprint(rows[0]["balance"]) != "0" ||
		rows[1]["full_name"] != nil || rows[1]["mood"] != nil {
		t.Fatalf("columns= bulk insert = %v", rows)
	}

	res, out := send(t, "POST", srv.URL+"/api/users?columns=email,nope", fmt.Sprintf(`{"email":"%s-x@example.com"}`, prefix), nil)
	wantProblem(t, res, out, 400, "invalid_body")
	res, out = send(t, "POST", srv.URL+"/api/users?columns=id,email", fmt.Sprintf(`{"email":"%s-y@example.com"}`, prefix), nil)
	wantProblem(t, res, out, 400, "column_readonly")

	rows = nil
	sendJSON(t, "PATCH", srv.URL+"/api/users?"+byPrefix(prefix)+"&columns=full_name&select=full_name,mood", `{"full_name":"patched","mood":"happy"}`, 200, &rows)
	if len(rows) != 2 || rows[0]["full_name"] != "patched" || rows[0]["mood"] != nil {
		t.Fatalf("columns= PATCH = %v", rows)
	}

	var put row
	sendJSON(t, "PUT", srv.URL+"/api/users?columns=email,full_name&on_conflict=email&select=email,full_name,mood", fmt.Sprintf(`{"email":"%s","full_name":"put","mood":"happy"}`, prefix+"-0@example.com"), 200, &put)
	if put["full_name"] != "put" || put["mood"] != nil {
		t.Fatalf("columns= PUT = %v", put)
	}
}
