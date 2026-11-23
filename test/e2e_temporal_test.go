package e2e

import (
	"context"
	"os"
	"reflect"
	"strings"
	"testing"

	"github.com/jackc/pgx/v5"
)

// temporalFixture creates the PostgreSQL 18 key variants (tk_ prefix, the
// shapes the tk_ goldens compile against) and drops them after the test:
// tk_bookings has only a temporal primary key, tk_rates a plain primary key
// plus a temporal unique, tk_notes a PERIOD foreign key to tk_bookings,
// tk_readings a NOT ENFORCED foreign key to tk_rooms. It skips below
// PostgreSQL 18. Note 1 spans both room-1 bookings, note 2 only the first;
// reading 2 references a room that does not exist.
func temporalFixture(t *testing.T) {
	t.Helper()
	url := os.Getenv("PDBR_TEST_DATABASE_URL")
	if url == "" {
		t.Skip("PDBR_TEST_DATABASE_URL not set; run `make test-e2e`")
	}
	ctx := context.Background()
	run := func(ddl ...string) error {
		conn, err := pgx.Connect(ctx, url)
		if err != nil {
			return err
		}
		defer conn.Close(ctx)
		for _, d := range ddl {
			if _, err := conn.Exec(ctx, d); err != nil {
				return err
			}
		}
		return nil
	}
	conn, err := pgx.Connect(ctx, url)
	if err != nil {
		t.Fatal(err)
	}
	var num int
	err = conn.QueryRow(ctx, `SELECT current_setting('server_version_num')::int`).Scan(&num)
	conn.Close(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if num < 180000 {
		t.Skipf("temporal keys need PostgreSQL 18+ (server_version_num %d)", num)
	}
	drop := []string{`DROP TABLE IF EXISTS public.tk_notes, public.tk_readings, public.tk_rates, public.tk_bookings, public.tk_rooms`}
	if err := run(drop...); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if err := run(drop...); err != nil {
			t.Errorf("temporal fixture cleanup: %v", err)
		}
	})
	err = run(
		`CREATE EXTENSION IF NOT EXISTS btree_gist`,
		`CREATE TABLE public.tk_rooms (id int PRIMARY KEY, name text NOT NULL)`,
		`CREATE TABLE public.tk_bookings (room_id int NOT NULL REFERENCES public.tk_rooms (id), valid_at daterange NOT NULL,
		    guest text NOT NULL, PRIMARY KEY (room_id, valid_at WITHOUT OVERLAPS))`,
		`CREATE INDEX tk_bookings_guest_idx ON public.tk_bookings (guest)`,
		`CREATE TABLE public.tk_rates (id int PRIMARY KEY, room_id int NOT NULL, valid_at daterange NOT NULL, price numeric NOT NULL,
		    UNIQUE (room_id, valid_at WITHOUT OVERLAPS))`,
		`CREATE TABLE public.tk_notes (id int GENERATED ALWAYS AS IDENTITY PRIMARY KEY, room_id int NOT NULL, valid_at daterange NOT NULL,
		    note text NOT NULL, FOREIGN KEY (room_id, PERIOD valid_at) REFERENCES public.tk_bookings (room_id, PERIOD valid_at))`,
		`CREATE TABLE public.tk_readings (id int PRIMARY KEY, room_id int NOT NULL, celsius numeric NOT NULL,
		    FOREIGN KEY (room_id) REFERENCES public.tk_rooms (id) NOT ENFORCED)`,
		`INSERT INTO public.tk_rooms VALUES (1, 'blue'), (2, 'red')`,
		`INSERT INTO public.tk_bookings VALUES (1, '[2026-01-01,2026-01-05)', 'ada'), (1, '[2026-01-05,2026-01-10)', 'bo'),
		    (2, '[2026-01-01,2026-01-03)', 'cy')`,
		`INSERT INTO public.tk_notes (room_id, valid_at, note) VALUES (1, '[2026-01-03,2026-01-07)', 'spans both'),
		    (1, '[2026-01-01,2026-01-02)', 'ada only')`,
		`INSERT INTO public.tk_readings VALUES (1, 1, 20.5), (2, 99, 18)`,
		`INSERT INTO public.tk_rates VALUES (1, 1, '[2026-01-01,2026-02-01)', 100)`,
	)
	if err != nil {
		t.Fatalf("temporal fixture: %v", err)
	}
}

