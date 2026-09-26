// Package coordinator implements the cluster's front door. It routes index
// requests to the owning shard (by hashing the document ID) and executes searches
// with the scatter-gather pattern: fan the query out to every shard in parallel,
// then merge their local results into a single global ranking.
package coordinator

import (
	"context"
	"encoding/json"
	"hash/fnv"
	"log"
	"net/http"
	"sort"
	"strconv"
	"sync"

	"github.com/1morshed1/seekr/internal/index"
	"github.com/1morshed1/seekr/internal/protocol"
)

// overFetchFactor controls how many results each shard returns relative to the
// requested top-K. Fetching more than we need per shard improves the accuracy of
// the merged global ranking (see docs/learning/05-scatter-gather.md).
const overFetchFactor = 2

// Server is the coordinator node.
type Server struct {
	shards []*shardClient
	log    *log.Logger
}

// New creates a coordinator that fans out to the given shard addresses. The slice
// index is the shard ID, and it is also what document hashing maps onto — so the
// address order defines the cluster topology and must be identical across restarts
// for routing to stay stable.
func New(addresses []string, logger *log.Logger) *Server {
	clients := make([]*shardClient, len(addresses))
	for i, addr := range addresses {
		clients[i] = newShardClient(i, addr)
	}
	return &Server{shards: clients, log: logger}
}

// shardFor maps a document ID to its owning shard using FNV hashing modulo the
// shard count. The same ID always lands on the same shard, so a document lives on
// exactly one shard and re-indexing updates it in place.
//
// Caveat: plain modulo hashing means changing the shard count reshuffles almost
// every document. Production systems use consistent hashing or a fixed number of
// "routing" buckets to make rebalancing cheap. We keep modulo for clarity.
func (s *Server) shardFor(docID string) int {
	h := fnv.New32a()
	_, _ = h.Write([]byte(docID))
	return int(h.Sum32() % uint32(len(s.shards)))
}

// Routes registers the coordinator's client-facing HTTP handlers.
func (s *Server) Routes() *http.ServeMux {
	mux := http.NewServeMux()
	mux.HandleFunc("/health", s.handleHealth)
	mux.HandleFunc("/api/v1/index", s.handleIndex)
	mux.HandleFunc("/api/v1/search", s.handleSearch)
	mux.HandleFunc("/api/v1/stats", s.handleStats)
	return mux
}

func (s *Server) handleHealth(w http.ResponseWriter, r *http.Request) {
	writeJSON(w, http.StatusOK, map[string]any{"status": "ok", "shards": len(s.shards)})
}

// handleIndex routes a document to its owning shard.
func (s *Server) handleIndex(w http.ResponseWriter, r *http.Request) {
	var req protocol.ClientIndexRequest
	if err := decodeJSON(r, &req); err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}
	if req.Document.ID == "" {
		http.Error(w, "document id required", http.StatusBadRequest)
		return
	}
	shardID := s.shardFor(req.Document.ID)
	if err := s.shards[shardID].index(r.Context(), protocol.IndexRequest{Document: req.Document}); err != nil {
		s.log.Printf("index doc %q -> shard %d failed: %v", req.Document.ID, shardID, err)
		http.Error(w, err.Error(), http.StatusBadGateway)
		return
	}
	writeJSON(w, http.StatusOK, protocol.ClientIndexResponse{OK: true, Shard: shardID})
}

// handleSearch runs the scatter-gather search.
func (s *Server) handleSearch(w http.ResponseWriter, r *http.Request) {
	q := r.URL.Query().Get("q")
	topK := 10
	if k := r.URL.Query().Get("k"); k != "" {
		if v, err := strconv.Atoi(k); err == nil && v > 0 {
			topK = v
		}
	}
	resp := s.Search(r.Context(), q, topK)
	writeJSON(w, http.StatusOK, resp)
}

