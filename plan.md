## Project 11: Distributed Full-Text Search Engine (Mini-Elasticsearch)

### Concept & Why It Matters

You are building a distributed search engine that indexes documents, stores them across a sharded cluster, and serves full-text search queries with ranking. Think of it as a miniature Elasticsearch or Apache Solr.

This project bridges information retrieval theory with distributed systems practice. You will implement an inverted index (the core data structure behind every search engine), TF-IDF scoring, a query parser, and shard-level scatter-gather query execution. These are concepts that appear in system design interviews constantly ("design a search system," "design Twitter search," "design a log search platform"), and building one from scratch gives you an unmatched depth of understanding.

It also complements Project 5 (Observability Platform) — if you build both, you can point to Project 11 as the search backend that could power Project 5's log search, replacing ClickHouse's built-in text search.

### Architecture Diagram

```
  Clients                                                                     
   │                                                                          
   │  POST /api/v1/index    (index a document)                                
   │  GET  /api/v1/search   (full-text search query)                          
   ▼                                                                          
┌──────────────────────────────────────────────────────────────────────────────┐
│                        SEARCH CLUSTER                                        │
│                                                                              │
│  ┌──────────────────────────────────────────────────────────────────────┐    │
│  │  Go Coordinator Node                                                 │   │
│  │                                                                      │   │
│  │  - Receives search queries from clients                              │   │
│  │  - Parses query into AST (AND, OR, NOT, phrase, wildcard)           │   │
│  │  - Scatter: sends query to all shard nodes in parallel              │   │
│  │  - Gather: collects results, merges, re-ranks, returns top-K       │   │
│  │  - Routes index requests to correct shard (by document ID hash)    │   │
│  │                                                                      │   │
│  │  Also handles:                                                       │   │
│  │  - Index creation/deletion                                           │   │
│  │  - Shard allocation and rebalancing                                  │   │
│  │  - Cluster health monitoring                                         │   │
│  └──────────┬───────────────────────────────────────────────────────────┘    │
│             │                                                                │
│     ┌───────┴────────┬─────────────────┐                                     │
│     ▼                ▼                 ▼                                     │
│  ┌──────────┐  ┌──────────┐  ┌──────────┐                                   │
│  │  Shard   │  │  Shard   │  │  Shard   │                                   │
│  │  Node 0  │  │  Node 1  │  │  Node 2  │                                   │
│  │          │  │          │  │          │                                   │
│  │ ┌──────┐ │  │ ┌──────┐ │  │ ┌──────┐ │                                   │
│  │ │Invert│ │  │ │Invert│ │  │ │Invert│ │                                   │
│  │ │ed    │ │  │ │ed    │ │  │ │ed    │ │                                   │
│  │ │Index │ │  │ │Index │ │  │ │Index │ │                                   │
│  │ └──────┘ │  │ └──────┘ │  │ └──────┘ │                                   │
│  │ ┌──────┐ │  │ ┌──────┐ │  │ ┌──────┐ │                                   │
│  │ │Doc   │ │  │ │Doc   │ │  │ │Doc   │ │                                   │
│  │ │Store │ │  │ │Store │ │  │ │Store │ │                                   │
│  │ └──────┘ │  │ └──────┘ │  │ └──────┘ │                                   │
│  │          │  │          │  │          │                                   │
│  │ gRPC    │  │ gRPC    │  │ gRPC    │                                   │
│  └──────────┘  └──────────┘  └──────────┘                                   │
│                                                                              │
│  Each shard node:                                                            │
│  - Maintains an inverted index in memory + WAL on disk                      │
│  - Stores original documents for retrieval                                  │
│  - Computes TF-IDF scores locally                                           │
│  - Returns top-K results per shard to coordinator                           │
│  - Periodically flushes in-memory index to SSTable files on disk            │
└──────────────────────────────────────────────────────────────────────────────┘
```

### Phase-by-Phase Build Plan