func guests(v any) []string {
	var out []string
	for _, b := range v.([]any) {
		out = append(out, b.(map[string]any)["guest"].(string))
	}
	return out
}

func TestTemporalEmbeds(t *testing.T) {
	temporalFixture(t)
	srv := testServer(t, nil).URL

	var rows []row
	getJSON(t, srv+"/api/tk_notes?select=id,note,tk_bookings(guest)&order=id.asc", &rows)
	if len(rows) != 2 || !reflect.DeepEqual(guests(rows[0]["tk_bookings"]), []string{"ada", "bo"}) || !reflect.DeepEqual(guests(rows[1]["tk_bookings"]), []string{"ada"}) {
		t.Fatalf("PERIOD forward embed = %v", rows)
	}

	getJSON(t, srv+"/api/tk_bookings?select=guest,tk_notes(note)", &rows)
	want := map[string]int{"ada": 2, "bo": 1, "cy": 0}
	for _, r := range rows {
		if n := len(r["tk_notes"].([]any)); n != want[r["guest"].(string)] {
			t.Errorf("PERIOD backward embed for %v: %d notes", r["guest"], n)
		}
	}

	getJSON(t, srv+"/api/tk_notes?select=id,tk_bookings!inner(guest)&tk_bookings.guest=eq.bo", &rows)
	if len(rows) != 1 || rows[0]["id"] != float64(1) || !reflect.DeepEqual(guests(rows[0]["tk_bookings"]), []string{"bo"}) {
		t.Fatalf("!inner PERIOD embed = %v", rows)
	}
	getJSON(t, srv+"/api/tk_notes?select=id,tk_bookings(guest)&tk_bookings.order=guest.desc&tk_bookings.limit=1&order=id.asc", &rows)
	if len(rows) != 2 || !reflect.DeepEqual(guests(rows[0]["tk_bookings"]), []string{"bo"}) || !reflect.DeepEqual(guests(rows[1]["tk_bookings"]), []string{"ada"}) {
		t.Fatalf("per-embed params on a PERIOD embed = %v", rows)
	}

	for q, want := range map[string][]int{
		"tk_bookings.guest=eq.bo":               {1},
		"tk_bookings!every.guest=eq.ada":        {2},
		"tk_bookings!none.guest=eq.bo":          {2},
		"order=tk_bookings(guest).asc,id.desc":  {2, 1},
		"order=tk_bookings(guest).desc,id.desc": {1, 2},
		"tk_bookings=exists.true&order=id.desc": {2, 1},
	} {
		getJSON(t, srv+"/api/tk_notes?select=id&"+q, &rows)
		if got := ids(rows, "id"); !eq(got, want) {
			t.Errorf("%s: ids = %v, want %v", q, got, want)
		}
	}

	getJSON(t, srv+"/api/tk_readings?select=id,tk_rooms(name)&order=id.asc", &rows)
	if len(rows) != 2 || rows[0]["tk_rooms"].(map[string]any)["name"] != "blue" || rows[1]["tk_rooms"] != nil {
		t.Fatalf("NOT ENFORCED embed = %v", rows)
	}

	var seen []string
	next := srv + "/api/tk_bookings?limit=1"
	for i := 0; i < 4 && next != ""; i++ {
		res := getJSON(t, next, &rows)
		for _, r := range rows {
			seen = append(seen, r["guest"].(string))
		}
		next = ""
		if cur := res.Header.Get("X-Next-Cursor"); cur != "" {
			next = srv + "/api/tk_bookings?limit=1&after=" + cur
		}
	}
	if !reflect.DeepEqual(seen, []string{"ada", "bo", "cy"}) {
		t.Errorf("keyset walk over a temporal primary key = %v", seen)
	}
}

