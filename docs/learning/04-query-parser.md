# Phase 3 — The query parser and execution

> Code: `internal/query/` (`lexer.go`, `parser.go`, `ast.go`, `execute.go`) · Tests: `internal/query/query_test.go`

Now we can index and score. This phase adds the language users actually type into a
search box and the machinery that runs it.

## Supported query language

```
golang                 term: analyzed like indexed text (so "Running" finds "runs")
golang kubernetes      implicit AND — both must match
golang AND kubernetes  explicit AND
kafka OR rabbitmq      OR — either may match
golang NOT python      AND golang, but exclude docs with python
"distributed systems"  phrase — the words must be adjacent
title:kubernetes       field — match only within the 'title' field
deploy*                prefix — deploy, deployment, deployed, ...
(a OR b) AND c         grouping with parentheses
```

## Two stages: lex, then parse

This is the classic compiler front-end split.

### 1. Lexer (`lexer.go`) — characters → tokens

The lexer scans the raw string and emits a flat list of tokens: words, quoted
phrases, `AND`/`OR`/`NOT` operators, `:`, `*`, `(`, `)`, and a terminal `EOF`.
It knows nothing about grammar — it just classifies characters. `AND`/`OR`/`NOT`
are recognized case-insensitively; everything else is a word.

### 2. Parser (`parser.go`) — tokens → AST

A **recursive descent parser**: one function per grammar rule, calling each other
to mirror the grammar's structure. The grammar, ordered by precedence (loosest
binds first):

```
orExpr  := andExpr ( OR andExpr )*
andExpr := unary ( AND? unary )*      ← adjacency means implicit AND
unary   := NOT? primary
primary := '(' orExpr ')' | PHRASE | WORD ':' value | WORD '*'?
```

So `a OR b AND c` parses as `a OR (b AND c)` because `parseOr` calls `parseAnd`
for each side — AND is "deeper" in the call tree, hence higher precedence. Read
the four `parse*` methods top to bottom; each is short and maps directly to one
line of the grammar.

**Robustness matters.** Users type garbage: `(((`, `title:`, `"unterminated`,
lone operators. The parser must never hang or panic on these. Two safeguards:
`advance()` never moves past `EOF`, and any clause that can't be formed makes the
loop bail instead of spinning. `TestParseDoesNotLoopOnGarbage` locks this in — it
was written *because* an early version panicked with an index-out-of-range on
`(((`.

## The AST (`ast.go`)

Four node types, each a `Query`:

- `TermQuery{Term, Field}`
- `PhraseQuery{Phrase, Field}`
- `PrefixQuery{Prefix, Field}`
- `BoolQuery{Must, Should, MustNot}` — the boolean combinator

## Execution (`execute.go`)

Each node has an `eval(idx) → map[docID]score`. Using a **map** (not a slice) makes
combining results in `BoolQuery` natural: intersection for AND, union for OR,
deletion for NOT.

### Term & prefix

Analyze the word the same way documents were analyzed, look up the posting list,
and accumulate each entry's BM25 score. Prefix expands to every vocabulary term
sharing the prefix (an implicit OR over the expansion). A field filter skips
documents where the term didn't appear in the requested field
(`FieldFreqs[field] == 0`).

### Phrase — the clever bit

A phrase isn't just "all these words"; the words must appear **in order and
adjacent**. This is why posting entries store positions.

The subtlety: what about stop words *inside* a phrase? `"king of england"` becomes
`king@0, england@2` after analysis (the removed "of" leaves a gap at position 1).
So instead of demanding consecutive `+1` positions, we **analyze the phrase to get
each term's offset relative to the first term**, then look for a document position
`p` where every term sits at `p + offset`. This handles stop words correctly and
is the same technique Lucene uses (a positional "span" match). See
`phraseMatches`.

### Boolean — Lucene semantics

`BoolQuery.eval` implements:

- **Must (AND):** intersect the candidate sets, summing scores.
- **Should (OR):** union scores. If there are Must clauses, Should only *boosts*
  docs already selected; if there are no Must clauses, Should defines the
  candidate set.
- **MustNot (NOT):** delete any matching doc from the result.
- A pure `NOT x` query starts from *all* documents (score 0) so there's something
  to exclude from.

### Ranking

`Search` collects the final `docID → score` map and `rank()` sorts it by score
descending (with docID as a deterministic tie-breaker), truncating to top-K.
`TestRankingOrder` proves a short document that repeats a term outranks a long one
that mentions it once — BM25 doing its job through the query layer.

## Trade-offs

- **Field queries score using the whole-document term frequency**, not the
  field-only frequency. Exact per-field scoring would need per-field length stats.
  We filter by field but score globally — a documented simplification.
- **Prefixes are lowercased, not stemmed** — stemming a partial word is
  meaningless. Matching a raw prefix against a stemmed vocabulary is imperfect but
  intuitive for users.
