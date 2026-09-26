# Architecture Decision Records (ADRs)

This file records the *why* behind significant engineering choices, so you can
learn not just what the code does but what alternatives were weighed. Each entry
follows a lightweight ADR format: **Context → Decision → Consequences**.

They are listed newest-relevant-first within each phase as the project grows.

---

## ADR-001: Use HTTP + JSON for inter-node RPC instead of gRPC

**Context.** The original plan specified gRPC between the coordinator and shard
nodes. gRPC is excellent in production (binary Protobuf, HTTP/2 streaming, strong
typing), but it requires a `protoc` compiler and code-generation plugins that are
not available in this build environment, and codegen adds a moving part that can
break a learning build.

**Decision.** Use plain HTTP with JSON bodies for all node-to-node communication,
defined in `internal/protocol`. The scatter-gather logic — the actual learning
objective of Phase 4 — is identical regardless of transport.

**Consequences.**
- ✅ Zero external tooling; `go build ./...` is all you need.
- ✅ Endpoints are trivially inspectable with `curl`, which is great for learning.
- ✅ The transport is isolated behind a small client/server, so swapping in gRPC
  later would be a contained change.
- ❌ JSON is slower and larger on the wire than Protobuf. For our scale
  (a laptop, thousands of docs) this is irrelevant; at Elasticsearch scale it
  would matter. The `05-scatter-gather.md` doc discusses this.

---

## ADR-002: Single standard analyzer shared by index and query paths

**Context.** Search only works if a word is normalized identically when indexed
and when searched. It is easy to accidentally use two slightly different
pipelines and get silently empty results.

**Decision.** Expose exactly one `analysis.NewStandardAnalyzer()` and have both
the shard (indexing) and the query parser (searching) depend on it.

**Consequences.**
- ✅ Impossible for the two paths to drift apart.
- ✅ `TestIndexQueryConsistency` documents and enforces the invariant.
- ❌ Less flexibility (no per-field analyzers). Acceptable for a teaching engine.

---

## ADR-003: In-memory index first, disk persistence layered on top

**Context.** We could store everything on disk from day one (like a B-tree
database) or keep everything in memory (fast but volatile).

**Decision.** Build a correct in-memory inverted index first (Phases 1–3), then
add a Write-Ahead Log for durability and SSTable flushing for bounded memory
(Phase 5). This is the LSM-tree architecture used by LevelDB/RocksDB/Cassandra.

**Consequences.**
- ✅ Each phase is independently testable and understandable.
- ✅ Mirrors how real log-structured storage engines are layered.
- ❌ The in-memory-first design means a single shard's live index must fit in RAM
  between flushes. We make the flush threshold configurable.

---

## ADR-004: Full-snapshot checkpoints instead of an incremental LSM tree

**Context.** Phase 5 needs SSTables. A true LSM tree flushes each memtable as a
*new* immutable segment and merges across many segments at read time, with
background compaction. That bounds memory but forces the read path to merge
segments and forces distributed BM25 to reconcile global term statistics across
them — a large increase in complexity.

**Decision.** Make each checkpoint a *full* snapshot of the index written in the
SSTable block format, keep only the latest snapshot (older ones are fully
superseded, so "compaction" is just deleting them), and reload the snapshot whole
on startup before replaying the WAL. This is the AOF+RDB model Redis uses.

**Consequences.**
- ✅ The search and scoring code from Phases 1–4 is reused unchanged and stays
  exact; there is no cross-segment merge to reason about.
- ✅ Still teaches every persistence mechanism: WAL durability with torn-write
  recovery, the real SSTable on-disk format with a sparse block index, atomic
  writes, checksums, crash recovery, and compaction.
- ❌ A checkpoint rewrites the whole index (O(index size)) and the live index must
  fit in RAM — it does not bound memory the way incremental segments do.
- ➡️ The incremental-segment LSM is documented as the natural next step in
  `06-persistence.md`.
