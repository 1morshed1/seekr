// Package protocol defines the JSON request/response types exchanged over HTTP
// between the coordinator and the shard nodes, plus the client endpoints exposed
// to end users.
//
// We use plain HTTP + JSON rather than gRPC (see docs/learning/decisions.md,
// ADR-001): it needs no code generation, is trivially inspectable with curl, and
// keeps the focus on the distributed algorithm rather than the transport.
package protocol

import "github.com/1morshed1/seekr/internal/index"

// --- Shard-facing API (coordinator -> shard) ---

// IndexRequest asks a shard to index (or replace) a document.
type IndexRequest struct {
	Document index.Document `json:"document"`
}

// IndexResponse acknowledges an index operation.
type IndexResponse struct {
	OK bool `json:"ok"`
}

// SearchRequest asks a shard to run a query against its local index and return
// its own top-K scored document IDs (no full documents — hydration is a separate
// step).
type SearchRequest struct {
	Query string `json:"query"`
	TopK  int    `json:"top_k"`
}

// SearchResponse is a shard's local result: scored doc IDs, already truncated to
// the requested top-K.
type SearchResponse struct {
	Hits []index.ScoredDoc `json:"hits"`
}

// HydrateRequest asks a shard for the full documents behind a set of IDs (the
// "fetch" phase after the "query" phase).
type HydrateRequest struct {
	DocIDs []string `json:"doc_ids"`
}

// HydrateResponse returns the requested documents that this shard holds.
type HydrateResponse struct {
	Documents map[string]index.Document `json:"documents"`
}

// StatsResponse reports a shard's document count (useful for cluster health and
// for understanding how documents are distributed).
type StatsResponse struct {
	DocCount int `json:"doc_count"`
}

// --- Client-facing API (user -> coordinator) ---

// ClientIndexRequest is what a user POSTs to the coordinator to index a document.
type ClientIndexRequest struct {
	Document index.Document `json:"document"`
}

// ClientIndexResponse tells the user which shard the document was routed to.
type ClientIndexResponse struct {
	OK    bool `json:"ok"`
	Shard int  `json:"shard"`
}

// ClientSearchResponse is the coordinator's merged, hydrated answer to a search.
type ClientSearchResponse struct {
	Query   string               `json:"query"`
	Total   int                  `json:"total"`
	Results []index.SearchResult `json:"results"`
	// PartialShards lists shards that failed to respond, so the caller knows the
	// results may be incomplete (graceful degradation).
	PartialShards []int `json:"partial_shards,omitempty"`
}

// ClusterStatsResponse aggregates per-shard stats for the whole cluster.
type ClusterStatsResponse struct {
	Shards     []ShardStat `json:"shards"`
	TotalDocs  int         `json:"total_docs"`
	ShardCount int         `json:"shard_count"`
}

// ShardStat is one shard's contribution to the cluster stats.
type ShardStat struct {
	Shard    int    `json:"shard"`
	Address  string `json:"address"`
	DocCount int    `json:"doc_count"`
	Healthy  bool   `json:"healthy"`
}
