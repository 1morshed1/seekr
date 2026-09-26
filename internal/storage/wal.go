// Package storage makes a shard's in-memory index durable using the two classic
// building blocks of log-structured storage engines:
//
//   - a Write-Ahead Log (WAL): every mutation is appended to a disk log *before*
//     it touches memory, so a crash can be recovered by replaying the log; and
//   - SSTables (Sorted String Tables): periodic immutable on-disk snapshots of the
//     index, which let us truncate the WAL and reload quickly after a restart.
//
// This is the same durability pattern used by Redis (AOF + RDB), and the same
// on-disk table format family used by LevelDB, RocksDB, and Cassandra. See
// docs/learning/06-persistence.md.
package storage

import (
	"bufio"
	"encoding/binary"
	"encoding/json"
	"errors"
	"hash/crc32"
	"io"
	"os"

	"github.com/1morshed1/seekr/internal/index"
)

// WALOp is the kind of mutation a WAL record represents.
type WALOp string

const (
	OpIndex  WALOp = "index"  // add or replace a document
	OpDelete WALOp = "delete" // remove a document
)

// WALEntry is one logged mutation.
type WALEntry struct {
	Op       WALOp          `json:"op"`
	Document index.Document `json:"document,omitempty"` // set for OpIndex
	DocID    string         `json:"doc_id,omitempty"`   // set for OpDelete
}

// On-disk record framing, chosen to make torn writes (a crash mid-append)
// detectable: for each record we write
//
//	[uint32 payload length][uint32 CRC32 of payload][payload bytes]
//
// On replay, a record whose bytes are missing or whose CRC doesn't match is
// treated as the truncated tail of a crash and stops replay cleanly — we keep
// everything before it and discard the incomplete record.
type WAL struct {
	f    *os.File
	w    *bufio.Writer
	path string
	size int64
}

// OpenWAL opens (creating if needed) the WAL file for appending.
func OpenWAL(path string) (*WAL, error) {
	f, err := os.OpenFile(path, os.O_CREATE|os.O_APPEND|os.O_WRONLY, 0o644)
	if err != nil {
		return nil, err
	}
	info, err := f.Stat()
	if err != nil {
		f.Close()
		return nil, err
	}
	return &WAL{f: f, w: bufio.NewWriter(f), path: path, size: info.Size()}, nil
}

// Append writes one entry durably. It flushes the buffer and fsyncs so the record
// survives a power loss — the whole point of a *write-ahead* log is that the log
// hits stable storage before we acknowledge the write.
func (wal *WAL) Append(entry WALEntry) error {
	payload, err := json.Marshal(entry)
	if err != nil {
		return err
	}
	var header [8]byte
	binary.LittleEndian.PutUint32(header[0:4], uint32(len(payload)))
	binary.LittleEndian.PutUint32(header[4:8], crc32.ChecksumIEEE(payload))

	if _, err := wal.w.Write(header[:]); err != nil {
		return err
	}
	if _, err := wal.w.Write(payload); err != nil {
		return err
	}
	if err := wal.w.Flush(); err != nil {
		return err
	}
	if err := wal.f.Sync(); err != nil {
		return err
	}
	wal.size += int64(len(header) + len(payload))
	return nil
}

// Size returns the current WAL size in bytes (used to decide when to flush).
func (wal *WAL) Size() int64 { return wal.size }

// Reset empties the WAL. Called after a successful SSTable flush, once the
// in-memory state it protected is safely on disk.
func (wal *WAL) Reset() error {
	if err := wal.w.Flush(); err != nil {
		return err
	}
	if err := wal.f.Truncate(0); err != nil {
		return err
	}
	if _, err := wal.f.Seek(0, io.SeekStart); err != nil {
		return err
	}
	wal.size = 0
	return nil
}

// Close flushes and closes the WAL file.
func (wal *WAL) Close() error {
	if err := wal.w.Flush(); err != nil {
		return err
	}
	return wal.f.Close()
}

// ReplayWAL reads every intact record from the WAL at path and calls fn for each,
// in order. It stops silently at the first truncated or corrupt record (the tail
// of a crashed write), which is the correct recovery behavior. A missing file
// replays nothing.
func ReplayWAL(path string, fn func(WALEntry) error) error {
	f, err := os.Open(path)
	if errors.Is(err, os.ErrNotExist) {
		return nil
	}
	if err != nil {
		return err
	}
	defer f.Close()

	r := bufio.NewReader(f)
	for {
		var header [8]byte
		if _, err := io.ReadFull(r, header[:]); err != nil {
			if err == io.EOF || err == io.ErrUnexpectedEOF {
				return nil // clean end, or a torn header — stop cleanly
			}
			return err
		}
		length := binary.LittleEndian.Uint32(header[0:4])
		wantCRC := binary.LittleEndian.Uint32(header[4:8])

		payload := make([]byte, length)
		if _, err := io.ReadFull(r, payload); err != nil {
			return nil // truncated payload -> crashed mid-write, stop
		}
		if crc32.ChecksumIEEE(payload) != wantCRC {
			return nil // corrupt record -> stop, keep what we had
		}
		var entry WALEntry
		if err := json.Unmarshal(payload, &entry); err != nil {
			return nil
		}
		if err := fn(entry); err != nil {
			return err
		}
	}
}
