package index

// This file defines the inverted index's core data structures: posting lists and
// their entries. If you understand these, you understand how search engines find
// documents in O(matching-docs) time instead of scanning everything.
//
// A "forward index" maps document -> words:
//
//	doc1 -> {the, quick, brown, fox}
//	doc2 -> {the, lazy, dog}
//
// An "inverted index" flips that to map word -> documents:
//
//	the   -> [doc1, doc2]
//	quick -> [doc1]
//	fox   -> [doc1]
//	dog   -> [doc2]
//
// Now "find docs containing 'fox'" is a single map lookup, and "find docs with
// both 'quick' AND 'fox'" is an intersection of two short lists. That inversion
// is the entire trick behind fast search.

// PostingEntry records everything we know about one term's appearance in one
// document.
type PostingEntry struct {
	// DocID identifies the document this entry belongs to.
	DocID string `json:"doc_id"`
	// TermFrequency is how many times the term occurs in the document (across all
	// fields). Higher frequency generally means the doc is more "about" the term,
	// which BM25 rewards (with diminishing returns).
	TermFrequency int `json:"tf"`
	// Positions are the ordinal locations of the term within the document, used
	// to answer phrase queries. "distributed systems" matches only if
	// "systems" occurs at position (p+1) for some position p of "distributed".
	Positions []int `json:"positions,omitempty"`
	// FieldFreqs breaks the term frequency down per field, so a FieldQuery
	// ("title:kubernetes") can check whether the term appeared in a given field,
	// and so we can boost matches in important fields.
	FieldFreqs map[string]int `json:"field_freqs,omitempty"`
}

// PostingList is the list of all documents that contain a given term. Entries
// are kept sorted by DocID, which makes intersecting two lists (for AND queries)
// a linear merge instead of a nested loop.
type PostingList struct {
	Term    string         `json:"term"`
	Entries []PostingEntry `json:"entries"`
}

// DocFrequency is the number of documents containing the term. This is the "df"
// in TF-IDF/BM25: a term in few documents (like "kubernetes") is more
// discriminating than one in many (like "data"), so it gets a higher weight.
func (pl *PostingList) DocFrequency() int {
	return len(pl.Entries)
}

// GetEntry returns the entry for docID using binary search (entries are sorted by
// DocID). The bool reports whether it was found.
func (pl *PostingList) GetEntry(docID string) (PostingEntry, bool) {
	lo, hi := 0, len(pl.Entries)
	for lo < hi {
		mid := (lo + hi) / 2
		switch {
		case pl.Entries[mid].DocID == docID:
			return pl.Entries[mid], true
		case pl.Entries[mid].DocID < docID:
			lo = mid + 1
		default:
			hi = mid
		}
	}
	return PostingEntry{}, false
}

// insertEntry adds an entry, keeping Entries sorted by DocID. If an entry for the
// same DocID already exists it is replaced (callers guarantee a doc is deleted
// before being re-indexed, so this is mostly defensive).
func (pl *PostingList) insertEntry(e PostingEntry) {
	lo, hi := 0, len(pl.Entries)
	for lo < hi {
		mid := (lo + hi) / 2
		if pl.Entries[mid].DocID < e.DocID {
			lo = mid + 1
		} else {
			hi = mid
		}
	}
	if lo < len(pl.Entries) && pl.Entries[lo].DocID == e.DocID {
		pl.Entries[lo] = e // replace existing
		return
	}
	// insert at position lo
	pl.Entries = append(pl.Entries, PostingEntry{})
	copy(pl.Entries[lo+1:], pl.Entries[lo:])
	pl.Entries[lo] = e
}

// removeEntry deletes the entry for docID if present, keeping the slice sorted.
func (pl *PostingList) removeEntry(docID string) {
	lo, hi := 0, len(pl.Entries)
	for lo < hi {
		mid := (lo + hi) / 2
		switch {
		case pl.Entries[mid].DocID == docID:
			pl.Entries = append(pl.Entries[:mid], pl.Entries[mid+1:]...)
			return
		case pl.Entries[mid].DocID < docID:
			lo = mid + 1
		default:
			hi = mid
		}
	}
}
