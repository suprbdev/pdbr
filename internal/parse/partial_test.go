package parse

import (
	"reflect"
	"testing"
)

func TestPreferPartial(t *testing.T) {
	b := buildFixture(t, true)
	on := allOn
	on.PartialBulk = true
	arr := `[{"email":"a@b.c"},{"email":"d@e.f"}]`
	do := func(method, url, prefer, body string, opts Options) (applied []string, partial bool, err error) {
		p, err := doParse(t, b, req{method: method, url: url, header: withHeader(jsonCT, "Prefer", prefer), body: body}, opts)
		if err != nil {
			return nil, false, err
		}
		return p.Prefer.Applied, p.Prefer.Partial, nil
	}
	for _, tc := range []struct {
		name         string
		method, url  string
		prefer, body string
		opts         Options
		partial      bool
		applied      []string
	}{
		{"bulk POST", "POST", "/api/users", "partial=items", arr, on, true, []string{"partial=items"}},
		{"bulk PUT", "PUT", "/api/users?on_conflict=email", "partial=items", arr, on, true, []string{"partial=items"}},
		{"echo order follows the header", "POST", "/api/users", "return=minimal, partial=items", arr, on, true, []string{"return=minimal", "partial=items"}},
		{"token is case-insensitive", "POST", "/api/users", "Partial=Items", arr, on, true, []string{"partial=items"}},
		{"single object is not bulk", "POST", "/api/users", "partial=items", `{"email":"a@b.c"}`, on, false, nil},
		{"config off", "POST", "/api/users", "partial=items", arr, allOn, false, nil},
		{"unknown value is ignored", "POST", "/api/users", "partial=all", arr, on, false, nil},
		{"absent keeps all or nothing", "POST", "/api/users", "return=minimal", arr, on, false, []string{"return=minimal"}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			applied, partial, err := do(tc.method, tc.url, tc.prefer, tc.body, tc.opts)
			if err != nil {
				t.Fatal(err)
			}
			if partial != tc.partial || !reflect.DeepEqual(applied, tc.applied) {
				t.Fatalf("partial = %v, applied = %v; want %v, %v", partial, applied, tc.partial, tc.applied)
			}
		})
	}
	t.Run("strict rejects the token while the config is off", func(t *testing.T) {
		_, _, err := do("POST", "/api/users", "handling=strict, partial=items", arr, allOn)
		wantCode(t, err, CodeInvalidPreference)
	})
	t.Run("element cap", func(t *testing.T) {
		capped := on
		capped.PartialBulkMaxItems = 2
		if _, partial, err := do("POST", "/api/users", "partial=items", arr, capped); err != nil || !partial {
			t.Fatalf("at the cap: partial = %v, err = %v", partial, err)
		}
		_, _, err := do("POST", "/api/users", "partial=items", `[{"email":"a@b.c"},{"email":"d@e.f"},{"email":"g@h.i"}]`, capped)
		wantCode(t, err, CodeBodyTooLarge)
		if _, partial, err := do("POST", "/api/users", "return=minimal", `[{"email":"a@b.c"},{"email":"d@e.f"},{"email":"g@h.i"}]`, capped); err != nil || partial {
			t.Fatalf("without the token the cap does not apply: partial = %v, err = %v", partial, err)
		}
	})
	t.Run("not applicable to PATCH", func(t *testing.T) {
		p, err := doParse(t, b, req{method: "PATCH", url: "/api/users?mood=eq.sad", header: withHeader(jsonCT, "Prefer", "partial=items"), body: `{"full_name":"x"}`}, on)
		if err != nil || p.Prefer.Partial || len(p.Prefer.Applied) != 0 {
			t.Fatalf("PATCH = %+v %v", p.Prefer, err)
		}
	})
}
