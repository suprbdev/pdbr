package inflect

import (
	"testing"

	"github.com/suprbdev/pdbcore/inflect"
)

func TestDefault(t *testing.T) {
	cases := []struct {
		kind inflect.Kind
		in   inflect.Input
		want string
	}{
		{KindResourcePath, inflect.Input{Schema: "public", Table: "user_accounts"}, "user_accounts"},
		{KindFieldName, inflect.Input{Schema: "public", Table: "users", Column: "created_at"}, "created_at"},
		{KindRelationForward, inflect.Input{Schema: "public", Table: "users", Constraint: "posts_author_id_fkey", Columns: []string{"author_id"}}, "users"},
		{KindRelationBackward, inflect.Input{Schema: "public", Table: "posts", Constraint: "posts_author_id_fkey", Columns: []string{"author_id"}}, "posts"},
		{KindRPCName, inflect.Input{Schema: "public", Function: "search_posts"}, "search_posts"},
		{KindComputedField, inflect.Input{Schema: "public", Table: "users", Function: "users_post_count"}, "post_count"},
		{KindComputedField, inflect.Input{Schema: "public", Table: "users", Function: "post_count"}, "post_count"},
		{KindComputedField, inflect.Input{Schema: "public", Table: "users", Function: "users_"}, "users_"},
		{KindEnumValue, inflect.Input{Enum: "mood", Value: "Happy Day"}, "Happy Day"},
		{inflect.Kind("unknown"), inflect.Input{Table: "x"}, ""},
	}
	for _, tc := range cases {
		if got := Default(tc.kind, tc.in); got != tc.want {
			t.Errorf("Default(%s, %+v) = %q, want %q", tc.kind, tc.in, got, tc.want)
		}
	}
}

func TestKindsAreDistinct(t *testing.T) {
	seen := map[inflect.Kind]bool{}
	for _, k := range Kinds {
		if seen[k] {
			t.Errorf("duplicate kind %q", k)
		}
		seen[k] = true
	}
	if len(Kinds) != 7 {
		t.Errorf("expected 7 kinds, got %d", len(Kinds))
	}
}
