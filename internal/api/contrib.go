package api

import (
	"encoding/json"
	"fmt"
	"slices"
	"strings"

	"github.com/suprbdev/pdbcore/contrib"
	"github.com/suprbdev/pdbcore/introspect"
)

// ContribFilter is one filter operator a column takes from a pdbcore/contrib
// vocabulary (full-text search, pg_trgm, unaccent, fuzzystrmatch, hstore,
// ltree, intarray, ranges, inet, pgvector, cube, earthdistance).
type ContribFilter struct {
	// Name is the operator's URL spelling (fts, trgm_sim, cs, ...).
	Name string
	// Op is the vocabulary operator, bound to its extension's schema.
	Op contrib.Op
	// Vocabulary is the vocabulary Op belongs to.
	Vocabulary contrib.Vocabulary
}

// ContribOrder is one order=<col>.<Name> term a column takes from a
// pdbcore/contrib vocabulary: full-text relevance (rank, rank_cd), a
// trigram distance (trgm_distance, trgm_word_distance,
// trgm_strict_word_distance), a pgvector distance (l2_distance,
// negative_inner_product, cosine_distance, l1_distance, hamming_distance,
// jaccard_distance), a cube distance (l2_distance, l1_distance,
// chebyshev_distance), an earthdistance distance (earth_distance on earth
// columns, earth_miles_distance on point columns) or the Levenshtein
// distance (levenshtein_distance).
type ContribOrder struct {
	// Name is the order modifier as written after the column.
	Name string
	// Rank is the relevance function of a rank term; "" for a distance.
	Rank contrib.Rank
	// Expr is the distance expression of a trigram or vector term.
	Expr contrib.Expr
	// Param is the query parameter holding the reference value a distance
	// term measures against: ParamTrgm, ParamVec, ParamLatLon or
	// ParamLevenshtein; "" for a rank term.
	Param string
}

type spelling struct{ url, op string }

var ftsSpellings = []spelling{
	{"fts", "matches"}, {"plfts", "matchesPlain"}, {"phfts", "matchesPhrase"}, {"wfts", "matchesWebsearch"},
}

var rangeSpellings = []spelling{
	{"ov", "overlaps"}, {"cs", "contains"}, {"cd", "containedBy"},
	{"sl", "strictlyLeftOf"}, {"sr", "strictlyRightOf"},
	{"nxr", "notExtendsRightOf"}, {"nxl", "notExtendsLeftOf"}, {"adj", "adjacentTo"},
}

var ltreeSpellings = []spelling{
	{"cs", "ancestorOf"}, {"cd", "descendantOf"},
	{"ancestor", "ancestorOf"}, {"descendant", "descendantOf"},
	{"ancestor_any", "ancestorOfAny"}, {"descendant_any", "descendantOfAny"},
	{"lquery", "matchesLquery"}, {"lquery_any", "matchesAnyLquery"}, {"ltxtquery", "matchesLtxtquery"},
}

