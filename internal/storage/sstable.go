package storage

import (
	"bytes"
	"encoding/binary"
	"encoding/json"
	"fmt"
	"hash/crc32"
	"os"
	"sort"

	"github.com/1morshed1/seekr/internal/index"
)

// An SSTable is an immutable, sorted, on-disk snapshot of an index. Its layout:
//
//	┌───────────────────────────────────────────────┐
//	│ Header:  magic "SEEK" (4) │ version (4)         │
//	├───────────────────────────────────────────────┤
//	│ Data section: consecutive term entries, grouped │
//	│   logically into fixed-size BLOCKS. Each entry:  │
//	│     termLen(4) │ term │ plLen(4) │ postingsJSON  │
//	│   Terms are stored in ascending order.           │
//	├───────────────────────────────────────────────┤
//	│ Sparse index: one pointer PER BLOCK:             │
//	│   numBlocks(4) │ for each: firstTermLen(4),      │
//	│     firstTerm, blockOffset(8), entryCount(4)     │
//	├───────────────────────────────────────────────┤
//	│ Meta section: documents + lengths + totals(JSON) │
//	├───────────────────────────────────────────────┤
//	│ Footer (fixed 32B): indexOffset(8) metaOffset(8) │
//	│   numTerms(8) crc(4) magic(4)                    │
//	└───────────────────────────────────────────────┘
//
// The sparse index is the key idea: instead of one pointer per term (huge), we
// keep one pointer per *block* (say 32 terms). To find a term we binary-search the
// sparse index to the one block that could contain it, then linearly scan just
// that block. This trades a tiny bit of scan for a much smaller index — exactly
// how LevelDB/RocksDB block indexes work.

const (
	ssMagic     = "SEEK"
	ssVersion   = uint32(1)
	ssBlockSize = 32 // terms per block (sparse-index granularity)
	ssHeaderLen = 8  // magic(4) + version(4)
	ssFooterLen = 32 // indexOffset(8) + metaOffset(8) + numTerms(8) + crc(4) + magic(4)
)

// metaBlob holds the non-term state we must persist to fully rebuild an index.
type metaBlob struct {
	Documents   map[string]index.Document `json:"documents"`
	DocLengths  map[string]int            `json:"doc_lengths"`
	TotalTokens int                       `json:"total_tokens"`
}

// blockPtr is one sparse-index entry.
type blockPtr struct {
	firstTerm  string
	offset     int64
	entryCount uint32
}

// WriteSSTable serializes a snapshot to path atomically: it writes to a temp file
// and renames it into place, so a crash mid-write never leaves a half-written
// table where a valid one used to be.
func WriteSSTable(path string, snap *index.Snapshot) error {
	terms := snap.SortedTerms()

	// --- data section + collect block pointers ---
	var data bytes.Buffer
	var ptrs []blockPtr
	for i := 0; i < len(terms); i += ssBlockSize {
		end := i + ssBlockSize
		if end > len(terms) {
			end = len(terms)
		}
		ptrs = append(ptrs, blockPtr{
			firstTerm:  terms[i],
			offset:     int64(ssHeaderLen + data.Len()),
			entryCount: uint32(end - i),
		})
		for _, term := range terms[i:end] {
			plBytes, err := json.Marshal(snap.Postings[term])
			if err != nil {
				return err
			}
			writeU32(&data, uint32(len(term)))
			data.WriteString(term)
			writeU32(&data, uint32(len(plBytes)))
			data.Write(plBytes)
		}
	}

	// --- sparse index section ---
	var idxBuf bytes.Buffer
	writeU32(&idxBuf, uint32(len(ptrs)))
	for _, p := range ptrs {
		writeU32(&idxBuf, uint32(len(p.firstTerm)))
		idxBuf.WriteString(p.firstTerm)
		writeI64(&idxBuf, p.offset)
		writeU32(&idxBuf, p.entryCount)
	}

	// --- meta section ---
	metaBytes, err := json.Marshal(metaBlob{
		Documents:   snap.Documents,
		DocLengths:  snap.DocLengths,
		TotalTokens: snap.TotalTokens,
	})
	if err != nil {
		return err
	}

	indexOffset := int64(ssHeaderLen + data.Len())
	metaOffset := indexOffset + int64(idxBuf.Len())

	// crc protects the whole payload (data + index + meta).
	crc := crc32.NewIEEE()
	crc.Write(data.Bytes())
	crc.Write(idxBuf.Bytes())
	crc.Write(metaBytes)

	// --- assemble and write atomically ---
	var out bytes.Buffer
	out.WriteString(ssMagic)
	writeU32(&out, ssVersion)
	out.Write(data.Bytes())
	out.Write(idxBuf.Bytes())
	out.Write(metaBytes)
	// footer
	writeI64(&out, indexOffset)
	writeI64(&out, metaOffset)
	writeI64(&out, int64(len(terms)))
	writeU32(&out, crc.Sum32())
	out.WriteString(ssMagic)

	tmp := path + ".tmp"
	if err := os.WriteFile(tmp, out.Bytes(), 0o644); err != nil {
		return err
	}
	return os.Rename(tmp, path)
}

