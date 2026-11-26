# seekr

[![CI](https://github.com/1morshed1/seekr/actions/workflows/ci.yml/badge.svg)](https://github.com/1morshed1/seekr/actions/workflows/ci.yml)
[![License: MIT](https://img.shields.io/badge/License-MIT-yellow.svg)](LICENSE)

A miniature **distributed full-text search engine** — think a tiny Elasticsearch
— built from scratch in Go. It implements the real building blocks
of a search system:

- an **inverted index** (the core data structure behind every search engine),
- **BM25** relevance scoring (the algorithm Elasticsearch uses by default),
- a **query parser** supporting `AND` / `OR` / `NOT`, `"phrases"`, `field:term`,
  and `prefix*` queries,
- **scatter-gather** distributed query execution across sharded nodes,
- **WAL + SSTable** persistence (the LSM-tree design used by RocksDB/Cassandra).

## Why this exists

It is a guided, heavily documented implementation meant to be *read*. Every
package explains itself, and `docs/learning/` walks through the ideas phase by
phase. Start at [`docs/learning/00-overview.md`](docs/learning/00-overview.md).

## Architecture

```
             ┌──────────────┐   search   ┌──────────────┐
  client ───▶│ Coordinator  │───────────▶│  Shard 0     │  inverted index + docs
             │  scatter /   │───────────▶│  Shard 1     │  inverted index + docs
             │  gather      │───────────▶│  Shard 2     │  inverted index + docs
             └──────────────┘  merge top-K└─────────────┘
```

Documents are routed to a shard by hashing their ID. Searches fan out to all
shards in parallel; the coordinator merges each shard's local top results into a
global ranking.

## Quick start

```bash
# Run the full test suite
go test ./...

# Build the binaries (added incrementally as phases land)
go build ./cmd/...
```

A one-command local cluster demo lives in `scripts/` (from Phase 4 onward).

## Project layout

```
cmd/
  seekr-shard/         # shard node binary
  seekr-coordinator/   # coordinator node binary
internal/
  analysis/            # tokenizer, filters, Porter stemmer
  index/               # inverted index + BM25 scoring
  query/               # query parser + AST + execution
  storage/             # WAL + SSTable persistence
  shard/               # shard HTTP server
  coordinator/         # coordinator HTTP server (scatter-gather)
  protocol/            # shared request/response types
docs/learning/         # the guided learning material — read this
scripts/               # demo + load-test scripts
```

## Features at a glance

- ✅ Text analysis pipeline: Unicode tokenizer, lowercase, stop words, Porter stemmer
- ✅ Inverted index with positions, per-field frequencies, and safe re-index/delete
- ✅ BM25 ranking (TF saturation + document-length normalization)
- ✅ Query language: term, implicit/explicit `AND`, `OR`, `NOT`, `"phrases"`,
  `field:term`, `prefix*`, and `(grouping)`
- ✅ Distributed scatter-gather across shards with graceful partial-result handling
- ✅ Durable storage: fsync'd WAL + immutable SSTables with a sparse block index,
  crash recovery, and compaction
- ✅ HTTP/JSON APIs, runnable binaries, a one-command cluster demo, and benchmarks

## Testing

```bash
go test ./...                        # full suite (unit + distributed integration)
go test -bench=. ./internal/index/   # indexing & query benchmarks
./scripts/demo.sh                    # live 3-shard cluster with sample queries
```

Durable shard (survives restarts):

```bash
go build -o bin/seekr-shard ./cmd/seekr-shard
./bin/seekr-shard --id 0 --addr :9001 --data ./data/shard0
```

## Status

All six phases are complete. See the table in the overview doc for what each
covers; the learning docs and code are written to be followed in order. Where the
engine deliberately simplifies (single standard analyzer, per-shard BM25 stats,
snapshot checkpoints instead of an incremental LSM), the relevant learning doc and
`docs/learning/decisions.md` explain the trade-off and what production does instead.

## License

[MIT](LICENSE)
