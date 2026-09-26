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
