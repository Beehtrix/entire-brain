package cli

import (
	"math"
	"strings"
	"time"
)

// BM25 replaces the flat term-overlap text score on the facts path. The flat
// scorer collapses at corpus scale: common terms match hundreds of facts and
// the discriminative rare term no longer decides the ordering. BM25's IDF
// weighting fixes exactly that, and the 2026-10 retrieval eval (eval ledger;
// trail #278) measured it matching or beating the semantic fusion arm across
// models and corpus sizes, which is why the embedder was removed.
const (
	bm25K1 = 1.2
	bm25B  = 0.75

	// Boost scales re-expressed for the BM25 range (one strong rare-term hit
	// contributes roughly idf ~= 4-7). The old int scorer weighted a locus
	// token at 6x a term hit and closed-negative at 3x; these keep those
	// proportions.
	bm25LocusBoost          = 8.0
	bm25ClosedNegativeBoost = 4.0
)

// factSearchTokens are the indexable tokens of one fact: its text plus its
// taxonomy paths (dots split so "architecture.sync.auth" matches "sync").
// Facts index BOTH plain lowercase tokens ("mysql") and the camel-split form
// the history normalizer produces ("my sql"), so a query matches whichever
// shape the asker types. The plain form is what the shipped-decision eval
// measured (trail #278); the camel-split additions only widen reachability.
func factSearchTokens(f factRecord) []string {
	haystack := f.Text + " " + strings.ReplaceAll(strings.Join(f.Paths, " "), ".", " ")
	tokens := factPlainTokens(haystack)
	seen := make(map[string]struct{}, len(tokens)*2)
	for _, t := range tokens {
		seen[t] = struct{}{}
	}
	for _, t := range strings.Fields(normalizeHistorySearchText(haystack)) {
		if _, ok := seen[t]; ok {
			continue
		}
		seen[t] = struct{}{}
		tokens = append(tokens, t)
	}
	return tokens
}

// factPlainTokens lowercases and splits on non-alphanumerics (underscore
// kept), the tokenization the retrieval eval measured.
func factPlainTokens(text string) []string {
	lower := strings.ToLower(text)
	return strings.FieldsFunc(lower, func(r rune) bool {
		return !(r >= 'a' && r <= 'z' || r >= '0' && r <= '9' || r == '_')
	})
}

// factQueryTerms are the deduplicated plain tokens of a query, with the
// history stopword list applied so "how does the" carries no weight.
func factQueryTerms(query string) []string {
	seen := map[string]struct{}{}
	var terms []string
	for _, t := range factPlainTokens(query) {
		if len(t) < 2 || historyQueryStopword(t) {
			continue
		}
		if _, ok := seen[t]; ok {
			continue
		}
		seen[t] = struct{}{}
		terms = append(terms, t)
	}
	return terms
}

// bm25Corpus holds the per-call corpus statistics BM25 needs. Recomputed per
// query over the candidate set — ponytail: O(corpus) per query, measured at
// well under a millisecond for 1,209 facts; an inverted index is the upgrade
// path if fact counts grow by orders of magnitude.
type bm25Corpus struct {
	docTokens [][]string
	df        map[string]int
	avgLen    float64
}

func newBM25Corpus(facts []factRecord) bm25Corpus {
	c := bm25Corpus{
		docTokens: make([][]string, len(facts)),
		df:        make(map[string]int, len(facts)*8),
	}
	total := 0
	for i, f := range facts {
		tokens := factSearchTokens(f)
		c.docTokens[i] = tokens
		total += len(tokens)
		seen := make(map[string]struct{}, len(tokens))
		for _, t := range tokens {
			if _, ok := seen[t]; ok {
				continue
			}
			seen[t] = struct{}{}
			c.df[t]++
		}
	}
	if len(facts) > 0 {
		c.avgLen = float64(total) / float64(len(facts))
	}
	return c
}

// bm25Score is the Okapi BM25 text relevance of candidate i for the query
// terms, over this corpus.
func (c bm25Corpus) bm25Score(i int, terms []string) float64 {
	tokens := c.docTokens[i]
	if len(tokens) == 0 || len(terms) == 0 {
		return 0
	}
	tf := make(map[string]int, len(tokens))
	for _, t := range tokens {
		tf[t]++
	}
	n := float64(len(c.docTokens))
	score := 0.0
	for _, term := range terms {
		f := float64(tf[term])
		if f == 0 {
			continue
		}
		idf := math.Log((n-float64(c.df[term])+0.5)/(float64(c.df[term])+0.5) + 1)
		norm := 1 - bm25B + bm25B*float64(len(tokens))/c.avgLen
		score += idf * f * (bm25K1 + 1) / (f + bm25K1*norm)
	}
	return score
}

// factBM25Score is the full facts ranking score: BM25 text relevance plus the
// two previously measured boosts (shared code locus, closed-negative kind).
// Boosts apply only to facts that already match, so neither creates a match on
// its own — same contract as the scorer this replaces.
func factBM25Score(f factRecord, c bm25Corpus, i int, terms []string, queryLocus []string) float64 {
	score := c.bm25Score(i, terms)
	if score <= 0 {
		return 0
	}
	if overlap := locusOverlapTokens(queryLocus, factLocusOf(f)); overlap > 0 {
		score += float64(overlap) * bm25LocusBoost
	}
	if factKindOrInferred(f) == factKindClosedNegative {
		score += bm25ClosedNegativeBoost
	}
	return score
}

// filterFacts narrows candidates by taxonomy path prefix, fact kind, and
// minimum update time. Zero values skip that filter. A path prefix matches a
// whole segment boundary: "architecture.sync" matches "architecture.sync" and
// "architecture.sync.auth", not "architecture.syncextra".
func filterFacts(facts []factRecord, pathPrefix, kind string, since time.Time) []factRecord {
	if pathPrefix == "" && kind == "" && since.IsZero() {
		return facts
	}
	out := make([]factRecord, 0, len(facts))
	for _, f := range facts {
		if kind != "" && f.Kind != kind {
			continue
		}
		if !since.IsZero() && f.UpdatedAt.Before(since) {
			continue
		}
		if pathPrefix != "" && !factMatchesPathPrefix(f, pathPrefix) {
			continue
		}
		out = append(out, f)
	}
	return out
}

func factMatchesPathPrefix(f factRecord, prefix string) bool {
	for _, p := range f.Paths {
		if p == prefix || strings.HasPrefix(p, prefix+".") {
			return true
		}
	}
	return false
}
