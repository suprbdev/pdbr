// Package smartcomments is the built-in smart_comments plugin: PostGraphile-
// style tags in database COMMENTs (@omit, @name, @deprecated, @fieldName,
// ...) customize the REST surface with plain DDL. The catalog half is
// pdbcore/smartcomments/catalog (shared with pdbq); this package adds the
// REST naming (InflectionHook) and surface (APIHook) halves over the same
// tag index. See docs/smart-comments.md.
package smartcomments

import (
	"context"
	"log/slog"
	"sort"
	"strings"

	coreinflect "github.com/suprbdev/pdbcore/inflect"
	"github.com/suprbdev/pdbcore/introspect"
	"github.com/suprbdev/pdbcore/smartcomments/catalog"
	"github.com/suprbdev/pdbcore/smarttags"

	"github.com/suprbdev/pdbr/internal/api"
	"github.com/suprbdev/pdbr/internal/inflect"
)

// Plugin embeds the shared catalog half (Name, Priority, Index) and adds
// TransformCatalog's smart-tag checks, Inflect and TransformAPI.
type Plugin struct {
	*catalog.Hook
	// findings are the smart-tag issues of the last TransformCatalog,
	// logged by TransformAPI once the configured logger is known.
	findings []catalog.Finding
}

// vocabulary is what pdbr honours: the @omit actions of TransformAPI and
// the catalog hook, on top of pdbcore's omit and contract @behavior
// tokens.
var vocabulary = smarttags.Vocabulary{
	OmitActions: []string{"read", "all", "create", "update", "delete", "many", "filter", "order"},
}

// TransformCatalog records the unknown @omit actions and unknown or
// deprecated @behavior tokens of the catalog (see catalog.Check), then
// runs the shared catalog half.
func (p *Plugin) TransformCatalog(ctx context.Context, c *introspect.Catalog) error {
	p.findings = catalog.Check(c, vocabulary)
	return p.Hook.TransformCatalog(ctx, c)
}

// Name is the plugin name (plugins.disabled entry). It replaces the
// embedded catalog hook's name.
const Name = "smart_comments"

// New returns the plugin; it takes no settings.
func New() *Plugin { return &Plugin{Hook: catalog.New()} }

// Name implements plugin.Plugin.
func (p *Plugin) Name() string { return Name }

// ---- InflectionHook ----

// Inflect applies @name at the head of the naming chain (the renamed
// identifier replaces the pg one in the Input, then next runs, so downstream
// naming plugins derive from the new name) and short-circuits the relation
// kinds with @fieldName / @foreignFieldName.
func (p *Plugin) Inflect(kind coreinflect.Kind, in coreinflect.Input, next coreinflect.Next) string {
	ix := p.Index()
	switch kind {
	case inflect.KindResourcePath:
		in.Table = ix.RenamedTable(in.Schema, in.Table)
	case inflect.KindFieldName:
		in.Column = ix.RenamedColumn(in.Schema, in.Table, in.Column)
	case inflect.KindRelationForward:
		if name := p.constraintTags(in).First("fieldName"); name != "" {
			return name
		}
		in.Table = ix.RenamedTable(in.Schema, in.Table)
	case inflect.KindRelationBackward:
		if name := p.constraintTags(in).First("foreignFieldName"); name != "" {
			return name
		}
		in.Columns = ix.RenamedColumns(in.Schema, in.Table, in.Columns)
		in.Table = ix.RenamedTable(in.Schema, in.Table)
	case inflect.KindRPCName, inflect.KindComputedField:
		if name := ix.Function(in.Schema, in.Function).First("name"); name != "" {
			in.Function = name
		}
	}
	return next(kind, in)
}

// constraintTags resolves an FK's tags. Constraints are indexed under the
// referencing table's schema; the forward kind carries the referenced
// table's schema instead, so a cross-schema FK falls back to a unique
// suffix match.
func (p *Plugin) constraintTags(in coreinflect.Input) smarttags.Tags {
	ix := p.Index()
	if tags := ix.Constraint(in.Schema, in.Constraint); tags != nil {
		return tags
	}
	if in.Constraint == "" {
		return nil
	}
	var hit smarttags.Tags
	n := 0
	for k, tags := range ix.Constraints {
		if strings.HasSuffix(k, "."+in.Constraint) {
			hit = tags
			n++
		}
	}
	if n == 1 {
		return hit
	}
	return nil
}

// ---- APIHook ----

// TransformAPI applies the tags with no catalog representation: route and
// method removal, hidden fields and body keys, filter/order withdrawal,
// backward-embed removal, deprecation marks and enum renames.
func (p *Plugin) TransformAPI(_ context.Context, b *api.Builder) error {
	p.SetLogger(b.Options.Logger)
	log := b.Options.Logger
	if log == nil {
		log = slog.Default()
	}
	for _, f := range p.findings {
		log.Warn("smart_comments: "+f.Message(), f.Args()...)
	}
	for _, key := range sortedKeys(b.Resources) {
		r := b.Resources[key]
		p.applyTableTags(b, r)
		p.applyColumnTags(b, r)
		p.applyConstraintTags(b, r)
		p.applyComputedTags(r)
	}
	for _, key := range sortedKeys(b.RPCs) {
		rpc := b.RPCs[key]
		if tags := p.Index().Function(rpc.Function.Schema, rpc.Function.Name); tags.Has("deprecated") {
			rpc.Deprecated, rpc.DeprecationReason = true, deprecationReason(tags)
		}
	}
	for _, key := range sortedKeys(b.Enums) {
		e := b.Enums[key]
		if name := p.Index().Enum(e.Enum.Schema, e.Enum.Name).First("name"); name != "" {
			b.RenameEnum(e, name)
		}
	}
	return nil
}

