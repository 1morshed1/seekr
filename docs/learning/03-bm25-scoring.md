# Phase 2 — BM25: ranking by relevance

> Code: `internal/index/bm25.go` · Tests: `TestBM25*` in `index_test.go`

Finding the documents that contain a word is easy (Phase 1). The hard, valuable
part is **ordering** them so the most relevant appears first. That is what a
scoring function does, and BM25 is the one Elasticsearch and Lucene use by default.

## The two questions a good score must answer

1. **How special is this word?** A document matching "kubernetes" is a stronger
   signal than one matching "the", because "kubernetes" is rare. → *IDF*
2. **How much is this document about the word?** A document that says "kafka" ten
   times is probably more about Kafka than one that mentions it once — but not ten
   times more, and not if it's just a huge document. → *TF, saturated and
   length-normalized*

BM25 multiplies an answer to each:

```
score(term, doc) = IDF(term) × TF_saturated(term, doc)
```

and a query with several terms sums the per-term scores.

## Part 1 — IDF (inverse document frequency)

```
idf = ln( 1 + (N - df + 0.5) / (df + 0.5) )
```

- `N` = total documents, `df` = documents containing the term.
- A term in **few** docs (small `df`) → large IDF → matches count a lot.
- A term in **most** docs (large `df`) → IDF near 0 → matches barely count.

`TestBM25RareTermScoresHigher` proves a rare term outscores a common one, and that
a term appearing in *every* document has an IDF near zero (it's noise).

## Part 2 — TF with saturation and length normalization

```
                tf · (k1 + 1)
TF_sat = ───────────────────────────────────────
          tf + k1 · (1 − b + b · (dl / avgdl))
```

Two knobs, both with clear jobs:

### `k1` (saturation, default 1.2)

As term frequency `tf` grows, `TF_sat` rises but **levels off**, approaching
`k1 + 1`. This encodes "the 10th occurrence isn't 10× the 1st." Without saturation
(plain TF-IDF), keyword-stuffed pages would dominate.

```
tf:      1     2     3     5    10    100
TF_sat: ~1.0  ~1.5  ~1.7  ~1.9  ~2.0  ~2.2   (bounded!)
```

### `b` (length normalization, default 0.75)

`dl/avgdl` is this document's length divided by the average. When a document is
longer than average, the denominator grows and the score shrinks. This stops a
5000-word article from beating a focused 20-word note just because length gives it
more chances to contain the term. `TestBM25LengthNormalization` proves the shorter
document wins for the same term.

- `b = 0` → ignore length entirely.
- `b = 1` → fully normalize by length.
- `0.75` → the empirically good middle Elasticsearch ships with.

## Why not plain TF-IDF?

Classic TF-IDF (`tf × idf`) has no saturation and crude length handling, so it
over-rewards repetition and long documents. BM25 fixes both with `k1` and `b`
while keeping the same rare-word intuition. It is a small formula with a big payoff
— read `bm25.go`; every term in the equation maps to one of the intuitions above.

## Distributed scoring caveat (foreshadowing Phase 4)

`N`, `df`, and `avgdl` here are **per-shard** statistics. When documents are split
across shards, each shard computes IDF from its *own* `N` and `df`, which can make
scores from different shards slightly inconsistent. Real engines can gather global
term statistics first (an extra round trip) for exactness; we accept the
approximation and explain the consequences in `05-scatter-gather.md`.
