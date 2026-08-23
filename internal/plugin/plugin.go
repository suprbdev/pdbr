// Package plugin defines pdbr's REST-specific hook surfaces (APIHook,
// CompileHook, RequestHook) and a Registry that layers their chain helpers
// on pdbcore/plugin's Registry (Plugin, CatalogHook, InflectionHook).
package plugin

import (
	"context"

	coreplugin "github.com/suprbdev/pdbcore/plugin"

	"github.com/suprbdev/pdbr/internal/api"
	"github.com/suprbdev/pdbr/internal/compile"
	"github.com/suprbdev/pdbr/internal/exec"
)

// Plugin, CatalogHook and InflectionHook are the shared pdbcore surfaces.
type (
	Plugin         = coreplugin.Plugin
	CatalogHook    = coreplugin.CatalogHook
	InflectionHook = coreplugin.InflectionHook
)

// APIHook adds/removes routes, methods, params, fields and schemas after
// default generation and before Build.
type APIHook interface {
	TransformAPI(ctx context.Context, b *api.Builder) error
}

// CompileHook wraps SQL generation per request, middleware-style.
type CompileHook interface {
	WrapCompile(next compile.Func) compile.Func
}

// RequestHook observes and controls the request lifecycle (auth, logging,
// transaction policy).
type RequestHook interface {
	BeforeRequest(ctx context.Context, op *exec.Operation) (context.Context, error)
	AfterRequest(ctx context.Context, op *exec.Operation, res *exec.Result)
}

// Registry is pdbcore's registry plus the pdbr chain helpers.
type Registry struct {
	*coreplugin.Registry
}

// NewRegistry builds a registry with the given plugins added in order.
func NewRegistry(plugins ...Plugin) *Registry {
	return &Registry{Registry: coreplugin.NewRegistry(plugins...)}
}

// TransformAPI runs all APIHooks in order.
func (r *Registry) TransformAPI(ctx context.Context, disabled map[string]bool, b *api.Builder) error {
	for _, p := range r.Enabled(disabled) {
		if h, ok := p.(APIHook); ok {
			if err := h.TransformAPI(ctx, b); err != nil {
				return err
			}
		}
	}
	return nil
}

// CompileChain wraps base with all CompileHooks (outermost = lowest priority).
func (r *Registry) CompileChain(disabled map[string]bool, base compile.Func) compile.Func {
	plugins := r.Enabled(disabled)
	chain := base
	for i := len(plugins) - 1; i >= 0; i-- {
		if h, ok := plugins[i].(CompileHook); ok {
			chain = h.WrapCompile(chain)
		}
	}
	return chain
}

// RequestHooks returns the RequestHook implementations in order.
func (r *Registry) RequestHooks(disabled map[string]bool) []RequestHook {
	var out []RequestHook
	for _, p := range r.Enabled(disabled) {
		if h, ok := p.(RequestHook); ok {
			out = append(out, h)
		}
	}
	return out
}

// Hooks lists the hook surfaces p implements, in the order `pdbr plugins
// list` prints them: catalog, inflection, api, compile, request. A plugin
// with no hooks yields ["none"].
func Hooks(p Plugin) []string {
	var out []string
	if _, ok := p.(CatalogHook); ok {
		out = append(out, "catalog")
	}
	if _, ok := p.(InflectionHook); ok {
		out = append(out, "inflection")
	}
	if _, ok := p.(APIHook); ok {
		out = append(out, "api")
	}
	if _, ok := p.(CompileHook); ok {
		out = append(out, "compile")
	}
	if _, ok := p.(RequestHook); ok {
		out = append(out, "request")
	}
	if len(out) == 0 {
		out = append(out, "none")
	}
	return out
}
