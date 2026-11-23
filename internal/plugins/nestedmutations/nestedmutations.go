// Package nestedmutations is the built-in plugin (priority 200) that lets a
// single-object POST, or a PATCH of a single-row path, write related rows in
// the same statement: forward relation keys take {"create": {…}} or
// {"connect": {…}} (and, on a row being updated, {"disconnect": true},
// {"update": {…}}, {"delete": true}); backward keys take lists under
// "create" and "connect" (and, on a row being updated, "update", "delete"
// and "disconnect"), nested up to max_depth. It exercises every
// REST-specific hook surface: the APIHook publishes the accepted keys
// (api.Surface.NestedKeys, read by the parser), their OpenAPI schemas
// (api.Surface.NestedSchema) and the `<name>.connect` key schemas, the
// CompileHook replaces the DML with a multi-CTE chain handed to
// compile.MutationChain, and the RequestHook forces a transaction.
package nestedmutations

import (
	"context"
	"fmt"
	"slices"
	"sort"
	"strings"

	"github.com/suprbdev/pdbcore/introspect"

	"github.com/suprbdev/pdbr/internal/api"
	"github.com/suprbdev/pdbr/internal/compile"
	"github.com/suprbdev/pdbr/internal/exec"
	"github.com/suprbdev/pdbr/internal/plan"
)

// Name is the plugin name (plugins.disabled entry and plugins.settings key).
const Name = "nested_mutations"

// DefaultMaxDepth is the default nesting bound (the root row is depth 1).
const DefaultMaxDepth = 3

// DefaultMaxOperations is the default bound on nested operations per
// request (every list item counts once).
const DefaultMaxOperations = 50

// Settings is the plugins.settings.nested_mutations section.
type Settings struct {
	MaxDepth      int `koanf:"max_depth" doc:"Deepest nesting of create/update operations (the root row is depth 1; connect, delete and disconnect add no level); deeper answers 400 embed_depth_exceeded."`
	MaxOperations int `koanf:"max_operations" doc:"Most nested operations per request (every list item and forward operation counts once); more answers 400 invalid_body."`
}

// DefaultSettings returns the defaults: max_depth DefaultMaxDepth,
// max_operations DefaultMaxOperations.
func DefaultSettings() Settings {
	return Settings{MaxDepth: DefaultMaxDepth, MaxOperations: DefaultMaxOperations}
}

// Validate reports settings out of range, keyed by their config path.
func (s Settings) Validate() []string {
	var errs []string
	if s.MaxDepth < 1 {
		errs = append(errs, "plugins.settings."+Name+".max_depth must be >= 1")
	}
	if s.MaxOperations < 1 {
		errs = append(errs, "plugins.settings."+Name+".max_operations must be >= 1")
	}
	return errs
}

// Plugin is the nested_mutations plugin.
type Plugin struct {
	maxDepth int
	maxOps   int
}

// New builds the plugin from its settings; a max_depth or max_operations
// below 1 (which Validate rejects) falls back to the default.
func New(s Settings) *Plugin {
	p := &Plugin{maxDepth: s.MaxDepth, maxOps: s.MaxOperations}
	if p.maxDepth < 1 {
		p.maxDepth = DefaultMaxDepth
	}
	if p.maxOps < 1 {
		p.maxOps = DefaultMaxOperations
	}
	return p
}

// Name implements plugin.Plugin.
func (p *Plugin) Name() string { return Name }

// Priority implements plugin.Plugin.
func (p *Plugin) Priority() int { return 200 }

// MaxDepth is the effective max_depth setting.
func (p *Plugin) MaxDepth() int { return p.maxDepth }

// MaxOperations is the effective max_operations setting.
func (p *Plugin) MaxOperations() int { return p.maxOps }

