// Package download fetches a file described by a .meta4: it tries mirrors in
// order, resumes with Range requests, checks every piece's SHA-1 as it
// arrives and the whole file's SHA-256 at the end.
package download

import (
	"context"
	"crypto/sha1"
	"crypto/sha256"
	"encoding"
	"encoding/hex"
	"errors"
	"fmt"
	"hash"
	"io"
	"net/http"
	"net/url"
	"os"
	"strconv"
	"strings"
	"sync/atomic"
	"time"

	"golang.org/x/time/rate"

	"github.com/kazie/zimon/internal/meta4"
)

// ErrHashMismatch means the finished file's SHA-256 did not match the .meta4.
var ErrHashMismatch = errors.New("sha-256 mismatch")

// errBadPiece means a mirror sent a piece that failed its SHA-1 check.
var errBadPiece = errors.New("piece failed its sha-1 check")

// localError marks a failure on this machine (disk full, I/O error, the
// database), which no other mirror can fix.
type localError struct{ error }

func (e localError) Unwrap() error { return e.error }

// errNoRange means a mirror ignored our Range request, so it can't resume.
var errNoRange = errors.New("mirror does not support resuming (no Range support)")

// Job describes one download.
type Job struct {
	File      *meta4.File
	Fallback  []string // URLs tried after the .meta4 mirrors
	PartPath  string
	Offset    int64  // verified bytes already in PartPath
	HashState []byte // sha-256 state at Offset
	RateLimit int64  // bytes per second; 0 = unlimited
	IfRange   string // sent with Range requests: a strong ETag or a Last-Modified date

	// Progress is called about ten times a second with the bytes received so far,
	// and once more at the end.
	Progress func(done int64)
	// Checkpoint is called after verified bytes are fsynced; persist it to resume later.
	Checkpoint func(offset int64, hashState []byte) error
	// Notify reports events worth showing: mirror switches, retries.
	Notify func(msg string)
}

// Downloader runs jobs.
type Downloader struct {
	HTTP  *http.Client
	Stall time.Duration // drop a connection that sends nothing for this long
	// A checkpoint (fsync + Checkpoint) happens once both CheckpointEvery bytes
	// and CheckpointInterval have passed, so slow disks are not flushed constantly.
	CheckpointEvery    int64
	CheckpointInterval time.Duration
	Passes             int // how many times to cycle through all mirrors
	Backoff            func(attempt int) time.Duration
}

const defaultPiece = 4 << 20

func pieceLength(f *meta4.File) int64 {
	if f.PieceLength > 0 {
		return f.PieceLength
	}
	return defaultPiece
}

// Resumes reports whether a download of f can carry on from offset verified
// bytes: only from a piece boundary, or the very end. Elsewhere it starts over.
func Resumes(f *meta4.File, offset int64) bool {
	return offset > 0 && offset <= f.Size && (offset%pieceLength(f) == 0 || offset == f.Size)
}

func (d *Downloader) defaults() {
	if d.HTTP == nil {
		d.HTTP = http.DefaultClient
	}
	if d.Stall == 0 {
		d.Stall = 60 * time.Second
	}
	if d.CheckpointEvery == 0 {
		d.CheckpointEvery = 64 << 20
	}
	if d.CheckpointInterval == 0 {
		d.CheckpointInterval = 5 * time.Second
	}
	if d.Passes <= 0 {
		d.Passes = 3
	}
	if d.Backoff == nil {
		d.Backoff = Backoff
	}
}

type run struct {
	d          *Downloader
	j          Job
	f          *os.File
	h          hash.Hash
	buf        []byte // one piece
	offset     int64
	saved      int64     // offset at the last checkpoint
	savedAt    time.Time // time of the last checkpoint
	reportedAt time.Time // time of the last Progress call
	pieceL     int64
	limiter    *rate.Limiter
}