// contribSpellings maps each vocabulary to its operators' URL spellings in
// documentation order. PostgREST spells operators after the SQL operator
// they render (cs is @> on every type), so cs/cd on ltree are aliases of
// ancestor/descendant and ov on inet is &&.
var contribSpellings = map[string][]spelling{
	contrib.VocabFullText:   ftsSpellings,
	contrib.VocabTextSearch: ftsSpellings,
	contrib.VocabJSONSearch: ftsSpellings,
	contrib.VocabTrgm: {
		{"trgm_sim", "similarTo"}, {"trgm_wsim", "wordSimilarTo"}, {"trgm_swsim", "strictWordSimilarTo"},
	},
	contrib.VocabHstore: {
		{"hk", "hasKey"}, {"hk_all", "hasAllKeys"}, {"hk_any", "hasAnyKeys"}, {"cs", "contains"}, {"cd", "containedBy"},
		{"hk_def", "hasDefinedKey"},
	},
	contrib.VocabLtree: ltreeSpellings,
	contrib.VocabLtreeArray: {
		{"ancestor", "ancestorOf"}, {"descendant", "descendantOf"},
		{"lquery", "matchesLquery"}, {"lquery_any", "matchesAnyLquery"}, {"ltxtquery", "matchesLtxtquery"},
	},
	contrib.VocabRange:      rangeSpellings,
	contrib.VocabMultirange: rangeSpellings,
	contrib.VocabInet: {
		{"cs", "containsOrEquals"}, {"cd", "containedByOrEquals"}, {"ov", "containsOrContainedBy"},
		{"cs_strict", "contains"}, {"cd_strict", "containedBy"},
	},
	contrib.VocabUnaccent: {
		{"unaccent_eq", "unaccentEqualTo"}, {"unaccent_ilike", "unaccentLike"},
		{"unaccent_sw", "unaccentStartsWith"}, {"unaccent_includes", "unaccentIncludes"},
	},
	contrib.VocabVector: {
		{"l2_distance_lt", "l2DistanceLessThan"}, {"negative_inner_product_lt", "negativeInnerProductLessThan"},
		{"cosine_distance_lt", "cosineDistanceLessThan"}, {"l1_distance_lt", "l1DistanceLessThan"},
	},
	contrib.VocabBit: {
		{"hamming_distance_lt", "hammingDistanceLessThan"}, {"jaccard_distance_lt", "jaccardDistanceLessThan"},
	},
	contrib.VocabFuzzy: {
		{"levenshtein_distance_lt", "levenshteinDistanceLessThan"},
		{"soundex", "soundexEqualTo"}, {"metaphone", "metaphoneEqualTo"}, {"dmetaphone", "doubleMetaphoneEqualTo"},
	},
	contrib.VocabIntarray: {{"query_int", "matchesQueryInt"}},
	contrib.VocabCube: {
		{"cs", "contains"}, {"cd", "containedBy"}, {"ov", "overlaps"},
		{"l2_distance_lt", "l2DistanceLessThan"}, {"l1_distance_lt", "l1DistanceLessThan"},
		{"chebyshev_distance_lt", "chebyshevDistanceLessThan"},
	},
	contrib.VocabEarth:      {{"earth_distance_lt", "earthDistanceLessThan"}},
	contrib.VocabEarthPoint: {{"earth_miles_distance_lt", "earthDistanceMilesLessThan"}},
}

var trgmOrders = []spelling{{"trgm_distance", "distance"}, {"trgm_word_distance", "wordDistance"}, {"trgm_strict_word_distance", "strictWordDistance"}}

var vecOrders = []spelling{
	{"l2_distance", "l2Distance"}, {"negative_inner_product", "negativeInnerProduct"}, {"cosine_distance", "cosineDistance"}, {"l1_distance", "l1Distance"},
}

var bitOrders = []spelling{{"hamming_distance", "hammingDistance"}, {"jaccard_distance", "jaccardDistance"}}

var fuzzyOrders = []spelling{{"levenshtein_distance", "levenshteinDistance"}}

var cubeOrders = []spelling{{"l2_distance", "l2Distance"}, {"l1_distance", "l1Distance"}, {"chebyshev_distance", "chebyshevDistance"}}

// distanceOrders maps each vocabulary with distance order terms to them and
// to the query parameter holding their reference value.
var distanceOrders = []struct {
	vocab, param string
	terms        []spelling
}{
	{contrib.VocabTrgm, ParamTrgm, trgmOrders},
	{contrib.VocabVector, ParamVec, vecOrders},
	{contrib.VocabBit, ParamVec, bitOrders},
	{contrib.VocabFuzzy, ParamLevenshtein, fuzzyOrders},
	{contrib.VocabCube, ParamVec, cubeOrders},
	{contrib.VocabEarth, ParamLatLon, []spelling{{"earth_distance", "earthDistance"}}},
	{contrib.VocabEarthPoint, ParamLatLon, []spelling{{"earth_miles_distance", "earthDistanceMiles"}}},
}

var rankOrders = []struct {
	name string
	rank contrib.Rank
}{{"rank", contrib.RankFrequency}, {"rank_cd", contrib.RankCoverDensity}}

// ContribFilterNames is every URL operator spelling a contrib vocabulary can
// contribute, sorted.
func ContribFilterNames() []string {
	seen := map[string]bool{}
	for _, sps := range contribSpellings {
		for _, sp := range sps {
			seen[sp.url] = true
		}
	}
	return sortedKeys(seen)
}

// IsFullTextOp reports whether name is one of the full-text operators
// (fts, plfts, phfts, wfts), the only ones taking a (config) argument.
func IsFullTextOp(name string) bool {
	for _, sp := range ftsSpellings {
		if sp.url == name {
			return true
		}
	}
	return false
}

