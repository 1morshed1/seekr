package index

import "sort"

// Snapshot is a complete, serializable copy of an index's state. The storage
// layer (internal/storage) turns a Snapshot into an on-disk SSTable and back,
// keeping the file-format concerns out of the index itself.
//
// It carries everything needed to fully reconstruct a working index: the term
// postings, the original documents (for hydration and for re-analysis on delete),
// each document's length, and the running total (so BM25's average-length stat is
// exact after a reload).
type Snapshot struct {
	Postings    map[string]*PostingList `json:"postings"`
	Documents   map[string]Document     `json:"documents"`
	DocLengths  map[string]int          `json:"doc_lengths"`
	TotalTokens int                     `json:"total_tokens"`
}

// SortedTerms returns the snapshot's terms in ascending order. The SSTable writer
// uses this so terms are stored sorted — that's the "Sorted" in Sorted String
// Table, and it's what makes the sparse block index usable for lookups.
func (s *Snapshot) SortedTerms() []string {
	terms := make([]string, 0, len(s.Postings))
	for t := range s.Postings {
		terms = append(terms, t)
	}
	sort.Strings(terms)
	return terms
}

// ExportSnapshot returns a deep copy of the index's current state, taken under the
// read lock so it is internally consistent (no half-applied document).
func (idx *InvertedIndex) ExportSnapshot() *Snapshot {
	idx.mu.RLock()
	defer idx.mu.RUnlock()

	snap := &Snapshot{
		Postings:    make(map[string]*PostingList, len(idx.postings)),
		Documents:   make(map[string]Document, len(idx.docs)),
		DocLengths:  make(map[string]int, len(idx.docLengths)),
		TotalTokens: idx.totalTokens,
	}
	for term, pl := range idx.postings {
		entries := make([]PostingEntry, len(pl.Entries))
		copy(entries, pl.Entries)
		snap.Postings[term] = &PostingList{Term: pl.Term, Entries: entries}
	}
	for id, doc := range idx.docs {
		snap.Documents[id] = doc
	}
	for id, l := range idx.docLengths {
		snap.DocLengths[id] = l
	}
	return snap
}

// ImportSnapshot replaces the index's entire state with the snapshot's contents.
// It is used at startup to load the most recent SSTable before replaying the WAL.
func (idx *InvertedIndex) ImportSnapshot(snap *Snapshot) {
	idx.mu.Lock()
	defer idx.mu.Unlock()

	idx.postings = make(map[string]*PostingList, len(snap.Postings))
	idx.docs = make(map[string]Document, len(snap.Documents))
	idx.docLengths = make(map[string]int, len(snap.DocLengths))
	idx.totalTokens = snap.TotalTokens

	for term, pl := range snap.Postings {
		entries := make([]PostingEntry, len(pl.Entries))
		copy(entries, pl.Entries)
		idx.postings[term] = &PostingList{Term: pl.Term, Entries: entries}
	}
	for id, doc := range snap.Documents {
		idx.docs[id] = doc
	}
	for id, l := range snap.DocLengths {
		idx.docLengths[id] = l
	}
}
