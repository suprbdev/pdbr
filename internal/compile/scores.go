package compile

import (
	"fmt"

	"github.com/suprbdev/pdbcore/contrib"
	"github.com/suprbdev/pdbcore/postgis"

	"github.com/suprbdev/pdbr/internal/api"
	"github.com/suprbdev/pdbr/internal/plan"
)

// scoreSQL renders a score pseudo-column of the row at alias against the
// request's reference parameter: ST_Distance from near= (metres on
// geography; on geometry SRID units, or metres on the spheroid with
// spheroid=true), a pg_trgm similarity to trgm=, a pgvector or cube
// distance from vec=, an earthdistance distance from latlon= or the
// Levenshtein distance to levenshtein=. The reference is bound again for
// each score, so the SQL text only depends on which scores are selected.
func (s *stmt) scoreSQL(p *api.PseudoColumn, args map[string]any, alias string) (string, error) {
	ref := colRef(alias, p.Column.Name)
	switch p.Score.Param {
	case "near":
		near := s.nearExpr(p.Column)
		if b, _ := args["spheroid"].(bool); b && postgis.FlavourOf(p.Column.PGType) == postgis.Geometry {
			return postgis.SpheroidDistanceSQL(ref, near), nil
		}
		return postgis.DistanceSQL(ref, near), nil
	case "trgm", "vec", "latlon", "levenshtein":
		val := s.plan.Trgm
		switch p.Score.Param {
		case "vec":
			val = s.plan.Vec
		case "latlon":
			val = s.plan.LatLon
		case "levenshtein":
			val = s.plan.Levenshtein
		}
		if val == nil {
			return "", errUser(CodeInvalidFilter, "%s requires %s=", p.Key, p.Score.Param)
		}
		params, err := p.Score.Expr.Params(val)
		if err != nil {
			return "", errUser(CodeInvalidFilter, "%s: %s", p.Score.Param, err.Error())
		}
		return p.Score.Expr.Render(ref, s.params(params)...), nil
	}
	return "", fmt.Errorf("compile: unknown score parameter %q", p.Score.Param)
}

// contribPseudoSQL renders a contrib accessor (<col>_nlevel, <col>_parent,
// <col>_lca, <col>_icount) or derived value (<col>_lca(path=),
// <col>_subpath(offset=,length=), whose arguments are bound) of the row at
// alias.
func (s *stmt) contribPseudoSQL(p *api.PseudoColumn, args map[string]any, alias string) (string, error) {
	ref := colRef(alias, p.Column.Name)
	cv := p.Contrib
	if cv.Accessor != nil {
		return cv.Accessor.SQL(ref), nil
	}
	params, err := cv.Expr.Params(cv.Operand(args))
	if err != nil {
		return "", errUser(CodeInvalidSelect, "%s: %s", p.Key, err.Error())
	}
	expr := cv.Expr.Render(ref, s.params(params)...)
	if expr == "" {
		return "", fmt.Errorf("compile: contrib pseudo-column %s rendered no SQL", p.Key)
	}
	return expr, nil
}

// headlineSQL renders a headline pseudo-column of the row at alias:
// ts_headline over its document against the query of the full-text
// filter of its scope (Op.HeadlineSQL over fresh placeholders for the
// same operand, like a rank term), with the options text bound as one
// parameter whether or not options were given.
func (s *stmt) headlineSQL(p *api.PseudoColumn, h *plan.Headline, alias string) (string, error) {
	missing := errUser(CodeInvalidFilter, "%s needs a non-negated fts, plfts, phfts or wfts filter on %s outside or=/not.", p.Key, p.Column.Name)
	if h == nil || h.Filter == nil || h.Document == nil {
		return "", missing
	}
	cf, ok := api.ContribFilterFor(s.catalog(), p.Column, string(h.Filter.Op), h.Filter.Value)
	if !ok || !api.IsFullTextOp(cf.Name) {
		return "", missing
	}
	params, err := cf.Op.Params(h.Filter.Value)
	if err != nil {
		return "", errUser(CodeInvalidFilter, "%s: %s", cf.Name, err.Error())
	}
	phs := s.params(params)
	doc := colRef(alias, h.Document.Name)
	if bounded := contrib.HeadlineDocumentSQL(doc, h.Document.PGType, s.param(int64(headlineDocument(h)))); bounded != "" {
		doc = bounded
	}
	expr := cf.Op.HeadlineSQL(doc, s.param(h.Options), phs...)
	if expr == "" {
		return "", fmt.Errorf("compile: %s rejected %d parameters", cf.Name, len(params))
	}
	return expr, nil
}