// IsContribOrder reports whether a modifier is a contrib order term: rank,
// rank_cd or one of the <metric>_distance terms (trgm_distance,
// l2_distance, negative_inner_product, earth_distance, ...).
func IsContribOrder(name string) bool {
	for _, r := range rankOrders {
		if r.name == name {
			return true
		}
	}
	return DistanceParam(name) != ""
}

// DistanceParam returns the query parameter a distance order modifier
// measures against (ParamTrgm for the trigram distances, ParamVec for the
// pgvector and cube ones, ParamLatLon for earth_distance and
// earth_miles_distance, ParamLevenshtein for levenshtein_distance); "" for
// anything else.
func DistanceParam(mod string) string {
	for _, d := range distanceOrders {
		for _, sp := range d.terms {
			if sp.url == mod {
				return d.param
			}
		}
	}
	return ""
}

// VectorVocabulary returns the pgvector vocabulary of a vector, halfvec or
// sparsevec column (bound to the extension's schema); false for any other
// column, and when the catalog does not record the vector extension.
func VectorVocabulary(cat *introspect.Catalog, c *introspect.Column) (contrib.Vocabulary, bool) {
	if c == nil || c.IsArray || strings.HasPrefix(c.PGType, "_") {
		return contrib.Vocabulary{}, false
	}
	for _, v := range contrib.ForColumn(cat, c) {
		if v.Name == contrib.VocabVector {
			return v, true
		}
	}
	return contrib.Vocabulary{}, false
}

// DistanceVocabulary returns the vocabulary holding a column's distances
// from ~vec=: the vector vocabulary of a vector, halfvec or sparsevec
// column, pgvector's bit vocabulary of a bit or varbit column, or the cube
// vocabulary of a cube column.
func DistanceVocabulary(cat *introspect.Catalog, c *introspect.Column) (contrib.Vocabulary, bool) {
	if c == nil || c.IsArray || strings.HasPrefix(c.PGType, "_") {
		return contrib.Vocabulary{}, false
	}
	for _, v := range contrib.ForColumn(cat, c) {
		if v.Name == contrib.VocabVector || v.Name == contrib.VocabBit || v.Name == contrib.VocabCube {
			return v, true
		}
	}
	return contrib.Vocabulary{}, false
}

// IsVectorThreshold reports whether a filter operator spelling is a
// pgvector or cube distance threshold (l2_distance_lt,
// cosine_distance_lt, hamming_distance_lt, chebyshev_distance_lt, ...),
// which measures against ~vec=.
func IsVectorThreshold(name string) bool { return ThresholdParam(name) == ParamVec }

// ThresholdParam returns the query parameter a distance-threshold filter
// operator measures against: ParamVec for the pgvector and cube
// thresholds, ParamLatLon for earth_distance_lt and
// earth_miles_distance_lt, ParamLevenshtein for levenshtein_distance_lt;
// "" for every other operator.
func ThresholdParam(name string) string {
	switch name {
	case "levenshtein_distance_lt":
		return ParamLevenshtein
	case "earth_distance_lt", "earth_miles_distance_lt":
		return ParamLatLon
	}
	for _, v := range []string{contrib.VocabVector, contrib.VocabBit, contrib.VocabCube} {
		for _, sp := range contribSpellings[v] {
			if sp.url == name && strings.HasSuffix(name, "_lt") {
				return ParamVec
			}
		}
	}
	return ""
}

// scoreNames are the score pseudo-column suffixes that are not their
// pdbcore expression name in snake_case: the trigram similarities carry
// the trgm_ metric prefix of their distance order terms, and the miles
// distance follows the <metric>_distance pattern.
var scoreNames = map[string]string{
	"similarity":           "trgm_similarity",
	"wordSimilarity":       "trgm_word_similarity",
	"strictWordSimilarity": "trgm_strict_word_similarity",
	"earthDistanceMiles":   "earth_miles_distance",
}

// ScoreName is the pseudo-column suffix of a score expression: its
// pdbcore name in snake_case (cosineDistance -> cosine_distance), with the
// trigram similarities prefixed trgm_ (similarity -> trgm_similarity) and
// earthDistanceMiles spelled earth_miles_distance, so every distance
// score is named like its order term.
func ScoreName(expr string) string {
	if n, ok := scoreNames[expr]; ok {
		return n
	}
	return ScoreKey(expr)
}

