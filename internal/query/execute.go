package query

import (
	"sort"
	"strings"

	"github.com/1morshed1/seekr/internal/index"
)

// Search parses queryStr, executes it against idx, and returns the top-K hits
// sorted by descending score. It holds the index read lock for the whole
// execution so the query sees a consistent snapshot.
//
// topK <= 0 means "return everything".
func Search(idx *index.InvertedIndex, queryStr string, topK int) ([]index.ScoredDoc, error) {
	q, err := Parse(queryStr)
	if err != nil {
		return nil, err
	}
	if q == nil {
		return nil, nil // empty query -> no results
	}

	var scores map[string]float64
	idx.WithRead(func() {
		scores = q.eval(idx)
	})
	return rank(scores, topK), nil
}

// rank converts a docID->score map into a slice sorted by score descending, with
// docID as a stable tie-breaker so results are deterministic. It truncates to
// topK when topK > 0.
func rank(scores map[string]float64, topK int) []index.ScoredDoc {
	out := make([]index.ScoredDoc, 0, len(scores))
	for id, s := range scores {
		out = append(out, index.ScoredDoc{DocID: id, Score: s})
	}
	sort.Slice(out, func(i, j int) bool {
		if out[i].Score != out[j].Score {
			return out[i].Score > out[j].Score
		}
		return out[i].DocID < out[j].DocID
	})
	if topK > 0 && len(out) > topK {
		out = out[:topK]
	}
	return out
}

// --- eval implementations ---

// eval for a term query: analyze the word the same way documents were indexed,
// then accumulate the BM25 score of every posting entry (optionally restricted to
// a field).
func (q *TermQuery) eval(idx *index.InvertedIndex) map[string]float64 {
	res := map[string]float64{}
	for _, term := range idx.Analyzer().AnalyzeToTerms(q.Term) {
		accumulateTerm(idx, term, q.Field, res)
	}
	return res
}

// eval for a prefix query: expand the prefix into every matching vocabulary term
// and accumulate their scores. This is effectively an OR over the expansion.
func (q *PrefixQuery) eval(idx *index.InvertedIndex) map[string]float64 {
	res := map[string]float64{}
	prefix := strings.ToLower(q.Prefix)
	for _, term := range idx.TermsWithPrefix(prefix) {
		accumulateTerm(idx, term, q.Field, res)
	}
	return res
}

// accumulateTerm adds the BM25 score of every entry in term's posting list into
// res, filtering by field when field != "".
func accumulateTerm(idx *index.InvertedIndex, term, field string, res map[string]float64) {
	pl := idx.PostingList(term)
	if pl == nil {
		return
	}
	df := pl.DocFrequency()
	for _, e := range pl.Entries {
		if field != "" && e.FieldFreqs[field] == 0 {
			continue // term didn't appear in the requested field for this doc
		}
		res[e.DocID] += idx.ScoreBM25(e, df)
	}
}

// termOffset pairs an analyzed phrase term with its position offset relative to
// the first term of the phrase. Analyzing the phrase (not just splitting it) means
// stop words inside the phrase are handled correctly: "king of england" analyzes
// to king@0, england@2 (the removed "of" leaves a gap), so we look for docs where
// "england" sits two positions after "king".
type termOffset struct {
	term   string
	offset int
}

