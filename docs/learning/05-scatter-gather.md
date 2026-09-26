# Phase 4 — Distributed scatter-gather

> Code: `internal/protocol/`, `internal/shard/`, `internal/coordinator/`, `cmd/` · Tests: `internal/coordinator/integration_test.go` · Demo: `scripts/demo.sh`

So far we've built a single search index. This phase splits the corpus across many
nodes and queries them all at once — the step that turns a library into a
*distributed* search engine.

## The two roles

```
                       ┌──────────────────────────────┐
   user ──index/search─▶│         Coordinator          │
                       │  • routes docs to shards     │
                       │  • scatters queries          │
                       │  • gathers + merges results  │
                       └───┬──────────┬──────────┬─────┘
                           ▼          ▼          ▼
                       ┌────────┐ ┌────────┐ ┌────────┐
                       │Shard 0 │ │Shard 1 │ │Shard 2 │
                       │ index  │ │ index  │ │ index  │
                       └────────┘ └────────┘ └────────┘
```

- A **shard** (`internal/shard`) is just our single-node engine behind an HTTP
  API. It knows nothing about the cluster — it indexes and searches its own slice.
- The **coordinator** (`internal/coordinator`) is the brain: it decides where
  documents live and how to combine per-shard answers.

We use **HTTP + JSON** between them (ADR-001) so you can `curl` every endpoint.

## Indexing: routing by hash

Each document must live on exactly one shard. The coordinator picks it with:

```
shard = fnv_hash(docID) % numShards
```

The same ID always hashes to the same shard, so re-indexing updates a document in
place (`TestReindexUpdatesInPlace`), and the corpus spreads roughly evenly
(`TestDocumentsDistributedAcrossShards`; the demo showed 3/5/2 across 3 shards).

**The catch with modulo hashing:** if you change `numShards`, almost every
document's shard changes, forcing a full re-index. Production systems use
**consistent hashing** (or a fixed large number of virtual buckets mapped onto
physical nodes) so adding a shard moves only a small fraction of documents. We use
plain modulo because it makes the routing obvious; the code comment on `shardFor`
flags the trade-off.

## Searching: scatter, gather, merge, hydrate

`Coordinator.Search` runs four steps. Read it alongside this list.

### 1. Scatter (fan-out)

Send the query to **every** shard concurrently (one goroutine each). Every shard
must be asked, because any shard could hold a matching document — there's no way to
know in advance which shards are relevant for a full-text query.

### 2. Gather (fan-in) with graceful degradation

Collect each shard's response. If a shard errors or times out, we **log it, record
the shard ID in `PartialShards`, and continue** with the results we did get. A
search that returns 90% of results is far more useful than one that returns an
error because one node hiccupped. `TestPartialResultsOnShardFailure` points one
shard at a dead address and asserts we still get results plus the partial flag.

### 3. Merge into a global top-K

Each shard returns *its own* top-K (sorted by its local BM25 score). The
coordinator concatenates them and re-sorts to get the global top-K.

#### Why each shard returns top-K, and the accuracy caveat

If 3 shards each return their top 10 for a top-10 request, the coordinator merges
30 and keeps the best 10. This is much cheaper than shipping every match. But it's
an **approximation**: a document ranked #11 on one shard might be globally in the
top 10, and we'd miss it. Two things mitigate this:

- We **over-fetch**: each shard returns `topK × overFetchFactor` (2× here), so the
  merge has more candidates to choose from. Bigger factor → more accurate → more
  data shipped.
- Scores are only *approximately* comparable across shards anyway (next point).

### 4. Hydrate (the fetch phase)

Shard search returns only scored IDs, not full documents — cheap to ship and
merge. Once we know the final winning IDs, we fetch their full documents. The
coordinator groups the winners **by owning shard** and issues **one hydrate
request per shard** (batched, in parallel), not one request per document. This
two-phase "query then fetch" is exactly how Elasticsearch works.

## The distributed relevance caveat (important!)

BM25's `IDF` depends on `N` (total docs) and `df` (docs containing the term). In
this design each shard computes those from **its own local data**. So the "same"
term can get a slightly different IDF on different shards, making cross-shard scores
not perfectly comparable — the merge in step 3 sorts numbers that were computed
against different denominators.

- **Why we accept it:** with documents spread roughly uniformly, local statistics
  approximate global ones well, and the top results are almost always correct.
- **How production fixes it:** an extra round trip (Elasticsearch calls it the
  `dfs_query_then_fetch` search type) first gathers global term statistics from all
  shards, distributes them, and *then* scores — exact, at the cost of one more
  network hop. Understanding *why* that mode exists is the payoff of this caveat.

## Try it

```bash
./scripts/demo.sh
```

It builds the binaries, starts 3 shards + a coordinator, indexes 10 documents, and
runs term / OR / AND / phrase / prefix / NOT queries through the coordinator —
each result hydrated from whichever shard owns it.

## API summary

Coordinator (user-facing):

```
POST /api/v1/index     {"document": {...}}        route + index a document
GET  /api/v1/search?q=<query>&k=<topK>            scatter-gather search
GET  /api/v1/stats                                 per-shard doc counts + health
```

Shard (coordinator-facing):

```
POST /v1/index     index one document locally
POST /v1/search    local top-K scored IDs
POST /v1/hydrate   full documents for a set of IDs
GET  /v1/stats     local doc count
```