// ScoreKey is an expression name in snake_case (cosineDistance ->
// cosine_distance, l2Distance -> l2_distance).
func ScoreKey(expr string) string {
	var sb strings.Builder
	for _, ch := range expr {
		if ch >= 'A' && ch <= 'Z' {
			sb.WriteByte('_')
			sb.WriteRune(ch - 'A' + 'a')
			continue
		}
		sb.WriteRune(ch)
	}
	return sb.String()
}

// addContribScores registers the score pseudo-columns of every resource's
// columns: ~<key>_trgm_similarity, ~<key>_trgm_word_similarity and
// ~<key>_trgm_strict_word_similarity (pg_trgm, against ~trgm=) on text
// columns the filter policy admits trigram operators on,
// ~<key>_levenshtein_distance (fuzzystrmatch, against ~levenshtein=) on
// text columns it admits the fuzzystrmatch operators on, ~<key>_<distance>
// (pgvector, against ~vec=) on every vector column and on bit columns an
// index (or the policy) admits pgvector's distances on, ~<key>_l2_distance,
// ~<key>_l1_distance and ~<key>_chebyshev_distance (cube, against ~vec=) on
// every cube column, ~<key>_earth_distance (earthdistance, metres from
// ~latlon=) on every earth column and ~<key>_earth_miles_distance
// (earthdistance, statute miles from ~latlon=) on point columns the policy
// admits unindexed surface on (no index serves <@>). It runs in Build,
// after the API hooks removed hidden columns.
func (b *Builder) addContribScores() {
	for _, r := range sortedResources(b.Resources) {
		for _, f := range r.Columns {
			for _, v := range contrib.ForColumn(b.Catalog, f.Column) {
				param, desc := "", ""
				switch v.Name {
				case contrib.VocabTrgm:
					if len(v.Ops) == 0 || !indexServes(b.Options, r, f.Column, v.Ops[0].Index, false) {
						continue
					}
					param, desc = ParamTrgm, "pg_trgm score against ~trgm=, 0 to 1 (higher is more similar)"
				case contrib.VocabVector, contrib.VocabBit:
					if v.Name == contrib.VocabBit {
						var hints []contrib.IndexHint
						for _, e := range v.Exprs {
							hints = append(hints, e.Index...)
						}
						if !indexServes(b.Options, r, f.Column, hints, true) {
							continue
						}
					}
					param, desc = ParamVec, "pgvector distance from ~vec= (lower is closer)"
				case contrib.VocabFuzzy:
					if !indexServes(b.Options, r, f.Column, nil, false) {
						continue
					}
					param, desc = ParamLevenshtein, "Levenshtein edit distance from ~levenshtein= (lower is closer)"
				case contrib.VocabCube:
					param, desc = ParamVec, "cube distance from ~vec= (lower is closer)"
				case contrib.VocabEarth:
					param, desc = ParamLatLon, "metres from ~latlon=<lat>,<lon> (lower is closer)"
				case contrib.VocabEarthPoint:
					if !indexServes(b.Options, r, f.Column, nil, false) {
						continue
					}
					param, desc = ParamLatLon, "statute miles from ~latlon=<lat>,<lon> (lower is closer)"
				default:
					continue
				}
				for _, e := range v.Exprs {
					if !e.Score {
						continue
					}
					key := PseudoKey(f.Key, ScoreName(e.Name))
					if r.Pseudo[key] != nil {
						b.Warn("contrib: score pseudo-column skipped: the key is taken", "resource", r.Path, "column", f.Column.Name, "key", key)
						continue
					}
					if r.Pseudo == nil {
						r.Pseudo = map[string]*PseudoColumn{}
					}
					typ := "number"
					if e.Type == "int4" {
						typ = "integer"
					}
					r.Pseudo[key] = &PseudoColumn{
						Key: key, Column: f.Column,
						Score:       &Score{Param: param, Expr: e},
						Schema:      &Schema{Type: typ},
						Description: strings.TrimSuffix(e.Description, ".") + "; " + desc + ", required to select it.",
					}
				}
			}
		}
	}
}

