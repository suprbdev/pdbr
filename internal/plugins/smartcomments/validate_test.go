package smartcomments_test

import (
	"bytes"
	"context"
	"log/slog"
	"strings"
	"testing"

	"github.com/suprbdev/pdbcore/introspect"
	"github.com/suprbdev/pdbcore/testutil"

	"github.com/suprbdev/pdbr/internal/api"
	"github.com/suprbdev/pdbr/internal/inflect"
	"github.com/suprbdev/pdbr/internal/plugin"
	"github.com/suprbdev/pdbr/internal/plugins/smartcomments"
)

// buildLogged builds the fixture API with the smart_comments plugin,
// returning what the build logged at WARN.
func buildLogged(t *testing.T, edit func(*introspect.Catalog)) []string {
	t.Helper()
	cat := testutil.FixtureCatalog()
	edit(cat)
	reg := plugin.NewRegistry(smartcomments.New())
	ctx := context.Background()
	if err := reg.TransformCatalog(ctx, nil, cat); err != nil {
		t.Fatal(err)
	}
	var logs bytes.Buffer
	b := api.New(cat, reg.Inflector(nil, inflect.Default), api.Options{
		FilterIndexedOnly: true, Functions: true, Schemas: []string{"public"}, MaxPageSize: 100,
		Logger: slog.New(slog.NewTextHandler(&logs, nil)),
	})
	if err := reg.TransformAPI(ctx, nil, b); err != nil {
		t.Fatal(err)
	}
	if _, err := b.Build(); err != nil {
		t.Fatal(err)
	}
	var out []string
	for _, line := range strings.Split(logs.String(), "\n") {
		if strings.Contains(line, "level=WARN") && strings.Contains(line, "smart_comments:") {
			out = append(out, line)
		}
	}
	return out
}

func TestSmartTagWarnings(t *testing.T) {
	got := buildLogged(t, func(c *introspect.Catalog) {
		table(c, "users").Comment = "@omit reed, delete\n@behavior -filterBy +contract:public -selectt"
		column(c, "users", "email").Comment = "@behavior -order -orderBy -Bogus"
		function(c, "users_post_count").Comment = "@omit aggregates"
	})
	want := []string{
		`msg="smart_comments: unknown @omit action ignored" table=public.users action=reed`,
		`msg="smart_comments: unknown @behavior token ignored" table=public.users token=-selectt`,
		`msg="smart_comments: deprecated @behavior token" column=public.users.email token=-order use=-orderBy`,
		`msg="smart_comments: unknown @behavior token ignored" column=public.users.email token=-Bogus`,
		`msg="smart_comments: unknown @omit action ignored" function=public.users_post_count action=aggregates`,
	}
	if len(got) != len(want) {
		t.Fatalf("warnings = %q, want %q", got, want)
	}
	for i, w := range want {
		if !strings.Contains(got[i], w) {
			t.Errorf("warning %d = %q, want %q", i, got[i], w)
		}
	}
}

func TestKnownSmartTagsDoNotWarn(t *testing.T) {
	got := buildLogged(t, func(c *introspect.Catalog) {
		table(c, "users").Comment = "@omit read, ALL, many, create, update, delete, filter, order\n" +
			"@behavior -select -insert -update -delete -filterBy -orderBy +contract:public -contract:partner contract:internal"
	})
	if len(got) != 0 {
		t.Errorf("warnings: %q", got)
	}
}

func TestBehaviorV5Spellings(t *testing.T) {
	for _, comment := range []string{"@behavior -filterBy -orderBy", "@behavior -filter -order"} {
		b := build(t, func(c *introspect.Catalog) { table(c, "posts").Comment = comment })
		posts := b.ByPath["posts"]
		if len(posts.FilterColumns) != 0 || len(posts.OrderColumns) != 0 {
			t.Errorf("%q left filter %v / order %v columns", comment, keys(posts.FilterColumns), keys(posts.OrderColumns))
		}
	}
}
