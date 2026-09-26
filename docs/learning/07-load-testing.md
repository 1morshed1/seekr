# Phase 6 — Load testing & performance

> Code: `internal/index/bench_test.go` · Run: `go test -bench=. ./internal/index/`

A search engine is only interesting if it's fast. This phase measures the two
numbers that matter most — **index throughput** and **query latency** — explains
what drives them, and shows how to reason about scaling. The point isn't to hit a
specific number on specific hardware; it's to learn *how to measure* and *what the
shape of the curve tells you*.

## How to run the benchmarks

```bash
# All index/query benchmarks
go test -run='^$' -bench=. -benchtime=2s ./internal/index/

# Just indexing throughput
go test -run='^$' -bench=BenchmarkIndexDocument -benchtime=2s ./internal/index/
```

Go's benchmark harness reports nanoseconds per operation (`ns/op`). Convert to a
rate: **ops/sec = 1e9 / ns-per-op**.

## Index throughput

`BenchmarkIndexDocument` indexes 100-word synthetic documents. On a shared 2.1 GHz
cloud vCPU we saw roughly **130 µs/op on a fresh index (~7,600 docs/sec/shard)**,
degrading as the index grows large.

**Why it degrades — a real lesson.** Posting-list entries are kept sorted by DocID
so intersections are fast (Phase 1). But that means inserting a new entry is an
`O(n)` slice shift in the worst case. As a term's posting list grows to tens of
thousands of entries, each insert into the *middle* of it gets slower. Trade-offs
have two sides: the sorted invariant that makes *queries* fast makes *inserts*
costlier at scale.

How production engines avoid this: they buffer writes in an unsorted (or
append-only) in-memory structure and only sort when flushing to a segment, so
indexing is amortized `O(1)` per document and the sort happens once per flush.
That's another motivation for the incremental-segment LSM design discussed in
`06-persistence.md`.

## Query latency

`BenchmarkSingleTermScore{1k,10k,100k}` measure the scoring loop over corpora of
1k / 10k / 100k documents. Single-term scoring is dominated by walking the term's
posting list and computing BM25 per entry — microseconds even at 100k docs, well
under the plan's 50 ms p99 target for a single shard.

What makes queries slower:

- **Boolean queries** intersect/union multiple posting lists — cost scales with the
  sizes of the lists involved.
- **Phrase queries** additionally verify positions per candidate document.
- **Prefix queries** currently scan the whole vocabulary (`TermsWithPrefix` is
  linear) — the first thing to optimize with a sorted-term structure if you push
  this to millions of terms.

## Shard scaling

The distributed win: each shard only indexes and searches *its slice* of the
corpus. With the query fanned out in parallel (Phase 4), per-shard work drops
roughly linearly as you add shards, so aggregate throughput rises while per-query
latency stays bounded by the *slowest* shard (plus merge + hydrate).

Measure it yourself with the demo cluster:

```bash
# 1 shard
./bin/seekr-coordinator --addr :8080 --shards http://localhost:9001 &
# vs 3 shards
./bin/seekr-coordinator --addr :8080 --shards http://localhost:9001,http://localhost:9002,http://localhost:9003 &
```

Index the same corpus into each and compare `/api/v1/stats` (documents per shard)
and search latency (`curl -w '%{time_total}\n'`). With 3 shards each holds ~1/3 of
the documents, so each does ~1/3 of the per-query scanning.

## Relevance (quality, not speed)

Speed is half the story; ranking *quality* is the other half. The rigorous way to
evaluate it is with a labeled dataset (Cranfield, MS MARCO, or Wikipedia
abstracts): run ~20 queries, inspect the top-10, and compute **precision@10**
(fraction of returned results that are relevant) and **recall** (fraction of
relevant documents returned). BM25 (Phase 2) is a strong, well-studied baseline;
this is how you'd prove it works on real data and tune `k1`/`b`.

The `scripts/demo.sh` output already shows qualitatively sensible ranking: a dense,
short match outranks a sparse mention, phrase queries require adjacency, and rare
terms dominate — all the BM25 behaviors the earlier phases built.

## Takeaways

- Measure rates as `1e9 / ns-per-op`; look at the *curve* across corpus sizes, not
  a single number.
- The sorted-posting invariant is a classic query-speed vs. insert-speed trade-off.
- Sharding buys near-linear reduction in per-shard work; latency is bounded by the
  slowest shard plus merge/hydrate.
- Latency is necessary but not sufficient — relevance needs its own evaluation.
