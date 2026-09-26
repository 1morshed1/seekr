# seekr — a distributed full-text search engine (learning overview)

Welcome. This document is your map. `seekr` is a miniature Elasticsearch built
from scratch in Go to teach the two things every search system combines:

1. **Information retrieval (IR)** — how do we store text so we can find it fast,
   and how do we rank results by relevance?
2. **Distributed systems** — how do we split data across many machines
   (shards), query them all at once, and merge the answers?

## The 30-second mental model

A search engine is, at its heart, one clever data structure — the **inverted
index** — wrapped in machinery to build it, query it, rank it, distribute it,
and persist it.

```
             ┌──────────────┐   query   ┌──────────────┐
  client ───▶│ Coordinator  │──────────▶│  Shard 0     │  inverted index + docs
             │ (fan-out /   │──────────▶│  Shard 1     │  inverted index + docs
             │  merge)      │──────────▶│  Shard 2     │  inverted index + docs
             └──────────────┘  results  └──────────────┘
```

- **Documents** are hashed to a **shard** by their ID, so each document lives on
  exactly one shard.
- To **search**, the coordinator sends the query to *every* shard in parallel
  (scatter), then merges each shard's top results into a global ranking
  (gather).

## The build, phase by phase

Each phase has its own learning doc. Read them in order — later phases assume the
earlier ones.

| Phase | Topic | Doc | Code |
|------|-------|-----|------|
| 0 | Text analysis (tokenize, stem) | `01-text-analysis.md` | `internal/analysis` |
| 1 | The inverted index | `02-inverted-index.md` | `internal/index` |
| 2 | BM25 relevance scoring | `03-bm25-scoring.md` | `internal/index` |
| 3 | Query parser (AND/OR/NOT/phrase/prefix) | `04-query-parser.md` | `internal/query` |
| 4 | Distributed scatter-gather | `05-scatter-gather.md` | `internal/shard`, `internal/coordinator` |
| 5 | Persistence: WAL + SSTables | `06-persistence.md` | `internal/storage` |

Plus `decisions.md`, which records the *why* behind the big engineering choices
(a lightweight version of what the industry calls Architecture Decision Records).

## How to read the code

Every package starts with a package-level doc comment (`// Package ...`) that
explains its job. Functions that implement a non-obvious algorithm carry a
comment describing the idea, not just the mechanics. If you read the docs here
and then the package comments in order, the code should feel inevitable rather
than mysterious.

## Running it

```bash
# run all tests
go test ./...

# build the binaries
go build ./cmd/...

# see scripts/ for a full local cluster demo (added in Phase 4)
```

## A note on scope and honesty

This is a *learning* engine, not a production one. Where we deliberately simplify
(single-writer shards, in-memory-first index, approximate distributed top-K),
the relevant learning doc says so and explains what a production system would do
instead. Understanding the simplification *is* the lesson.