// NestedKeys lists the body keys a resource accepts for nested input: every
// unambiguous relation key. Ambiguous keys need @fieldName /
// @foreignFieldName inflection first. Which operations a key offers is
// checked when the body is resolved (unknown_relation otherwise; see
// unavailable).
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
// the parser, Surface.NestedSchema for the .insert and .update schemas,
// and one `<name>.connect` key schema per resource a nested key of a
// writable resource leads to.
func (p *Plugin) TransformAPI(_ context.Context, b *api.Builder) error {
	b.Surface.NestedKeys = NestedKeys
	b.Surface.NestedSchema = NestedSchema
	keys := make([]string, 0, len(b.Resources))
	for k := range b.Resources {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	for _, k := range keys {
		from := b.Resources[k]
		if !from.Methods[api.MethodPost] && !from.Methods[api.MethodPatch] {
			continue
		}
		for _, rel := range NestedKeys(from) {
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

// unavailable returns why rel does not offer op on a row being inserted
// (update false) or updated, or "" when it does. Operations that write an
// FK column need it in the body columns of the row holding it (so `@omit
// create` / `@omit update` on the column remove them) and the referenced
// columns readable (copying a column hidden by `@omit read` into a readable
// FK would reveal it); disconnect needs the FK nullable. create, update and
// delete need the related resource's POST, PATCH and DELETE (privileges,
// `@omit create|update|delete`), a backward connect or disconnect its PATCH.
func unavailable(rel *api.Relation, op string, update bool) string {
	if !update && (op == opUpdate || op == opDelete || op == opDisconnect) {
		return "only on a row being updated"
	}
	from, to := rel.From, rel.To
	holder, referenced, holderMethod := from, to, api.MethodPost
	if update {
		holderMethod = api.MethodPatch
	}
	if !rel.Forward {
		holder, referenced, holderMethod = to, from, api.MethodPatch
		if op == opCreate {
			holderMethod = api.MethodPost
		}
	}
	need := func(m api.Method, what string) string {
		if !to.Methods[m] {
			return "no " + what + " on the related table"
		}
		return ""
	}
	switch op {
	case opCreate:
		if why := need(api.MethodPost, "INSERT"); why != "" {
			return why
		}
	case opUpdate:
		return need(api.MethodPatch, "UPDATE")
	case opDelete:
		return need(api.MethodDelete, "DELETE")
	case opConnect, opDisconnect:
		if !rel.Forward {
			if why := need(api.MethodPatch, "UPDATE"); why != "" {
				return why
			}
		}
	}
	writable := holder.InputColumnsFor(holderMethod)
	for _, pair := range rel.EqualityPairs() {
		f := holder.FieldByColumn(pair.Column)
		if f == nil || writable[f.Key] == nil {
			return "the foreign key column " + pair.Column + " is not writable"
		}
		if referenced.FieldByColumn(pair.RefColumn) == nil {
			return "the foreign key references a column hidden from reads"
		}
		if op == opDisconnect && f.Column.NotNull {
			return "the foreign key column " + f.Key + " is NOT NULL"
		}
	}
	return ""
}

// NestedSchema renders the OpenAPI schema of nested key rel of r in a
// create (api.MethodPost) or single-row update (api.MethodPatch) body: one
// property per operation the key offers there, or nil when it offers none.
func NestedSchema(r *api.Resource, rel *api.Relation, m api.Method) *api.Schema {
	if m != api.MethodPost && m != api.MethodPatch || rel == nil || rel.To == nil || rel.FK == nil {
		return nil
	}
	update := m == api.MethodPatch
	to := rel.To
	keyed := len(keySets(to, rel.Forward && rel.FK.Period)) > 0
	ok := func(op string) bool { return unavailable(rel, op, update) == "" }
	key := api.SchemaRef(to.Path + ".connect")
	props := map[string]*api.Schema{}
	cols := strings.Join(fieldKeys(r, rel.EqualityColumns()), ",")
	if rel.Forward {
		if ok(opCreate) {
			props[opCreate] = api.SchemaRef(to.Path + ".insert")
		}
		if ok(opConnect) && keyed {
			props[opConnect] = key
		}
		if update {
			if ok(opDisconnect) {
				props[opDisconnect] = &api.Schema{Type: "boolean", Const: true, Description: "Clear " + cols + "."}
			}
			if ok(opUpdate) {
				props[opUpdate] = api.SchemaRef(to.Path + ".update")
			}
			if ok(opDelete) {
				props[opDelete] = &api.Schema{Type: "boolean", Const: true, Description: "Delete the referenced row (the foreign key's ON DELETE action applies to this row)."}
			}
		}
		if len(props) == 0 {
			return nil
		}
		s := &api.Schema{Type: "object", Properties: props, MinProperties: intPtr(1), AdditionalProperties: boolPtr(false)}
		if update {
			s.Description = "Nested input (nested_mutations plugin) for the " + to.Path + " row referenced by " + cols + ": at most one of create / connect / disconnect (which set " + cols + "), plus update or delete of the row referenced before the request. Single-row PATCH only."
		} else {
			s.MaxProperties = intPtr(1)
			s.Description = "Nested input (nested_mutations plugin): exactly one of " + strings.Join(sortedSchemaKeys(props), " / ") + " for the " + to.Path + " row referenced by " + cols + ", which then need not be sent."
		}
		return s
	}
	list := func(items *api.Schema) *api.Schema {
		return &api.Schema{Type: "array", Items: items, MinItems: intPtr(1)}
	}
	if ok(opCreate) {
		props[opCreate] = list(api.SchemaRef(to.Path + ".insert"))
	}
	if keyed {
		if ok(opConnect) {
			props[opConnect] = list(key)
		}
		if update && ok(opUpdate) {
			props[opUpdate] = list(&api.Schema{
				Type:                 "object",
				Properties:           map[string]*api.Schema{"where": key, "data": api.SchemaRef(to.Path + ".update")},
				Required:             []string{"data", "where"},
				AdditionalProperties: boolPtr(false),
			})
		}
		if update && ok(opDelete) {
			props[opDelete] = list(key)
		}
		if update && ok(opDisconnect) {
			props[opDisconnect] = list(key)
		}
	}
	if len(props) == 0 {
		return nil
	}
	fk := strings.Join(fieldKeys(to, rel.EqualityColumns()), ",")
	desc := "Nested input (nested_mutations plugin): " + to.Path + " rows to create referencing this row, or existing ones (by key) to point at it (their " + fk + " is filled in)."
	if update {
		desc = "Nested input (nested_mutations plugin) for the " + to.Path + " rows referencing this row through " + fk + ": create new ones, connect existing ones, and update, delete or disconnect (clear " + fk + " of) rows referencing it before the request. Single-row PATCH only."
	}
	return &api.Schema{Type: "object", Properties: props, MinProperties: intPtr(1), AdditionalProperties: boolPtr(false), Description: desc}
}

func fieldKeys(r *api.Resource, cols []string) []string {
	out := make([]string, len(cols))
	for i, c := range cols {
		out[i] = keyFor(r, c)
	}
	return out
}

func sortedSchemaKeys(m map[string]*api.Schema) []string {
	keys := make([]string, 0, len(m))
	for k := range m {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	return keys
}

func intPtr(n int) *int { return &n }

func boolPtr(b bool) *bool { return &b }

// keySets lists the column sets naming one existing row of r: the primary
// key (unless hidden from reads), then each unique constraint, then, with
// entity, for a temporal (WITHOUT OVERLAPS) key, its columns without the
// period, which a PERIOD relation connects by. Sets with a column hidden
// from reads are left out.
func keySets(r *api.Resource, entity bool) [][]string {
	var sets [][]string
	if pk := r.PrimaryKey(); pk != nil && !r.HiddenKey {
		sets = append(sets, pk.Columns)
	}
	for _, u := range r.Uniques {
		sets = append(sets, u.Columns)
	}
	for _, k := range append([]*introspect.Constraint{r.Table.PrimaryKey}, r.Table.Uniques...) {
		if !entity || k == nil || !k.WithoutOverlaps || slices.ContainsFunc(sets, func(s []string) bool { return sameSet(s, k.EqualityColumns()) }) {
			continue
		}
		sets = append(sets, k.EqualityColumns())
	}
	out := sets[:0]
	for _, cols := range sets {
		if !slices.ContainsFunc(cols, func(c string) bool { return r.FieldByColumn(c) == nil }) {
			out = append(out, cols)
		}
	}
	return out
}

// connectSchema describes the key of an existing row: exactly one of
// keySets.
func connectSchema(b *api.Builder, r *api.Resource) *api.Schema {
	temporal := false
	for _, k := range append([]*introspect.Constraint{r.Table.PrimaryKey}, r.Table.Uniques...) {
		if k != nil && k.WithoutOverlaps {
			temporal = true
		}
	}
	var alts []*api.Schema
	for _, cols := range keySets(r, true) {
		s := &api.Schema{Type: "object", Properties: map[string]*api.Schema{}}
		for _, c := range cols {
			f := r.FieldByColumn(c)
			s.Properties[f.Key] = b.ColumnSchema(f.Column)
			s.Required = append(s.Required, f.Key)
		}
		alts = append(alts, s)
	}
	if len(alts) == 0 {
		return nil
	}
	desc := "Key of an existing " + r.Path + " row for a nested connect, update, delete or disconnect (nested_mutations plugin): the primary key or one unique constraint's columns."
	if temporal {
		desc += " A temporal (WITHOUT OVERLAPS) key is named without its period column, which a forward connect over a PERIOD foreign key accepts."
	}
	if len(alts) == 1 {
		alts[0].Description = desc
		return alts[0]
	}
	return &api.Schema{Description: desc, OneOf: alts}
}

// WrapCompile replaces the DML of a POST, or of a PATCH on a single-row
// path, whose body carries nested keys with the multi-CTE chain (forward
// operations, the main row, operations on children, visibility CTEs,
// guards) and compiles the response over it; every other request goes to
// next.
func (p *Plugin) WrapCompile(next compile.Func) compile.Func {
	return func(ctx context.Context, req *compile.Request) (*compile.Statement, error) {
		if !hasNested(req) {
			return next(ctx, req)
		}
		return p.compileNested(req)
	}
}

func hasNested(req *compile.Request) bool {
	if req == nil || req.Plan == nil || req.Plan.Resource == nil {
		return false
	}
	pl := req.Plan
	if pl.Body == nil || pl.Body.Objects != nil || len(pl.Body.Nested) == 0 {
		return false
	}
	return pl.Method == plan.POST || (pl.Method == plan.PATCH && pl.Key != nil)
}

func (p *Plugin) compileNested(req *compile.Request) (*compile.Statement, error) {
	c := compile.New(req.Built)
	g := &chain{p: p, c: c, built: req.Built, ps: &compile.ParamSet{}, writes: map[string]compile.TableWrites{}}
	root := req.Plan.Resource
	nested := make([]nestedIn, 0, len(req.Plan.Body.Nested))
	for key, n := range req.Plan.Body.Nested {
		nested = append(nested, nestedIn{key: key, rel: n.Relation, raw: n.Raw})
	}
	sort.Slice(nested, func(i, j int) bool { return nested[i].key < nested[j].key })
	ch := compile.Chain{Params: g.ps, Writes: g.writes}
	if req.Plan.Method == plan.PATCH {
		loc, err := g.pathKey(root, req.Plan.Key)
		if err != nil {
			return nil, err
		}
		if err := g.updateRow(updIn{res: root, cols: req.Plan.Body.Object, nested: nested, depth: 1, cte: "__mut", loc: loc}); err != nil {
			return nil, err
		}
		t := root.Table
		ch.RootKeep = "(" + loc(compile.TableRefSQL(t.Schema, t.Name)) + ") IS NOT TRUE"
		g.guards = append([]string{compile.GuardSQL("__mut", fmt.Sprintf("row of %q:", root.Path))}, g.guards...)
	} else if _, err := g.insertRow(rowIn{res: root, cols: req.Plan.Body.Object, nested: nested, depth: 1, cte: "__mut"}); err != nil {
		return nil, err
	}
	ch.CTEs = strings.Join(g.ctes, ",\n")
	ch.Guards = g.guards
	return c.MutationChain(req, ch)
}

// BeforeRequest marks a request carrying nested keys as transactional
// (op.ForceTx), so the chain is atomic whatever else decides the
// transaction.
func (p *Plugin) BeforeRequest(ctx context.Context, op *exec.Operation) (context.Context, error) {
	if op != nil && op.Plan != nil && op.Plan.Body != nil && len(op.Plan.Body.Nested) > 0 {
		op.ForceTx = true
	}
	return ctx, nil
}

// AfterRequest implements plugin.RequestHook (no-op).
func (p *Plugin) AfterRequest(context.Context, *exec.Operation, *exec.Result) {}
