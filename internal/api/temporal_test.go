package api

import (
	"reflect"
	"strings"
	"testing"

	"github.com/suprbdev/pdbcore/introspect"
	"github.com/suprbdev/pdbcore/testutil"
)

func temporalCatalog() *introspect.Catalog {
	cat := testutil.FixtureCatalog()
	priv := introspect.Privileges{Select: true, Insert: true, Update: true, Delete: true}
	col := func(pos int, name, typ string) *introspect.Column {
		return &introspect.Column{Name: name, Position: pos, PGType: typ, TypeSchema: "pg_catalog", NotNull: true}
	}
	cat.Tables = append(cat.Tables,
		&introspect.Table{
			Schema: "public", Name: "tk_rooms", Kind: introspect.RelTable, Privileges: priv,
			Columns:    []*introspect.Column{col(1, "id", "int4"), col(2, "name", "text")},
			PrimaryKey: &introspect.Constraint{Name: "tk_rooms_pkey", Columns: []string{"id"}},
		},
		&introspect.Table{
			Schema: "public", Name: "tk_bookings", Kind: introspect.RelTable, Privileges: priv,
			Columns:    []*introspect.Column{col(1, "room_id", "int4"), col(2, "valid_at", "daterange"), col(3, "guest", "text")},
			PrimaryKey: &introspect.Constraint{Name: "tk_bookings_pkey", Columns: []string{"room_id", "valid_at"}, WithoutOverlaps: true},
		},
		&introspect.Table{
			Schema: "public", Name: "tk_rates", Kind: introspect.RelTable, Privileges: priv,
			Columns:    []*introspect.Column{col(1, "id", "int4"), col(2, "room_id", "int4"), col(3, "valid_at", "daterange")},
			PrimaryKey: &introspect.Constraint{Name: "tk_rates_pkey", Columns: []string{"id"}},
			Uniques:    []*introspect.Constraint{{Name: "tk_rates_room_id_valid_at_key", Columns: []string{"room_id", "valid_at"}, WithoutOverlaps: true}},
		},
		&introspect.Table{
			Schema: "public", Name: "tk_notes", Kind: introspect.RelTable, Privileges: priv,
			Columns:    []*introspect.Column{col(1, "id", "int4"), col(2, "room_id", "int4"), col(3, "valid_at", "daterange")},
			PrimaryKey: &introspect.Constraint{Name: "tk_notes_pkey", Columns: []string{"id"}},
			ForeignKeys: []*introspect.ForeignKey{{
				Name: "tk_notes_room_id_valid_at_fkey", Columns: []string{"room_id", "valid_at"},
				RefSchema: "public", RefTable: "tk_bookings", RefColumns: []string{"room_id", "valid_at"}, Period: true,
			}},
		},
		&introspect.Table{
			Schema: "public", Name: "tk_readings", Kind: introspect.RelTable, Privileges: priv,
			Columns:     []*introspect.Column{col(1, "id", "int4"), col(2, "room_id", "int4")},
			PrimaryKey:  &introspect.Constraint{Name: "tk_readings_pkey", Columns: []string{"id"}},
			ForeignKeys: []*introspect.ForeignKey{{Name: "tk_readings_room_id_fkey", Columns: []string{"room_id"}, RefSchema: "public", RefTable: "tk_rooms", RefColumns: []string{"id"}, NotEnforced: true}},
		},
	)
	return cat
}

