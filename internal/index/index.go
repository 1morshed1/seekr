package index

import (
	"sort"
	"sync"

	"github.com/1morshed1/seekr/internal/analysis"
)

// fieldPositionGap is the position offset inserted between fields when indexing a
// multi-field document. It ensures a phrase query cannot accidentally "match"
// across a field boundary — e.g. a title ending in "distributed" and a body
// starting with "systems" should not satisfy the phrase "distributed systems".
const fieldPositionGap = 1000

// InvertedIndex is a single shard's in-memory search index. It maps terms to
// posting lists, stores the original documents for retrieval, and tracks the
// statistics BM25 needs (document count, document lengths, average length).
//
// It is safe for concurrent use: reads take a read lock, mutations take a write
// lock.
type InvertedIndex struct {
	mu sync.RWMutex

	analyzer *analysis.Analyzer

	postings map[string]*PostingList // term -> posting list
	docs     map[string]Document     // doc ID -> original document (for hydration)

	// docLengths[docID] is the number of analyzed tokens in the document. BM25
	// uses it to penalize very long documents (which trivially contain more
	// terms). totalTokens is their sum, so averageDocLength is O(1).
	docLengths  map[string]int
	totalTokens int
}

// New creates an empty index using the given analyzer. Pass
// analysis.NewStandardAnalyzer() for the default pipeline.
func New(analyzer *analysis.Analyzer) *InvertedIndex {
	return &InvertedIndex{
		analyzer:   analyzer,
		postings:   make(map[string]*PostingList),
		docs:       make(map[string]Document),
		docLengths: make(map[string]int),
	}
}

// analyzeDocument runs every field through the analyzer and returns, per term,
// its total frequency, its positions (offset per field so phrases don't cross
// field boundaries), and its per-field frequencies. It also returns the total
// token count (the document length).
func (idx *InvertedIndex) analyzeDocument(doc Document) (map[string]*PostingEntry, int) {
	entries := make(map[string]*PostingEntry)
	totalTokens := 0

	// Analyze fields in a deterministic (sorted) order so positions are stable.
	fieldNames := make([]string, 0, len(doc.Fields))
	for f := range doc.Fields {
		fieldNames = append(fieldNames, f)
	}
	sort.Strings(fieldNames)

	base := 0 // running position offset across fields
	for _, field := range fieldNames {
		tokens := idx.analyzer.Analyze(doc.Fields[field])
		for _, tok := range tokens {
			totalTokens++
			pos := base + tok.Position
			e, ok := entries[tok.Term]
			if !ok {
				e = &PostingEntry{DocID: doc.ID, FieldFreqs: map[string]int{}}
				entries[tok.Term] = e
			}
			e.TermFrequency++
			e.Positions = append(e.Positions, pos)
			e.FieldFreqs[field]++
		}
		// Advance the base past this field plus a gap, so the next field's
		// positions are well separated.
		base += fieldMaxPosition(tokens) + fieldPositionGap
	}
	return entries, totalTokens
}

// fieldMaxPosition returns the highest position in a token slice, or 0.
func fieldMaxPosition(tokens []analysis.Token) int {
	max := 0
	for _, t := range tokens {
		if t.Position > max {
			max = t.Position
		}
	}
	return max
}

// IndexDocument adds (or replaces) a document in the index. If a document with
// the same ID already exists it is removed first, so re-indexing keeps all
// statistics correct.
func (idx *InvertedIndex) IndexDocument(doc Document) {
	idx.mu.Lock()
	defer idx.mu.Unlock()
	idx.indexLocked(doc)
}

// indexLocked is the lock-free core, callable from other locked methods (e.g.
// WAL replay).
func (idx *InvertedIndex) indexLocked(doc Document) {
	if _, exists := idx.docs[doc.ID]; exists {
		idx.deleteLocked(doc.ID)
	}

	entries, totalTokens := idx.analyzeDocument(doc)
	for term, e := range entries {
		pl, ok := idx.postings[term]
		if !ok {
			pl = &PostingList{Term: term}
			idx.postings[term] = pl
		}
		pl.insertEntry(*e)
	}

	idx.docs[doc.ID] = doc
	idx.docLengths[doc.ID] = totalTokens
	idx.totalTokens += totalTokens
}

// DeleteDocument removes a document and all of its posting entries.
func (idx *InvertedIndex) DeleteDocument(docID string) bool {
	idx.mu.Lock()
	defer idx.mu.Unlock()
	return idx.deleteLocked(docID)
}

// deleteLocked is the lock-free deletion core. It re-analyzes the stored document
// to learn which terms it contributed, then removes its entry from each of those
// posting lists (pruning any list that becomes empty).
func (idx *InvertedIndex) deleteLocked(docID string) bool {
	doc, exists := idx.docs[docID]
	if !exists {
		return false
	}
	entries, _ := idx.analyzeDocument(doc)
	for term := range entries {
		if pl, ok := idx.postings[term]; ok {
			pl.removeEntry(docID)
			if len(pl.Entries) == 0 {
				delete(idx.postings, term)
			}
		}
	}
	idx.totalTokens -= idx.docLengths[docID]
	delete(idx.docLengths, docID)
	delete(idx.docs, docID)
	return true
}

// --- Read accessors used by query execution and scoring ---

// DocCount returns the number of documents in the index (the "N" in BM25's IDF).
func (idx *InvertedIndex) DocCount() int {
	idx.mu.RLock()
	defer idx.mu.RUnlock()
	return len(idx.docs)
}

// GetDocument returns the stored document for hydration.
func (idx *InvertedIndex) GetDocument(docID string) (Document, bool) {
	idx.mu.RLock()
	defer idx.mu.RUnlock()
	d, ok := idx.docs[docID]
	return d, ok
}

// PostingList returns the posting list for a term (nil if the term is unknown).
// The returned pointer aliases internal state; callers must only read it while
// holding no expectation of mutation. Query execution holds the index read lock
// via WithRead.
func (idx *InvertedIndex) PostingList(term string) *PostingList {
	return idx.postings[term]
}

// TermsWithPrefix returns every indexed term that starts with prefix. Used by
// PrefixQuery ("deploy*"). This is a linear scan of the vocabulary; a production
// engine would use a trie or sorted-term binary search for O(log n) prefix seeks.
func (idx *InvertedIndex) TermsWithPrefix(prefix string) []string {
	var out []string
	for term := range idx.postings {
		if len(term) >= len(prefix) && term[:len(prefix)] == prefix {
			out = append(out, term)
		}
	}
	return out
}

// averageDocLength returns the mean document length (the "avgdl" in BM25). It is
// 0 when the index is empty.
func (idx *InvertedIndex) averageDocLength() float64 {
	if len(idx.docs) == 0 {
		return 0
	}
	return float64(idx.totalTokens) / float64(len(idx.docs))
}

// docLength returns the token count of a document (0 if unknown).
func (idx *InvertedIndex) docLength(docID string) int {
	return idx.docLengths[docID]
}

// WithRead runs fn while holding the index's read lock. Query execution uses this
// so an entire query sees a consistent snapshot of the index.
func (idx *InvertedIndex) WithRead(fn func()) {
	idx.mu.RLock()
	defer idx.mu.RUnlock()
	fn()
}

// Analyzer exposes the index's analyzer so the query layer can normalize query
// terms identically to how documents were indexed.
func (idx *InvertedIndex) Analyzer() *analysis.Analyzer {
	return idx.analyzer
}
