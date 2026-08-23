package api

import (
	"testing"

	"github.com/suprbdev/pdbcore/testutil"

	"github.com/suprbdev/pdbr/internal/inflect"
)

func BenchmarkOpenAPI(b *testing.B) {
	built, err := New(testutil.FixtureCatalog(), inflect.Default, defaultOptions()).Build()
	if err != nil {
		b.Fatal(err)
	}
	b.ReportAllocs()
	for b.Loop() {
		if _, err := built.OpenAPI.JSON(); err != nil {
			b.Fatal(err)
		}
	}
}