func TestTemporalRoutesAndErrors(t *testing.T) {
	temporalFixture(t)
	srv := testServer(t, nil).URL

	res, body := get(t, srv+"/api/tk_bookings/1,%5B2026-01-01%2C2026-01-05)")
	wantProblem(t, res, body, 404, "not_found")
	res, body = get(t, srv+"/api/tk_rates/by/room_id,valid_at/1,%5B2026-01-01%2C2026-02-01)")
	wantProblem(t, res, body, 404, "not_found")
	var r row
	getJSON(t, srv+"/api/tk_rates/1", &r)
	if r["price"] != "100" {
		t.Fatalf("tk_rates/1 = %v", r)
	}

	res, body = send(t, "PUT", srv+"/api/tk_bookings", `{"room_id":1,"valid_at":"[2026-01-01,2026-01-05)","guest":"ada"}`, nil)
	wantProblem(t, res, body, 405, "method_not_allowed")
	if allow := res.Header.Get("Allow"); allow != "GET, HEAD, POST, PATCH, DELETE" {
		t.Errorf("tk_bookings Allow = %q", allow)
	}
	var list []row
	sendJSON(t, "PATCH", srv+"/api/tk_bookings?room_id=eq.2", `{"guest":"zed"}`, 200, &list)
	if len(list) != 1 || list[0]["guest"] != "zed" {
		t.Errorf("bulk PATCH on a temporal-only table = %v", list)
	}
	res, body = send(t, "DELETE", srv+"/api/tk_bookings?guest=eq.bo", "", nil)
	wantProblem(t, res, body, 409, "pg:23503")
	sendJSON(t, "DELETE", srv+"/api/tk_bookings?guest=eq.zed", "", 200, &list)
	if len(list) != 1 {
		t.Errorf("bulk DELETE on a temporal-only table = %v", list)
	}
	res, body = send(t, "PUT", srv+"/api/tk_rates?on_conflict=room_id,valid_at", `{"id":1,"room_id":1,"valid_at":"[2026-01-01,2026-02-01)","price":1}`, nil)
	wantProblem(t, res, body, 400, "temporal_conflict_target")
	sendJSON(t, "PUT", srv+"/api/tk_rates", `{"id":1,"room_id":1,"valid_at":"[2026-01-01,2026-02-01)","price":120}`, 200, &r)
	if r["price"] != "120" {
		t.Errorf("PUT on the plain primary key = %v", r)
	}

	res, body = send(t, "POST", srv+"/api/tk_bookings", `{"room_id":1,"valid_at":"[2026-01-02,2026-01-03)","guest":"dup"}`, nil)
	wantProblem(t, res, body, 409, "pg:23P01")
	res, body = send(t, "POST", srv+"/api/tk_rates", `{"id":2,"room_id":1,"valid_at":"[2026-01-15,2026-01-20)","price":1}`, nil)
	wantProblem(t, res, body, 409, "pg:23P01")
	res, body = send(t, "POST", srv+"/api/tk_notes", `{"room_id":2,"valid_at":"[2026-01-01,2026-01-09)","note":"uncovered"}`, nil)
	wantProblem(t, res, body, 409, "pg:23503")
	sendJSON(t, "POST", srv+"/api/tk_readings", `{"id":3,"room_id":42,"celsius":1}`, 201, &r)

	ignore := map[string]string{"Prefer": "resolution=ignore-duplicates"}
	res, body = send(t, "POST", srv+"/api/tk_bookings", `{"room_id":1,"valid_at":"[2026-01-02,2026-01-03)","guest":"dup"}`, ignore)
	if res.StatusCode != 204 {
		t.Errorf("ignore-duplicates on an overlapping temporal PK = %d %s", res.StatusCode, body)
	}
	sendJSON(t, "POST", srv+"/api/tk_bookings", `{"room_id":1,"valid_at":"[2026-02-01,2026-02-03)","guest":"new"}`, 201, &r)
	res, body = send(t, "POST", srv+"/api/tk_rates?on_conflict=room_id,valid_at", `{"id":5,"room_id":1,"valid_at":"[2026-01-10,2026-01-11)","price":1}`, ignore)
	if res.StatusCode != 204 {
		t.Errorf("ignore-duplicates on a temporal unique = %d %s", res.StatusCode, body)
	}
	res, body = send(t, "POST", srv+"/api/tk_rates?on_conflict=room_id,valid_at", `{"id":5,"room_id":1,"valid_at":"[2026-01-10,2026-01-11)","price":1}`,
		map[string]string{"Prefer": "resolution=merge-duplicates"})
	wantProblem(t, res, body, 400, "temporal_conflict_target")
	sendJSON(t, "PUT", srv+"/api/tk_rates/1", `{"room_id":1,"valid_at":"[2026-01-01,2026-02-01)","price":130}`, 200, &r)
	if r["price"] != "130" {
		t.Errorf("PUT on the plain-key row path = %v", r)
	}

	doc := openAPIDoc(t, srv)
	for path := range doc["paths"].(map[string]any) {
		if strings.HasPrefix(path, "/tk_bookings/") || strings.HasPrefix(path, "/tk_rates/by/") {
			t.Errorf("OpenAPI path for a temporal key: %s", path)
		}
	}
	if emb := schemaOf(doc, "tk_notes")["properties"].(map[string]any)["tk_bookings"].(map[string]any); emb["type"] != "array" {
		t.Errorf("OpenAPI PERIOD forward embed = %v", emb)
	}
	if emb := schemaOf(doc, "tk_readings")["properties"].(map[string]any)["tk_rooms"].(map[string]any); len(emb["anyOf"].([]any)) != 2 {
		t.Errorf("OpenAPI NOT ENFORCED embed = %v", emb)
	}
}

