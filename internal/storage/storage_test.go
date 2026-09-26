package storage

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/1morshed1/seekr/internal/analysis"
	"github.com/1morshed1/seekr/internal/index"
	"github.com/1morshed1/seekr/internal/query"
)

func doc(id, body string) index.Document {
	return index.Document{ID: id, Fields: map[string]string{"body": body}}
}

func TestWALAppendAndReplay(t *testing.T) {
	path := filepath.Join(t.TempDir(), "wal.log")
	wal, err := OpenWAL(path)
	if err != nil {
		t.Fatal(err)
	}
	entries := []WALEntry{
		{Op: OpIndex, Document: doc("d1", "hello world")},
		{Op: OpIndex, Document: doc("d2", "goodbye world")},
		{Op: OpDelete, DocID: "d1"},
	}
	for _, e := range entries {
		if err := wal.Append(e); err != nil {
			t.Fatal(err)
		}
	}
	wal.Close()

	var replayed []WALEntry
	if err := ReplayWAL(path, func(e WALEntry) error {
		replayed = append(replayed, e)
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	if len(replayed) != 3 {
		t.Fatalf("replayed %d entries, want 3", len(replayed))
	}
	if replayed[2].Op != OpDelete || replayed[2].DocID != "d1" {
		t.Fatalf("last entry wrong: %+v", replayed[2])
	}
}

// TestWALTruncatedTailIgnored simulates a crash mid-append: a trailing partial
// record must be ignored, and everything before it preserved.
func TestWALTruncatedTailIgnored(t *testing.T) {
	path := filepath.Join(t.TempDir(), "wal.log")
	wal, _ := OpenWAL(path)
	wal.Append(WALEntry{Op: OpIndex, Document: doc("d1", "intact record")})
	wal.Close()

	// Append garbage bytes that look like the start of a torn record.
	f, _ := os.OpenFile(path, os.O_APPEND|os.O_WRONLY, 0o644)
	f.Write([]byte{0xFF, 0xFF, 0xFF, 0x7F, 0x01, 0x02}) // huge length, no payload
	f.Close()

	var n int
	if err := ReplayWAL(path, func(WALEntry) error { n++; return nil }); err != nil {
		t.Fatal(err)
	}
	if n != 1 {
		t.Fatalf("expected 1 intact record despite torn tail, got %d", n)
	}
}

func TestSSTableRoundTrip(t *testing.T) {
	idx := index.New(analysis.NewStandardAnalyzer())
	for i, body := range []string{
		"distributed systems and consensus",
		"kafka distributed streaming platform",
		"python data engineering pipelines",
	} {
		idx.IndexDocument(index.Document{ID: string(rune('a' + i)), Fields: map[string]string{"body": body}})
	}
	snap := idx.ExportSnapshot()

	path := filepath.Join(t.TempDir(), "table.sst")
	if err := WriteSSTable(path, snap); err != nil {
		t.Fatal(err)
	}

	loaded, err := ReadSSTable(path)
	if err != nil {
		t.Fatal(err)
	}
	if loaded.TotalTokens != snap.TotalTokens {
		t.Fatalf("total tokens: got %d want %d", loaded.TotalTokens, snap.TotalTokens)
	}
	if len(loaded.Documents) != len(snap.Documents) {
		t.Fatalf("doc count: got %d want %d", len(loaded.Documents), len(snap.Documents))
	}
	if len(loaded.Postings) != len(snap.Postings) {
		t.Fatalf("term count: got %d want %d", len(loaded.Postings), len(snap.Postings))
	}

	// Rebuild an index from the loaded snapshot and confirm search still works.
	idx2 := index.New(analysis.NewStandardAnalyzer())
	idx2.ImportSnapshot(loaded)
	res, err := query.Search(idx2, "distributed", 10)
	if err != nil {
		t.Fatal(err)
	}
	if len(res) != 2 {
		t.Fatalf("search after reload returned %d, want 2", len(res))
	}
}

// TestSSTablePointLookup exercises the sparse-index seek path with enough terms to
// span multiple blocks.
func TestSSTablePointLookup(t *testing.T) {
	idx := index.New(analysis.NewStandardAnalyzer())
	// Index many distinct single-word docs so the vocabulary spans several blocks.
	words := []string{}
	for c := 'a'; c <= 'z'; c++ {
		for d := 'a'; d <= 'z'; d++ {
			words = append(words, string([]rune{c, d, 'x'}))
		}
	}
	for i, w := range words {
		idx.IndexDocument(index.Document{ID: string(rune(i)) + "id", Fields: map[string]string{"body": w}})
	}
	path := filepath.Join(t.TempDir(), "big.sst")
	if err := WriteSSTable(path, idx.ExportSnapshot()); err != nil {
		t.Fatal(err)
	}
	r, err := OpenReader(path)
	if err != nil {
		t.Fatal(err)
	}
	// A term we know exists.
	pl, found, err := r.Get("mmx")
	if err != nil || !found {
		t.Fatalf("Get(mmx) found=%v err=%v", found, err)
	}
	if pl.Term != "mmx" {
		t.Fatalf("wrong term returned: %s", pl.Term)
	}
	// A term that does not exist.
	if _, found, _ := r.Get("zzzzz"); found {
		t.Fatal("Get(zzzzz) should not be found")
	}
}

func TestSSTableChecksumDetectsCorruption(t *testing.T) {
	idx := index.New(analysis.NewStandardAnalyzer())
	idx.IndexDocument(doc("d1", "some content here"))
	path := filepath.Join(t.TempDir(), "c.sst")
	WriteSSTable(path, idx.ExportSnapshot())

	// Flip a byte in the data region (after the 8-byte header).
	raw, _ := os.ReadFile(path)
	raw[12] ^= 0xFF
	os.WriteFile(path, raw, 0o644)

	if _, err := ReadSSTable(path); err == nil {
		t.Fatal("expected checksum error on corrupted sstable")
	}
}

// TestEngineCrashRecovery is the headline test: index some docs, flush a
// checkpoint, index more (only in the WAL), then reopen the engine and confirm
// everything is present — exactly what happens after a shard restarts.
func TestEngineCrashRecovery(t *testing.T) {
	dir := t.TempDir()

	eng, err := Open(dir, 0) // manual flush
	if err != nil {
		t.Fatal(err)
	}
	eng.IndexDocument(doc("d1", "distributed systems"))
	eng.IndexDocument(doc("d2", "kafka streaming"))
	if err := eng.Flush(); err != nil { // checkpoint d1,d2 to an SSTable
		t.Fatal(err)
	}
	// These live only in the WAL, not the checkpoint.
	eng.IndexDocument(doc("d3", "python engineering"))
	eng.DeleteDocument("d1")
	eng.Close()

	// "Restart": reopen the same directory.
	eng2, err := Open(dir, 0)
	if err != nil {
		t.Fatal(err)
	}
	defer eng2.Close()
	idx := eng2.Index()

	if idx.DocCount() != 2 { // d2, d3 (d1 deleted)
		t.Fatalf("after recovery DocCount = %d, want 2", idx.DocCount())
	}
	if _, ok := idx.GetDocument("d1"); ok {
		t.Fatal("d1 should be deleted after recovery")
	}
	if _, ok := idx.GetDocument("d3"); !ok {
		t.Fatal("d3 (WAL-only) should be present after recovery")
	}
	res, _ := query.Search(idx, "python", 10)
	if len(res) != 1 || res[0].DocID != "d3" {
		t.Fatalf("search for python after recovery: %+v", res)
	}
}

// TestEngineAutoFlushCompacts verifies that crossing the threshold triggers a
// flush and that old SSTables are compacted away.
func TestEngineAutoFlushCompacts(t *testing.T) {
	dir := t.TempDir()
	eng, err := Open(dir, 1) // tiny threshold -> flush after (nearly) every write
	if err != nil {
		t.Fatal(err)
	}
	for i := 0; i < 5; i++ {
		eng.IndexDocument(doc("d"+string(rune('0'+i)), "content number here"))
	}
	eng.Close()

	tables, _ := filepath.Glob(filepath.Join(dir, "sstable-*.sst"))
	if len(tables) != 1 {
		t.Fatalf("expected exactly 1 SSTable after compaction, got %d: %v", len(tables), tables)
	}
	// Reopen and confirm all docs survived the repeated flush/compact cycles.
	eng2, _ := Open(dir, 1)
	defer eng2.Close()
	if eng2.Index().DocCount() != 5 {
		t.Fatalf("DocCount after auto-flush recovery = %d, want 5", eng2.Index().DocCount())
	}
}