func (p *Plugin) applyTableTags(b *api.Builder, r *api.Resource) {
	t := r.Table
	tags := p.Index().Table(t.Schema, t.Name)
	if tags == nil {
		return
	}
	omits, _ := tags.Omits()
	if omits["read"] {
		b.RemoveMethod(r, api.MethodGet, api.MethodHead)
	}
	if omits["all"] {
		r.OmitList = true
	}
	if omits["create"] {
		b.RemoveMethod(r, api.MethodPost, api.MethodPut)
	}
	if omits["update"] {
		b.RemoveMethod(r, api.MethodPatch, api.MethodPut)
	}
	if omits["delete"] {
		b.RemoveMethod(r, api.MethodDelete)
	}
	if omits["many"] {
		for _, rel := range relationsTo(b, r, false) {
			b.RemoveRelation(rel)
		}
	}
	if omits["filter"] {
		for k := range r.FilterColumns {
			b.RemoveFilterColumn(r, k)
		}
		for _, c := range r.Computed {
			c.Filterable = false
		}
	}
	if omits["order"] {
		for k := range r.OrderColumns {
			b.RemoveOrderColumn(r, k)
		}
		for _, c := range r.Computed {
			c.Orderable = false
		}
	}
	if tags.Has("deprecated") {
		r.Deprecated, r.DeprecationReason = true, deprecationReason(tags)
	}
}

func (p *Plugin) applyColumnTags(b *api.Builder, r *api.Resource) {
	t := r.Table
	for _, c := range t.Columns {
		tags := p.Index().Column(t.Schema, t.Name, c.Name)
		if tags == nil {
			continue
		}
		f := r.FieldByColumn(c.Name)
		if f == nil {
			f = r.RasterByColumn(c.Name)
		}
		if f == nil {
			continue
		}
		key := f.Key
		omits, everything := tags.Omits()
		omitted := func(action string) bool { return everything || omits[action] }
		switch {
		case everything:
			b.RemoveField(r, key, true)
		case omitted("read"):
			b.RemoveField(r, key, false)
		}
		if omitted("create") {
			if r.NoCreate == nil {
				r.NoCreate = map[string]bool{}
			}
			r.NoCreate[key] = true
		}
		if omitted("update") {
			if r.NoUpdate == nil {
				r.NoUpdate = map[string]bool{}
			}
			r.NoUpdate[key] = true
		}
		if omitted("filter") {
			b.RemoveFilterColumn(r, key)
		}
		if omitted("order") {
			b.RemoveOrderColumn(r, key)
		}
		if tags.Has("deprecated") {
			f.Deprecated, f.DeprecationReason = true, deprecationReason(tags)
		}
	}
}

// applyConstraintTags handles @omit many / filter / order on the table's
// foreign keys (bare @omit is resolved at the catalog level).
func (p *Plugin) applyConstraintTags(b *api.Builder, r *api.Resource) {
	t := r.Table
	for _, fk := range t.ForeignKeys {
		tags := p.Index().Constraint(t.Schema, fk.Name)
		if tags == nil {
			continue
		}
		omits, _ := tags.Omits()
		for _, rel := range relationsOf(b, fk) {
			if omits["many"] && !rel.Forward {
				b.RemoveRelation(rel)
				continue
			}
			if omits["filter"] {
				rel.Filterable = false
			}
			if omits["order"] {
				rel.Orderable = false
			}
		}
	}
}

// applyComputedTags handles @deprecated and @omit filter / order on computed
// columns (advanced_filters reads the same tags from the catalog; both only
// ever narrow the flags).
func (p *Plugin) applyComputedTags(r *api.Resource) {
	for _, key := range sortedKeys(r.Computed) {
		c := r.Computed[key]
		tags := p.Index().Function(c.Function.Schema, c.Function.Name)
		if tags == nil {
			continue
		}
		if tags.Has("deprecated") {
			c.Deprecated, c.DeprecationReason = true, deprecationReason(tags)
		}
		omits, _ := tags.Omits()
		if omits["filter"] {
			c.Filterable = false
		}
		if omits["order"] {
			c.Orderable = false
		}
	}
}

// relationsTo lists the relations of every resource that embed r in the
// given direction.
func relationsTo(b *api.Builder, r *api.Resource, forward bool) []*api.Relation {
	var out []*api.Relation
	for _, key := range sortedKeys(b.Resources) {
		from := b.Resources[key]
		for _, rk := range sortedKeys(from.Relations) {
			for _, rel := range from.Relations[rk] {
				if rel.To == r && rel.Forward == forward {
					out = append(out, rel)
				}
			}
		}
	}
	return out
}

// relationsOf lists both directions generated from fk.
func relationsOf(b *api.Builder, fk *introspect.ForeignKey) []*api.Relation {
	var out []*api.Relation
	for _, key := range sortedKeys(b.Resources) {
		from := b.Resources[key]
		for _, rk := range sortedKeys(from.Relations) {
			for _, rel := range from.Relations[rk] {
				if rel.FK == fk {
					out = append(out, rel)
				}
			}
		}
	}
	return out
}

func deprecationReason(tags smarttags.Tags) string {
	if r := tags.First("deprecated"); r != "" {
		return r
	}
	return "No longer supported"
}

func sortedKeys[V any](m map[string]V) []string {
	out := make([]string, 0, len(m))
	for k := range m {
		out = append(out, k)
	}
	sort.Strings(out)
	return out
}
