package e2e

import (
	"encoding/json"
	"strconv"
	"strings"
	"testing"
)

func TestRangeHeaderPaging(t *testing.T) {
	sweep(t)
	srv := testServer(t, nil).URL
	sendJSON(t, "POST", srv+"/api/posts", `[{"author_id":1,"title":"ZZR a"},{"author_id":1,"title":"ZZR b"},{"author_id":1,"title":"ZZR c"},{"author_id":1,"title":"ZZR d"},{"author_id":1,"title":"ZZR e"}]`, 201, nil)
	total := dbInt(t, `SELECT count(*) FROM posts`)
	n := strconv.FormatInt(total, 10)
	exact := map[string]string{"Prefer": "count=exact", "Range-Unit": "items"}
	with := func(rng string, extra map[string]string) map[string]string {
		h := map[string]string{"Range": rng}
		for k, v := range extra {
			h[k] = v
		}
		return h
	}

	type want struct {
		status int
		cr     string
		rows   int
	}
	cases := []struct {
		name   string
		method string
		url    string
		hdr    map[string]string
		want   want
	}{
		{"closed, no count", "GET", "/api/posts?order=id", with("0-1", nil), want{200, "0-1/*", 2}},
		{"closed, counted partial", "GET", "/api/posts?order=id", with("0-1", exact), want{206, "0-1/" + n, 2}},
		{"open ended", "GET", "/api/posts?order=id", with("2-", exact), want{206, "2-" + strconv.FormatInt(total-1, 10) + "/" + n, int(total) - 2}},
		{"whole result", "GET", "/api/posts?order=id", with("0-"+strconv.FormatInt(total+10, 10), exact), want{200, "0-" + strconv.FormatInt(total-1, 10) + "/" + n, int(total)}},
		{"past total", "GET", "/api/posts?order=id", with(strconv.FormatInt(total+5, 10)+"-", exact), want{416, "*/" + n, 0}},
		{"at total", "GET", "/api/posts?order=id", with(n+"-", exact), want{206, "*/" + n, 0}},
		{"past total, no count", "GET", "/api/posts?order=id", with(strconv.FormatInt(total+5, 10)+"-", nil), want{200, "*/*", 0}},
		{"intersects limit", "GET", "/api/posts?order=id&limit=1", with("0-3", exact), want{206, "0-0/" + n, 1}},
		{"count=true param", "GET", "/api/posts?order=id&count=true", with("1-2", nil), want{206, "1-2/" + n, 2}},
		{"head", "HEAD", "/api/posts?order=id", with("0-1", exact), want{206, "0-1/" + n, 0}},
		{"rpc", "GET", "/api/rpc/search_posts?term=ZZR&order=id", with("1-2", exact), want{206, "1-2/5", 2}},
		{"limit/offset partial", "GET", "/api/posts?order=id&limit=2&offset=1", exact, want{206, "1-2/" + n, 2}},
		{"limit/offset past total", "GET", "/api/posts?order=id&offset=" + strconv.FormatInt(total+1, 10), exact, want{416, "*/" + n, 0}},
		{"unit bytes ignored", "GET", "/api/posts?order=id&limit=1", with("3-4", map[string]string{"Range-Unit": "bytes"}), want{200, "0-0/*", 1}},
	}
	for _, tc := range cases {
		res, body := send(t, tc.method, srv+tc.url, "", tc.hdr)
		if res.StatusCode != tc.want.status || res.Header.Get("Content-Range") != tc.want.cr {
			t.Errorf("%s: %d %q, want %d %q (%s)", tc.name, res.StatusCode, res.Header.Get("Content-Range"), tc.want.status, tc.want.cr, body)
			continue
		}
		if tc.method == "GET" && tc.want.status != 416 {
			var rows []row
			if err := json.Unmarshal(body, &rows); err != nil || len(rows) != tc.want.rows {
				t.Errorf("%s: %d rows (%v), want %d: %s", tc.name, len(rows), err, tc.want.rows, body)
			}
		}
	}

	res, body := send(t, "GET", srv+"/api/posts?order=id", "", with("5-2", nil))
	if res.StatusCode != 400 || !strings.Contains(string(body), "invalid_range") {
		t.Errorf("inverted range = %d %s", res.StatusCode, body)
	}
	res, body = send(t, "GET", srv+"/api/posts?order=id", "", with(strconv.FormatInt(total+5, 10)+"-", exact))
	if res.StatusCode != 416 || !strings.Contains(string(body), "range_not_satisfiable") {
		t.Errorf("416 body = %d %s", res.StatusCode, body)
	}

	var page []row
	res = getJSON(t, srv+"/api/posts?order=id&limit=1", &page)
	cursor := res.Header.Get("X-Next-Cursor")
	res, body = send(t, "GET", srv+"/api/posts?order=id&after="+cursor, "", with("0-1", nil))
	if res.StatusCode != 400 || !strings.Contains(string(body), "keyset_unsupported") {
		t.Errorf("range with after = %d %s", res.StatusCode, body)
	}

	res, body = send(t, "GET", srv+"/api/posts?order=id", "", with("0-1", map[string]string{"Prefer": "count=exact", "Accept": "text/csv"}))
	if res.StatusCode != 206 || res.Header.Get("Content-Range") != "0-1/"+n {
		t.Errorf("csv range = %d %v %s", res.StatusCode, res.Header, body)
	}
	if got := len(csvRows(t, body)); got != 3 {
		t.Errorf("csv rows = %d, want header + 2", got)
	}

	res, body = send(t, "GET", srv+"/api/rpc/search_posts?term=ZZR&order=id", "", with("1-2", nil))
	if link := res.Header.Get("Link"); link != `</api/rpc/search_posts?term=ZZR&order=id&offset=3&limit=2>; rel="next", </api/rpc/search_posts?term=ZZR&order=id&offset=0&limit=2>; rel="prev"` {
		t.Errorf("Link = %q (%s)", link, body)
	}

	res, body = send(t, "DELETE", srv+"/api/posts?title=like.ZZR%20*", "", with("0-1", map[string]string{"Prefer": "count=exact"}))
	if res.StatusCode != 200 || res.Header.Get("Content-Range") != "0-4/5" {
		t.Errorf("bulk DELETE ignores Range: %d %v %s", res.StatusCode, res.Header, body)
	}
}
