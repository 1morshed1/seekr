# Phase 5 — Persistence: WAL + SSTables

> Code: `internal/storage/` (`wal.go`, `sstable.go`, `engine.go`) · Tests: `internal/storage/storage_test.go`

Everything so far lives in memory. Restart a shard and its index is gone. This
phase makes a shard **durable** using the two building blocks behind most modern
storage engines (LevelDB, RocksDB, Cassandra, and — in spirit — Redis).

## The core problem

We want two things that pull in opposite directions:

- **Durability:** an acknowledged write must survive a crash.
- **Speed:** we don't want to rewrite the whole index to disk on every single write.

The classic answer is to **log every change immediately** (cheap, sequential
append) and **snapshot the whole thing occasionally** (expensive, but rare).

```
write ──▶ 1. append to WAL (fsync)  ──▶ 2. update in-memory index
                                          │
                       WAL big enough? ───┘
                                          ▼
                              3. checkpoint to SSTable, reset WAL
```

## Part 1 — The Write-Ahead Log (`wal.go`)

A WAL is an append-only file. **Before** a mutation touches memory, we append a
record describing it and `fsync` to force it to stable storage. That ordering —
*write ahead* of the memory change — is the whole guarantee: if we crash right
after acknowledging, the record is already on disk and replay will re-apply it.

### Record framing and torn writes

Each record is:

```
[uint32 payload length][uint32 CRC32 of payload][payload JSON]
```

Why the length and CRC? A crash can happen *mid-append*, leaving a half-written
record at the tail. On replay we detect this: a record whose bytes are missing
(short read) or whose CRC doesn't match is treated as the torn tail of a crash —
we stop there and keep everything before it. `TestWALTruncatedTailIgnored` writes
an intact record, appends garbage, and asserts recovery keeps exactly the intact
one. This is how real WALs stay safe against partial writes.

## Part 2 — SSTables (`sstable.go`)

An SSTable ("Sorted String Table") is an **immutable**, **sorted** on-disk file of
`term → posting list`. Immutable means once written it never changes (so it's
safe to read without locks); sorted means we can build a compact index over it.

### File layout

```
Header:  magic "SEEK" + version
Data:    term entries in ascending order, grouped into fixed-size BLOCKS
         each entry: termLen | term | postingsLen | postingsJSON
Index:   ONE pointer per block: firstTerm + fileOffset + entryCount   (sparse!)
Meta:    documents + doc lengths + total tokens (JSON)
Footer:  indexOffset | metaOffset | numTerms | CRC | magic
```

### The sparse index — the clever part

We don't store a pointer for every term (that index would be huge). We store one
pointer per **block** of ~32 terms. To find a term:

1. **Binary search the sparse index** for the one block whose range could contain
   it (the last block whose first term ≤ the target).
2. **Seek to that block and linearly scan** its ~32 entries.

So a lookup is `O(log blocks) + O(block size)` while the index stays ~32× smaller
than a per-term one. This is exactly how LevelDB/RocksDB block indexes work.
`TestSSTablePointLookup` builds a table spanning many blocks and seeks real terms
through `Reader.Get`.

### Integrity & atomicity

A CRC over the whole payload catches bit-rot and truncation
(`TestSSTableChecksumDetectsCorruption` flips a byte and expects a load error).
Writes go to a `.tmp` file and are `rename`d into place — an atomic operation on
POSIX — so a crash mid-write never corrupts the previous good table.

## Part 3 — The engine ties it together (`engine.go`)

`Engine` owns the in-memory index, the WAL, and the SSTable files.

- **Write:** append to WAL → apply to memory → if WAL exceeds the flush threshold,
  checkpoint.
- **Checkpoint (`Flush`):** write the *entire* in-memory index to a new SSTable,
  then reset the WAL, then delete older SSTables (compaction).
- **Recovery (`Open`):** load the newest SSTable, then replay the WAL on top of it.

### Why replay-after-load is safe

The WAL may contain entries already captured by the last checkpoint (e.g. if we
crashed after writing the SSTable but before resetting the WAL). Re-applying them
is harmless because our operations are **idempotent**: re-indexing an ID replaces
it, and deleting a missing ID is a no-op. `TestEngineCrashRecovery` checkpoints
two docs, adds a third and a delete *to the WAL only*, then reopens the directory
and verifies the exact final state — the real restart scenario. We also proved it
by hand: index into a `--data` shard, kill it, restart, and search still works
with no re-indexing.

## An honest simplification: snapshot checkpoints vs. incremental LSM

Our checkpoint writes the **whole** index each time, and recovery loads it **whole**
into memory. This keeps the search path from Phases 1–4 completely unchanged and
correct (BM25 stats stay exact), and it cleanly demonstrates WAL durability, the
SSTable format, crash recovery, and compaction.

A production **LSM tree** goes further: each flush writes only the *new* data as a
separate immutable segment, and reads merge across many segments (newest wins),
with background compaction merging segments over time. That bounds memory (you
don't hold the whole index in RAM) but adds real complexity: reads must merge
several segments, and distributed BM25 needs global term statistics across them.
We deliberately stopped at the snapshot model — the same pairing Redis uses (AOF
log + RDB snapshot) — and name the incremental-segment LSM as the next step. See
ADR-004 in `decisions.md`.

## Try it

```bash
go build -o bin/seekr-shard ./cmd/seekr-shard
DATA=$(mktemp -d)
./bin/seekr-shard --id 0 --addr :9401 --data "$DATA" &   # durable shard
curl -s -X POST localhost:9401/v1/index -d '{"document":{"id":"p1","fields":{"body":"write ahead log"}}}'
kill %1                                                    # simulate a crash
./bin/seekr-shard --id 0 --addr :9402 --data "$DATA" &    # restart
curl -s localhost:9402/v1/stats                            # doc is still there!
```
