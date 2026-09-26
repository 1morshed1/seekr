package storage

import (
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"sync"

	"github.com/1morshed1/seekr/internal/analysis"
	"github.com/1morshed1/seekr/internal/index"
)

// Engine makes a single shard's index durable by combining a WAL (for every recent
// mutation) with periodic SSTable checkpoints (a full on-disk snapshot).
//
// Lifecycle of a write:
//
//  1. append the mutation to the WAL (fsync'd — survives a crash);
//  2. apply it to the in-memory index;
//  3. if the WAL has grown past the flush threshold, checkpoint the whole index to
//     a new SSTable and reset the WAL.
//
// Recovery on startup: load the newest SSTable, then replay the WAL on top of it.
// Because indexing is idempotent (re-index replaces, delete-missing is a no-op),
// replaying entries that were already captured by the last checkpoint is harmless.
//
// This "log + checkpoint" pairing is the same idea as Redis's AOF + RDB. See
// docs/learning/06-persistence.md for the full walkthrough, including how this
// differs from an incremental-segment LSM tree.
type Engine struct {
	mu             sync.Mutex
	dir            string
	idx            *index.InvertedIndex
	wal            *WAL
	flushThreshold int64
	seq            int // monotonically increasing SSTable sequence number
}

// Open initializes an engine rooted at dir, recovering any previous state. A
// flushThreshold of 0 disables automatic flushing (useful in tests where you want
// to trigger Flush explicitly).
func Open(dir string, flushThreshold int64) (*Engine, error) {
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return nil, err
	}
	idx := index.New(analysis.NewStandardAnalyzer())

	e := &Engine{dir: dir, idx: idx, flushThreshold: flushThreshold}

	// 1. Load the most recent SSTable checkpoint, if any.
	latest, seq, err := e.latestSSTable()
	if err != nil {
		return nil, err
	}
	e.seq = seq
	if latest != "" {
		snap, err := ReadSSTable(latest)
		if err != nil {
			return nil, fmt.Errorf("loading checkpoint: %w", err)
		}
		idx.ImportSnapshot(snap)
	}

	// 2. Replay the WAL on top of the checkpoint to recover writes since then.
	if err := ReplayWAL(e.walPath(), func(entry WALEntry) error {
		e.apply(entry)
		return nil
	}); err != nil {
		return nil, fmt.Errorf("replaying WAL: %w", err)
	}

	// 3. Open the WAL for appending new writes.
	wal, err := OpenWAL(e.walPath())
	if err != nil {
		return nil, err
	}
	e.wal = wal
	return e, nil
}

// Index returns the underlying in-memory index (the shard reads/searches it).
func (e *Engine) Index() *index.InvertedIndex { return e.idx }

// apply mutates the in-memory index from a WAL entry (used during replay and by
// the write methods).
func (e *Engine) apply(entry WALEntry) {
	switch entry.Op {
	case OpIndex:
		e.idx.IndexDocument(entry.Document)
	case OpDelete:
		e.idx.DeleteDocument(entry.DocID)
	}
}

// IndexDocument durably indexes a document: WAL first, then memory, then maybe
// checkpoint.
func (e *Engine) IndexDocument(doc index.Document) error {
	e.mu.Lock()
	defer e.mu.Unlock()
	if err := e.wal.Append(WALEntry{Op: OpIndex, Document: doc}); err != nil {
		return err
	}
	e.idx.IndexDocument(doc)
	return e.maybeFlush()
}

// DeleteDocument durably removes a document.
func (e *Engine) DeleteDocument(docID string) error {
	e.mu.Lock()
	defer e.mu.Unlock()
	if err := e.wal.Append(WALEntry{Op: OpDelete, DocID: docID}); err != nil {
		return err
	}
	e.idx.DeleteDocument(docID)
	return e.maybeFlush()
}

// maybeFlush checkpoints when the WAL has grown past the threshold. Caller holds
// the lock.
func (e *Engine) maybeFlush() error {
	if e.flushThreshold > 0 && e.wal.Size() >= e.flushThreshold {
		return e.flushLocked()
	}
	return nil
}

// Flush forces a checkpoint regardless of WAL size.
func (e *Engine) Flush() error {
	e.mu.Lock()
	defer e.mu.Unlock()
	return e.flushLocked()
}

// flushLocked writes the entire in-memory index to a new SSTable, removes older
// SSTables (compaction — with full-snapshot checkpoints, older ones are fully
// superseded), and resets the WAL. Caller holds the lock.
func (e *Engine) flushLocked() error {
	snap := e.idx.ExportSnapshot()
	e.seq++
	newPath := e.sstablePath(e.seq)
	if err := WriteSSTable(newPath, snap); err != nil {
		return err
	}
	// Only after the new checkpoint is safely on disk do we drop the WAL and old
	// tables. If we crash before this line, recovery just reloads the previous
	// checkpoint and replays the WAL — no data loss.
	if err := e.wal.Reset(); err != nil {
		return err
	}
	return e.removeSSTablesBefore(e.seq)
}

// Close flushes buffered WAL data and closes the file.
func (e *Engine) Close() error {
	e.mu.Lock()
	defer e.mu.Unlock()
	return e.wal.Close()
}

// --- file naming helpers ---

func (e *Engine) walPath() string { return filepath.Join(e.dir, "wal.log") }

func (e *Engine) sstablePath(seq int) string {
	return filepath.Join(e.dir, fmt.Sprintf("sstable-%06d.sst", seq))
}

// latestSSTable returns the path and sequence number of the highest-numbered
// SSTable in the directory, or ("", 0, nil) if none exist.
func (e *Engine) latestSSTable() (string, int, error) {
	entries, err := filepath.Glob(filepath.Join(e.dir, "sstable-*.sst"))
	if err != nil {
		return "", 0, err
	}
	if len(entries) == 0 {
		return "", 0, nil
	}
	sort.Strings(entries) // zero-padded names sort by sequence
	latest := entries[len(entries)-1]
	var seq int
	_, err = fmt.Sscanf(filepath.Base(latest), "sstable-%06d.sst", &seq)
	if err != nil {
		return "", 0, err
	}
	return latest, seq, nil
}

// removeSSTablesBefore deletes every SSTable with a sequence number below keepSeq.
func (e *Engine) removeSSTablesBefore(keepSeq int) error {
	entries, err := filepath.Glob(filepath.Join(e.dir, "sstable-*.sst"))
	if err != nil {
		return err
	}
	for _, path := range entries {
		var seq int
		if _, err := fmt.Sscanf(filepath.Base(path), "sstable-%06d.sst", &seq); err != nil {
			continue
		}
		if seq < keepSeq {
			_ = os.Remove(path)
		}
	}
	return nil
}