#### Phase 1: The Inverted Index

The inverted index is the fundamental data structure. For every term (word) in the corpus, it stores a list of documents that contain that term, along with metadata (positions, frequency) needed for ranking.

**Data Structures:**

```go
// The inverted index maps terms to posting lists
type InvertedIndex struct {
    mu       sync.RWMutex
    postings map[string]*PostingList   // term -> posting list
    docCount int                        // total documents in this shard
    docNorms map[string]float64         // doc_id -> document length norm
}

// A posting list is an ordered list of documents containing a term
type PostingList struct {
    Term         string
    DocFrequency int            // how many documents contain this term (for IDF)
    Entries      []PostingEntry  // sorted by DocID for efficient intersection
}

// One entry per document in the posting list
type PostingEntry struct {
    DocID         string
    TermFrequency int         // how many times the term appears in this document (for TF)
    Positions     []int       // the positions where the term appears (for phrase queries)
    FieldBoosts   map[string]float64  // per-field weights (title matches > body matches)
}
```

**Text Analysis Pipeline:**

Before indexing, raw text passes through an analysis pipeline:

```go
type Analyzer struct {
    Tokenizer   Tokenizer
    Filters     []TokenFilter
}

func (a *Analyzer) Analyze(text string) []Token {
    tokens := a.Tokenizer.Tokenize(text)
    for _, filter := range a.Filters {
        tokens = filter.Apply(tokens)
    }
    return tokens
}

// Standard analyzer pipeline:
// 1. Unicode tokenizer: split on whitespace and punctuation
// 2. Lowercase filter: "The" -> "the"
// 3. Stop word filter: remove "the", "a", "an", "is", "and", etc.
// 4. Stemmer (Porter stemmer): "running" -> "run", "computers" -> "comput"

type Token struct {
    Term     string
    Position int      // position in the original text (for phrase queries)
    Field    string   // which field this came from (title, body, tags)
}
```

**Indexing a Document:**

```go
func (idx *InvertedIndex) IndexDocument(doc Document) {
    idx.mu.Lock()
    defer idx.mu.Unlock()
    
    // Analyze each field
    allTokens := []Token{}
    for field, content := range doc.Fields {
        tokens := idx.analyzer.Analyze(content)
        for i := range tokens {
            tokens[i].Field = field
        }
        allTokens = append(allTokens, tokens...)
    }
    
    // Build term frequencies
    termFreqs := map[string]int{}
    termPositions := map[string][]int{}
    for _, token := range allTokens {
        termFreqs[token.Term]++
        termPositions[token.Term] = append(termPositions[token.Term], token.Position)
    }
    
    // Update posting lists
    for term, freq := range termFreqs {
        pl, exists := idx.postings[term]
        if !exists {
            pl = &PostingList{Term: term}
            idx.postings[term] = pl
        }
        
        pl.Entries = append(pl.Entries, PostingEntry{
            DocID:         doc.ID,
            TermFrequency: freq,
            Positions:     termPositions[term],
        })
        pl.DocFrequency++
    }
    
    // Store document norm (for length normalization in TF-IDF)
    idx.docNorms[doc.ID] = 1.0 / math.Sqrt(float64(len(allTokens)))
    idx.docCount++
}
```

#### Phase 2: TF-IDF Scoring

**The Scoring Formula (BM25, an improvement over basic TF-IDF):**

BM25 is what Elasticsearch uses by default. It handles term frequency saturation (the 10th occurrence of a word is not 10x as important as the 1st) and document length normalization:

