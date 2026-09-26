# Phase 0 — Text analysis: turning messy text into clean terms

> Code: `internal/analysis/` · Tests: `internal/analysis/analysis_test.go`

Before we can index or search anything, we have to decide *what counts as a
word*. This is the **analysis pipeline**, and getting it right (and consistent)
is the quiet foundation everything else stands on.

## The problem

Humans write messy text:

```
"The Running Foxes are FAST!"
```

A naive search engine that stored the literal string would fail to match a query
for `run fox` — different case, different word forms, extra noise words. The
analysis pipeline fixes this by normalizing text into a stream of clean **terms**.

## The pipeline

```
raw text ──▶ Tokenizer ──▶ [filter, filter, ...] ──▶ []Token
```

Our `NewStandardAnalyzer()` chains four stages:

### 1. Tokenizer — split into words

`UnicodeTokenizer` breaks text on any character that isn't a letter or digit.
So `"C++ is great, really!"` becomes `["C", "is", "great", "really"]`. Each token
remembers its **position** (0, 1, 2, ...) — we'll need positions for phrase
queries in Phase 3.

### 2. Lowercase filter — collapse case

`"Kafka"` and `"kafka"` should be the same term. So everything is lowercased.

### 3. Stop-word filter — drop noise words

Words like *the, a, is, and* appear in nearly every document, so they barely help
distinguish one document from another. We drop a small default set of them.

**Subtle but important:** when we drop a stop word we do **not** renumber the
remaining positions. `"the running fox"` yields `running@1, fox@2` — a gap where
`the@0` used to be. Keeping the original positions means phrase matching still
lines up with the real text.

### 4. Porter stemmer — reduce words to their root

This is the algorithmically rich part. The **Porter stemming algorithm** (1980)
strips English suffixes so related words collapse together:

```
running  -> run
computes -> comput
computer -> comput
happily  -> happili
```

Notice `comput` and `happili` aren't real English words. That's fine! A stem only
needs to be **consistent** — the same for every related word — not pretty. Because
we stem at index time *and* query time, searching `computing` finds documents
about `computers`.

#### How Porter works (the intuition)

The algorithm's core trick is a "measure" `m`: roughly, how many
vowel→consonant transitions a word has, a proxy for syllable count.

```
m=0: tree, by       (too short to safely strip much)
m=1: trouble, oats
m=2: troubles, private
```

Suffix-stripping rules are *gated* on `m`. For example `-ic` is only removed when
what remains has `m > 1`, which stops the algorithm from mangling short words.
The algorithm runs in a fixed sequence of steps (1a, 1b, 1c, 2, 3, 4, 5a, 5b),
each handling a family of suffixes. Read `stemmer.go` top to bottom — every step
has a comment with examples.

## The golden rule: index-time analysis == query-time analysis

If you index `run` (stemmed) but search for the literal `running`, you get zero
results. **The same analyzer must be used on both paths.** That's why
`NewStandardAnalyzer()` is the single source of truth, imported by both the shard
(index side) and the query parser (search side). `TestIndexQueryConsistency`
guards this invariant.

## Trade-offs we deliberately made

- **Stemming can over-collapse.** `university` and `universe` can stem to the same
  root, causing false matches. Production engines offer gentler stemmers
  (e.g. KStem) or dictionary-based lemmatization. We chose classic Porter because
  implementing it teaches the most.
- **Stop-word removal breaks some phrases.** `"to be or not to be"` becomes empty.
  Real engines make the stop list configurable per field. We keep a small list.
- **Our tokenizer discards symbols**, so `C++` loses the `++`. Fine for prose,
  bad for code search. A specialized tokenizer would keep them.

## Try it

```bash
go test ./internal/analysis/ -v
```

Watch `TestStandardAnalyzerEndToEnd`: `"The Running Foxes are Fast"` →
`["run", "fox", "fast"]`. That transformation is the whole phase in one line.
