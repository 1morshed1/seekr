package index

import "math"

// BM25 ("Best Matching 25") is the ranking function Elasticsearch and Lucene use
// by default. It scores how relevant a document is to a term, improving on plain
// TF-IDF in two ways that match human intuition:
//
//  1. Term-frequency saturation. The 10th occurrence of a word does not make a
//     document 10x more relevant than the 1st. BM25 lets the contribution of TF
//     level off, controlled by k1.
//  2. Document-length normalization. A term appearing in a 20-word tweet is a
//     stronger signal than the same term buried in a 5000-word article. BM25
//     discounts long documents, controlled by b.
//
// The score for one (term, document) pair is:
//
//	score = IDF(term) * TF_saturated(term, doc)
//
// and a multi-term query sums the per-term scores.
const (
	// k1 controls how quickly term frequency saturates. Typical range 1.2–2.0.
	// Lower = saturates faster (TF matters less beyond the first few hits).
	bm25K1 = 1.2
	// b controls how strongly document length is penalized, from 0 (no length
	// normalization) to 1 (full). 0.75 is the standard default.
	bm25B = 0.75
)

// IDF computes the inverse document frequency for a term with the given document
// frequency (df = number of docs containing it), using the BM25 variant:
//
//	idf = ln( 1 + (N - df + 0.5) / (df + 0.5) )
//
// Intuition: a term in *few* documents (small df) is rare and discriminating, so
// it gets a large IDF; a term in *most* documents (large df) is common and near
// useless, so its IDF approaches 0. The +0.5 smoothing and the +1 inside the log
// keep the value non-negative even for very common terms.
func (idx *InvertedIndex) IDF(docFrequency int) float64 {
	n := float64(len(idx.docs))
	df := float64(docFrequency)
	return math.Log(1 + (n-df+0.5)/(df+0.5))
}

// ScoreBM25 returns the BM25 contribution of a single posting entry for a term
// whose posting list has the given document frequency.
//
// It must be called with the index read lock held (query execution wraps the
// whole query in idx.WithRead), because it reads shared statistics.
func (idx *InvertedIndex) ScoreBM25(entry PostingEntry, docFrequency int) float64 {
	idf := idx.IDF(docFrequency)

	tf := float64(entry.TermFrequency)
	dl := float64(idx.docLength(entry.DocID))
	avgdl := idx.averageDocLength()

	// Guard against a degenerate empty index.
	if avgdl == 0 {
		return 0
	}

	// TF saturation with length normalization:
	//
	//   tf * (k1 + 1)
	//   ---------------------------------------------
	//   tf + k1 * (1 - b + b * (dl / avgdl))
	//
	// As tf -> infinity this approaches (k1 + 1), i.e. it is bounded — that is the
	// "saturation". The denominator grows for longer-than-average documents
	// (dl > avgdl), lowering their score.
	denom := tf + bm25K1*(1-bm25B+bm25B*(dl/avgdl))
	tfComponent := (tf * (bm25K1 + 1)) / denom

	return idf * tfComponent
}