// VectorOperand decodes a vector given as query text: a JSON array of
// numbers becomes a []any of json.Number for the vector codec, anything
// else stays the pgvector text form it is.
func VectorOperand(text string) (any, error) {
	if !strings.HasPrefix(strings.TrimSpace(text), "[") {
		return text, nil
	}
	dec := json.NewDecoder(strings.NewReader(text))
	dec.UseNumber()
	var list []any
	if err := dec.Decode(&list); err != nil || dec.More() {
		return nil, fmt.Errorf("vector value %q is not a JSON array of numbers", text)
	}
	return list, nil
}

// RangeOperand reports whether a cs operand on a range column is a range
// (a literal such as [1,5) or empty, or a structured object) rather than an
// element value, which compiles to element containment.
func RangeOperand(v any) bool {
	switch x := v.(type) {
	case contrib.RangeValue, map[string]any:
		return true
	case string:
		t := strings.TrimSpace(x)
		return strings.EqualFold(t, "empty") || strings.HasPrefix(t, "[") || strings.HasPrefix(t, "(") || strings.HasPrefix(t, "{")
	}
	return false
}

// OwnerVocabulary returns the extension vocabulary that owns a scalar
// column's type (citext, hstore, ltree): comparisons on it must be spelled
// with Vocabulary.Operator and the operand cast to the column type, or
// PostgreSQL resolves them to the wrong operator (citext) or none at all
// (hstore, ltree) when the extension schema is off the search_path.
// pgvector, cube and earth types are not owned this way: they take no
// comparisons (DistanceOnly).
func OwnerVocabulary(cat *introspect.Catalog, c *introspect.Column) (contrib.Vocabulary, bool) {
	if c == nil || c.IsArray || strings.HasPrefix(c.PGType, "_") {
		return contrib.Vocabulary{}, false
	}
	for _, v := range contrib.ForColumn(cat, c) {
		if v.Extension != "" && v.Codec != nil && !distanceOnly(v) {
			return v, true
		}
	}
	return contrib.Vocabulary{}, false
}

// distanceOnly reports whether a vocabulary's type takes no comparisons:
// its values are filtered by their own operators and ordered by distance
// only (pgvector's vector types, cube, earthdistance's earth).
func distanceOnly(v contrib.Vocabulary) bool {
	switch v.Name {
	case contrib.VocabVector, contrib.VocabCube, contrib.VocabEarth:
		return true
	}
	return false
}

// DistanceOnly returns the vocabulary of a column whose type takes no
// comparisons, plain ordering, distinct or min/max: a pgvector vector,
// halfvec or sparsevec, a cube or an earth column (false for any other
// column). Such columns are filtered with is.null and their vocabulary's
// operators and ordered by distance only.
func DistanceOnly(cat *introspect.Catalog, c *introspect.Column) (contrib.Vocabulary, bool) {
	if c == nil || c.IsArray || strings.HasPrefix(c.PGType, "_") {
		return contrib.Vocabulary{}, false
	}
	for _, v := range contrib.ForColumn(cat, c) {
		if distanceOnly(v) {
			return v, true
		}
	}
	return contrib.Vocabulary{}, false
}

// CodecFor returns the value codec of a column whose JSON input differs from
// its text form (hstore objects, structured ranges and multiranges, vectors
// as lists of numbers, cubes as lists of coordinates, earth locations as
// {lat, lon} objects).
func CodecFor(cat *introspect.Catalog, c *introspect.Column) (*contrib.Codec, bool) {
	if c == nil || c.IsArray || strings.HasPrefix(c.PGType, "_") {
		return nil, false
	}
	for _, v := range contrib.ForColumn(cat, c) {
		if v.Codec == nil {
			continue
		}
		switch v.Codec.Arg {
		case contrib.ArgHstore, contrib.ArgRange, contrib.ArgMultirange, contrib.ArgVector, contrib.ArgCube, contrib.ArgLatLon:
			return v.Codec, true
		}
	}
	return nil, false
}

