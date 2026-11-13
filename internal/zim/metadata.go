package zim

import (
	"bufio"
	"bytes"
	"encoding/binary"
	"errors"
	"fmt"
	"io"
	"os"

	"github.com/klauspost/compress/zstd"
)

// Metadata is what a ZIM says about itself in its M namespace. A value it
// lacks is "".
type Metadata struct {
	Name        string // e.g. wikipedia_en_all: what releases of one ZIM share
	Flavour     string // e.g. maxi
	Title       string
	Description string
	Language    string // ISO 639-3, comma-separated
	Creator     string
	Publisher   string
	Date        string // YYYY-MM-DD
	Tags        string // semicolon-separated
}

// ErrCompression means the metadata sits in a cluster compressed in a way
// zimon does not read (xz, zlib or bzip2, which older ZIMs use).
var ErrCompression = errors.New("the ZIM's metadata is compressed with a method zimon does not read")

// Limits that keep a damaged or hostile file from costing much.
const (
	maxBlob     = 1 << 20  // a metadata value
	maxEntry    = 64 << 10 // a directory entry
	maxMetadata = 10000    // entries in the M namespace looked at
	maxUnpacked = 64 << 20 // bytes of a compressed cluster read to reach a value
)

// ReadMetadata reads the metadata of the ZIM at path.
func ReadMetadata(path string) (*Metadata, error) {
	f, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	defer f.Close()
	st, err := f.Stat()
	if err != nil {
		return nil, err
	}
	return MetadataOf(f, st.Size())
}

// MetadataOf reads the metadata of a ZIM of size bytes from r.
func MetadataOf(r io.ReaderAt, size int64) (*Metadata, error) {
	z, err := open(r, size)
	if err != nil {
		return nil, err
	}
	md := &Metadata{}
	want := map[string]*string{"Name": &md.Name, "Flavour": &md.Flavour, "Title": &md.Title,
		"Description": &md.Description, "Language": &md.Language, "Creator": &md.Creator,
		"Publisher": &md.Publisher, "Date": &md.Date, "Tags": &md.Tags}
	// The M namespace is a few dozen entries, side by side: one search for
	// its start, then a walk through it.
	i, err := z.first('M')
	if err != nil {
		return nil, err
	}
	for end := min(z.entries, i+maxMetadata); i < end && len(want) > 0; i++ {
		e, err := z.entry(i)
		if err != nil {
			return nil, err
		}
		if e.namespace != 'M' {
			break
		}
		to, ok := want[e.path]
		if !ok || !e.content {
			continue
		}
		v, err := z.blob(e.cluster, e.blob)
		if err != nil {
			return nil, fmt.Errorf("metadata %s: %w", e.path, err)
		}
		*to = string(bytes.TrimSpace(v))
		delete(want, e.path)
	}
	return md, nil
}

// reader finds entries and blobs in a ZIM.
type reader struct {
	r          io.ReaderAt
	size       int64
	entries    uint32
	clusters   uint32
	pathPtrs   int64
	clusterPtr int64
	unpacked   map[uint32][]byte // compressed clusters, decompressed: metadata shares one
}

func open(r io.ReaderAt, size int64) (*reader, error) {
	b := make([]byte, HeaderSize)
	if _, err := r.ReadAt(b, 0); err != nil {
		return nil, fmt.Errorf("read ZIM header: %w", err)
	}
	if _, err := Parse(b); err != nil {
		return nil, err
	}
	z := &reader{r: r, size: size, unpacked: map[uint32][]byte{},
		entries:    binary.LittleEndian.Uint32(b[24:]),
		clusters:   binary.LittleEndian.Uint32(b[28:]),
		pathPtrs:   int64(binary.LittleEndian.Uint64(b[32:])),
		clusterPtr: int64(binary.LittleEndian.Uint64(b[48:])),
	}
	if !z.fits(z.pathPtrs, 8*int64(z.entries)) || !z.fits(z.clusterPtr, 8*int64(z.clusters)) {
		return nil, errors.New("damaged ZIM: its pointer lists lie outside the file")
	}
	return z, nil
}

// fits reports whether n bytes at off are inside the file.
func (z *reader) fits(off, n int64) bool {
	return off >= 0 && n >= 0 && off <= z.size && n <= z.size-off
}

func (z *reader) u64(off int64) (int64, error) {
	var b [8]byte
	if _, err := z.r.ReadAt(b[:], off); err != nil {
		return 0, err
	}
	v := binary.LittleEndian.Uint64(b[:])
	if v > uint64(z.size) {
		return 0, errors.New("damaged ZIM: a pointer lies outside the file")
	}
	return int64(v), nil
}

// entry is a directory entry, as far as metadata needs it.
type entry struct {
	namespace byte
	path      string
	content   bool // else a redirect or something older
	cluster   uint32
	blob      uint32
}

