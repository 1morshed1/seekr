package index

import (
	"math"
	"testing"

	"github.com/1morshed1/seekr/internal/analysis"
)

func newTestIndex() *InvertedIndex {
	return New(analysis.NewStandardAnalyzer())
}

func doc(id, title, body string) Document {
	return Document{ID: id, Fields: map[string]string{"title": title, "body": body}}
}

func TestIndexAndPostingLists(t *testing.T) {
	idx := newTestIndex()
	idx.IndexDocument(doc("d1", "The Quick Brown Fox", "foxes are quick"))
	idx.IndexDocument(doc("d2", "Lazy Dog", "the dog sleeps"))

	if got := idx.DocCount(); got != 2 {
		t.Fatalf("DocCount = %d, want 2", got)
	}

	// "quick" appears in d1 only (title + body -> tf 2); "fox" stems to "fox"
	// and appears in both title ("fox") and body ("foxes"->"fox") of d1.
	pl := idx.PostingList("quick")
	if pl == nil {
		t.Fatal("expected posting list for 'quick'")
	}
	if pl.DocFrequency() != 1 {
		t.Fatalf("df(quick) = %d, want 1", pl.DocFrequency())
	}
	e, ok := pl.GetEntry("d1")
	if !ok || e.TermFrequency != 2 {
		t.Fatalf("quick@d1 tf = %v (found=%v), want 2", e.TermFrequency, ok)
	}

	// "dog" only in d2.
	if pl := idx.PostingList("dog"); pl == nil || pl.DocFrequency() != 1 {
		t.Fatalf("df(dog) wrong: %+v", pl)
	}
}

func TestFieldFreqsTracked(t *testing.T) {
	idx := newTestIndex()
	idx.IndexDocument(doc("d1", "kubernetes guide", "learn kubernetes deeply"))
	e, ok := idx.PostingList("kubernet").GetEntry("d1") // "kubernetes" stems to "kubernet"
	if !ok {
		t.Fatal("no entry for kubernet@d1")
	}
	if e.FieldFreqs["title"] != 1 || e.FieldFreqs["body"] != 1 {
		t.Fatalf("field freqs = %+v, want title:1 body:1", e.FieldFreqs)
	}
}

func TestReindexReplacesCleanly(t *testing.T) {
	idx := newTestIndex()
	idx.IndexDocument(doc("d1", "alpha", "alpha alpha"))
	// Re-index the same ID with different content.
	idx.IndexDocument(doc("d1", "beta", "beta"))

	if idx.PostingList("alpha") != nil {
		t.Fatal("old term 'alpha' should be gone after re-index")
	}
	if idx.DocCount() != 1 {
		t.Fatalf("DocCount = %d, want 1 after re-index", idx.DocCount())
	}
	if pl := idx.PostingList("beta"); pl == nil || pl.DocFrequency() != 1 {
		t.Fatalf("beta posting wrong: %+v", pl)
	}
}

func TestDeleteDocument(t *testing.T) {
	idx := newTestIndex()
	idx.IndexDocument(doc("d1", "gamma", ""))
	idx.IndexDocument(doc("d2", "gamma", ""))
	if !idx.DeleteDocument("d1") {
		t.Fatal("delete d1 returned false")
	}
	pl := idx.PostingList("gamma")
	if pl == nil || pl.DocFrequency() != 1 {
		t.Fatalf("after delete, df(gamma) = %v, want 1", pl)
	}
	if _, ok := pl.GetEntry("d1"); ok {
		t.Fatal("d1 still present in posting list after delete")
	}
	if idx.DeleteDocument("nope") {
		t.Fatal("deleting a missing doc should return false")
	}
}

// TestBM25RareTermScoresHigher verifies the central relevance intuition: a rarer
// term (higher IDF) contributes more to the score than a common one.
func TestBM25RareTermScoresHigher(t *testing.T) {
	idx := newTestIndex()
	// "common" appears in every doc; "rare" appears in only one.
	idx.IndexDocument(doc("d1", "", "common rare"))
	idx.IndexDocument(doc("d2", "", "common word"))
	idx.IndexDocument(doc("d3", "", "common thing"))

	var rareScore, commonScore float64
	idx.WithRead(func() {
		rarePL := idx.PostingList("rare")
		commonPL := idx.PostingList("common")
		re, _ := rarePL.GetEntry("d1")
		ce, _ := commonPL.GetEntry("d1")
		rareScore = idx.ScoreBM25(re, rarePL.DocFrequency())
		commonScore = idx.ScoreBM25(ce, commonPL.DocFrequency())
	})

	if !(rareScore > commonScore) {
		t.Fatalf("expected rare term to outscore common: rare=%.4f common=%.4f", rareScore, commonScore)
	}
	// A term present in every document should have IDF ~ 0 (near-useless).
	idx.WithRead(func() {
		if idf := idx.IDF(idx.DocCount()); idf > 0.4 {
			t.Fatalf("IDF for term in all docs should be small, got %.4f", idf)
		}
	})
}

// TestBM25LengthNormalization verifies that the same term in a shorter document
// scores at least as high as in a much longer one.
func TestBM25LengthNormalization(t *testing.T) {
	idx := newTestIndex()
	idx.IndexDocument(doc("short", "", "kubernetes"))
	idx.IndexDocument(doc("long", "", "kubernetes plus lots of other words padding padding padding words here"))

	var shortScore, longScore float64
	idx.WithRead(func() {
		pl := idx.PostingList("kubernet")
		se, _ := pl.GetEntry("short")
		le, _ := pl.GetEntry("long")
		shortScore = idx.ScoreBM25(se, pl.DocFrequency())
		longScore = idx.ScoreBM25(le, pl.DocFrequency())
	})
	if shortScore < longScore || math.Abs(shortScore-longScore) < 1e-9 {
		t.Fatalf("short doc should score higher: short=%.4f long=%.4f", shortScore, longScore)
	}
}

func TestTermsWithPrefix(t *testing.T) {
	idx := newTestIndex()
	idx.IndexDocument(doc("d1", "deploy deployment deployed deploring", ""))
	got := map[string]bool{}
	for _, term := range idx.TermsWithPrefix("deploy") {
		got[term] = true
	}
	// All of deploy/deployment/deployed stem to "deploy"; "deploring" -> "deplor".
	if !got["deploy"] {
		t.Fatalf("expected 'deploy' among prefix matches, got %v", got)
	}
	if got["deplor"] {
		t.Fatalf("'deplor' should not match prefix 'deploy'")
	}
}
