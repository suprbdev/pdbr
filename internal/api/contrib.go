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
// vocabulary (full-text search, pg_trgm, unaccent, hstore, ltree, ranges,
// inet).
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
// trigram distance (trgm_dist, trgm_wdist, trgm_swdist) or a pgvector
// distance (vec_l2, vec_ip, vec_cos, vec_l1).
type ContribOrder struct {
	// Name is the order modifier as written after the column.
	Name string
	// Rank is the relevance function of a rank term; "" for a distance.
	Rank contrib.Rank
	// Expr is the distance expression of a trigram or vector term.
	Expr contrib.Expr
	// Param is the query parameter holding the reference value a distance
	// term measures against: "trgm" or "vec"; "" for a rank term.
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
	contrib.VocabTrgm: {
		{"trgm_sim", "similarTo"}, {"trgm_wsim", "wordSimilarTo"}, {"trgm_swsim", "strictWordSimilarTo"},
	},
	contrib.VocabHstore: {
		{"hk", "hasKey"}, {"hk_all", "hasAllKeys"}, {"hk_any", "hasAnyKeys"}, {"cs", "contains"}, {"cd", "containedBy"},
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
		{"vec_l2_lt", "l2DistanceLessThan"}, {"vec_ip_lt", "negativeInnerProductLessThan"},
		{"vec_cos_lt", "cosineDistanceLessThan"}, {"vec_l1_lt", "l1DistanceLessThan"},
	},
	contrib.VocabBit: {
		{"vec_hamming_lt", "hammingDistanceLessThan"}, {"vec_jaccard_lt", "jaccardDistanceLessThan"},
	},
}

var trgmOrders = []spelling{{"trgm_dist", "distance"}, {"trgm_wdist", "wordDistance"}, {"trgm_swdist", "strictWordDistance"}}

var vecOrders = []spelling{
	{"vec_l2", "l2Distance"}, {"vec_ip", "negativeInnerProduct"}, {"vec_cos", "cosineDistance"}, {"vec_l1", "l1Distance"},
}

var bitOrders = []spelling{{"vec_hamming", "hammingDistance"}, {"vec_jaccard", "jaccardDistance"}}

