// Package advancedfilters is the built-in plugin that opens the relation and
// computed-column query surface: relation filters (rel.col=op.v, rel!none.,
// rel!every., rel=exists.true|false), related-column ordering
// (order=rel(col).asc) and computed-column filter/order. It implements only
// APIHook: it flips api.Builder.Surface and refines the per-relation /
// per-computed Filterable/Orderable flags; parse and the core compiler read
// those, so disabling the plugin removes the feature end to end.
//
// Per-object tuning is read straight from catalog comments with
// pdbcore/smarttags, so it works with the smart-comments plugin disabled:
// "@omit filter" / "@omit order" on a foreign key or computed function drop
// that half; with relations_opt_in / computed_opt_in only objects tagged
// "@filterable" (alias "@sortable") get any surface.
package advancedfilters

import (
	"context"

	"github.com/suprbdev/pdbcore/smarttags"

	"github.com/suprbdev/pdbr/internal/api"
)

// Plugin is the advanced-filters plugin; build it with New.
type Plugin struct {
	relations      bool
	computed       bool
	relationOrder  bool
	relationsOptIn bool
	computedOptIn  bool
}

// New builds the plugin from its plugins.settings.advanced-filters map.
// Defaults: relations, computed and relation_order on; opt-in modes off.
// Unknown keys and mistyped values are ignored.
func New(settings map[string]any) *Plugin {
	p := &Plugin{relations: true, computed: true, relationOrder: true}
	set := func(dst *bool, key string) {
		if v, ok := settings[key].(bool); ok {
			*dst = v
		}
	}
	set(&p.relations, "relations")
	set(&p.computed, "computed")
	set(&p.relationOrder, "relation_order")
	set(&p.relationsOptIn, "relations_opt_in")
	set(&p.computedOptIn, "computed_opt_in")
	return p
}

func (p *Plugin) Name() string  { return "advanced-filters" }
func (p *Plugin) Priority() int { return 150 }

// TransformAPI sets the Surface flags and narrows every relation's and
// computed column's Filterable/Orderable to what the settings and the
// object's comment allow. Flags already cleared by an earlier plugin
// (smart-comments' @omit filter|order|many) are never turned back on.
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