func (z *reader) entry(i uint32) (*entry, error) {
	off, err := z.u64(z.pathPtrs + 8*int64(i))
	if err != nil {
		return nil, err
	}
	br := bufio.NewReaderSize(io.NewSectionReader(z.r, off, min(z.size-off, maxEntry)), 256)
	var fixed [8]byte
	if _, err := io.ReadFull(br, fixed[:]); err != nil {
		return nil, err
	}
	e := &entry{namespace: fixed[3]}
	mime := binary.LittleEndian.Uint16(fixed[0:])
	switch mime {
	case 0xffff: // a redirect: one index
		if _, err := br.Discard(4); err != nil {
			return nil, err
		}
	case 0xfffe, 0xfffd: // a link target or a deleted entry: nothing more
	default:
		var cb [8]byte
		if _, err := io.ReadFull(br, cb[:]); err != nil {
			return nil, err
		}
		e.content, e.cluster, e.blob = true, binary.LittleEndian.Uint32(cb[0:]), binary.LittleEndian.Uint32(cb[4:])
	}
	path, err := br.ReadBytes(0) // paths of crawled pages can be long
	if err != nil {
		return nil, fmt.Errorf("damaged ZIM: an entry's path: %w", err)
	}
	e.path = string(path[:len(path)-1])
	return e, nil
}

// first returns the index of the first entry in namespace, or the number
// of entries when it has none. Entries are sorted by namespace, then path.
func (z *reader) first(namespace byte) (uint32, error) {
	lo, hi := uint32(0), z.entries
	for lo < hi {
		mid := lo + (hi-lo)/2
		e, err := z.entry(mid)
		if err != nil {
			return 0, err
		}
		if e.namespace < namespace {
			lo = mid + 1
		} else {
			hi = mid
		}
	}
	return lo, nil
}

// Cluster compression, the low 4 bits of a cluster's first byte; bit 4
// says its blob offsets are 8 bytes rather than 4.
const (
	compNone    = 1
	compZlib    = 2 // all three: of ZIMs older than zimon reads metadata from
	compBzip2   = 3
	compXZ      = 4
	compZstd    = 5
	extendedBit = 0x10
)

func (z *reader) blob(cluster, blob uint32) ([]byte, error) {
	if cluster >= z.clusters {
		return nil, errors.New("damaged ZIM: an entry points past the last cluster")
	}
	start, err := z.u64(z.clusterPtr + 8*int64(cluster))
	if err != nil {
		return nil, err
	}
	var info [1]byte
	if _, err := z.r.ReadAt(info[:], start); err != nil {
		return nil, err
	}
	extended := info[0]&extendedBit != 0
	data := io.NewSectionReader(z.r, start+1, z.size-start-1)
	switch info[0] & 0x0f {
	case 0, compNone:
		return readBlob(data, blob, extended)
	case compZstd:
		b, ok := z.unpacked[cluster]
		if !ok {
			if b, err = unzstd(data); err != nil {
				return nil, err
			}
			z.unpacked[cluster] = b
		}
		return readBlob(bytes.NewReader(b), blob, extended)
	case compZlib, compBzip2, compXZ:
		return nil, ErrCompression
	}
	return nil, fmt.Errorf("damaged ZIM: unknown cluster compression %d", info[0]&0x0f)
}

// unzstd decompresses a cluster, up to maxUnpacked bytes of it.
func unzstd(r io.Reader) ([]byte, error) {
	d, err := zstd.NewReader(r, zstd.WithDecoderConcurrency(1), zstd.WithDecoderMaxMemory(maxUnpacked))
	if err != nil {
		return nil, err
	}
	defer d.Close()
	b, err := io.ReadAll(io.LimitReader(d, maxUnpacked))
	if err != nil && len(b) == 0 {
		return nil, err
	}
	return b, nil // what is there; a blob past it fails in readBlob
}

// readBlob reads blob n of a cluster's (uncompressed) data: a list of
// offsets, from the start of the data, then the blobs.
func readBlob(data io.Reader, n uint32, extended bool) ([]byte, error) {
	size := int64(4)
	if extended {
		size = 8
	}
	offset := func() (int64, error) {
		var b [8]byte
		if _, err := io.ReadFull(data, b[:size]); err != nil {
			return 0, err
		}
		if extended {
			return int64(binary.LittleEndian.Uint64(b[:])), nil
		}
		return int64(binary.LittleEndian.Uint32(b[:])), nil
	}
	first, err := offset()
	if err != nil {
		return nil, err
	}
	if first < 2*size || first%size != 0 || int64(n) >= first/size-1 {
		return nil, errors.New("damaged ZIM: a blob is not in its cluster")
	}
	var from, to int64
	read := size // bytes of data read so far
	for i := int64(1); i <= int64(n)+1; i++ {
		o, err := offset()
		if err != nil {
			return nil, err
		}
		read += size
		if i == int64(n) {
			from = o
		}
		to = o
	}
	if n == 0 {
		from = first
	}
	if from < first || to < from || to-from > maxBlob {
		return nil, errors.New("damaged ZIM: a blob's offsets are wrong")
	}
	if _, err := io.CopyN(io.Discard, data, from-read); err != nil {
		return nil, err
	}
	b := make([]byte, to-from)
	_, err = io.ReadFull(data, b)
	return b, err
}
