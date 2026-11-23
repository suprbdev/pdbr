package compile

import (
	"fmt"

	"github.com/suprbdev/pdbcore/postgis"

	"github.com/suprbdev/pdbr/internal/api"
)

// scoreSQL renders a score pseudo-column of the row at alias against the
// request's reference parameter: ST_Distance from near= (metres on
// geography; on geometry SRID units, or metres on the spheroid with
// spheroid=true), a pg_trgm similarity to trgm= or a pgvector distance from
// vec=. The reference is bound again for each score, so the SQL text only
// depends on which scores are selected.
func (s *stmt) scoreSQL(p *api.PseudoColumn, args map[string]any, alias string) (string, error) {
	ref := colRef(alias, p.Column.Name)
	switch p.Score.Param {
	case "near":
		near := s.nearExpr(p.Column)
		if b, _ := args["spheroid"].(bool); b && postgis.FlavourOf(p.Column.PGType) == postgis.Geometry {
			return postgis.SpheroidDistanceSQL(ref, near), nil
		}
		return postgis.DistanceSQL(ref, near), nil
	case "trgm", "vec":
		val := s.plan.Trgm
		if p.Score.Param == "vec" {
			val = s.plan.Vec
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
