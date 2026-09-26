# Phase 1 — The inverted index

> Code: `internal/index/posting.go`, `internal/index/index.go` · Tests: `internal/index/index_test.go`

This is the single most important data structure in all of search. If you take
one thing from this project, take this.

## Forward vs inverted

A **forward index** answers "what words are in this document?":

```
doc1 -> {the, quick, brown, fox}
doc2 -> {the, lazy, dog}
```

That's how documents naturally look, but it's useless for search — to find "fox"
you'd have to scan every document.

An **inverted index** flips the mapping to answer "which documents contain this
word?":

```
the   -> [doc1, doc2]
quick -> [doc1]
fox   -> [doc1]
dog   -> [doc2]
```

Now "find documents containing fox" is a single hash-map lookup. That inversion
is the entire reason search engines are fast.

## The pieces (read `posting.go`)

```
InvertedIndex
├── postings:  map[term] -> *PostingList
├── docs:      map[docID] -> Document        (kept so we can return the original)
├── docLengths:map[docID] -> token count     (for BM25 length normalization)
└── totalTokens: int                         (sum of lengths -> average length)

PostingList (one per term)
├── Term
└── Entries: []PostingEntry   (SORTED BY DocID)

PostingEntry (one per document that contains the term)
├── DocID
├── TermFrequency   (how many times the term appears -> BM25 TF)
├── Positions       (where it appears -> phrase queries)
└── FieldFreqs      (per-field counts -> field queries + boosting)
```

### Why entries are sorted by DocID

An `AND` query ("quick AND fox") needs the documents that appear in *both* posting
lists. If both lists are sorted by DocID, you can find the intersection with a
single linear merge (walk two pointers), which is O(len(a) + len(b)). Unsorted,
you'd need a nested loop or a hash set. This sorted-merge is the workhorse of
boolean query execution in Phase 3. We keep the lists sorted on insert
(`insertEntry` uses binary search to find the slot) and read them with binary
search (`GetEntry`).

## Indexing a document (read `index.go`)

`IndexDocument` does four things:

1. **If the ID already exists, delete it first.** This makes re-indexing safe and
   keeps every statistic exact (`TestReindexReplacesCleanly`).
2. **Analyze every field** through the shared analyzer, accumulating per-term
   frequency, positions, and per-field counts.
3. **Insert entries** into each term's posting list.
4. **Update statistics** — store the document, its length, and add to the total
   token count.

### The field-position gap trick

When a document has multiple fields (title, body) we concatenate their token
positions, but we insert a large gap (`fieldPositionGap = 1000`) between fields.
Why? So a phrase query can't match *across* a field boundary — a title ending in
"distributed" followed by a body starting with "systems" should not count as the
phrase "distributed systems". The gap makes their positions non-consecutive.

## Deletion (read `deleteLocked`)

To delete a document we need to remove its entry from every posting list it
contributed to. Rather than store a separate per-document term set, we simply
**re-analyze the stored document** to rediscover its terms, then remove its entry
from each. Any posting list that becomes empty is pruned from the vocabulary. This
trades a little CPU at delete time for less memory and less bookkeeping — a fine
trade for a teaching engine.

## Concurrency

The index is guarded by a `sync.RWMutex`: many readers (queries) can run at once,
but a writer (indexing) is exclusive. Query execution wraps an entire query in
`WithRead(...)` so the whole query sees one consistent snapshot — you never see a
half-indexed document.

## Trade-offs

- **Everything is in memory.** Fast, but a shard's live index must fit in RAM.
  Phase 5 adds SSTable flushing to bound memory and a WAL for durability.
- **Prefix search is a linear vocabulary scan** (`TermsWithPrefix`). Real engines
  keep terms in a sorted structure (an FST or B-tree) for O(log n) prefix seeks.
  We note this rather than build it, to keep the phase focused.
