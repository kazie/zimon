package kiwix

import (
	"archive/tar"
	"bytes"
	"compress/gzip"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"

	"github.com/kazie/zimon/internal/fakekiwix"
)

var platform = runtime.GOOS + "/" + runtime.GOARCH

// pinFor pins archive for this system under the given file name.
func pinFor(name string, archive []byte) map[string]Pin {
	sum := sha256.Sum256(archive)
	return map[string]Pin{platform: {Archive: name, Size: int64(len(archive)), SHA256: hex.EncodeToString(sum[:])}}
}

// tarGz builds an archive from headers, with body as each regular file's content.
func tarGz(t *testing.T, hdrs ...tar.Header) []byte {
	t.Helper()
	var b bytes.Buffer
	gz := gzip.NewWriter(&b)
	tw := tar.NewWriter(gz)
	for _, h := range hdrs {
		body := "#!/bin/sh\n"
		if h.Typeflag == tar.TypeReg {
			h.Size = int64(len(body))
		}
		if err := tw.WriteHeader(&h); err != nil {
			t.Fatal(err)
		}
		if h.Typeflag == tar.TypeReg {
			_, _ = tw.Write([]byte(body))
		}
	}
	tw.Close()
	gz.Close()
	return b.Bytes()
}

// withPin changes this system's pin in pins.
func withPin(pins map[string]Pin, change func(*Pin)) map[string]Pin {
	p := pins[platform]
	change(&p)
	return map[string]Pin{platform: p}
}

func TestFetch(t *testing.T) {
	fk := fakekiwix.New(nil)
	// Archives the fake doesn't have, served as they are, beside the fake's.
	odd := map[string][]byte{
		"symlink.tar.gz": tarGz(t, tar.Header{Name: "kiwix-tools_x/kiwix-serve", Typeflag: tar.TypeSymlink, Linkname: "/bin/sh"}),
		"deep.tar.gz":    tarGz(t, tar.Header{Name: "a/b/kiwix-serve", Typeflag: tar.TypeReg, Mode: 0o755}),
		"other.tar.gz":   tarGz(t, tar.Header{Name: "kiwix-tools_x/kiwix-manage", Typeflag: tar.TypeReg, Mode: 0o755}),
		"notgz.tar.gz":   []byte("not an archive"),
	}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if b, ok := odd[strings.TrimPrefix(r.URL.Path, "/release/kiwix-tools/")]; ok {
			_, _ = w.Write(b)
			return
		}
		fk.ServeHTTP(w, r)
	}))
	t.Cleanup(srv.Close)
	base := srv.URL + "/release/kiwix-tools/"
	good := pinFor(fakekiwix.KiwixToolsArchive, fakekiwix.KiwixTools())
	oddPin := func(name string) map[string]Pin { return pinFor(name, odd[name]) }

	tests := []struct {
		name    string
		pins    map[string]Pin
		broken  bool
		wantErr string
	}{
		{name: "good", pins: good},
		{name: "a byte wrong", pins: good, broken: true, wantErr: "sha-256 mismatch"},
		{name: "another hash", pins: withPin(good, func(p *Pin) { p.SHA256 = strings.Repeat("0", 64) }), wantErr: "sha-256 mismatch"},
		{name: "another size", pins: withPin(good, func(p *Pin) { p.Size-- }), wantErr: "different file"},
		{name: "missing", pins: pinFor("kiwix-tools_gone.tar.gz", []byte("gone")), wantErr: "404"},
		{name: "a symlink", pins: oddPin("symlink.tar.gz"), wantErr: "no kiwix-serve"},
		{name: "too deep", pins: oddPin("deep.tar.gz"), wantErr: "no kiwix-serve"},
		{name: "no kiwix-serve", pins: oddPin("other.tar.gz"), wantErr: "no kiwix-serve"},
		{name: "not gzip", pins: oddPin("notgz.tar.gz"), wantErr: "gzip"},
		{name: "unsupported", pins: map[string]Pin{}, wantErr: ErrUnsupported.Error()},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			fk.BreakKiwixTools(tt.broken)
			dir := t.TempDir()
			f := Fetcher{BaseURL: base, Pins: tt.pins, Backoff: noWait}
			var last int64
			bin, err := f.Fetch(context.Background(), dir, func(n int64) { last = n })
			if tt.wantErr != "" {
				if err == nil || !strings.Contains(err.Error(), tt.wantErr) {
					t.Fatalf("err = %v, want one with %q", err, tt.wantErr)
				}
				if left, _ := os.ReadDir(dir); len(left) > 0 {
					t.Errorf("left behind: %v", left)
				}
				return
			}
			if err != nil {
				t.Fatal(err)
			}
			if bin != OwnPath(dir) {
				t.Errorf("bin = %s, want %s", bin, OwnPath(dir))
			}
			if last != tt.pins[platform].Size {
				t.Errorf("progress ended at %d, want %d", last, tt.pins[platform].Size)
			}
			fi, err := os.Stat(bin)
			if err != nil {
				t.Fatal(err)
			}
			if fi.Mode().Perm()&0o100 == 0 {
				t.Errorf("mode %v is not executable", fi.Mode())
			}
			body, _ := os.ReadFile(bin)
			if !strings.Contains(string(body), "kiwix-serve $*") {
				t.Errorf("got %q, not the archive's kiwix-serve", body)
			}
		})
	}
	fk.BreakKiwixTools(false)

	t.Run("once, beside older releases", func(t *testing.T) {
		dir := t.TempDir()
		old := filepath.Join(dir, "kiwix-tools-3.7.0")
		if err := os.MkdirAll(old, 0o755); err != nil {
			t.Fatal(err)
		}
		f := Fetcher{BaseURL: base, Pins: good}
		before := fk.Hits()["kiwix-tools"]
		for range 2 {
			if _, err := f.Fetch(context.Background(), dir, nil); err != nil {
				t.Fatal(err)
			}
		}
		if n := fk.Hits()["kiwix-tools"] - before; n != 1 {
			t.Errorf("downloaded %d times, want once", n)
		}
		// Old releases stay until prune: a zimon serve may still run one.
		if got := Old(dir); len(got) != 1 || got[0] != old {
			t.Errorf("Old = %q, want [%s]", got, old)
		}
	})
}