// ContribFilterFor resolves the contrib operator a filter spelled name on c
// with operand v compiles to, ignoring the filter policy (the parser applies
// it). cs on a range column with an element operand resolves to element
// containment.
func ContribFilterFor(cat *introspect.Catalog, c *introspect.Column, name string, v any) (ContribFilter, bool) {
	for _, voc := range contrib.ForColumn(cat, c) {
		for _, sp := range contribSpellings[voc.Name] {
			if sp.url != name {
				continue
			}
			op := sp.op
			if name == "cs" && (voc.Name == contrib.VocabRange || voc.Name == contrib.VocabMultirange) && !RangeOperand(v) {
				op = "containsElement"
			}
			if o, ok := voc.Op(op); ok {
				return ContribFilter{Name: name, Op: o, Vocabulary: voc}, true
			}
		}
	}
	return ContribFilter{}, false
}

// ContribFilters returns the contrib filter operators column c of r accepts
// under the filter policy, in documentation order. With
// filters.indexed_only an operator needs an index on the column whose
// access method serves it (pdbcore's index hints; operator classes are not
// introspected) unless the column is listed in filters.allow_columns.
func (b *Built) ContribFilters(r *Resource, c *introspect.Column) []ContribFilter {
	return contribFilters(b.Catalog, b.Options, r, c)
}

// ContribOrders returns the contrib order terms column c of r accepts under
// the filter policy: rank and rank_cd wherever a full-text operator is
// accepted, trigram distances where an ordering index (GiST) serves them,
// vector distances where an HNSW or IVFFlat index does, or anywhere when
// filters.indexed_only is off.
func (b *Built) ContribOrders(r *Resource, c *introspect.Column) []ContribOrder {
	return contribOrders(b.Catalog, b.Options, r, c)
}

func contribFilters(cat *introspect.Catalog, opts Options, r *Resource, c *introspect.Column) []ContribFilter {
	var out []ContribFilter
	seen := map[string]bool{}
	for _, voc := range contrib.ForColumn(cat, c) {
		for _, sp := range contribSpellings[voc.Name] {
			o, ok := voc.Op(sp.op)
			hints := o.Index
			if voc.Name == contrib.VocabTextSearch || voc.Name == contrib.VocabJSONSearch {
				hints = nil
			}
			if !ok || seen[sp.url] || !indexServes(opts, r, c, hints, false) {
				continue
			}
			seen[sp.url] = true
			out = append(out, ContribFilter{Name: sp.url, Op: o, Vocabulary: voc})
		}
	}
	return out
}

func contribOrders(cat *introspect.Catalog, opts Options, r *Resource, c *introspect.Column) []ContribOrder {
	var out []ContribOrder
	for _, f := range contribFilters(cat, opts, r, c) {
		if IsFullTextOp(f.Name) {
			for _, ro := range rankOrders {
				out = append(out, ContribOrder{Name: ro.name, Rank: ro.rank})
			}
			break
		}
	}
	for _, voc := range contrib.ForColumn(cat, c) {
		for _, d := range distanceOrders {
			if voc.Name != d.vocab {
				continue
			}
			for _, sp := range d.terms {
				e, ok := voc.Expr(sp.op)
				if ok && indexServes(opts, r, c, e.Index, true) {
					out = append(out, ContribOrder{Name: sp.url, Expr: e, Param: d.param})
				}
			}
		}
	}
	return out
}

// indexServes applies filters.indexed_only to a contrib operator or
// expression: some index of the table must cover the column with one of the
// hinted access methods and, where the hint and the catalog both name one,
// operator classes (ordering hints for ORDER BY, predicate hints
// otherwise). Expression indexes carry no columns, so full-text search on a
// text, json or jsonb column (served only by an index on to_tsvector) needs
// filters.allow_columns.
func indexServes(opts Options, r *Resource, c *introspect.Column, hints []contrib.IndexHint, ordering bool) bool {
	if !opts.FilterIndexedOnly || r == nil || r.Table == nil {
		return true
	}
	t := r.Table
	if slices.Contains(opts.AllowColumns[t.Schema+"."+t.Name], c.Name) {
		return true
	}
	for _, h := range hints {
		if h.Ordering != ordering {
			continue
		}
		for _, ix := range t.Indexes {
			if i := slices.Index(ix.Columns, c.Name); i >= 0 && h.Serves(ix.Method, ix.OpClass(i)) {
				return true
			}
		}
	}
	return false
}

// contribPseudo is one accessor or derived value a vocabulary exposes as a
// pseudo-column: the accessor or expression name and, for a derived value,
// its select arguments.
type contribPseudo struct {
	vocab, name string
	derived     bool
	args        []ContribArg
}