// ReadSSTable reads an entire SSTable back into a Snapshot (used at startup to
// reload the most recent table). It verifies the magic numbers and CRC and errors
// on any corruption.
func ReadSSTable(path string) (*index.Snapshot, error) {
	raw, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}
	if len(raw) < ssHeaderLen+ssFooterLen {
		return nil, fmt.Errorf("sstable %s too small", path)
	}
	if string(raw[0:4]) != ssMagic {
		return nil, fmt.Errorf("sstable %s: bad header magic", path)
	}

	footer := raw[len(raw)-ssFooterLen:]
	if string(footer[len(footer)-4:]) != ssMagic {
		return nil, fmt.Errorf("sstable %s: bad footer magic", path)
	}
	indexOffset := int64(binary.LittleEndian.Uint64(footer[0:8]))
	metaOffset := int64(binary.LittleEndian.Uint64(footer[8:16]))
	footerStart := int64(len(raw) - ssFooterLen)

	if indexOffset < ssHeaderLen || metaOffset < indexOffset || metaOffset > footerStart {
		return nil, fmt.Errorf("sstable %s: corrupt offsets", path)
	}

	dataBytes := raw[ssHeaderLen:indexOffset]
	idxBytes := raw[indexOffset:metaOffset]
	metaBytes := raw[metaOffset:footerStart]

	wantCRC := binary.LittleEndian.Uint32(footer[24:28])
	crc := crc32.NewIEEE()
	crc.Write(dataBytes)
	crc.Write(idxBytes)
	crc.Write(metaBytes)
	if crc.Sum32() != wantCRC {
		return nil, fmt.Errorf("sstable %s: checksum mismatch (corrupt file)", path)
	}

	// Parse the data section (all term entries, sequentially).
	postings := make(map[string]*index.PostingList)
	pos := 0
	for pos < len(dataBytes) {
		term, pl, next, err := parseEntry(dataBytes, pos)
		if err != nil {
			return nil, fmt.Errorf("sstable %s: %w", path, err)
		}
		postings[term] = pl
		pos = next
	}

	var meta metaBlob
	if err := json.Unmarshal(metaBytes, &meta); err != nil {
		return nil, fmt.Errorf("sstable %s: meta: %w", path, err)
	}

	return &index.Snapshot{
		Postings:    postings,
		Documents:   meta.Documents,
		DocLengths:  meta.DocLengths,
		TotalTokens: meta.TotalTokens,
	}, nil
}

// Reader supports point lookups of a single term via the sparse index, without
// loading the whole table into memory. This demonstrates how the on-disk block
// index is actually used to seek.
type Reader struct {
	raw        []byte
	index      []blockPtr
	metaOffset int64
}

// OpenReader loads only the sparse index (and keeps the file bytes) for seeking.
func OpenReader(path string) (*Reader, error) {
	raw, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}
	if len(raw) < ssHeaderLen+ssFooterLen || string(raw[0:4]) != ssMagic {
		return nil, fmt.Errorf("sstable %s: invalid", path)
	}
	footer := raw[len(raw)-ssFooterLen:]
	indexOffset := int64(binary.LittleEndian.Uint64(footer[0:8]))
	metaOffset := int64(binary.LittleEndian.Uint64(footer[8:16]))

	idxBytes := raw[indexOffset:metaOffset]
	ptrs, err := parseSparseIndex(idxBytes)
	if err != nil {
		return nil, err
	}
	return &Reader{raw: raw, index: ptrs, metaOffset: metaOffset}, nil
}