// eval for a phrase query: find documents where the analyzed phrase terms occur
// at the right relative positions.
func (q *PhraseQuery) eval(idx *index.InvertedIndex) map[string]float64 {
	tokens := idx.Analyzer().Analyze(q.Phrase)
	if len(tokens) == 0 {
		return nil
	}
	// A single-term "phrase" is just a term query.
	if len(tokens) == 1 {
		tq := &TermQuery{Term: q.Phrase, Field: q.Field}
		return tq.eval(idx)
	}

	base := tokens[0].Position
	pattern := make([]termOffset, len(tokens))
	distinct := map[string]struct{}{}
	for i, t := range tokens {
		pattern[i] = termOffset{term: t.Term, offset: t.Position - base}
		distinct[t.Term] = struct{}{}
	}

	// Gather posting lists for the distinct terms; if any term is absent, the
	// phrase cannot match anything.
	lists := map[string]*index.PostingList{}
	for term := range distinct {
		pl := idx.PostingList(term)
		if pl == nil {
			return nil
		}
		lists[term] = pl
	}

	// Candidate docs = documents that contain the (rarest) anchor term. We use the
	// first term as the anchor and verify the full pattern per candidate.
	anchor := lists[pattern[0].term]
	res := map[string]float64{}
	for _, entry := range anchor.Entries {
		docID := entry.DocID
		if q.Field != "" && entry.FieldFreqs[q.Field] == 0 {
			continue
		}
		if phraseMatches(docID, pattern, lists, q.Field) {
			// Score the phrase as the sum of its terms' BM25 contributions.
			var score float64
			for term := range distinct {
				pl := lists[term]
				if e, ok := pl.GetEntry(docID); ok {
					score += idx.ScoreBM25(e, pl.DocFrequency())
				}
			}
			res[docID] = score
		}
	}
	return res
}

// phraseMatches reports whether docID contains the phrase pattern: there exists a
// start position p such that every (term, offset) has term at position p+offset.
func phraseMatches(docID string, pattern []termOffset, lists map[string]*index.PostingList, field string) bool {
	// positionsOf returns the sorted positions of a term within the doc.
	positionsOf := func(term string) []int {
		e, ok := lists[term].GetEntry(docID)
		if !ok {
			return nil
		}
		return e.Positions
	}

	anchorPositions := positionsOf(pattern[0].term)
	for _, start := range anchorPositions {
		ok := true
		for _, po := range pattern[1:] {
			want := start + po.offset
			if !containsInt(positionsOf(po.term), want) {
				ok = false
				break
			}
		}
		if ok {
			return true
		}
	}
	return false
}

// containsInt does a binary search over a sorted int slice (positions are stored
// in ascending order).
func containsInt(sorted []int, target int) bool {
	lo, hi := 0, len(sorted)
	for lo < hi {
		mid := (lo + hi) / 2
		switch {
		case sorted[mid] == target:
			return true
		case sorted[mid] < target:
			lo = mid + 1
		default:
			hi = mid
		}
	}
	return false
}

// eval for a boolean query, implementing Lucene-style Must/Should/MustNot.
func (q *BoolQuery) eval(idx *index.InvertedIndex) map[string]float64 {
	var result map[string]float64

	// 1. Must clauses: intersect candidate sets, sum scores.
	if len(q.Must) > 0 {
		for i, m := range q.Must {
			sub := m.eval(idx)
			if i == 0 {
				result = sub
				continue
			}
			result = intersect(result, sub)
		}
	}

	// 2. Should clauses.
	if len(q.Should) > 0 {
		should := map[string]float64{}
		for _, s := range q.Should {
			for id, sc := range s.eval(idx) {
				should[id] += sc
			}
		}
		if result == nil {
			// No Must clauses: Should defines the candidate set (OR).
			result = should
		} else {
			// With Must clauses present, Should only boosts already-selected docs.
			for id := range result {
				result[id] += should[id]
			}
		}
	}

	// 3. If there were neither Must nor Should clauses (a pure NOT query), start
	// from every document with a neutral score so exclusion has something to trim.
	if result == nil {
		result = map[string]float64{}
		for _, id := range idx.AllDocIDs() {
			result[id] = 0
		}
	}

	// 4. MustNot clauses: remove any matching document.
	for _, n := range q.MustNot {
		for id := range n.eval(idx) {
			delete(result, id)
		}
	}

	return result
}

// intersect keeps only docs present in both maps, summing their scores.
func intersect(a, b map[string]float64) map[string]float64 {
	out := map[string]float64{}
	// iterate the smaller map for efficiency
	if len(b) < len(a) {
		a, b = b, a
	}
	for id, sa := range a {
		if sb, ok := b[id]; ok {
			out[id] = sa + sb
		}
	}
	return out
}