// contribPseudos lists the exposed contrib accessors and derived values.
// The other vocabularies' accessors (inet family, range bounds, vector
// dims) are not exposed.
var contribPseudos = []contribPseudo{
	{vocab: contrib.VocabLtree, name: "nlevel"},
	{vocab: contrib.VocabLtree, name: "parent"},
	{vocab: contrib.VocabLtree, name: "lca", derived: true, args: []ContribArg{{Name: "path", Type: "string"}}},
	{vocab: contrib.VocabLtree, name: "subpath", derived: true,
		args: []ContribArg{{Name: "offset", Type: "integer"}, {Name: "length", Type: "integer", Optional: true}}},
	{vocab: contrib.VocabLtreeArray, name: "lca"},
	{vocab: contrib.VocabIntarray, name: "icount"},
}

// ContribArgSyntax renders the select syntax of a derived pseudo-column's
// arguments: "(path=<string>)", "(offset=<integer>[,length=<integer>])";
// "" without arguments.
func (c *ContribValue) ContribArgSyntax() string {
	if c == nil || len(c.Args) == 0 {
		return ""
	}
	var sb strings.Builder
	sb.WriteByte('(')
	for i, a := range c.Args {
		part := a.Name + "=<" + a.Type + ">"
		if i > 0 {
			part = "," + part
		}
		if a.Optional {
			part = "[" + part + "]"
		}
		sb.WriteString(part)
	}
	sb.WriteByte(')')
	return sb.String()
}

// Operand builds the derived expression's operand from the coerced select
// arguments: the value of a single argument, else an object of them.
func (c *ContribValue) Operand(args map[string]any) any {
	if len(c.Args) == 1 {
		return args[c.Args[0].Name]
	}
	m := make(map[string]any, len(args))
	for k, v := range args {
		m[k] = v
	}
	return m
}

// addContribPseudos registers the contrib accessor and derived
// pseudo-columns of every resource's columns: ~<key>_nlevel,
// ~<key>_parent, ~<key>_lca(path=) and ~<key>_subpath(offset=,length=) on
// ltree columns, ~<key>_lca on ltree[] columns and ~<key>_icount on int4[]
// columns with
// intarray installed. The integer accessors (nlevel, icount) also take
// filters and order terms where the column does and the filter policy
// admits them: no index serves them, so under filters.indexed_only only
// for columns in filters.allow_columns. It runs in Build, after the API
// hooks removed hidden columns and withdrew filter and order surface.
func (b *Builder) addContribPseudos() {
	for _, r := range sortedResources(b.Resources) {
		for _, f := range r.Columns {
			for _, v := range contrib.ForColumn(b.Catalog, f.Column) {
				for _, cp := range contribPseudos {
					if cp.vocab != v.Name {
						continue
					}
					cv := &ContribValue{Args: cp.args}
					typ, desc := "", ""
					if cp.derived {
						e, ok := v.Expr(cp.name)
						if !ok {
							continue
						}
						cv.Expr, typ, desc = &e, e.Type, e.Description
					} else {
						a, ok := v.Accessor(cp.name)
						if !ok {
							continue
						}
						cv.Accessor, typ, desc = &a, a.Type, a.Description
					}
					key := PseudoKey(f.Key, ScoreKey(cp.name))
					if r.Pseudo[key] != nil {
						b.Warn("contrib: pseudo-column skipped: the key is taken", "resource", r.Path, "column", f.Column.Name, "key", key)
						continue
					}
					pc := &PseudoColumn{Key: key, Column: f.Column, Contrib: cv, Schema: &Schema{Type: "string"}, Description: desc}
					if typ == "int4" {
						pc.Schema = &Schema{Type: "integer", Format: "int32"}
						unindexed := indexServes(b.Options, r, f.Column, nil, false)
						pc.Filterable = r.FilterColumns[f.Key] && unindexed
						pc.Orderable = r.OrderColumns[f.Key] && unindexed
					}
					if r.Pseudo == nil {
						r.Pseudo = map[string]*PseudoColumn{}
					}
					r.Pseudo[key] = pc
				}
			}
		}
	}
}

// headlineOptions maps the select arguments of a headline pseudo-column to
// pdbcore's headline option keys, in documentation order.
var headlineOptions = []spelling{
	{"max_words", "maxWords"}, {"min_words", "minWords"}, {"short_word", "shortWord"},
	{"max_fragments", "maxFragments"}, {"highlight_all", "highlightAll"},
	{"start_sel", "startSel"}, {"stop_sel", "stopSel"}, {"fragment_delimiter", "fragmentDelimiter"},
}

