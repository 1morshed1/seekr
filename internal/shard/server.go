// Package shard implements a single shard node: an HTTP server wrapping one
// in-memory inverted index. A shard owns a subset of the corpus (documents routed
// to it by the coordinator), indexes them, and answers local search and hydration
// requests.
//
// A shard knows nothing about other shards or the cluster — it just serves its own
// slice of the data. All cross-shard coordination lives in the coordinator.
package shard

import (
	"encoding/json"
	"log"
	"net/http"

	"github.com/1morshed1/seekr/internal/analysis"
	"github.com/1morshed1/seekr/internal/index"
	"github.com/1morshed1/seekr/internal/protocol"
	"github.com/1morshed1/seekr/internal/query"
	"github.com/1morshed1/seekr/internal/storage"
)

// Server is a shard node. It always searches an in-memory index (idx). When
// engine is non-nil the shard is durable: writes go through the storage engine
// (WAL + SSTable) and idx is the engine's in-memory index; when engine is nil the
// shard is purely in-memory (handy for tests and ephemeral clusters).
type Server struct {
	id     int
	idx    *index.InvertedIndex
	engine *storage.Engine
	log    *log.Logger
}

// New creates an in-memory shard server (no persistence).
func New(id int, logger *log.Logger) *Server {
	return &Server{
		id:  id,
		idx: index.New(analysis.NewStandardAnalyzer()),
		log: logger,
	}
}

// NewPersistent creates a durable shard backed by a storage engine rooted at dir.
// It recovers any previous state (loads the newest SSTable, replays the WAL) before
// serving. flushThreshold is the WAL size in bytes that triggers a checkpoint.
func NewPersistent(id int, logger *log.Logger, dir string, flushThreshold int64) (*Server, error) {
	eng, err := storage.Open(dir, flushThreshold)
	if err != nil {
		return nil, err
	}
	return &Server{
		id:     id,
		idx:    eng.Index(),
		engine: eng,
		log:    logger,
	}, nil
}

// Index exposes the underlying index.
func (s *Server) Index() *index.InvertedIndex { return s.idx }

// Close releases the storage engine (flushing the WAL). Safe to call on an
// in-memory shard.
func (s *Server) Close() error {
	if s.engine != nil {
		return s.engine.Close()
	}
	return nil
}

// Routes registers the shard's HTTP handlers on a mux and returns it.
func (s *Server) Routes() *http.ServeMux {
	mux := http.NewServeMux()
	mux.HandleFunc("/health", s.handleHealth)
	mux.HandleFunc("/v1/index", s.handleIndex)
	mux.HandleFunc("/v1/delete", s.handleDelete)
	mux.HandleFunc("/v1/search", s.handleSearch)
	mux.HandleFunc("/v1/hydrate", s.handleHydrate)
	mux.HandleFunc("/v1/stats", s.handleStats)
	return mux
}

func (s *Server) handleHealth(w http.ResponseWriter, r *http.Request) {
	writeJSON(w, http.StatusOK, map[string]any{"status": "ok", "shard": s.id})
}

// handleIndex indexes a single document into this shard's index.
func (s *Server) handleIndex(w http.ResponseWriter, r *http.Request) {
	var req protocol.IndexRequest
	if err := decodeJSON(r, &req); err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}
	if req.Document.ID == "" {
		http.Error(w, "document id required", http.StatusBadRequest)
		return
	}
	// Durable path (WAL first) when persistence is enabled, else straight to memory.
	if s.engine != nil {
		if err := s.engine.IndexDocument(req.Document); err != nil {
			http.Error(w, err.Error(), http.StatusInternalServerError)
			return
		}
	} else {
		s.idx.IndexDocument(req.Document)
	}
	writeJSON(w, http.StatusOK, protocol.IndexResponse{OK: true})
}

// handleDelete removes a document from this shard.
func (s *Server) handleDelete(w http.ResponseWriter, r *http.Request) {
	var req protocol.HydrateRequest // reuse: a list of doc IDs to delete
	if err := decodeJSON(r, &req); err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}
	for _, id := range req.DocIDs {
		if s.engine != nil {
			if err := s.engine.DeleteDocument(id); err != nil {
				http.Error(w, err.Error(), http.StatusInternalServerError)
				return
			}
		} else {
			s.idx.DeleteDocument(id)
		}
	}
	writeJSON(w, http.StatusOK, protocol.IndexResponse{OK: true})
}

// handleSearch runs a query against this shard's local index and returns its
// local top-K scored doc IDs.
func (s *Server) handleSearch(w http.ResponseWriter, r *http.Request) {
	var req protocol.SearchRequest
	if err := decodeJSON(r, &req); err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}
	hits, err := query.Search(s.idx, req.Query, req.TopK)
	if err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}
	writeJSON(w, http.StatusOK, protocol.SearchResponse{Hits: hits})
}

// handleHydrate returns the full documents for a set of IDs that this shard holds.
func (s *Server) handleHydrate(w http.ResponseWriter, r *http.Request) {
	var req protocol.HydrateRequest
	if err := decodeJSON(r, &req); err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}
	docs := make(map[string]index.Document, len(req.DocIDs))
	for _, id := range req.DocIDs {
		if doc, ok := s.idx.GetDocument(id); ok {
			docs[id] = doc
		}
	}
	writeJSON(w, http.StatusOK, protocol.HydrateResponse{Documents: docs})
}

func (s *Server) handleStats(w http.ResponseWriter, r *http.Request) {
	writeJSON(w, http.StatusOK, protocol.StatsResponse{DocCount: s.idx.DocCount()})
}

// --- small JSON helpers shared by the handlers ---

func decodeJSON(r *http.Request, v any) error {
	defer r.Body.Close()
	return json.NewDecoder(r.Body).Decode(v)
}

func writeJSON(w http.ResponseWriter, status int, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(v)
}
