package download

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/kazie/zimon/internal/fakekiwix"
	"github.com/kazie/zimon/internal/meta4"
)

const pieceLen = 64 << 10

func setup(t *testing.T, size int64) (*fakekiwix.Server, *httptest.Server, fakekiwix.Book, *meta4.File) {
	t.Helper()
	book := fakekiwix.Book{Name: "test_en_all", Title: "Test", Language: "eng", Category: "other", Date: "2026-10-01", Size: size}
	fk := fakekiwix.New([]fakekiwix.Book{book})
	fk.PieceLength = pieceLen
	srv := httptest.NewServer(fk)
	t.Cleanup(srv.Close)
	m, _, err := meta4.Fetch(context.Background(), srv.Client(), srv.URL+"/zim/other/"+book.FileName()+".meta4")
	if err != nil {
		t.Fatal(err)
	}
	return fk, srv, book, m
}

func newDownloader(srv *httptest.Server) *Downloader {
	return &Downloader{HTTP: srv.Client(), CheckpointEvery: pieceLen, CheckpointInterval: time.Nanosecond, Backoff: func(int) time.Duration { return 0 }}
}

type checkpoint struct {
	offset int64
	state  []byte
}

func job(t *testing.T, m *meta4.File, cp *checkpoint) Job {
	return Job{
		File:     m,
		PartPath: filepath.Join(t.TempDir(), m.Name+".part"),
		Checkpoint: func(offset int64, state []byte) error {
			cp.offset, cp.state = offset, state
			return nil
		},
	}
}

func checkFile(t *testing.T, path string, want []byte) {
	t.Helper()
	got, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(got, want) {
		t.Fatalf("file content differs (%d vs %d bytes)", len(got), len(want))
	}
}

func TestHappyPath(t *testing.T) {
	fk, srv, book, m := setup(t, 5*pieceLen+123)
	var cp checkpoint
	j := job(t, m, &cp)
	var lastProgress int64
	j.Progress = func(done int64) { lastProgress = done }
	got, err := newDownloader(srv).Run(context.Background(), j)
	if err != nil {
		t.Fatal(err)
	}
	content := fk.Content(book)
	sum := sha256.Sum256(content)
	if got != hex.EncodeToString(sum[:]) {
		t.Errorf("SHA256 = %s", got)
	}
	checkFile(t, j.PartPath, content)
	if lastProgress != int64(len(content)) || cp.offset != int64(len(content)) {
		t.Errorf("progress %d, checkpoint %d, want %d", lastProgress, cp.offset, len(content))
	}
	if fk.Hits()["mirror1"] != 1 || fk.Hits()["mirror2"] != 0 {
		t.Errorf("hits = %v", fk.Hits())
	}
}

func TestResumeAfterCut(t *testing.T) {
	fk, srv, book, m := setup(t, 8*pieceLen)
	m.Mirrors = m.Mirrors[:1] // only mirror1, so the cut fails the whole run
	fk.CutMirror(1, 3*pieceLen+500)
	var cp checkpoint
	j := job(t, m, &cp)
	d := newDownloader(srv)
	d.Passes = 1
	if _, err := d.Run(context.Background(), j); err == nil {
		t.Fatal("expected the first run to fail")
	}
	if cp.offset != 3*pieceLen {
		t.Fatalf("checkpoint at %d, want %d (last whole piece)", cp.offset, 3*pieceLen)
	}

	j.Offset, j.HashState = cp.offset, cp.state
	got, err := d.Run(context.Background(), j)
	if err != nil {
		t.Fatal(err)
	}
	content := fk.Content(book)
	sum := sha256.Sum256(content)
	if got != hex.EncodeToString(sum[:]) {
		t.Errorf("resumed hash wrong")
	}
	checkFile(t, j.PartPath, content)
}

func TestBadPieceFailsOver(t *testing.T) {
	fk, srv, book, m := setup(t, 6*pieceLen)
	fk.BreakMirror(1)
	var cp checkpoint
	j := job(t, m, &cp)
	var notes []string
	j.Notify = func(s string) { notes = append(notes, s) }
	if _, err := newDownloader(srv).Run(context.Background(), j); err != nil {
		t.Fatal(err)
	}
	checkFile(t, j.PartPath, fk.Content(book))
	if fk.Hits()["mirror1"] != 1 || fk.Hits()["mirror2"] != 1 {
		t.Errorf("hits = %v", fk.Hits())
	}
	if len(notes) != 1 {
		t.Errorf("notes = %v", notes)
	}
}