// Search is the heart of the coordinator: scatter the query to all shards in
// parallel, gather their local top-K, merge into a global top-K, then hydrate the
// winners into full documents.
func (s *Server) Search(ctx context.Context, queryStr string, topK int) protocol.ClientSearchResponse {
	// --- SCATTER: query every shard concurrently ---
	type shardOutcome struct {
		id   int
		hits []index.ScoredDoc
		err  error
	}
	// Ask each shard for more than topK to improve merge accuracy.
	perShardK := topK * overFetchFactor

	ch := make(chan shardOutcome, len(s.shards))
	var wg sync.WaitGroup
	for _, sc := range s.shards {
		wg.Add(1)
		go func(sc *shardClient) {
			defer wg.Done()
			resp, err := sc.search(ctx, protocol.SearchRequest{Query: queryStr, TopK: perShardK})
			ch <- shardOutcome{id: sc.id, hits: resp.Hits, err: err}
		}(sc)
	}
	wg.Wait()
	close(ch)

	// --- GATHER: collect results, tolerating shard failures (partial results) ---
	var merged []index.ScoredDoc
	var partial []int
	for out := range ch {
		if out.err != nil {
			s.log.Printf("shard %d search failed: %v", out.id, out.err)
			partial = append(partial, out.id)
			continue
		}
		merged = append(merged, out.hits...)
	}

	// --- MERGE: global ranking from the shard-local rankings ---
	sort.Slice(merged, func(i, j int) bool {
		if merged[i].Score != merged[j].Score {
			return merged[i].Score > merged[j].Score
		}
		return merged[i].DocID < merged[j].DocID
	})
	if topK > 0 && len(merged) > topK {
		merged = merged[:topK]
	}

	// --- HYDRATE: fetch the full documents for the winning IDs ---
	results := s.hydrate(ctx, merged)

	sort.Ints(partial)
	return protocol.ClientSearchResponse{
		Query:         queryStr,
		Total:         len(results),
		Results:       results,
		PartialShards: partial,
	}
}

// hydrate turns scored IDs into full SearchResults by fetching each document from
// its owning shard. It batches requests: one hydrate call per shard, not one per
// document.
func (s *Server) hydrate(ctx context.Context, scored []index.ScoredDoc) []index.SearchResult {
	// Group the winning IDs by owning shard.
	byShard := map[int][]string{}
	scoreOf := map[string]float64{}
	for _, sd := range scored {
		shardID := s.shardFor(sd.DocID)
		byShard[shardID] = append(byShard[shardID], sd.DocID)
		scoreOf[sd.DocID] = sd.Score
	}

	// Fetch documents from each shard (in parallel).
	docs := map[string]index.Document{}
	var mu sync.Mutex
	var wg sync.WaitGroup
	for shardID, ids := range byShard {
		wg.Add(1)
		go func(shardID int, ids []string) {
			defer wg.Done()
			resp, err := s.shards[shardID].hydrate(ctx, protocol.HydrateRequest{DocIDs: ids})
			if err != nil {
				s.log.Printf("hydrate from shard %d failed: %v", shardID, err)
				return
			}
			mu.Lock()
			for id, doc := range resp.Documents {
				docs[id] = doc
			}
			mu.Unlock()
		}(shardID, ids)
	}
	wg.Wait()

	// Rebuild the ranked list, preserving the merged order.
	results := make([]index.SearchResult, 0, len(scored))
	for _, sd := range scored {
		doc, ok := docs[sd.DocID]
		if !ok {
			continue // document vanished (deleted between query and fetch)
		}
		results = append(results, index.SearchResult{
			DocID:    sd.DocID,
			Score:    sd.Score,
			Document: doc,
		})
	}
	return results
}

// handleStats aggregates per-shard document counts and health.
func (s *Server) handleStats(w http.ResponseWriter, r *http.Request) {
	out := protocol.ClusterStatsResponse{ShardCount: len(s.shards)}
	for _, sc := range s.shards {
		stat := protocol.ShardStat{Shard: sc.id, Address: sc.address}
		if resp, err := sc.stats(r.Context()); err == nil {
			stat.DocCount = resp.DocCount
			stat.Healthy = true
			out.TotalDocs += resp.DocCount
		}
		out.Shards = append(out.Shards, stat)
	}
	writeJSON(w, http.StatusOK, out)
}

// --- JSON helpers ---

func decodeJSON(r *http.Request, v any) error {
	defer r.Body.Close()
	return json.NewDecoder(r.Body).Decode(v)
}

func writeJSON(w http.ResponseWriter, status int, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(v)
}