// Get returns the posting list for term, seeking via the sparse index and scanning
// only the one candidate block. Found reports whether the term exists.
func (r *Reader) Get(term string) (*index.PostingList, bool, error) {
	if len(r.index) == 0 {
		return nil, false, nil
	}
	// Binary search for the last block whose firstTerm <= term.
	lo, hi := 0, len(r.index)
	for lo < hi {
		mid := (lo + hi) / 2
		if r.index[mid].firstTerm <= term {
			lo = mid + 1
		} else {
			hi = mid
		}
	}
	if lo == 0 {
		return nil, false, nil // term precedes the first block's first term
	}
	blk := r.index[lo-1]

	pos := int(blk.offset)
	for i := uint32(0); i < blk.entryCount; i++ {
		t, pl, next, err := parseEntry(r.raw, pos)
		if err != nil {
			return nil, false, err
		}
		if t == term {
			return pl, true, nil
		}
		if t > term {
			break // block is sorted; term isn't here
		}
		pos = next
	}
	return nil, false, nil
}

// parseEntry decodes one term entry at offset pos in buf, returning the term, its
// posting list, and the offset of the next entry.
func parseEntry(buf []byte, pos int) (string, *index.PostingList, int, error) {
	if pos+4 > len(buf) {
		return "", nil, 0, fmt.Errorf("truncated term length")
	}
	tl := int(binary.LittleEndian.Uint32(buf[pos : pos+4]))
	pos += 4
	if pos+tl > len(buf) {
		return "", nil, 0, fmt.Errorf("truncated term")
	}
	term := string(buf[pos : pos+tl])
	pos += tl
	if pos+4 > len(buf) {
		return "", nil, 0, fmt.Errorf("truncated postings length")
	}
	pll := int(binary.LittleEndian.Uint32(buf[pos : pos+4]))
	pos += 4
	if pos+pll > len(buf) {
		return "", nil, 0, fmt.Errorf("truncated postings")
	}
	var pl index.PostingList
	if err := json.Unmarshal(buf[pos:pos+pll], &pl); err != nil {
		return "", nil, 0, err
	}
	pos += pll
	return term, &pl, pos, nil
}

// parseSparseIndex decodes the block-pointer index.
func parseSparseIndex(buf []byte) ([]blockPtr, error) {
	if len(buf) < 4 {
		return nil, fmt.Errorf("truncated sparse index")
	}
	n := int(binary.LittleEndian.Uint32(buf[0:4]))
	pos := 4
	ptrs := make([]blockPtr, 0, n)
	for i := 0; i < n; i++ {
		if pos+4 > len(buf) {
			return nil, fmt.Errorf("truncated index entry")
		}
		tl := int(binary.LittleEndian.Uint32(buf[pos : pos+4]))
		pos += 4
		first := string(buf[pos : pos+tl])
		pos += tl
		offset := int64(binary.LittleEndian.Uint64(buf[pos : pos+8]))
		pos += 8
		count := binary.LittleEndian.Uint32(buf[pos : pos+4])
		pos += 4
		ptrs = append(ptrs, blockPtr{firstTerm: first, offset: offset, entryCount: count})
	}
	// The index is written in sorted order already; guard anyway.
	sort.SliceStable(ptrs, func(i, j int) bool { return ptrs[i].firstTerm < ptrs[j].firstTerm })
	return ptrs, nil
}

func writeU32(b *bytes.Buffer, v uint32) {
	var tmp [4]byte
	binary.LittleEndian.PutUint32(tmp[:], v)
	b.Write(tmp[:])
}

func writeI64(b *bytes.Buffer, v int64) {
	var tmp [8]byte
	binary.LittleEndian.PutUint64(tmp[:], uint64(v))
	b.Write(tmp[:])
}
