package plugin

import (
	"context"
	"reflect"
	"testing"

	"github.com/suprbdev/pdbcore/inflect"
	"github.com/suprbdev/pdbcore/introspect"

	"github.com/suprbdev/pdbr/internal/api"
	"github.com/suprbdev/pdbr/internal/compile"
	"github.com/suprbdev/pdbr/internal/exec"
)

type recorder struct {
	name string
	prio int
	log  *[]string
}

func (r *recorder) Name() string  { return r.name }
func (r *recorder) Priority() int { return r.prio }
func (r *recorder) TransformAPI(_ context.Context, _ *api.Builder) error {
	*r.log = append(*r.log, "api:"+r.name)
	return nil
}
func (r *recorder) WrapCompile(next compile.Func) compile.Func {
	return func(ctx context.Context, req *compile.Request) (*compile.Statement, error) {
		*r.log = append(*r.log, "compile:"+r.name)
		return next(ctx, req)
	}
}
func (r *recorder) BeforeRequest(ctx context.Context, _ *exec.Operation) (context.Context, error) {
	*r.log = append(*r.log, "before:"+r.name)
	return ctx, nil
}
func (r *recorder) AfterRequest(_ context.Context, _ *exec.Operation, _ *exec.Result) {
	*r.log = append(*r.log, "after:"+r.name)
}

type catalogOnly struct{}

func (catalogOnly) Name() string  { return "cat" }
func (catalogOnly) Priority() int { return 1 }
func (catalogOnly) TransformCatalog(_ context.Context, _ *introspect.Catalog) error {
	return nil
}
func (catalogOnly) Inflect(kind inflect.Kind, in inflect.Input, next inflect.Next) string {
	return next(kind, in)
}

type bare struct{}

func (bare) Name() string  { return "bare" }
func (bare) Priority() int { return 5 }

func TestOrdering(t *testing.T) {
	var log []string
	b := &recorder{"b", 200, &log}
	a := &recorder{"a", 100, &log}
	c := &recorder{"c", 200, &log} // ties break by registration order
	r := NewRegistry(b, a, c)

	if err := r.TransformAPI(context.Background(), nil, nil); err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(log, []string{"api:a", "api:b", "api:c"}) {
		t.Errorf("api order = %v", log)
	}
	log = nil
	base := func(_ context.Context, _ *compile.Request) (*compile.Statement, error) {
		log = append(log, "base")
		return &compile.Statement{}, nil
	}
	if _, err := r.CompileChain(nil, base)(context.Background(), nil); err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(log, []string{"compile:a", "compile:b", "compile:c", "base"}) {
		t.Errorf("compile order = %v (lowest priority must be outermost)", log)
	}
	log = nil
	hooks := r.RequestHooks(map[string]bool{"b": true})
	ctx := context.Background()
	for _, h := range hooks {
		ctx, _ = h.BeforeRequest(ctx, nil)
	}
	for _, h := range hooks {
		h.AfterRequest(ctx, nil, nil)
	}
	if !reflect.DeepEqual(log, []string{"before:a", "before:c", "after:a", "after:c"}) {
		t.Errorf("request order = %v", log)
	}
}

func TestDisabled(t *testing.T) {
	var log []string
	r := NewRegistry(&recorder{"a", 1, &log})
	if err := r.TransformAPI(context.Background(), map[string]bool{"a": true}, nil); err != nil {
		t.Fatal(err)
	}
	if len(log) != 0 {
		t.Errorf("disabled plugin ran: %v", log)
	}
}

func TestHooks(t *testing.T) {
	var log []string
	if got := Hooks(&recorder{"x", 1, &log}); !reflect.DeepEqual(got, []string{"api", "compile", "request"}) {
		t.Errorf("Hooks = %v", got)
	}
	if got := Hooks(catalogOnly{}); !reflect.DeepEqual(got, []string{"catalog", "inflection"}) {
		t.Errorf("Hooks = %v", got)
	}
	if got := Hooks(bare{}); !reflect.DeepEqual(got, []string{"none"}) {
		t.Errorf("Hooks = %v", got)
	}
}