// HeadlineOption returns the pdbcore option key of a headline select
// argument, or "" when name is none.
func HeadlineOption(name string) string {
	for _, o := range headlineOptions {
		if o.url == name {
			return o.op
		}
	}
	return ""
}

// SelectArgSyntax renders the select argument syntax of a pseudo-column:
// a derived contrib value's, a headline's, a raster value's or statistics'
// optional band, or a raster envelope's optional precision; "" for the
// others.
func (pc *PseudoColumn) SelectArgSyntax() string {
	switch {
	case pc.Headline != nil:
		return pc.Headline.HeadlineArgSyntax()
	case pc.Raster.TakesBand():
		return "[(band=<integer>)]"
	case pc.Raster != nil && pc.Raster.Accessor != nil && pc.Raster.Accessor.GeoJSON:
		return "[(precision=<integer>)]"
	}
	return pc.Contrib.ContribArgSyntax()
}

// HeadlineArg returns the select argument spelling of a pdbcore headline
// option key (maxWords -> max_words).
func HeadlineArg(key string) string {
	for _, o := range headlineOptions {
		if o.op == key {
			return o.url
		}
	}
	return key
}

// HeadlineArgSyntax renders the select syntax of a headline pseudo-column:
// "(document=<a|b>[,<option>=<value>...])" on a tsvector column,
// "[(<option>=<value>,...)]" on a document column.
func (h *Headline) HeadlineArgSyntax() string {
	opts := make([]string, len(headlineOptions))
	for i, o := range headlineOptions {
		opts[i] = o.url
	}
	list := strings.Join(opts, "|")
	if h.Documents == nil {
		return "[(<" + list + ">=<value>,...)]"
	}
	return "(document=<" + strings.Join(sortedKeys(h.Documents), "|") + ">[,<" + list + ">=<value>...])"
}

// addContribHeadlines registers ~<key>_headline on every column that takes a
// full-text filter under the filter policy: a text, json or jsonb column
// highlights itself, a tsvector column the readable document column its
// document= select argument names (none is registered without one). It
// runs in Build, after the API hooks removed hidden columns and withdrew filter
// surface.
func (b *Builder) addContribHeadlines() {
	for _, r := range sortedResources(b.Resources) {
		var docs map[string]*introspect.Column
		for _, f := range r.Columns {
			if contrib.IsDocument(f.Column.PGType) && !f.Column.IsArray {
				if docs == nil {
					docs = map[string]*introspect.Column{}
				}
				docs[f.Key] = f.Column
			}
		}
		for _, f := range r.Columns {
			if !r.FilterColumns[f.Key] || !hasFullText(contribFilters(b.Catalog, b.Options, r, f.Column)) {
				continue
			}
			h := &Headline{}
			schema := &Schema{Type: "string"}
			desc := "ts_headline snippet of " + f.Column.Name + " where its full-text filter (fts, plfts, phfts, wfts) in the same scope matched"
			if !contrib.IsDocument(f.Column.PGType) {
				if len(docs) == 0 {
					continue
				}
				h.Documents = docs
				desc = "ts_headline snippet of the document= column where the full-text filter on " + f.Column.Name + " (fts, plfts, phfts, wfts) in the same scope matched"
			} else if !contrib.IsText(f.Column.PGType) {
				schema = nil
			}
			key := PseudoKey(f.Key, "headline")
			if r.Pseudo[key] != nil {
				b.Warn("contrib: headline pseudo-column skipped: the key is taken", "resource", r.Path, "column", f.Column.Name, "key", key)
				continue
			}
			if r.Pseudo == nil {
				r.Pseudo = map[string]*PseudoColumn{}
			}
			r.Pseudo[key] = &PseudoColumn{
				Key: key, Column: f.Column, Headline: h, Schema: schema,
				Description: desc + "; that filter is required to select it.",
			}
		}
	}
}

// hasFullText reports whether fs holds a full-text operator.
func hasFullText(fs []ContribFilter) bool {
	for _, f := range fs {
		if IsFullTextOp(f.Name) {
			return true
		}
	}
	return false
}
