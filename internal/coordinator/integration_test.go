package coordinator

import (
	"context"
	"io"
	"log"
	"net/http/httptest"
	"testing"

	"github.com/1morshed1/seekr/internal/index"
	"github.com/1morshed1/seekr/internal/protocol"
	"github.com/1morshed1/seekr/internal/shard"
)

// startCluster spins up n in-process shard servers and returns a coordinator
// wired to them, plus a cleanup func.
func startCluster(t *testing.T, n int) (*Server, func()) {
	t.Helper()
	quietLog := log.New(io.Discard, "", 0)

	var addresses []string
	var servers []*httptest.Server
	for i := 0; i < n; i++ {
		sh := shard.New(i, quietLog)
		ts := httptest.NewServer(sh.Routes())
		servers = append(servers, ts)
		addresses = append(addresses, ts.URL)
	}

	coord := New(addresses, quietLog)
	cleanup := func() {
		for _, ts := range servers {
			ts.Close()
		}
	}
	return coord, cleanup
}

func indexDoc(t *testing.T, coord *Server, id, title, body string) int {
	t.Helper()
	shardID := coord.shardFor(id)
	req := protocol.IndexRequest{Document: index.Document{
		ID:     id,
		Fields: map[string]string{"title": title, "body": body},
	}}
	if err := coord.shards[shardID].index(context.Background(), req); err != nil {
		t.Fatalf("index %s failed: %v", id, err)
	}
	return shardID
}

func TestScatterGatherEndToEnd(t *testing.T) {
	coord, cleanup := startCluster(t, 3)
	defer cleanup()

	docs := []struct{ id, title, body string }{
		{"doc-a", "Distributed Systems", "kubernetes orchestrates distributed systems"},
		{"doc-b", "Kafka Streaming", "kafka is a distributed streaming platform"},
		{"doc-c", "Python Guide", "python programming and deployment"},
		{"doc-d", "Go Concurrency", "goroutines channels and distributed workers"},
		{"doc-e", "Search Engines", "inverted index and distributed search ranking"},
		{"doc-f", "Databases", "sstable lsm tree and write ahead log"},
	}
	for _, d := range docs {
		indexDoc(t, coord, d.id, d.title, d.body)
	}

	// A term present in several documents across (likely) multiple shards.
	resp := coord.Search(context.Background(), "distributed", 10)
	if len(resp.PartialShards) != 0 {
		t.Fatalf("unexpected partial shards: %v", resp.PartialShards)
	}
	got := map[string]bool{}
	for _, r := range resp.Results {
		got[r.DocID] = true
		if r.Document.ID != r.DocID {
			t.Fatalf("hydrated doc mismatch: %+v", r)
		}
	}
	for _, want := range []string{"doc-a", "doc-b", "doc-d", "doc-e"} {
		if !got[want] {
			t.Errorf("expected %s in results for 'distributed', got %v", want, got)
		}
	}
	// python-only doc should not appear.
	if got["doc-c"] {
		t.Errorf("doc-c should not match 'distributed'")
	}
}

func TestResultsAreRankedDescending(t *testing.T) {
	coord, cleanup := startCluster(t, 3)
	defer cleanup()
	indexDoc(t, coord, "d1", "", "search engines index text with lots of padding words here and there")
	indexDoc(t, coord, "d2", "", "search search search")

	resp := coord.Search(context.Background(), "search", 10)
	if len(resp.Results) != 2 {
		t.Fatalf("expected 2 results, got %d", len(resp.Results))
	}
	if resp.Results[0].Score < resp.Results[1].Score {
		t.Fatalf("results not sorted by score: %+v", resp.Results)
	}
	if resp.Results[0].DocID != "d2" {
		t.Fatalf("expected d2 first (denser match), got %s", resp.Results[0].DocID)
	}
}

func TestDocumentsDistributedAcrossShards(t *testing.T) {
	coord, cleanup := startCluster(t, 3)
	defer cleanup()
	seen := map[int]int{}
	for _, id := range []string{"a", "b", "c", "d", "e", "f", "g", "h", "i", "j"} {
		seen[indexDoc(t, coord, id, id, id)]++
	}
	// With 10 docs over 3 shards, at least two shards should hold something. This
	// is a sanity check that routing actually spreads data (not a strict balance
	// guarantee).
	if len(seen) < 2 {
		t.Fatalf("documents not distributed across shards: %v", seen)
	}
}

func TestPartialResultsOnShardFailure(t *testing.T) {
	coord, cleanup := startCluster(t, 3)
	defer cleanup()
	for _, id := range []string{"a", "b", "c", "d", "e", "f"} {
		indexDoc(t, coord, id, "distributed content", "distributed content here")
	}
	// Kill one shard's client by pointing it at a dead address.
	coord.shards[1] = newShardClient(1, "http://127.0.0.1:1") // nothing listening

	resp := coord.Search(context.Background(), "distributed", 10)
	if len(resp.PartialShards) == 0 {
		t.Fatal("expected partial-results flag when a shard is down")
	}
	// We should still get results from the surviving shards rather than an error.
	if len(resp.Results) == 0 {
		t.Fatal("expected partial results from surviving shards")
	}
}

func TestReindexUpdatesInPlace(t *testing.T) {
	coord, cleanup := startCluster(t, 3)
	defer cleanup()
	indexDoc(t, coord, "same-id", "alpha", "alpha alpha")
	indexDoc(t, coord, "same-id", "beta", "beta beta")

	// Old term gone, new term present, still one document.
	if r := coord.Search(context.Background(), "alpha", 10); len(r.Results) != 0 {
		t.Fatalf("alpha should be gone after re-index, got %v", r.Results)
	}
	r := coord.Search(context.Background(), "beta", 10)
	if len(r.Results) != 1 || r.Results[0].DocID != "same-id" {
		t.Fatalf("beta should match the re-indexed doc, got %+v", r.Results)
	}
}
