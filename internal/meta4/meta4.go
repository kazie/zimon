// Package meta4 parses Metalink 4 (RFC 5854) files as served by download.kiwix.org.
package meta4

import (
	"bytes"
	"cmp"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/xml"
	"errors"
	"fmt"
	"io"
	"net/http"
	"slices"
	"strings"
	"time"
)

// File describes one downloadable file.
type File struct {
	Name        string
	Size        int64
	SHA256      string   // hex, lower case; may be empty
	PieceLength int64    // 0 when there are no (usable) piece hashes
	Pieces      []string // hex SHA-1 digests, in order
	Mirrors     []Mirror // best first
}

// Mirror is one place to download the file from.
type Mirror struct {
	URL      string
	Priority int // lower is better; 0 means unspecified
}

type xmlMetalink struct {
	Files []xmlFile `xml:"urn:ietf:params:xml:ns:metalink file"`
}

type xmlFile struct {
	Name   string    `xml:"name,attr"`
	Size   int64     `xml:"urn:ietf:params:xml:ns:metalink size"`
	Hashes []xmlHash `xml:"urn:ietf:params:xml:ns:metalink hash"`
	Pieces *struct {
		Length int64     `xml:"length,attr"`
		Type   string    `xml:"type,attr"`
		Hashes []xmlHash `xml:"urn:ietf:params:xml:ns:metalink hash"`
	} `xml:"urn:ietf:params:xml:ns:metalink pieces"`
	URLs []struct {
		Priority int    `xml:"priority,attr"`
		URL      string `xml:",chardata"`
	} `xml:"urn:ietf:params:xml:ns:metalink url"`
}

type xmlHash struct {
	Type  string `xml:"type,attr"`
	Value string `xml:",chardata"`
}

// ParseSHA256 checks a sha-256 in hex and returns it as zimon compares
// them: lower case, without spaces. "" stays "".
func ParseSHA256(s string) (string, error) {
	s = strings.ToLower(strings.TrimSpace(s))
	if b, err := hex.DecodeString(s); s != "" && (err != nil || len(b) != sha256.Size) {
		return "", fmt.Errorf("%q is not a sha-256 (64 hex digits)", s)
	}
	return s, nil
}

// Parse reads the first <file> of a metalink document.
func Parse(r io.Reader) (*File, error) {
	var ml xmlMetalink
	if err := xml.NewDecoder(r).Decode(&ml); err != nil {
		return nil, fmt.Errorf("parse meta4: %w", err)
	}
	if len(ml.Files) == 0 {
		return nil, errors.New("parse meta4: no <file> element")
	}
	xf := ml.Files[0]
	f := &File{Name: strings.TrimSpace(xf.Name), Size: xf.Size}
	if f.Name == "" || strings.ContainsAny(f.Name, `/\`) || f.Name == "." || f.Name == ".." {
		return nil, fmt.Errorf("parse meta4: unsafe file name %q", xf.Name)
	}
	if f.Size <= 0 {
		return nil, errors.New("parse meta4: missing <size>")
	}
	for _, h := range xf.Hashes {
		if strings.EqualFold(h.Type, "sha-256") {
			sum, err := ParseSHA256(h.Value)
			if err != nil {
				return nil, fmt.Errorf("parse meta4: %w", err)
			}
			f.SHA256 = sum
		}
	}
	if p := xf.Pieces; p != nil && strings.EqualFold(p.Type, "sha-1") {
		f.PieceLength = p.Length
		for _, h := range p.Hashes {
			f.Pieces = append(f.Pieces, strings.ToLower(strings.TrimSpace(h.Value)))
		}
		if !piecesOK(f) {
			// A broken piece list counts as none, rather than failing every
			// piece: the sha-256, if any, still checks the whole file.
			f.PieceLength, f.Pieces = 0, nil
		}
	}
	for _, u := range xf.URLs {
		if url := strings.TrimSpace(u.URL); url != "" {
			f.Mirrors = append(f.Mirrors, Mirror{URL: url, Priority: u.Priority})
		}
	}
	// Lower priorities first; unspecified (0) last.
	unset := func(m Mirror) int {
		if m.Priority == 0 {
			return 1
		}
		return 0
	}
	slices.SortStableFunc(f.Mirrors, func(a, b Mirror) int {
		return cmp.Or(cmp.Compare(unset(a), unset(b)), cmp.Compare(a.Priority, b.Priority))
	})
	return f, nil
}

// MaxPieceLength bounds a piece, which a download holds in memory. Kiwix
// uses 4 MiB.
const MaxPieceLength = 64 << 20

// piecesOK reports whether f's pieces cover the file, in well-formed SHA-1
// digests, each a sane size.
func piecesOK(f *File) bool {
	if f.PieceLength <= 0 || f.PieceLength > MaxPieceLength || int64(len(f.Pieces)) != (f.Size-1)/f.PieceLength+1 {
		return false
	}
	for _, p := range f.Pieces {
		if b, err := hex.DecodeString(p); err != nil || len(b) != 20 {
			return false
		}
	}
	return true
}

// Fetch downloads and parses a .meta4. It also returns the raw bytes so a
// download can be resumed later with the same mirror list and hashes.
func Fetch(ctx context.Context, hc *http.Client, url string) (*File, []byte, error) {
	// A .meta4 is a few kB; don't let a stalled server hang the caller.
	ctx, cancel := context.WithTimeout(ctx, time.Minute)
	defer cancel()
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return nil, nil, err
	}
	resp, err := hc.Do(req)
	if err != nil {
		return nil, nil, fmt.Errorf("fetch meta4: %w", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return nil, nil, fmt.Errorf("fetch meta4: %s returned %s", url, resp.Status)
	}
	raw, err := io.ReadAll(io.LimitReader(resp.Body, 16<<20))
	if err != nil {
		return nil, nil, fmt.Errorf("fetch meta4: %w", err)
	}
	f, err := Parse(bytes.NewReader(raw))
	if err != nil {
		return nil, nil, err
	}
	return f, raw, nil
}
