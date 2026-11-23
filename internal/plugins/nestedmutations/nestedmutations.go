// Package nestedmutations is the built-in plugin (priority 200) that lets a
// single-object POST create or connect related rows in the same statement:
// forward relation keys take {"create": {…}} or {"connect": {…}}, backward
// keys {"create": [{…}, …]}, nested up to max_depth. It exercises every
// REST-specific hook surface: the APIHook publishes the accepted keys
// (api.Surface.NestedKeys, read by the parser) and the `<name>.connect`
// schemas, the CompileHook replaces the DML with a multi-CTE chain handed to
// compile.MutationWithCTEs, and the RequestHook forces a transaction.
package nestedmutations

import (
	"context"
	"slices"
	"sort"
	"strings"

	"github.com/suprbdev/pdbcore/introspect"

	"github.com/suprbdev/pdbr/internal/api"
	"github.com/suprbdev/pdbr/internal/compile"
	"github.com/suprbdev/pdbr/internal/exec"
	"github.com/suprbdev/pdbr/internal/plan"
)

// Name is the plugin name (config key under plugins.settings).
const Name = "nested-mutations"

// DefaultMaxDepth is the default nesting bound (the root row is depth 1).
const DefaultMaxDepth = 3

// Plugin is the nested-mutations plugin.
type Plugin struct {
	forceTx  bool
	maxDepth int
}

// New builds the plugin from plugins.settings.nested-mutations:
// force_transactions (true) and max_depth (3). Unknown keys are ignored.
func New(settings map[string]any) *Plugin {
	p := &Plugin{forceTx: true, maxDepth: DefaultMaxDepth}
	if v, ok := settings["force_transactions"].(bool); ok {
		p.forceTx = v
	}
	if n, ok := intSetting(settings["max_depth"]); ok && n > 0 {
		p.maxDepth = n
	}
	return p
}

func intSetting(v any) (int, bool) {
	switch x := v.(type) {
	case int:
		return x, true
	case int64:
		return int(x), true
	case uint64:
		return int(x), true
	case float64:
		return int(x), true
	}
	return 0, false
}

// Name implements plugin.Plugin.
func (p *Plugin) Name() string { return Name }

// Priority implements plugin.Plugin.
func (p *Plugin) Priority() int { return 200 }

// MaxDepth is the effective max_depth setting.
func (p *Plugin) MaxDepth() int { return p.maxDepth }

// ForceTransactions is the effective force_transactions setting.
func (p *Plugin) ForceTransactions() bool { return p.forceTx }

// NestedKeys lists the body keys a resource accepts for nested input: every
// unambiguous relation key (forward: create/connect the referenced row;
// backward: create referencing rows). Ambiguous keys need @fieldName /
// @foreignFieldName inflection first. Privileges are checked when the body
// is resolved (create needs INSERT on the related table → unknown_relation).
func NestedKeys(res *api.Resource) map[string]*api.Relation {
	if res == nil {
		return nil
	}
	out := make(map[string]*api.Relation, len(res.Relations))
	for key, cands := range res.Relations {
		if len(cands) != 1 || cands[0].To == nil || cands[0].FK == nil {
			continue
		}
		out[key] = cands[0]
	}
	return out
}