// The next mirror is tried at once; only a new round of retries waits,
// and none comes when every mirror sent a bad piece.
func TestBackoffOnlyBetweenRounds(t *testing.T) {
	for _, c := range []struct {
		name   string
		damage func(fk *fakekiwix.Server, n int)
		count  int // mirrors broken, from the first
		waits  int
	}{
		{"one cut", cut, 1, 0},
		{"all cut", cut, 3, 1}, // 3: the canonical URL and both mirrors
		{"all bad", (*fakekiwix.Server).BreakMirror, 3, 0},
	} {
		fk, srv, _, m := setup(t, 2*pieceLen)
		for n := range c.count {
			c.damage(fk, n)
		}
		d := newDownloader(srv)
		d.Passes = 2
		waits := 0
		d.Backoff = func(int) time.Duration { waits++; return 0 }
		var cp checkpoint
		_, _ = d.Run(context.Background(), job(t, m, &cp))
		if waits != c.waits {
			t.Errorf("%s: %d waits, want %d", c.name, waits, c.waits)
		}
	}
}

// cut makes mirror n drop the connection half way into the first piece.
func cut(fk *fakekiwix.Server, n int) { fk.CutMirror(n, pieceLen/2) }

func TestNoRangeMirrorIsSkippedOnResume(t *testing.T) {
	fk, srv, book, m := setup(t, 4*pieceLen)
	var cp checkpoint
	j := job(t, m, &cp)
	d := newDownloader(srv)
	if _, err := d.Run(context.Background(), j); err != nil {
		t.Fatal(err)
	}
	// Pretend we stopped after two pieces, and mirror1 lost Range support.
	full, _ := os.ReadFile(j.PartPath)
	if err := os.WriteFile(j.PartPath, full[:2*pieceLen], 0o644); err != nil {
		t.Fatal(err)
	}
	fk.NoRangeMirror(1)
	h := sha256.New()
	h.Write(full[:2*pieceLen])
	state, _ := h.(interface{ MarshalBinary() ([]byte, error) }).MarshalBinary()
	j.Offset, j.HashState = 2*pieceLen, state
	if _, err := d.Run(context.Background(), j); err != nil {
		t.Fatal(err)
	}
	checkFile(t, j.PartPath, fk.Content(book))
	if fk.Hits()["mirror2"] != 1 {
		t.Errorf("hits = %v", fk.Hits())
	}
}

func TestHashMismatch(t *testing.T) {
	_, srv, _, m := setup(t, 3*pieceLen)
	m.SHA256 = hex.EncodeToString(make([]byte, 32))
	var cp checkpoint
	_, err := newDownloader(srv).Run(context.Background(), job(t, m, &cp))
	if !errors.Is(err, ErrHashMismatch) {
		t.Fatalf("err = %v, want ErrHashMismatch", err)
	}
}

func TestBadResumeStateStartsOver(t *testing.T) {
	fk, srv, book, m := setup(t, 3*pieceLen)
	var cp checkpoint
	j := job(t, m, &cp)
	j.Offset, j.HashState = pieceLen, []byte("garbage")
	if _, err := newDownloader(srv).Run(context.Background(), j); err != nil {
		t.Fatal(err)
	}
	checkFile(t, j.PartPath, fk.Content(book))
}

func TestCancelKeepsProgress(t *testing.T) {
	fk, srv, _, m := setup(t, 16*pieceLen)
	fk.SetThrottle(8 * pieceLen) // two seconds for the whole file
	var cp checkpoint
	j := job(t, m, &cp)
	ctx, cancel := context.WithTimeout(context.Background(), 700*time.Millisecond)
	defer cancel()
	_, err := newDownloader(srv).Run(ctx, j)
	if !errors.Is(err, context.DeadlineExceeded) && !errors.Is(err, context.Canceled) {
		t.Fatalf("err = %v", err)
	}
	if cp.offset == 0 || cp.offset%pieceLen != 0 {
		t.Fatalf("checkpoint = %d; want a non-zero piece boundary", cp.offset)
	}
	if st, _ := os.Stat(j.PartPath); st.Size() < cp.offset {
		t.Fatalf("part file (%d) shorter than checkpoint (%d)", st.Size(), cp.offset)
	}
}