```go
const (
    k1 = 1.2   // term frequency saturation parameter
    b  = 0.75  // document length normalization parameter
)

func (idx *InvertedIndex) scoreBM25(entry PostingEntry, docFrequency int) float64 {
    // IDF: log((N - df + 0.5) / (df + 0.5) + 1)
    // where N = total docs, df = docs containing this term
    N := float64(idx.docCount)
    df := float64(docFrequency)
    idf := math.Log((N-df+0.5)/(df+0.5) + 1)
    
    // TF with saturation: (tf * (k1 + 1)) / (tf + k1 * (1 - b + b * dl/avgdl))
    tf := float64(entry.TermFrequency)
    dl := 1.0 / idx.docNorms[entry.DocID]  // document length
    avgdl := idx.averageDocLength()
    tfSaturated := (tf * (k1 + 1)) / (tf + k1*(1-b+b*dl/avgdl))
    
    return idf * tfSaturated
}
```

#### Phase 3: Query Parser

Build a recursive descent parser that converts query strings into an AST:

```
Supported syntax:
  "distributed systems"    -> PhraseQuery (exact phrase match)
  golang AND kubernetes    -> BoolQuery{Must: [golang, kubernetes]}
  kafka OR rabbitmq        -> BoolQuery{Should: [kafka, rabbitmq]}
  golang NOT python        -> BoolQuery{Must: [golang], MustNot: [python]}
  title:kubernetes         -> FieldQuery (search only in 'title' field)
  deploy*                  -> PrefixQuery (matches deploy, deployment, deployed)
```

```go
type Query interface {
    Execute(idx *InvertedIndex) []ScoredDoc
}

type TermQuery struct {
    Term  string
    Field string  // empty = search all fields
}

type PhraseQuery struct {
    Terms []string
}

type BoolQuery struct {
    Must    []Query   // AND: all must match
    Should  []Query   // OR: at least one must match
    MustNot []Query   // NOT: none can match
}

type PrefixQuery struct {
    Prefix string
}
```

**Phrase Query Execution (using positions):**

A phrase query "distributed systems" means the word "distributed" must appear immediately before "systems" in the document. This is why we store positions in the posting list:

```go
func (pq *PhraseQuery) Execute(idx *InvertedIndex) []ScoredDoc {
    // Get posting lists for all terms in the phrase
    postingLists := make([]*PostingList, len(pq.Terms))
    for i, term := range pq.Terms {
        pl, exists := idx.postings[term]
        if !exists {
            return nil  // if any term is missing, no results
        }
        postingLists[i] = pl
    }
    
    // Intersect posting lists (find documents containing all terms)
    candidates := intersectPostingLists(postingLists)
    
    // For each candidate, check if the terms appear consecutively
    var results []ScoredDoc
    for _, docID := range candidates {
        positions := make([][]int, len(pq.Terms))
        for i, pl := range postingLists {
            entry := pl.GetEntry(docID)
            positions[i] = entry.Positions
        }
        
        if hasConsecutivePositions(positions) {
            results = append(results, ScoredDoc{
                DocID: docID,
                Score: idx.scorePhraseMatch(docID, pq.Terms),
            })
        }
    }
    return results
}

// Check if term positions form a consecutive sequence
// positions[0] contains positions of the first term
// positions[1] contains positions of the second term
// We need positions[1][j] == positions[0][i] + 1 for some i, j
func hasConsecutivePositions(positions [][]int) bool {
    if len(positions) < 2 {
        return true
    }
    
    for _, startPos := range positions[0] {
        found := true
        currentPos := startPos
        for termIdx := 1; termIdx < len(positions); termIdx++ {
            currentPos++
            if !containsInt(positions[termIdx], currentPos) {
                found = false
                break
            }
        }
        if found {
            return true
        }
    }
    return false
}
```

#### Phase 4: Distributed Query Execution (Scatter-Gather)

The coordinator receives a query from the client and fans it out to all shard nodes:

