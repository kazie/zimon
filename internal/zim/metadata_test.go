package zim_test

import (
	"bytes"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/kazie/zimon/internal/zim"
	"github.com/kazie/zimon/internal/zim/zimtest"
)

// hello_en_all was written by libzim (zimwriterfs), metadata zstd-compressed.
func TestMetadataOfARealZIM(t *testing.T) {
	md, err := zim.ReadMetadata("../server/testdata/hello_en_all_2026-11.zim")
	if err != nil {
		t.Fatal(err)
	}
	want := zim.Metadata{Name: "hello_en_all", Title: "Hello", Description: "A tiny test ZIM", Language: "eng",
		Creator: "zimon", Publisher: "zimon", Date: "2026-11-08", Tags: "_ftindex:yes;_ftindex"}
	if *md != want {
		t.Errorf("got %+v\nwant %+v", *md, want)
	}
}

func TestMetadata(t *testing.T) {
	meta := map[string]string{"Name": "mysite_en_all", "Title": "My site", "Date": "2026-10-01", "Language": "eng",
		"Illustration_48x48@1": "\x89PNG", "Counter": "text/html=3"}
	for _, tc := range []struct {
		name string
		z    zimtest.ZIM
		err  error
	}{
		{"plain", zimtest.ZIM{Metadata: meta}, nil},
		{"zstd", zimtest.ZIM{Metadata: meta, Zstd: true}, nil},
		{"with content", zimtest.ZIM{Metadata: meta, Zstd: true, Filler: 5000}, nil},
		{"a long path", zimtest.ZIM{Metadata: meta, Zstd: true, Filler: 10, FillerAt: "page?" + strings.Repeat("q=x&", 3000)}, nil},
		{"xz", zimtest.ZIM{Metadata: meta, XZ: true}, zim.ErrCompression},
	} {
		t.Run(tc.name, func(t *testing.T) {
			tc.z.UUID = "00112233-4455-6677-8899-aabbccddeeff"
			b := zimtest.Build(tc.z)
			md, err := zim.MetadataOf(bytes.NewReader(b), int64(len(b)))
			if !errors.Is(err, tc.err) {
				t.Fatalf("err = %v, want %v", err, tc.err)
			}
			if err != nil {
				return
			}
			want := zim.Metadata{Name: "mysite_en_all", Title: "My site", Date: "2026-10-01", Language: "eng"}
			if *md != want {
				t.Errorf("got %+v", *md)
			}
			path := filepath.Join(t.TempDir(), "x.zim")
			if err := os.WriteFile(path, b, 0o644); err != nil {
				t.Fatal(err)
			}
			if h, err := zim.ReadFile(path); err != nil || h.UUID != tc.z.UUID {
				t.Errorf("header: %+v %v", h, err)
			}
		})
	}
}

func TestMetadataOfDamagedZIMs(t *testing.T) {
	b := zimtest.Build(zimtest.ZIM{Metadata: map[string]string{"Name": "x", "Title": "X"}, Zstd: true})
	for n := 0; n < len(b); n += 7 {
		if _, err := zim.MetadataOf(bytes.NewReader(b[:n]), int64(n)); err == nil && n < len(b)-16 {
			t.Errorf("read metadata from the first %d of %d bytes", n, len(b))
		}
	}
	if _, err := zim.MetadataOf(bytes.NewReader(make([]byte, 100)), 100); !errors.Is(err, zim.ErrNotZIM) {
		t.Errorf("zeros: %v", err)
	}
}

// FuzzMetadataOf: whatever the bytes, reading metadata fails cleanly.
func FuzzMetadataOf(f *testing.F) {
	meta := map[string]string{"Name": "x_en_all", "Title": "X", "Date": "2026-01-01"}
	f.Add(zimtest.Build(zimtest.ZIM{Metadata: meta}))
	f.Add(zimtest.Build(zimtest.ZIM{Metadata: meta, Zstd: true, Filler: 100}))
	f.Fuzz(func(_ *testing.T, b []byte) {
		_, _ = zim.MetadataOf(bytes.NewReader(b), int64(len(b)))
	})
}