// Run downloads j.File into j.PartPath and returns the file's SHA-256. On
// error the part file keeps every verified byte, and Checkpoint has been
// called, so a later Run resumes.
func (d *Downloader) Run(ctx context.Context, j Job) (string, error) {
	d.defaults()
	if j.Progress == nil {
		j.Progress = func(int64) {}
	}
	if j.Checkpoint == nil {
		j.Checkpoint = func(int64, []byte) error { return nil }
	}
	if j.Notify == nil {
		j.Notify = func(string) {}
	}
	f, err := os.OpenFile(j.PartPath, os.O_RDWR|os.O_CREATE, 0o644)
	if err != nil {
		return "", err
	}
	defer f.Close()

	r := &run{d: d, j: j, f: f, h: sha256.New(), pieceL: pieceLength(j.File), savedAt: time.Now()}
	// Resume only from a piece boundary (or the very end) that the part file really has.
	if Resumes(j.File, j.Offset) {
		if st, err := f.Stat(); err == nil && st.Size() >= j.Offset {
			if um, ok := r.h.(encoding.BinaryUnmarshaler); ok && um.UnmarshalBinary(j.HashState) == nil {
				r.offset = j.Offset
			}
		}
	}
	r.saved = r.offset
	if err := f.Truncate(r.offset); err != nil {
		return "", err
	}
	if j.RateLimit > 0 {
		burst := int(min(j.RateLimit, 256<<10))
		r.limiter = rate.NewLimiter(rate.Limit(j.RateLimit), max(burst, 1))
	}
	j.Progress(r.offset)
	if r.offset < j.File.Size {
		r.buf = make([]byte, r.pieceL)
		if err := r.fetchAll(ctx); err != nil {
			return "", err
		}
		j.Progress(r.offset)
	}
	sum := hex.EncodeToString(r.h.Sum(nil))
	if j.File.SHA256 != "" && sum != j.File.SHA256 {
		return "", fmt.Errorf("%w: got %s, want %s", ErrHashMismatch, sum, j.File.SHA256)
	}
	if err := f.Sync(); err != nil {
		return "", err
	}
	return sum, nil
}

func (r *run) fetchAll(ctx context.Context) error {
	urls := make([]string, 0, len(r.j.File.Mirrors)+len(r.j.Fallback))
	for _, m := range r.j.File.Mirrors {
		urls = append(urls, m.URL)
	}
	urls = append(urls, r.j.Fallback...)
	if len(urls) == 0 {
		return errors.New("no mirrors to download from")
	}
	banned := map[string]bool{}
	var lastErr error
	attempt := 0
	restarted := false
	for pass := 0; pass < r.d.Passes; pass++ {
		if pass > 0 {
			// Every mirror failed once more: give them a moment before the
			// next round. Within a round, the next mirror is tried at once.
			attempt++
			select {
			case <-ctx.Done():
				return ctx.Err()
			case <-time.After(r.d.Backoff(attempt)):
			}
		}
		noRange, tried := 0, 0
		for _, u := range urls {
			if banned[u] {
				continue
			}
			tried++
			before := r.offset
			err := r.fetchFrom(ctx, u)
			if cerr := r.checkpoint(); cerr != nil {
				return cerr
			}
			if err == nil {
				return nil
			}
			if ctx.Err() != nil {
				return ctx.Err()
			}
			if errors.As(err, new(localError)) {
				return err
			}
			lastErr = fmt.Errorf("%s: %w", Host(u), err)
			if errors.Is(err, errNoRange) {
				noRange++
				continue // not a transient failure: no point waiting
			}
			if errors.Is(err, errBadPiece) {
				banned[u] = true
			}
			if r.offset > before {
				attempt = 0 // it was making progress; don't punish it with long waits
			}
			r.j.Notify(fmt.Sprintf("%s failed (%v), trying the next mirror", Host(u), err))
		}
		if tried > 0 && noRange == tried && !restarted {
			// Nobody can resume: start over from the first byte (once), and
			// don't count this pass.
			if err := r.restart(); err != nil {
				return err
			}
			r.j.Notify("no mirror supports resuming; starting the download again from the beginning")
			restarted = true
			pass--
		}
		if len(banned) == len(urls) {
			break // every mirror sent a bad piece: waiting won't help
		}
	}
	return fmt.Errorf("download failed after trying every mirror: %w", lastErr)
}

