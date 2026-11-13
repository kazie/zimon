// Package zimtest writes small but valid ZIM files for tests: a header, the
// given metadata in the M namespace, and some filler content.
package zimtest

import (
	"bytes"
	"crypto/md5"
	"encoding/binary"
	"maps"
	"slices"

	"github.com/klauspost/compress/zstd"

	"github.com/kazie/zimon/internal/zim"
)

// ZIM describes a file to build.
type ZIM struct {
	UUID     string            // 8-4-4-4-12 hex
	Metadata map[string]string // e.g. "Name": "x_en_all"
	Zstd     bool              // compress the metadata cluster, as libzim does
	XZ       bool              // mark it xz-compressed instead (its bytes stay plain), as old ZIMs are
	Filler   int               // bytes of content in a second cluster, to give the file a size
	FillerAt string            // the content's path; default "filler"
}

// Build returns the file's bytes.
func Build(z ZIM) []byte {
	keys := slices.Sorted(maps.Keys(z.Metadata))
	type dirent struct {
		ns            byte
		path          string
		cluster, blob uint32
	}
	var ents []dirent
	if z.Filler > 0 {
		at := z.FillerAt
		if at == "" {
			at = "filler"
		}
		ents = append(ents, dirent{ns: 'C', path: at, cluster: 1})
	}
	for i, k := range keys {
		ents = append(ents, dirent{ns: 'M', path: k, blob: uint32(i)})
	}
	slices.SortFunc(ents, func(a, b dirent) int {
		if a.ns != b.ns {
			return int(a.ns) - int(b.ns)
		}
		return bytes.Compare([]byte(a.path), []byte(b.path))
	})

	mimes := []byte("text/plain\x00\x00")
	var dirents [][]byte
	for _, e := range ents {
		var b bytes.Buffer
		_ = binary.Write(&b, binary.LittleEndian, uint16(0)) // text/plain
		b.WriteByte(0)                                       // no parameters
		b.WriteByte(e.ns)
		_ = binary.Write(&b, binary.LittleEndian, uint32(0)) // revision
		_ = binary.Write(&b, binary.LittleEndian, e.cluster)
		_ = binary.Write(&b, binary.LittleEndian, e.blob)
		b.WriteString(e.path)
		b.WriteString("\x00\x00") // the path's end, an empty title
		dirents = append(dirents, b.Bytes())
	}

	values := make([][]byte, len(keys))
	for i, k := range keys {
		values[i] = []byte(z.Metadata[k])
	}
	clusters := [][]byte{cluster(values, z.Zstd, z.XZ)}
	if z.Filler > 0 {
		clusters = append(clusters, cluster([][]byte{bytes.Repeat([]byte("zimon "), z.Filler/6+1)[:z.Filler]}, false, false))
	}

	n := uint32(len(ents))
	mimePos := int64(zim.HeaderSize)
	pathPtrPos := mimePos + int64(len(mimes))
	titlePtrPos := pathPtrPos + 8*int64(n)
	pos := titlePtrPos + 4*int64(n)
	var direntPos []int64
	for _, d := range dirents {
		direntPos = append(direntPos, pos)
		pos += int64(len(d))
	}
	clusterPtrPos := pos
	pos += 8 * int64(len(clusters))
	var clusterPos []int64
	for _, c := range clusters {
		clusterPos = append(clusterPos, pos)
		pos += int64(len(c))
	}

	var out bytes.Buffer
	hdr := make([]byte, zim.HeaderSize)
	binary.LittleEndian.PutUint32(hdr[0:], zim.Magic)
	binary.LittleEndian.PutUint16(hdr[4:], 6)
	binary.LittleEndian.PutUint16(hdr[6:], 1)
	if u, err := zim.ParseUUID(z.UUID); err == nil {
		copy(hdr[8:], u)
	}
	binary.LittleEndian.PutUint32(hdr[24:], n)
	binary.LittleEndian.PutUint32(hdr[28:], uint32(len(clusters)))
	binary.LittleEndian.PutUint64(hdr[32:], uint64(pathPtrPos))
	binary.LittleEndian.PutUint64(hdr[40:], uint64(titlePtrPos))
	binary.LittleEndian.PutUint64(hdr[48:], uint64(clusterPtrPos))
	binary.LittleEndian.PutUint64(hdr[56:], uint64(mimePos))
	binary.LittleEndian.PutUint32(hdr[64:], 0xffffffff) // no main page
	binary.LittleEndian.PutUint32(hdr[68:], 0xffffffff)
	binary.LittleEndian.PutUint64(hdr[72:], uint64(pos))
	out.Write(hdr)
	out.Write(mimes)
	for _, p := range direntPos {
		_ = binary.Write(&out, binary.LittleEndian, uint64(p))
	}
	for i := range n {
		_ = binary.Write(&out, binary.LittleEndian, i)
	}
	for _, d := range dirents {
		out.Write(d)
	}
	for _, p := range clusterPos {
		_ = binary.Write(&out, binary.LittleEndian, uint64(p))
	}
	for _, c := range clusters {
		out.Write(c)
	}
	sum := md5.Sum(out.Bytes())
	out.Write(sum[:])
	return out.Bytes()
}

// cluster lays blobs out after their 4-byte offsets, compressed or not.
func cluster(blobs [][]byte, compress, xz bool) []byte {
	var data bytes.Buffer
	off := uint32(4 * (len(blobs) + 1))
	for _, b := range blobs {
		_ = binary.Write(&data, binary.LittleEndian, off)
		off += uint32(len(b))
	}
	_ = binary.Write(&data, binary.LittleEndian, off)
	for _, b := range blobs {
		data.Write(b)
	}
	switch {
	case xz:
		return append([]byte{4}, data.Bytes()...)
	case compress:
		enc, _ := zstd.NewWriter(nil)
		return append([]byte{5}, enc.EncodeAll(data.Bytes(), nil)...)
	}
	return append([]byte{1}, data.Bytes()...)
}
