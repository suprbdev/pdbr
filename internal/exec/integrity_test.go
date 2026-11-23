package exec

import (
	"context"
	"io"
	"log/slog"
	"testing"

	"github.com/jackc/pgx/v5/pgconn"
	"github.com/suprbdev/pdbcore/pgexec"
	"github.com/suprbdev/pdbcore/testutil"

	"github.com/suprbdev/pdbr/internal/api"
	"github.com/suprbdev/pdbr/internal/inflect"
)

func TestIntegrityErrorsHideHiddenObjects(t *testing.T) {
	ab := api.New(testutil.FixtureCatalog(), inflect.Default, api.Options{
		Functions: true, BasePath: "/api", Schemas: []string{"public"},
		MaxPageSize: 100, Logger: slog.New(slog.NewTextHandler(io.Discard, nil)),
	})
	users := ab.Resource("public", "users")
	ab.RemoveField(users, "email", true)
	ab.RemoveField(users, "full_name", false)
	b, err := ab.Build()
	if err != nil {
		t.Fatal(err)
	}
	pg := func(code, table, column, constraint string) *pgconn.PgError {
		return &pgconn.PgError{Code: code, Message: "raw " + code, SchemaName: "public", TableName: table, ColumnName: column, ConstraintName: constraint}
	}
	cases := []struct {
		err  *pgconn.PgError
		want string
	}{
		{pg("23503", "secrets", "", "secrets_owner_id_fkey"), "foreign key violation"},
		{pg("23001", "secrets", "", "secrets_owner_id_fkey"), "foreign key violation"},
		{pg("23503", "posts", "", "posts_secret_fkey"), "foreign key violation"},
		{pg("23503", "posts", "", "posts_author_id_fkey"), "raw 23503"},
		{pg("23502", "users", "email", ""), "not-null violation"},
		{pg("23502", "users", "full_name", ""), "raw 23502"},
		{pg("23502", "users", "ssn", ""), "not-null violation"},
		{pg("23502", "posts", "title", ""), "raw 23502"},
		{pg("23505", "users", "", "users_email_key"), "unique violation"},
		{pg("23505", "users", "", "users_pkey"), "raw 23505"},
		{pg("23505", "users", "", "users_some_expr_idx"), "raw 23505"},
		{pg("23514", "vault", "", "vault_pin_check"), "check violation"},
		{pg("23514", "posts", "", "posts_title_check"), "raw 23514"},
		{pg("23P01", "vault", "", "vault_excl"), "exclusion violation"},
		{pg("23514", "", "", ""), "raw 23514"},
		{&pgconn.PgError{Code: "23505", Message: "raw 23505", SchemaName: "audit", TableName: "users", ConstraintName: "users_pkey"}, "unique violation"},
	}
	for _, tc := range cases {
		e := newExec(&fakeDB{row: fakeRow{err: tc.err}}, b, nil, Options{Detail: pgexec.Prod})
		xe := errOf(t, e.Execute(context.Background(), listOp(b, "/users")))
		if xe.Detail != tc.want || xe.Code != "pg:"+tc.err.Code {
			t.Errorf("%s %s.%s %s: detail %q code %s, want %q", tc.err.Code, tc.err.TableName, tc.err.ColumnName, tc.err.ConstraintName, xe.Detail, xe.Code, tc.want)
		}
		e = newExec(&fakeDB{row: fakeRow{err: tc.err}}, b, nil, Options{Detail: pgexec.Dev})
		if xe := errOf(t, e.Execute(context.Background(), listOp(b, "/users"))); xe.Detail != tc.err.Message {
			t.Errorf("dev %s: detail %q", tc.err.Code, xe.Detail)
		}
	}
}