func TestRateLimit(t *testing.T) {
	_, srv, _, m := setup(t, 4*pieceLen)
	var cp checkpoint
	j := job(t, m, &cp)
	j.RateLimit = 2 * pieceLen // 4 pieces at 2 pieces/s: about 1.5 s after the burst
	start := time.Now()
	if _, err := newDownloader(srv).Run(context.Background(), j); err != nil {
		t.Fatal(err)
	}
	if el := time.Since(start); el < time.Second {
		t.Errorf("finished in %v; the rate limit was not applied", el)
	}
}

func TestNoMirrorCanResumeStartsOver(t *testing.T) {
	fk, srv, book, m := setup(t, 4*pieceLen)
	var cp checkpoint
	j := job(t, m, &cp)
	d := newDownloader(srv)
	if _, err := d.Run(context.Background(), j); err != nil {
		t.Fatal(err)
	}
	// Stopped after two pieces last time; now nobody honours Range.
	full, _ := os.ReadFile(j.PartPath)
	if err := os.WriteFile(j.PartPath, full[:2*pieceLen], 0o644); err != nil {
		t.Fatal(err)
	}
	h := sha256.New()
	h.Write(full[:2*pieceLen])
	state, _ := h.(interface{ MarshalBinary() ([]byte, error) }).MarshalBinary()
	j.Offset, j.HashState = 2*pieceLen, state
	for i := 0; i <= 2; i++ {
		fk.NoRangeMirror(i)
	}
	j.Fallback = []string{srv.URL + "/zim/other/" + book.FileName()}
	if _, err := d.Run(context.Background(), j); err != nil {
		t.Fatal(err)
	}
	checkFile(t, j.PartPath, fk.Content(book))
}

func TestLocalErrorStopsAtOnce(t *testing.T) {
	fk, srv, _, m := setup(t, 4*pieceLen)
	var cp checkpoint
	j := job(t, m, &cp)
	calls := 0
	j.Checkpoint = func(int64, []byte) error {
		calls++
		return errors.New("database is locked")
	}
	if _, err := newDownloader(srv).Run(context.Background(), j); err == nil {
		t.Fatal("expected the checkpoint failure to stop the download")
	}
	if hits := fk.Hits(); hits["mirror2"] != 0 {
		t.Errorf("a local failure was blamed on the mirror and retried elsewhere: %v", hits)
	}
}

// A resume sends If-Range: a file published since comes whole (200), and the
// download starts over rather than splice two files.
func TestIfRange(t *testing.T) {
	fk, _, book, m := setup(t, 4*pieceLen)
	content := fk.Content(book)
	etag := `"v1"`
	var asked []string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		asked = append(asked, r.Header.Get("If-Range"))
		w.Header().Set("ETag", etag)
		http.ServeContent(w, r, "x.zim", time.Time{}, bytes.NewReader(content))
	}))
	t.Cleanup(srv.Close)
	m.Mirrors = []meta4.Mirror{{URL: srv.URL}}
	var first *checkpoint
	j := job(t, m, &checkpoint{})
	j.Checkpoint = func(offset int64, state []byte) error {
		if first == nil {
			first = &checkpoint{offset, state}
		}
		return nil
	}
	d := newDownloader(srv)
	if _, err := d.Run(context.Background(), j); err != nil {
		t.Fatal(err)
	}
	for _, tc := range []struct{ ifRange, want string }{{`"v1"`, "resumed"}, {`"v0"`, "started over"}} {
		asked = nil
		j.Offset, j.HashState, j.IfRange = first.offset, first.state, tc.ifRange
		got, err := d.Run(context.Background(), j)
		if err != nil || got != m.SHA256 {
			t.Fatalf("If-Range %s: %s, %v", tc.ifRange, got, err)
		}
		if asked[0] != tc.ifRange || (len(asked) == 1) != (tc.want == "resumed") {
			t.Errorf("If-Range %s: asked %q, want it %s", tc.ifRange, asked, tc.want)
		}
		checkFile(t, j.PartPath, content)
	}
}