func TestTemporalKeyRoutes(t *testing.T) {
	b := build(t, temporalCatalog(), defaultOptions())
	bookings, rates := b.ByPath["tk_bookings"], b.ByPath["tk_rates"]
	if bookings.PrimaryKey() != nil || bookings.Methods[MethodPut] || bookings.Etag || !bookings.Keyset || len(bookings.Uniques) != 0 {
		t.Errorf("tk_bookings: pk=%v put=%v etag=%v keyset=%v uniques=%v", bookings.PrimaryKey(), bookings.Methods[MethodPut], bookings.Etag, bookings.Keyset, bookings.Uniques)
	}
	if rates.PrimaryKey() == nil || !rates.Methods[MethodPut] || len(rates.Uniques) != 0 {
		t.Errorf("tk_rates: pk=%v put=%v uniques=%v", rates.PrimaryKey(), rates.Methods[MethodPut], rates.Uniques)
	}
	if rates.TemporalKeyFor([]string{"valid_at", "room_id"}) == nil || rates.TemporalKeyFor([]string{"id"}) != nil {
		t.Error("TemporalKeyFor")
	}
	var patterns []string
	for _, rt := range b.Routes {
		if rt.Resource == bookings || rt.Resource == rates {
			patterns = append(patterns, rt.Pattern)
		}
	}
	if want := []string{"/tk_bookings", "/tk_rates", "/tk_rates/{id}"}; !reflect.DeepEqual(patterns, want) {
		t.Errorf("routes = %v, want %v", patterns, want)
	}
	for path := range b.OpenAPI.Paths {
		if strings.HasPrefix(path, "/tk_bookings/") || strings.HasPrefix(path, "/tk_rates/by/") {
			t.Errorf("OpenAPI path for a temporal key: %s", path)
		}
	}
	if b.OpenAPI.Paths["/tk_bookings"].Put != nil {
		t.Error("PUT documented on a resource whose only key is temporal")
	}
}

func TestTemporalRelations(t *testing.T) {
	b := build(t, temporalCatalog(), defaultOptions())
	fwd := b.ByPath["tk_notes"].Relations["tk_bookings"][0]
	back := b.ByPath["tk_bookings"].Relations["tk_notes"][0]
	plain := b.ByPath["tk_readings"].Relations["tk_rooms"][0]
	if !fwd.Forward || !fwd.ToMany() || !back.ToMany() || plain.ToMany() {
		t.Errorf("ToMany: forward period %v, backward period %v, plain forward %v", fwd.ToMany(), back.ToMany(), plain.ToMany())
	}
	if got := fwd.EqualityColumns(); !reflect.DeepEqual(got, []string{"room_id"}) {
		t.Errorf("EqualityColumns = %v", got)
	}

	s := b.OpenAPI.Components.Schemas
	emb := s["tk_notes"].Properties["tk_bookings"]
	if emb.Type != "array" || emb.Items.Ref != "#/components/schemas/tk_bookings" || !strings.Contains(emb.Description, "PERIOD") {
		t.Errorf("PERIOD forward embed = %+v", emb)
	}
	if emb := s["tk_bookings"].Properties["tk_notes"]; emb.Type != "array" {
		t.Errorf("PERIOD backward embed = %+v", emb)
	}
	emb = s["tk_readings"].Properties["tk_rooms"]
	if len(emb.AnyOf) != 2 || emb.AnyOf[0].Ref != "#/components/schemas/tk_rooms" || emb.AnyOf[1].Type != "null" || !strings.Contains(emb.Description, "NOT ENFORCED") {
		t.Errorf("NOT ENFORCED forward embed = %+v", emb)
	}

	cat := temporalCatalog()
	fk := cat.Table("public", "tk_readings").ForeignKeys[0]
	fk.NotEnforced, fk.NotValid = false, true
	emb = build(t, cat, defaultOptions()).OpenAPI.Components.Schemas["tk_readings"].Properties["tk_rooms"]
	if len(emb.AnyOf) != 2 || !strings.Contains(emb.Description, "NOT VALID foreign key: the referenced row may be missing") {
		t.Errorf("NOT VALID forward embed = %+v", emb)
	}
}

func TestTemporalNestedInputs(t *testing.T) {
	b := New(temporalCatalog(), nil, defaultOptions())
	b.Surface.NestedKeys = func(r *Resource) map[string]*Relation {
		out := map[string]*Relation{}
		for key, cands := range r.Relations {
			out[key] = cands[0]
		}
		return out
	}
	b.Surface.NestedSchema = func(*Resource, *Relation, Method) *Schema { return &Schema{Type: "object"} }
	built, err := b.Build()
	if err != nil {
		t.Fatal(err)
	}
	notes := built.OpenAPI.Components.Schemas["tk_notes.insert"]
	if !reflect.DeepEqual(notes.Required, []string{"id", "valid_at"}) {
		t.Errorf("tk_notes.insert required = %v, want the period column kept", notes.Required)
	}
	if notes.Properties["tk_bookings"] == nil {
		t.Error("nested property missing")
	}
}
