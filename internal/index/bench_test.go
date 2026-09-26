package index

import (
	"fmt"
	"math/rand"
	"testing"

	"github.com/1morshed1/seekr/internal/analysis"
)

// vocabulary of realistic-ish terms to build synthetic documents from.
var benchVocab = []string{
	"distributed", "systems", "kafka", "streaming", "kubernetes", "container",
	"database", "storage", "engine", "index", "search", "ranking", "consensus",
	"replication", "partition", "shard", "cluster", "network", "latency",
	"throughput", "python", "golang", "rust", "concurrency", "goroutine",
	"channel", "queue", "broker", "message", "pipeline", "deployment", "cloud",
	"observability", "metrics", "logging", "tracing", "cache", "memory", "disk",
	"compaction", "wal", "sstable", "bloom", "filter", "hash", "tree", "graph",
}

// makeDoc builds a document of roughly n words drawn from the vocabulary.
func makeDoc(r *rand.Rand, id string, n int) Document {
	body := make([]byte, 0, n*8)
	for i := 0; i < n; i++ {
		if i > 0 {
			body = append(body, ' ')
		}
		body = append(body, benchVocab[r.Intn(len(benchVocab))]...)
	}
	return Document{ID: id, Fields: map[string]string{"body": string(body)}}
}

// BenchmarkIndexDocument measures indexing throughput (documents/second is
// 1e9/ns-per-op). Each iteration indexes one ~100-word document.
func BenchmarkIndexDocument(b *testing.B) {
	r := rand.New(rand.NewSource(1))
	idx := New(analysis.NewStandardAnalyzer())
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		idx.IndexDocument(makeDoc(r, fmt.Sprintf("d%d", i), 100))
	}
}

// buildCorpus creates an index of n documents for query benchmarks.
func buildCorpus(n int) *InvertedIndex {
	r := rand.New(rand.NewSource(42))
	idx := New(analysis.NewStandardAnalyzer())
	for i := 0; i < n; i++ {
		idx.IndexDocument(makeDoc(r, fmt.Sprintf("d%d", i), 100))
	}
	return idx
}

// benchScoreTerm mimics a single-term query's scoring loop so we can measure raw
// index+BM25 throughput without the query parser.
func benchScoreTerm(idx *InvertedIndex, term string) int {
	count := 0
	idx.WithRead(func() {
		pl := idx.PostingList(term)
		if pl == nil {
			return
		}
		df := pl.DocFrequency()
		for _, e := range pl.Entries {
			_ = idx.ScoreBM25(e, df)
			count++
		}
	})
	return count
}

func BenchmarkSingleTermScore1k(b *testing.B)   { benchQuery(b, 1000) }
func BenchmarkSingleTermScore10k(b *testing.B)  { benchQuery(b, 10000) }
func BenchmarkSingleTermScore100k(b *testing.B) { benchQuery(b, 100000) }

func benchQuery(b *testing.B, corpusSize int) {
	idx := buildCorpus(corpusSize)
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		benchScoreTerm(idx, "distributed")
	}
}
