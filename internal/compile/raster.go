package compile

import (
	"fmt"

	"github.com/suprbdev/pdbcore/postgis"

	"github.com/suprbdev/pdbr/internal/api"
	"github.com/suprbdev/pdbr/internal/plan"
)

// rasterPseudoSQL renders a raster pseudo-column of the row at alias: a
// header-only metadata accessor (the envelope with precision= decimal
// digits when given), the pixel value at ~near= or the summary statistics
// of a band. The band (default 1) and the point are bind parameters, so
// the SQL text does not depend on whether band= is given.
func (s *stmt) rasterPseudoSQL(p *api.PseudoColumn, args map[string]any, alias string) (string, error) {
	ref := colRef(alias, p.Column.Name)
	rv := p.Raster
	switch {
	case rv.Accessor != nil:
		digits, ok, err := s.precisionArg(args)
		switch {
		case err != nil:
			return "", err
		case !ok:
			return rv.Accessor.SQL(ref, postgis.Output{JSON: true}), nil
		case rv.Accessor.Suffix != "Envelope":
			return "", errUser(CodeInvalidSelect, "precision applies to GeoJSON values; %s is not one", p.Key)
		}
		return postgis.OutputSQL("ST_Envelope("+ref+")", postgis.Output{JSON: true, Precision: digits}), nil
	case rv.Value:
		if s.plan.Near == nil {
			return "", errUser(CodeInvalidParameter, "%s requires ~near=", p.Key)
		}
		geom, err := s.spatialOperand(postgis.Geometry, 0, s.plan.Near)
		if err != nil {
			return "", err
		}
		return postgis.RasterValueSQL(ref, geom, s.param(rasterBand(args))), nil
	case rv.SummaryStats:
		return postgis.RasterSummaryStatsJSONSQL(ref, s.param(rasterBand(args))), nil
	}
	return "", fmt.Errorf("compile: raster pseudo-column %s has no surface", p.Key)
}

// rasterBand is the band= select argument of a raster value or statistics
// pseudo-column, 1 when absent.
func rasterBand(args map[string]any) int64 {
	if n, ok := args["band"].(int64); ok {
		return n
	}
	return 1
}

// rasterCondSQL renders a filter on a raster column: is.null / not.is.null,
// or st_intersects with a geometry operand (bound once, without a typmod SRID:
// it must be in the raster's SRID).
func (s *stmt) rasterCondSQL(ref string, c *plan.Cond) (string, error) {
	switch c.Op {
	case plan.OpIs:
		return isSQL(ref, c.Value, c.Negate)
	case plan.OpRasterIntersects:
		geom, err := s.spatialOperand(postgis.Geometry, 0, c.Value)
		if err != nil {
			return "", err
		}
		return negate(postgis.RasterIntersectsSQL(ref, geom), c.Negate), nil
	}
	return "", errUser(CodeInvalidFilter, "operator %q is not valid for raster columns (st_intersects, is)", c.Op)
}