func TestTemporalNestedMutations(t *testing.T) {
	temporalFixture(t)
	srv := testServer(t, nil).URL

	var r row
	sendJSON(t, "POST", srv+"/api/tk_notes?select=note,room_id,tk_bookings(guest)",
		`{"note":"connected","valid_at":"[2026-01-02,2026-01-08)","tk_bookings":{"connect":{"room_id":1}}}`, 201, &r)
	if r["room_id"] != float64(1) || !reflect.DeepEqual(guests(r["tk_bookings"]), []string{"ada", "bo"}) {
		t.Fatalf("PERIOD connect = %v", r)
	}
	res, body := send(t, "POST", srv+"/api/tk_notes",
		`{"note":"uncovered","valid_at":"[2026-01-02,2026-01-08)","tk_bookings":{"connect":{"room_id":2}}}`, nil)
	wantProblem(t, res, body, 409, "pg:23503")
	res, body = send(t, "POST", srv+"/api/tk_notes", `{"note":"no period","tk_bookings":{"connect":{"room_id":1}}}`, nil)
	wantProblem(t, res, body, 400, "invalid_body")

	sendJSON(t, "POST", srv+"/api/tk_bookings?select=guest,tk_notes(note)",
		`{"room_id":2,"valid_at":"[2026-02-01,2026-02-05)","guest":"di","tk_notes":{"create":[{"note":"child","valid_at":"[2026-02-02,2026-02-03)"}]}}`, 201, &r)
	if notes := r["tk_notes"].([]any); len(notes) != 1 || notes[0].(map[string]any)["note"] != "child" {
		t.Fatalf("PERIOD backward create = %v", r)
	}

	sendJSON(t, "POST", srv+"/api/tk_notes?select=note,tk_bookings(guest)",
		`{"note":"parent created","valid_at":"[2026-01-08,2026-01-12)","tk_bookings":{"create":{"room_id":1,"valid_at":"[2026-01-10,2026-01-15)","guest":"ed"}}}`, 201, &r)
	if !reflect.DeepEqual(guests(r["tk_bookings"]), []string{"bo", "ed"}) {
		t.Fatalf("PERIOD forward create = %v", r)
	}
}
