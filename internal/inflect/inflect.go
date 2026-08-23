// Package inflect declares pdbr's naming kinds and its default inflector:
// raw PostgreSQL identifiers everywhere (PostgREST-style), computed-column
// keys minus the `<table>_` prefix, relation keys equal to the referenced /
// referencing table name. The chain types and string helpers live in
// pdbcore/inflect so InflectionHook plugins compile against pdbcore alone.
package inflect

import (
	"strings"

	"github.com/suprbdev/pdbcore/inflect"
)

// Kind values pdbr asks the naming chain for.
const (
	// KindResourcePath names the path segment of a table/view (Schema, Table).
	KindResourcePath inflect.Kind = "resource_path"
	// KindFieldName names a column's JSON key (Schema, Table, Column).
	KindFieldName inflect.Kind = "field_name"
	// KindRelationForward names the embed key on the referencing side; Input
	// carries the referenced table (Schema, Table), Constraint and Columns.
	KindRelationForward inflect.Kind = "relation_forward"
	// KindRelationBackward names the embed key on the referenced side; Input
	// carries the referencing table (Schema, Table), Constraint and Columns.
	KindRelationBackward inflect.Kind = "relation_backward"
	// KindRPCName names the /rpc/{fn} segment (Schema, Function).
	KindRPCName inflect.Kind = "rpc_name"
	// KindComputedField names a computed column (Schema, Table, Function).
	KindComputedField inflect.Kind = "computed_field"
	// KindEnumValue names an enum label as exposed to clients (Enum, Value).
	KindEnumValue inflect.Kind = "enum_value"
)

// Kinds lists every kind in a stable order (for documentation and tests).
var Kinds = []inflect.Kind{
	KindResourcePath, KindFieldName, KindRelationForward, KindRelationBackward,
	KindRPCName, KindComputedField, KindEnumValue,
}

// Default is pdbr's innermost naming function: identity on pg identifiers,
// with the computed-column prefix rule.
func Default(kind inflect.Kind, in inflect.Input) string {
	switch kind {
	case KindResourcePath, KindRelationForward, KindRelationBackward:
		return in.Table
	case KindFieldName:
		return in.Column
	case KindRPCName:
		return in.Function
	case KindComputedField:
		return ComputedKey(in.Table, in.Function)
	case KindEnumValue:
		return in.Value
	}
	return ""
}

// ComputedKey strips the `<table>_` prefix from a computed-column function
// name (users_post_count -> post_count); a function without the prefix keeps
// its full name.
func ComputedKey(table, function string) string {
	if k, ok := strings.CutPrefix(function, table+"_"); ok && k != "" {
		return k
	}
	return function
}
