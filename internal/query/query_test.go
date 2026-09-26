package query

import (
	"testing"

	"github.com/1morshed1/seekr/internal/analysis"
	"github.com/1morshed1/seekr/internal/index"
)

func buildIndex() *index.InvertedIndex {
	idx := index.New(analysis.NewStandardAnalyzer())
	idx.IndexDocument(index.Document{ID: "d1", Fields: map[string]string{
		"title": "Distributed Systems",
		"body":  "kubernetes orchestrates distributed systems at scale",
	}})
	idx.IndexDocument(index.Document{ID: "d2", Fields: map[string]string{
		"title": "Kafka Streaming",
		"body":  "kafka is a distributed streaming platform, not python",
	}})
	idx.IndexDocument(index.Document{ID: "d3", Fields: map[string]string{
		"title": "Python Guide",
		"body":  "python programming and deployment of python apps",
	}})
	return idx
}

func ids(res []index.ScoredDoc) map[string]bool {
	m := map[string]bool{}
	for _, r := range res {
		m[r.DocID] = true
	}
	return m
}

func mustSearch(t *testing.T, idx *index.InvertedIndex, q string) []index.ScoredDoc {
	t.Helper()
	res, err := Search(idx, q, 0)
	if err != nil {
		t.Fatalf("Search(%q) error: %v", q, err)
	}
	return res
}

func TestTermQuery(t *testing.T) {
	idx := buildIndex()
	got := ids(mustSearch(t, idx, "kubernetes"))
	if !got["d1"] || len(got) != 1 {
		t.Fatalf("kubernetes -> %v, want only d1", got)
	}
}

func TestImplicitAnd(t *testing.T) {
	idx := buildIndex()
	// "distributed streaming" -> both terms; only d2 has both.
	got := ids(mustSearch(t, idx, "distributed streaming"))
	if !got["d2"] || len(got) != 1 {
		t.Fatalf("distributed streaming -> %v, want only d2", got)
	}
}

func TestExplicitAnd(t *testing.T) {
	idx := buildIndex()
	got := ids(mustSearch(t, idx, "distributed AND kubernetes"))
	if !got["d1"] || len(got) != 1 {
		t.Fatalf("distributed AND kubernetes -> %v, want only d1", got)
	}
}

func TestOr(t *testing.T) {
	idx := buildIndex()
	got := ids(mustSearch(t, idx, "kubernetes OR kafka"))
	if !got["d1"] || !got["d2"] || len(got) != 2 {
		t.Fatalf("kubernetes OR kafka -> %v, want d1 and d2", got)
	}
}

func TestNot(t *testing.T) {
	idx := buildIndex()
	// distributed appears in d1 and d2; exclude python (d2 mentions python).
	got := ids(mustSearch(t, idx, "distributed NOT python"))
	if !got["d1"] || got["d2"] {
		t.Fatalf("distributed NOT python -> %v, want d1 only", got)
	}
}

func TestPhrase(t *testing.T) {
	idx := buildIndex()
	// "distributed systems" is an adjacent phrase in d1 only.
	got := ids(mustSearch(t, idx, `"distributed systems"`))
	if !got["d1"] || len(got) != 1 {
		t.Fatalf(`"distributed systems" -> %v, want only d1`, got)
	}
	// A phrase whose words exist but aren't adjacent should not match.
	none := ids(mustSearch(t, idx, `"kubernetes kafka"`))
	if len(none) != 0 {
		t.Fatalf(`"kubernetes kafka" -> %v, want no matches`, none)
	}
}

func TestFieldQuery(t *testing.T) {
	idx := buildIndex()
	// "python" is in d3's title and body, but only d3 has it in the title.
	got := ids(mustSearch(t, idx, "title:python"))
	if !got["d3"] || len(got) != 1 {
		t.Fatalf("title:python -> %v, want only d3", got)
	}
	// "kafka" is only in d2's title.
	got2 := ids(mustSearch(t, idx, "title:kafka"))
	if !got2["d2"] || len(got2) != 1 {
		t.Fatalf("title:kafka -> %v, want only d2", got2)
	}
}

func TestPrefixQuery(t *testing.T) {
	idx := buildIndex()
	// "deploy*" should match d3 (deployment stems to deploy).
	got := ids(mustSearch(t, idx, "deploy*"))
	if !got["d3"] {
		t.Fatalf("deploy* -> %v, want to include d3", got)
	}
}

func TestGrouping(t *testing.T) {
	idx := buildIndex()
	// (kubernetes OR kafka) AND distributed -> d1 and d2 both have distributed.
	got := ids(mustSearch(t, idx, "(kubernetes OR kafka) AND distributed"))
	if !got["d1"] || !got["d2"] || len(got) != 2 {
		t.Fatalf("grouped query -> %v, want d1 and d2", got)
	}
}

func TestRankingOrder(t *testing.T) {
	idx := index.New(analysis.NewStandardAnalyzer())
	// d1 mentions the term once; d2 mentions it several times in a short doc, so
	// d2 should rank higher.
	idx.IndexDocument(index.Document{ID: "d1", Fields: map[string]string{"body": "search engines index text and more and more words here padding"}})
	idx.IndexDocument(index.Document{ID: "d2", Fields: map[string]string{"body": "search search search"}})
	res := mustSearch(t, idx, "search")
	if len(res) != 2 {
		t.Fatalf("expected 2 results, got %d", len(res))
	}
	if res[0].DocID != "d2" {
		t.Fatalf("expected d2 ranked first, got %s (scores: %+v)", res[0].DocID, res)
	}
}

func TestParseDoesNotLoopOnGarbage(t *testing.T) {
	// Stray operators / punctuation should parse without hanging or panicking.
	for _, q := range []string{":", "*", "AND OR NOT", "(((", "title:", `"unterminated`} {
		if _, err := Parse(q); err != nil {
			t.Fatalf("Parse(%q) unexpected error: %v", q, err)
		}
	}
}

func TestTopKTruncation(t *testing.T) {
	idx := buildIndex()
	res, err := Search(idx, "distributed", 1)
	if err != nil {
		t.Fatal(err)
	}
	if len(res) != 1 {
		t.Fatalf("topK=1 returned %d results", len(res))
	}
}
