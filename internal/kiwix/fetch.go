package kiwix

import (
	"archive/tar"
	"compress/gzip"
	"context"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"path"
	"path/filepath"
	"runtime"
	"strings"
	"time"

	"github.com/kazie/zimon/internal/config"
	"github.com/kazie/zimon/internal/download"
	"github.com/kazie/zimon/internal/meta4"
)

// DownloadURL is where Kiwix publishes kiwix-tools releases.
const DownloadURL = "https://download.kiwix.org/release/kiwix-tools/"

// Pin is one kiwix-tools archive as zimon expects it. Kiwix publishes only
// md5 sums, from the same server, so zimon carries the sha-256 itself.
type Pin struct {
	Archive string // file name, under DownloadURL
	Size    int64
	SHA256  string
}

// ErrUnsupported means zimon has no kiwix-tools build pinned for this system.
var ErrUnsupported = fmt.Errorf("kiwix-serve is not installed, and zimon can't fetch it for %s/%s", runtime.GOOS, runtime.GOARCH)

// ErrNotFetched means kiwix-serve isn't installed, and Fetch can get it.
var ErrNotFetched = errors.New("kiwix-serve not installed: zimon serve fetches it")

const ownPrefix = "kiwix-tools-"

// OwnPath is where Fetch puts kiwix-serve: in a folder of the state
// directory named after the release.
func OwnPath(stateDir string) string {
	return filepath.Join(stateDir, ownPrefix+Version, "kiwix-serve")
}

// own returns OwnPath if a kiwix-serve that can run is there.
func own(stateDir string) (string, bool) {
	p := OwnPath(stateDir)
	fi, err := os.Stat(p)
	return p, err == nil && fi.Mode().IsRegular() && fi.Mode().Perm()&0o111 != 0
}

// Old lists what fetches left in stateDir besides Version's kiwix-serve: the
// folders of other releases, and the files of fetches that stopped. A file
// written to in the last hour may be a fetch under way, so it stays.
func Old(stateDir string) []string {
	var old []string
	for _, pattern := range []string{ownPrefix + "*", "." + ownPrefix + "*", filepath.Join(ownPrefix+Version, ".*")} {
		found, _ := filepath.Glob(filepath.Join(stateDir, pattern))
		for _, p := range found {
			hidden := strings.HasPrefix(filepath.Base(p), ".")
			if fi, err := os.Stat(p); filepath.Base(p) == ownPrefix+Version || err != nil ||
				hidden && time.Since(fi.ModTime()) < time.Hour {
				continue
			}
			old = append(old, p)
		}
	}
	return old
}

// Fetcher gets Kiwix's kiwix-serve build for this system. The zero value
// fetches the pinned release from Kiwix.
type Fetcher struct {
	Client    *http.Client                    // nil: http.DefaultClient
	BaseURL   string                          // "": DownloadURL
	Pins      map[string]Pin                  // nil: Pins
	RateLimit int64                           // bytes per second; 0 = unlimited
	Backoff   func(attempt int) time.Duration // between tries; nil: the downloader's
}

// Pin is the archive for this system, or ErrUnsupported.
func (f Fetcher) Pin() (Pin, error) {
	pins := f.Pins
	if pins == nil {
		pins = Pins
	}
	p, ok := pins[runtime.GOOS+"/"+runtime.GOARCH]
	if !ok {
		return Pin{}, ErrUnsupported
	}
	return p, nil
}

// Locate finds kiwix-serve: the configured path, else the one on PATH, else
// the one Fetch put in stateDir. When there is none, it returns OwnPath and
// ErrNotFetched if Fetch can get it there, or ErrUnsupported.
func (f Fetcher) Locate(configured, stateDir string) (string, error) {
	p, err := find(configured, stateDir)
	if !errors.Is(err, errNotInstalled) {
		return p, err
	}
	if _, err := f.Pin(); err != nil {
		return "", err
	}
	return OwnPath(stateDir), ErrNotFetched
}

// Fetch downloads the archive into stateDir, checks its size and sha-256,
// and puts its kiwix-serve at OwnPath(stateDir), unless one is there
// already. progress, if not nil, gets the bytes downloaded so far. A failed
// fetch leaves nothing behind.
func (f Fetcher) Fetch(ctx context.Context, stateDir string, progress func(done int64)) (string, error) {
	pin, err := f.Pin()
	if err != nil {
		return "", err
	}
	if p, ok := own(stateDir); ok {
		return p, nil
	}
	if err := os.MkdirAll(stateDir, 0o755); err != nil {
		return "", err
	}
	part, err := os.CreateTemp(stateDir, ".kiwix-tools-*.tar.gz")
	if err != nil {
		return "", err
	}
	part.Close()
	defer os.Remove(part.Name())
	base := f.BaseURL
	if base == "" {
		base = DownloadURL
	}
	d := download.Downloader{HTTP: f.Client, Backoff: f.Backoff}
	if _, err := d.Run(ctx, download.Job{
		File:      &meta4.File{Name: pin.Archive, Size: pin.Size, SHA256: pin.SHA256},
		Fallback:  []string{base + pin.Archive},
		PartPath:  part.Name(),
		RateLimit: f.RateLimit,
		Progress:  progress,
	}); err != nil {
		return "", fmt.Errorf("%s: %w", pin.Archive, err)
	}
	dest := OwnPath(stateDir)
	if err := extractServe(part.Name(), dest); err != nil {
		return "", fmt.Errorf("%s: %w", pin.Archive, err)
	}
	return dest, nil
}

// CheckArchive reports whether a kiwix-tools archive holds kiwix-serve.
func CheckArchive(r io.Reader) error {
	_, err := findServe(r)
	return err
}

// findServe returns a reader at the archive's kiwix-serve: a regular file at
// the top or in the release's folder.
func findServe(r io.Reader) (*tar.Reader, error) {
	gz, err := gzip.NewReader(r)
	if err != nil {
		return nil, err
	}
	tr := tar.NewReader(gz)
	for {
		hdr, err := tr.Next()
		if errors.Is(err, io.EOF) {
			return nil, errors.New("no kiwix-serve in it")
		}
		if err != nil {
			return nil, err
		}
		dir, name := path.Split(hdr.Name)
		if name == "kiwix-serve" && hdr.Typeflag == tar.TypeReg && !strings.Contains(strings.TrimSuffix(dir, "/"), "/") {
			return tr, nil
		}
	}
}

// extractServe writes the archive's kiwix-serve to dest, whole or not at
// all: two fetches at once both end with a whole file.
func extractServe(archive, dest string) error {
	a, err := os.Open(archive)
	if err != nil {
		return err
	}
	defer a.Close()
	tr, err := findServe(a)
	if err != nil {
		return err
	}
	data, err := io.ReadAll(tr)
	if err != nil {
		return err
	}
	return config.WriteFileAtomic(dest, data, 0o755)
}