```go
func (c *Coordinator) Search(ctx context.Context, query string, topK int) ([]SearchResult, error) {
    parsedQuery := c.parser.Parse(query)
    
    // Scatter: send query to all shards in parallel
    type shardResult struct {
        shardID int
        results []ScoredDoc
        err     error
    }
    
    ch := make(chan shardResult, len(c.shards))
    for i, shard := range c.shards {
        go func(shardID int, addr string) {
            results, err := c.queryShardRPC(ctx, addr, parsedQuery, topK)
            ch <- shardResult{shardID: shardID, results: results, err: err}
        }(i, shard.Address)
    }
    
    // Gather: collect results from all shards
    allResults := []ScoredDoc{}
    for range c.shards {
        sr := <-ch
        if sr.err != nil {
            // Log error but continue with partial results
            log.Warn("shard query failed", "shard", sr.shardID, "error", sr.err)
            continue
        }
        allResults = append(allResults, sr.results...)
    }
    
    // Merge and re-rank (global top-K from shard-local top-K results)
    sort.Slice(allResults, func(i, j int) bool {
        return allResults[i].Score > allResults[j].Score
    })
    
    if len(allResults) > topK {
        allResults = allResults[:topK]
    }
    
    // Fetch full documents for the final results
    return c.hydrateResults(ctx, allResults)
}
```

**Why Each Shard Returns top-K (not all results):**

If you have 10 million documents across 3 shards and the user wants the top 10 results, each shard returns its local top-10. The coordinator merges 30 results and picks the global top 10. This is not always perfectly accurate (a document ranked 11th on one shard might be globally top-10), but the approximation is excellent in practice. For perfect accuracy, have each shard return top-K * 1.5 or top-K * 2.

#### Phase 5: Persistence (WAL + SSTables)

The in-memory inverted index is fast but volatile. To survive restarts:

**Write-Ahead Log (WAL):**

Every index operation is first appended to a WAL file on disk. On crash recovery, replay the WAL to reconstruct the in-memory index:

```go
func (idx *InvertedIndex) IndexDocument(doc Document) error {
    // Write to WAL first (crash-safe)
    if err := idx.wal.Append(WALEntry{Type: "index", Document: doc}); err != nil {
        return err
    }
    
    // Then update in-memory index
    idx.indexDocumentInMemory(doc)
    
    // Periodically flush to SSTable and truncate WAL
    if idx.walSize() > idx.flushThreshold {
        idx.flushToSSTable()
    }
    
    return nil
}
```

**SSTable (Sorted String Table) Flush:**

When the WAL gets too large, flush the in-memory index to an SSTable file on disk. An SSTable is a sorted, immutable file of term-to-posting-list mappings:

```
SSTable file format:
┌──────────────────────────────────┐
│  Header (magic number, version)  │
├──────────────────────────────────┤
│  Data Block 0                    │  <- sorted entries: term -> posting list
│  Data Block 1                    │
│  ...                             │
├──────────────────────────────────┤
│  Index Block                     │  <- sparse index: first term of each data block -> block offset
├──────────────────────────────────┤
│  Footer (index offset, checksum) │
└──────────────────────────────────┘
```

On search, check the in-memory index first (most recent data), then check SSTable files from newest to oldest. Periodically merge (compact) SSTable files to reduce the number of files that need to be checked.

This is the same storage architecture used by LevelDB, RocksDB, Cassandra, and HBase. If you build it for a search engine, you understand how half the databases in the industry work.

### Load Testing

- **Index throughput:** Documents indexed per second. Target: 10,000+ documents/sec per shard.
- **Query latency:** Search response time for a single-term query, a phrase query, and a boolean query. Target: under 50ms p99 for 1 million documents.
- **Relevance:** Index a well-known dataset (e.g., Wikipedia abstracts, Cranfield collection, or MS MARCO) and manually evaluate the top-10 results for 20 queries. Document precision and recall. This is not just a systems benchmark — it proves your ranking algorithm works.
- **Shard scaling:** Measure query latency with 1, 2, and 3 shards for the same dataset. Verify that adding shards reduces per-shard work (near-linear speedup).

### Cost Management

Like the message broker, this runs entirely on your laptop. Each shard node is a separate Go process with its own data directory. The coordinator is another Go process. No external services required.

---
---