// distanceOrders maps each vocabulary with distance order terms to them and
// to the query parameter holding their reference value.
var distanceOrders = []struct {
	vocab, param string
	terms        []spelling
}{
	{contrib.VocabTrgm, "trgm", trgmOrders},
	{contrib.VocabVector, "vec", vecOrders},
	{contrib.VocabBit, "vec", bitOrders},
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

// IsContribOrder reports whether a modifier is a contrib order term
// (rank, rank_cd, trgm_dist, trgm_wdist, trgm_swdist, vec_l2, vec_ip,
// vec_cos, vec_l1, vec_hamming, vec_jaccard).
func IsContribOrder(name string) bool {
	for _, r := range rankOrders {
		if r.name == name {
			return true
		}
	}
	return DistanceParam(name) != ""
}

// DistanceParam returns the query parameter a distance order modifier
// measures against ("trgm" for trgm_*, "vec" for vec_*); "" for anything
// else.
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

// DistanceVocabulary returns the vocabulary holding a column's pgvector
// distances: the vector vocabulary of a vector, halfvec or sparsevec
// column, or pgvector's bit vocabulary of a bit or varbit column.
func DistanceVocabulary(cat *introspect.Catalog, c *introspect.Column) (contrib.Vocabulary, bool) {
	if c == nil || c.IsArray || strings.HasPrefix(c.PGType, "_") {
		return contrib.Vocabulary{}, false
	}
	for _, v := range contrib.ForColumn(cat, c) {
		if v.Name == contrib.VocabVector || v.Name == contrib.VocabBit {
			return v, true
		}
	}
	return contrib.Vocabulary{}, false
}

// IsVectorThreshold reports whether a filter operator spelling is a
// pgvector distance threshold (vec_l2_lt, vec_cos_lt, vec_hamming_lt, ...),
// which measures against vec=.
func IsVectorThreshold(name string) bool {
	for _, v := range []string{contrib.VocabVector, contrib.VocabBit} {
		for _, sp := range contribSpellings[v] {
			if sp.url == name {
				return true
			}
		}
	}
	return false
}

// ScoreKey is the pseudo-column suffix of a score expression: its pdbcore
// name in snake_case (cosineDistance -> cosine_distance, l2Distance ->
// l2_distance).
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
// columns: <key>_similarity, <key>_word_similarity and
// <key>_strict_word_similarity (pg_trgm, against trgm=) on text columns the
// filter policy admits trigram operators on, and <key>_<distance>
// (pgvector, against vec=) on every vector column and on bit columns an
// index (or the policy) admits pgvector's distances on. A resource with a
// column or computed column named trgm (vec) has no trgm= (vec=) and gets
// none of its scores; a real, computed or other pseudo-column owning a key
// keeps it. It runs in Build, after the API hooks removed hidden columns.
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
					param, desc = "trgm", "pg_trgm score against trgm=, 0 to 1 (higher is more similar)"
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
					param, desc = "vec", "pgvector distance from vec= (lower is closer)"
				default:
					continue
				}
				if r.NameTaken(param) {
					continue
				}
				for _, e := range v.Exprs {
					if !e.Score {
						continue
					}
					key := f.Key + "_" + ScoreKey(e.Name)
					if r.Field(key) != nil || r.Computed[key] != nil || r.Pseudo[key] != nil {
						b.Warn("contrib: score pseudo-column skipped: the key is taken", "resource", r.Path, "column", f.Column.Name, "key", key)
						continue
					}
					if r.Pseudo == nil {
						r.Pseudo = map[string]*PseudoColumn{}
					}
					r.Pseudo[key] = &PseudoColumn{
						Key: key, Column: f.Column,
						Score:       &Score{Param: param, Expr: e},
						Schema:      &Schema{Type: "number"},
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
// (hstore, ltree) when the extension schema is off the search_path. pgvector
// types are not owned this way: they take no comparisons (VectorVocabulary).
func OwnerVocabulary(cat *introspect.Catalog, c *introspect.Column) (contrib.Vocabulary, bool) {
	if c == nil || c.IsArray || strings.HasPrefix(c.PGType, "_") {
		return contrib.Vocabulary{}, false
	}
	for _, v := range contrib.ForColumn(cat, c) {
		if v.Extension != "" && v.Codec != nil && v.Name != contrib.VocabVector {
			return v, true
		}
	}
	return contrib.Vocabulary{}, false
}

// CodecFor returns the value codec of a column whose JSON input differs from
// its text form (hstore objects, structured ranges and multiranges, vectors
// as lists of numbers).
func CodecFor(cat *introspect.Catalog, c *introspect.Column) (*contrib.Codec, bool) {
	if c == nil || c.IsArray || strings.HasPrefix(c.PGType, "_") {
		return nil, false
	}
	for _, v := range contrib.ForColumn(cat, c) {
		if v.Codec == nil {
			continue
		}
		switch v.Codec.Arg {
		case contrib.ArgHstore, contrib.ArgRange, contrib.ArgMultirange, contrib.ArgVector:
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
// filters.indexed_only is off — unless r has a column or computed column
// named trgm (vec), which takes the trgm= (vec=) parameter's place.
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
			if voc.Name == contrib.VocabTextSearch {
				hints = nil
			}
			if !ok || seen[sp.url] || !indexServes(opts, r, c, hints, false) {
				continue
			}
			if IsVectorThreshold(sp.url) && r != nil && r.NameTaken("vec") {
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
			if voc.Name != d.vocab || (r != nil && r.NameTaken(d.param)) {
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

// NameTaken reports whether r has a column or computed column named key; a
// soft-reserved query parameter (near, trgm, vec) of that name is then a
// filter.
func (r *Resource) NameTaken(key string) bool {
	return r.Field(key) != nil || r.Computed[key] != nil
}

// indexServes applies filters.indexed_only to a contrib operator or
// expression: some index of the table must cover the column with one of the
// hinted access methods (ordering hints for ORDER BY, predicate hints
// otherwise). Expression indexes carry no columns, so full-text search on a
// text column (served only by an index on to_tsvector) needs
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
			if ix.Method == h.Method && slices.Contains(ix.Columns, c.Name) {
				return true
			}
		}
	}
	return false
}
