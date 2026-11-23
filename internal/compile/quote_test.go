package compile

import "testing"

func TestQuoteLiteral(t *testing.T) {
	for in, want := range map[string]string{
		"":          "''",
		"a":         "'a'",
		"a'b":       "'a''b'",
		`a\`:        `E'a\\'`,
		`a'b\`:      `E'a''b\\'`,
		`\' OR 1=1`: `E'\\'' OR 1=1'`,
	} {
		if got := quoteLiteral(in); got != want {
			t.Errorf("quoteLiteral(%q) = %s, want %s", in, got, want)
		}
	}
}
