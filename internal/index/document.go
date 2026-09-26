package index

// Document is the unit we index and retrieve. It has a stable ID and a set of
// named fields (e.g. "title", "body", "tags"). Splitting content into fields
// lets callers search a specific field ("title:kubernetes") and lets us weight
// a title match more heavily than a body match.
type Document struct {
	ID     string            `json:"id"`
	Fields map[string]string `json:"fields"`
}

// ScoredDoc is a search hit: a document ID and its relevance score. It is the
// currency passed around during query execution and merged by the coordinator.
type ScoredDoc struct {
	DocID string  `json:"doc_id"`
	Score float64 `json:"score"`
}

// SearchResult is a fully "hydrated" hit returned to the client: the score plus
// the original document so the caller can render a title/snippet without a second
// round trip.
type SearchResult struct {
	DocID    string   `json:"doc_id"`
	Score    float64  `json:"score"`
	Document Document `json:"document"`
}
