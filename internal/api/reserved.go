package api

import "strings"

// ReservedPrefix starts every name pdbr generates beside the catalog's own:
// the reference query parameters (~near, ~trgm, ~vec, ~latlon,
// ~levenshtein, ~geometry) and every pseudo-column key (~location_area,
// ~title_trgm_similarity, ...). A query parameter or select, filter or
// order name with the prefix always resolves in that namespace and never
// to a column, so adding a column or function cannot change what a request
// means.
const ReservedPrefix = "~"

// The reference query parameters: request-wide values that distance order
// terms, score pseudo-columns and distance thresholds measure against, and
// the Feature geometry of an application/geo+json response.
const (
	ParamNear        = ReservedPrefix + "near"
	ParamTrgm        = ReservedPrefix + "trgm"
	ParamVec         = ReservedPrefix + "vec"
	ParamLatLon      = ReservedPrefix + "latlon"
	ParamLevenshtein = ReservedPrefix + "levenshtein"
	ParamGeometry    = ReservedPrefix + "geometry"
)

// ReferenceParams lists the reserved query parameters in documentation
// order.
var ReferenceParams = []string{ParamNear, ParamTrgm, ParamVec, ParamLatLon, ParamLevenshtein, ParamGeometry}

// IsReserved reports whether a query parameter, select item, filter key or
// order term starts with ReservedPrefix.
func IsReserved(name string) bool { return strings.HasPrefix(name, ReservedPrefix) }

// PseudoKey is the key of the pseudo-column suffix derives from the field
// key: ReservedPrefix + field + "_" + suffix (~location_area).
func PseudoKey(field, suffix string) string {
	return ReservedPrefix + field + "_" + suffix
}

// SpatialDistance names the spatial distance from ~near=: the order term
// (order=<col>.st_distance) and the score pseudo-column suffix
// (~<col>_st_distance).
const SpatialDistance = "st_distance"