func noWait(int) time.Duration { return 0 }

// exe writes a stand-in kiwix-serve at path.
func exe(t *testing.T, path string) string {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte("#!/bin/sh\n"), 0o755); err != nil {
		t.Fatal(err)
	}
	return path
}

func TestFind(t *testing.T) {
	tests := []struct {
		name                  string
		configured, path, own bool
		noExec                bool   // the own copy lost its x bits
		want                  string // configured, path, own or "" for errNotInstalled
	}{
		{name: "nothing", want: ""},
		{name: "own copy", own: true, want: "own"},
		{name: "own copy that can't run", own: true, noExec: true, want: ""},
		{name: "PATH before own copy", path: true, own: true, want: "path"},
		{name: "configured first", configured: true, path: true, own: true, want: "configured"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			dir := t.TempDir()
			paths := map[string]string{"configured": filepath.Join(dir, "configured", "kiwix-serve"),
				"path": filepath.Join(dir, "bin", "kiwix-serve"), "own": OwnPath(filepath.Join(dir, "state"))}
			configured := ""
			if tt.configured {
				configured = exe(t, paths["configured"])
			}
			if tt.path {
				exe(t, paths["path"])
			}
			if tt.own {
				exe(t, paths["own"])
				if tt.noExec {
					if err := os.Chmod(paths["own"], 0o644); err != nil {
						t.Fatal(err)
					}
				}
			}
			t.Setenv("PATH", filepath.Join(dir, "bin"))
			got, err := find(configured, filepath.Join(dir, "state"))
			if tt.want == "" {
				if !errors.Is(err, errNotInstalled) {
					t.Fatalf("got %q, %v; want errNotInstalled", got, err)
				}
				return
			}
			if err != nil || got != paths[tt.want] {
				t.Errorf("got %q, %v; want %s", got, err, paths[tt.want])
			}
		})
	}
}

func TestLocate(t *testing.T) {
	t.Setenv("PATH", t.TempDir())
	dir := t.TempDir()
	pins := pinFor("kiwix-tools_x.tar.gz", []byte("x"))
	got, err := Fetcher{Pins: pins}.Locate("", dir)
	if !errors.Is(err, ErrNotFetched) || got != OwnPath(dir) {
		t.Errorf("fetchable: got %q, %v", got, err)
	}
	if got, err := (Fetcher{Pins: map[string]Pin{}}).Locate("", dir); !errors.Is(err, ErrUnsupported) || got != "" {
		t.Errorf("unsupported: got %q, %v", got, err)
	}
	exe(t, OwnPath(dir))
	if got, err := (Fetcher{Pins: pins}).Locate("", dir); err != nil || got != OwnPath(dir) {
		t.Errorf("fetched: got %q, %v", got, err)
	}
}