func (r *run) fetchFrom(ctx context.Context, rawURL string) error {
	reqCtx, cancel := context.WithCancel(ctx)
	defer cancel()
	var stalled atomic.Bool
	watchdog := time.AfterFunc(r.d.Stall, func() { stalled.Store(true); cancel() })
	defer watchdog.Stop()

	req, err := http.NewRequestWithContext(reqCtx, http.MethodGet, rawURL, nil)
	if err != nil {
		return err
	}
	if r.offset > 0 {
		req.Header.Set("Range", "bytes="+strconv.FormatInt(r.offset, 10)+"-")
		if r.j.IfRange != "" { // changed since: the whole file comes, as if Range were ignored
			req.Header.Set("If-Range", r.j.IfRange)
		}
	}
	resp, err := r.d.HTTP.Do(req)
	watchdog.Stop() // from here on it only runs during reads; see readPiece
	if err != nil {
		if stalled.Load() {
			return fmt.Errorf("no response for %s", r.d.Stall)
		}
		return err
	}
	defer resp.Body.Close()
	switch {
	case r.offset > 0 && resp.StatusCode == http.StatusPartialContent:
		if start, ok := rangeStart(resp.Header.Get("Content-Range")); !ok || start != r.offset {
			return fmt.Errorf("asked for bytes from %d, got %q", r.offset, resp.Header.Get("Content-Range"))
		}
	case r.offset == 0 && (resp.StatusCode == http.StatusOK || resp.StatusCode == http.StatusPartialContent):
	case r.offset > 0 && resp.StatusCode == http.StatusOK:
		return errNoRange
	default:
		return fmt.Errorf("HTTP %s", resp.Status)
	}
	if want := r.j.File.Size - r.offset; resp.ContentLength >= 0 && resp.ContentLength != want {
		return fmt.Errorf("mirror has a different file (%d bytes left, expected %d)", resp.ContentLength, want)
	}

	for r.offset < r.j.File.Size {
		idx := r.offset / r.pieceL
		n := min(r.pieceL, r.j.File.Size-r.offset)
		piece := r.buf[:n]
		if err := r.readPiece(reqCtx, resp.Body, piece, watchdog); err != nil {
			if stalled.Load() {
				return fmt.Errorf("no data for %s", r.d.Stall)
			}
			return err
		}
		if len(r.j.File.Pieces) > 0 {
			sum := sha1.Sum(piece)
			if hex.EncodeToString(sum[:]) != r.j.File.Pieces[idx] {
				return fmt.Errorf("%w (piece %d)", errBadPiece, idx)
			}
		}
		if _, err := r.f.WriteAt(piece, r.offset); err != nil {
			return localError{err}
		}
		r.h.Write(piece)
		r.offset += n
		if r.checkpointDue() {
			if err := r.checkpoint(); err != nil {
				return localError{err}
			}
		}
	}
	return nil
}

// readPiece fills piece from body. The stall watchdog only runs while
// waiting for the network, so slow disks, slow consumers and rate limiting
// are never mistaken for a dead mirror.
func (r *run) readPiece(ctx context.Context, body io.Reader, piece []byte, watchdog *time.Timer) error {
	const chunk = 64 << 10
	got := 0
	for got < len(piece) {
		end := min(got+chunk, len(piece))
		if r.limiter != nil {
			end = min(end, got+r.limiter.Burst())
		}
		watchdog.Reset(r.d.Stall)
		n, err := body.Read(piece[got:end])
		watchdog.Stop()
		if r.limiter != nil && n > 0 {
			// Pay for what arrived: a TLS read returns at most 16 KiB, so
			// paying up front for the whole chunk would cut the speed to a quarter.
			if werr := r.limiter.WaitN(ctx, n); werr != nil {
				return werr
			}
		}
		if n > 0 {
			got += n
			if now := time.Now(); now.Sub(r.reportedAt) >= 100*time.Millisecond {
				r.reportedAt = now
				r.j.Progress(r.offset + int64(got))
			}
		}
		if err == io.EOF && got < len(piece) {
			return io.ErrUnexpectedEOF
		}
		if err != nil && err != io.EOF {
			return err
		}
	}
	return nil
}

// checkpointDue reports whether enough bytes and time have passed since the
// last checkpoint.
func (r *run) checkpointDue() bool {
	return r.offset-r.saved >= r.d.CheckpointEvery && time.Since(r.savedAt) >= r.d.CheckpointInterval
}

// restart throws away all progress and starts from byte 0.
func (r *run) restart() error {
	if err := r.f.Truncate(0); err != nil {
		return err
	}
	r.offset, r.saved = 0, 0
	r.h.Reset()
	return r.j.Checkpoint(0, nil)
}

// checkpoint fsyncs the part file and records progress, if there is any new.
func (r *run) checkpoint() error {
	if r.offset == r.saved {
		return nil
	}
	if err := r.f.Sync(); err != nil {
		return err
	}
	state, err := r.h.(encoding.BinaryMarshaler).MarshalBinary()
	if err != nil {
		return err
	}
	if err := r.j.Checkpoint(r.offset, state); err != nil {
		return err
	}
	r.saved, r.savedAt = r.offset, time.Now()
	return nil
}

// rangeStart parses "bytes 100-199/200".
func rangeStart(cr string) (int64, bool) {
	rest, ok := strings.CutPrefix(cr, "bytes ")
	if !ok {
		return 0, false
	}
	start, _, ok := strings.Cut(rest, "-")
	if !ok {
		return 0, false
	}
	n, err := strconv.ParseInt(start, 10, 64)
	return n, err == nil
}

// Backoff is how long to wait before retry round attempt: 1 s, doubling,
// at most 30 s.
func Backoff(attempt int) time.Duration { return min(time.Second<<min(attempt, 5), 30*time.Second) }

// Host is a URL's host, for messages; the URL itself when it has none.
func Host(rawURL string) string {
	if u, err := url.Parse(rawURL); err == nil && u.Host != "" {
		return u.Host
	}
	return rawURL
}
