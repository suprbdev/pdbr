package parse

import (
	"testing"
)

func TestRangeHeader(t *testing.T) {
	b := buildFixture(t, true)
	after := cursor(7)
	type want struct {
		limit, offset  int
		set, fromRange bool
		code           string
	}
	cases := []struct {
		name   string
		method string
		url    string
		hdr    map[string]string
		want   want
	}{
		{"closed", "GET", "/api/posts", map[string]string{"Range": "0-24"}, want{25, 0, true, true, ""}},
		{"closed with unit", "GET", "/api/posts", map[string]string{"Range": "10-19", "Range-Unit": "items"}, want{10, 10, true, true, ""}},
		{"unit case-insensitive", "GET", "/api/posts", map[string]string{"Range": "1-1", "Range-Unit": "Items"}, want{1, 1, true, true, ""}},
		{"open ended", "GET", "/api/posts", map[string]string{"Range": "10-"}, want{100, 10, false, true, ""}},
		{"clamped to cap", "GET", "/api/posts", map[string]string{"Range": "0-499"}, want{100, 0, true, true, ""}},
		{"HEAD", "HEAD", "/api/posts", map[string]string{"Range": "5-9"}, want{5, 5, true, true, ""}},
		{"limit narrows", "GET", "/api/posts?limit=5", map[string]string{"Range": "0-24"}, want{5, 0, true, true, ""}},
		{"range narrows limit", "GET", "/api/posts?limit=50", map[string]string{"Range": "0-9"}, want{10, 0, true, true, ""}},
		{"offset shifts range", "GET", "/api/posts?offset=5&limit=10", map[string]string{"Range": "0-24"}, want{10, 5, true, true, ""}},
		{"offset with open range", "GET", "/api/posts?offset=30", map[string]string{"Range": "10-"}, want{100, 30, false, true, ""}},
		{"no overlap", "GET", "/api/posts?offset=50", map[string]string{"Range": "0-9"}, want{code: CodeInvalidRange}},
		{"last before first", "GET", "/api/posts", map[string]string{"Range": "5-2"}, want{code: CodeInvalidRange}},
		{"with after", "GET", "/api/posts?after=" + after, map[string]string{"Range": "0-9"}, want{code: CodeKeysetUnsupported}},
		{"malformed ignored", "GET", "/api/posts", map[string]string{"Range": "items=0-9"}, want{100, 0, false, false, ""}},
		{"negative ignored", "GET", "/api/posts", map[string]string{"Range": "-5"}, want{100, 0, false, false, ""}},
		{"other unit ignored", "GET", "/api/posts", map[string]string{"Range": "0-9", "Range-Unit": "bytes"}, want{100, 0, false, false, ""}},
		{"ignored on single row", "GET", "/api/posts/1", map[string]string{"Range": "0-9"}, want{}},
		{"ignored on bulk delete", "DELETE", "/api/posts?id=eq.1", map[string]string{"Range": "0-9"}, want{100, 0, false, false, ""}},
		{"rpc setof table", "GET", "/api/rpc/search_posts?term=x", map[string]string{"Range": "2-4"}, want{3, 2, true, true, ""}},
	}
	for _, tc := range cases {
		p, err := doParse(t, b, req{method: tc.method, url: tc.url, header: tc.hdr}, allOn)
		if tc.want.code != "" {
			wantCode(t, err, tc.want.code)
			continue
		}
		if err != nil {
			t.Errorf("%s: %v", tc.name, err)
			continue
		}
		if tc.name == "ignored on single row" {
			if p.Page.FromRange {
				t.Errorf("%s: range applied", tc.name)
			}
			continue
		}
		pg := p.Page
		if pg.Limit != tc.want.limit || pg.Offset != tc.want.offset || pg.LimitSet != tc.want.set || pg.FromRange != tc.want.fromRange {
			t.Errorf("%s: page = %+v, want %+v", tc.name, pg, tc.want)
		}
	}
}
