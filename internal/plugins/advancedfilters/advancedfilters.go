// Package advancedfilters is the built-in plugin that opens the relation and
// computed-column query surface: relation filters (rel.col=op.v, rel!none.,
// rel!every., rel=exists.true|false), related-column ordering
// (order=rel(col).asc) and computed-column filter/order. It implements only
// APIHook: it flips api.Builder.Surface and refines the per-relation /
// per-computed Filterable/Orderable flags; parse and the core compiler read
// those, so disabling the plugin removes the feature end to end.
//
// Per-object tuning is read straight from catalog comments with
// pdbcore/smarttags, so it works with the smart_comments plugin disabled:
// "@omit filter" / "@omit order" on a foreign key or computed function drop
// that half; with relations_opt_in / computed_opt_in only objects tagged
// "@filterable" (alias "@sortable") get any surface.
package advancedfilters

import (
	"context"

	"github.com/suprbdev/pdbcore/smarttags"

	"github.com/suprbdev/pdbr/internal/api"
)

// Name is the plugin name (plugins.disabled entry and plugins.settings key).
const Name = "advanced_filters"

// Settings is the plugins.settings.advanced_filters section.
type Settings struct {
	Relations      bool `koanf:"relations" doc:"Relation filters (rel.col=op.v, rel!none., rel!every., rel=exists.true|false)."`
	Computed       bool `koanf:"computed" doc:"Filtering and ordering on computed columns (follows schema.functions)."`
	RelationOrder  bool `koanf:"relation_order" doc:"Ordering by a related column (order=rel(col).asc); off when relations is off."`
	RelationsOptIn bool `koanf:"relations_opt_in" doc:"Only foreign keys tagged @filterable (or @sortable) get relation filters and ordering."`
	ComputedOptIn  bool `koanf:"computed_opt_in" doc:"Only computed-column functions tagged @filterable (or @sortable) get filters and ordering."`
}

// DefaultSettings returns the defaults: relations, computed and
// relation_order on, opt-in modes off.
func DefaultSettings() Settings {
	return Settings{Relations: true, Computed: true, RelationOrder: true}
}

// Plugin is the advanced_filters plugin; build it with New.
type Plugin struct {
	relations      bool
	computed       bool
	relationOrder  bool
	relationsOptIn bool
	computedOptIn  bool
}

// New builds the plugin from its settings.
func New(s Settings) *Plugin {
	return &Plugin{
		relations:      s.Relations,
		computed:       s.Computed,
		relationOrder:  s.RelationOrder,
		relationsOptIn: s.RelationsOptIn,
		computedOptIn:  s.ComputedOptIn,
	}
}

func (p *Plugin) Name() string  { return Name }
func (p *Plugin) Priority() int { return 150 }

// TransformAPI sets the Surface flags and narrows every relation's and
// computed column's Filterable/Orderable to what the settings and the
// object's comment allow. Flags already cleared by an earlier plugin
// (smart_comments' @omit filter|order|many) are never turned back on.
func (p *Plugin) TransformAPI(_ context.Context, b *api.Builder) error {
	relOrder := p.relations && p.relationOrder
	computed := p.computed && b.Options.Functions
	b.Surface.RelationFilters = p.relations
	b.Surface.RelationOrder = relOrder
	b.Surface.ComputedFilters = computed
	b.Surface.ComputedOrder = computed
	for _, r := range b.Resources {
		for _, cands := range r.Relations {
			for _, rel := range cands {
				skipFilter, skipOrder := omits(rel.FK.Comment, p.relationsOptIn)
				backwardOmitted := !rel.Forward && !rel.Embeddable
				rel.Filterable = rel.Filterable && p.relations && !skipFilter && !backwardOmitted
				rel.Orderable = rel.Orderable && relOrder && !skipOrder && !backwardOmitted
			}
		}
		for _, c := range r.Computed {
			skipFilter, skipOrder := omits(c.Function.Comment, p.computedOptIn)
			setOf := c.Function.ReturnsSet || c.SetOf != nil
			c.Filterable = c.Filterable && computed && !skipFilter && !setOf
			c.Orderable = c.Orderable && computed && !skipOrder && !setOf
		}
	}
	return nil
}

// omits reports which halves a comment removes: a bare @omit or the
// filter/order actions, or — in opt-in mode — the absence of
// @filterable/@sortable.
func omits(comment string, optIn bool) (skipFilter, skipOrder bool) {
	tags, _ := smarttags.Parse(comment)
	actions, everything := tags.Omits()
	optedOut := optIn && !tags.Has("filterable") && !tags.Has("sortable")
	skipFilter = everything || optedOut || actions["filter"]
	skipOrder = everything || optedOut || actions["order"]
	return skipFilter, skipOrder
}