// TransformAPI publishes the nested-input surface: Surface.NestedKeys for
// the parser (and the M6 .insert schemas) and one `<name>.connect` schema
// per resource that a forward relation can connect to.
func (p *Plugin) TransformAPI(_ context.Context, b *api.Builder) error {
	b.Surface.NestedKeys = NestedKeys
	keys := make([]string, 0, len(b.Resources))
	for k := range b.Resources {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	for _, k := range keys {
		from := b.Resources[k]
		if !from.Methods[api.MethodPost] {
			continue
		}
		for _, rel := range NestedKeys(from) {
			if !rel.Forward {
				continue
			}
			name := rel.To.Path + ".connect"
			if _, ok := b.Extra.Schemas[name]; ok {
				continue
			}
			if s := connectSchema(b, rel.To); s != nil {
				b.Extra.Schemas[name] = s
			}
		}
	}
	return nil
}

// connectSchema describes the key of an existing row: exactly one of the
// resource's key sets (primary key, then each unique constraint), and for a
// temporal (WITHOUT OVERLAPS) key its columns without the period, which a
// PERIOD relation connects by.
func connectSchema(b *api.Builder, r *api.Resource) *api.Schema {
	var sets [][]string
	if pk := r.PrimaryKey(); pk != nil {
		sets = append(sets, pk.Columns)
	}
	for _, u := range r.Uniques {
		sets = append(sets, u.Columns)
	}
	temporal := false
	for _, k := range append([]*introspect.Constraint{r.Table.PrimaryKey}, r.Table.Uniques...) {
		if k == nil || !k.WithoutOverlaps || slices.ContainsFunc(sets, func(s []string) bool { return sameSet(s, k.EqualityColumns()) }) {
			continue
		}
		sets = append(sets, k.EqualityColumns())
		temporal = true
	}
	var alts []*api.Schema
	for _, cols := range sets {
		s := &api.Schema{Type: "object", Properties: map[string]*api.Schema{}}
		for _, c := range cols {
			f := r.FieldByColumn(c)
			if f == nil {
				s = nil
				break
			}
			s.Properties[f.Key] = b.ColumnSchema(f.Column)
			s.Required = append(s.Required, f.Key)
		}
		if s != nil {
			alts = append(alts, s)
		}
	}
	if len(alts) == 0 {
		return nil
	}
	desc := "Key of an existing " + r.Path + " row for a nested {\"connect\": …} (nested-mutations plugin): the primary key or one unique constraint's columns."
	if temporal {
		desc += " A temporal (WITHOUT OVERLAPS) key is named without its period column."
	}
	if len(alts) == 1 {
		alts[0].Description = desc
		return alts[0]
	}
	return &api.Schema{Description: desc, OneOf: alts}
}

// WrapCompile replaces the DML of a POST whose body carries nested keys with
// the multi-CTE chain (parents, the main row, children, visibility CTEs) and
// compiles the response over it; every other request goes to next.
func (p *Plugin) WrapCompile(next compile.Func) compile.Func {
	return func(ctx context.Context, req *compile.Request) (*compile.Statement, error) {
		if !hasNested(req) {
			return next(ctx, req)
		}
		return p.compileNested(req)
	}
}

func hasNested(req *compile.Request) bool {
	return req != nil && req.Plan != nil && req.Plan.Resource != nil && req.Plan.Method == plan.POST &&
		req.Plan.Body != nil && req.Plan.Body.Objects == nil && len(req.Plan.Body.Nested) > 0
}

func (p *Plugin) compileNested(req *compile.Request) (*compile.Statement, error) {
	c := compile.New(req.Built)
	g := &chain{p: p, c: c, built: req.Built, ps: &compile.ParamSet{}, inserted: map[string][]string{}}
	root := req.Plan.Resource
	nested := make([]nestedIn, 0, len(req.Plan.Body.Nested))
	for key, n := range req.Plan.Body.Nested {
		nested = append(nested, nestedIn{key: key, rel: n.Relation, raw: n.Raw})
	}
	sort.Slice(nested, func(i, j int) bool { return nested[i].key < nested[j].key })
	if _, err := g.insertRow(rowIn{res: root, cols: req.Plan.Body.Object, nested: nested, depth: 1, cte: "__mut"}); err != nil {
		return nil, err
	}
	return c.MutationWithCTEs(req, strings.Join(g.ctes, ",\n"), g.ps, g.inserted)
}

// BeforeRequest forces a transaction for a request carrying nested keys
// (force_transactions), so the chain is atomic even with
// transactions.mutations off.
func (p *Plugin) BeforeRequest(ctx context.Context, op *exec.Operation) (context.Context, error) {
	if p.forceTx && op != nil && op.Plan != nil && op.Plan.Body != nil && len(op.Plan.Body.Nested) > 0 {
		op.ForceTx = true
	}
	return ctx, nil
}

// AfterRequest implements plugin.RequestHook (no-op).
func (p *Plugin) AfterRequest(context.Context, *exec.Operation, *exec.Result) {}
