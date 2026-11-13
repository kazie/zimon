package library

import (
	"bytes"
	"cmp"
	"context"
	"crypto/sha256"
	"encoding"
	"encoding/hex"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/kazie/zimon/internal/catalog"
	"github.com/kazie/zimon/internal/download"
	"github.com/kazie/zimon/internal/setup"
	"github.com/kazie/zimon/internal/store"
)

// peer serves content as another zimon's /api/v1/files does, and records
// the Range of each request.
type peer struct {
	*httptest.Server
	mu     sync.Mutex
	ranges []string
}

func newPeer(t *testing.T, content []byte) *peer {
	p := &peer{}
	p.Server = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		p.mu.Lock()
		p.ranges = append(p.ranges, r.Header.Get("Range"))
		p.mu.Unlock()
		http.ServeContent(w, r, "f.zim", time.Time{}, bytes.NewReader(content))
	}))
	t.Cleanup(p.Close)
	return p
}

// pullSource is devdocs_en_go's file in e's fake Kiwix, served by a peer called srv,
// and the file.
func (e *env) pullSource() (Source, *peer, []byte) {
	e.t.Helper()
	b, ok := e.fk.Book("devdocs_en_go")
	if !ok {
		e.t.Fatal("no book devdocs_en_go")
	}
	content := e.fk.Content(b)
	sum := sha256.Sum256(content)
	p := newPeer(e.t, content)
	return Source{From: "srv", URL: p.URL, HTTP: p.Client(), Name: b.FileName(), Size: int64(len(content)), SHA256: hex.EncodeToString(sum[:]), Pull: true}, p, content
}

// A pull checks the file against Kiwix's .meta4 when it is the catalog's
// release and Kiwix can be reached, and else against the other zimon's
// sha-256.
func TestPull(t *testing.T) {
	zeros := hex.EncodeToString(make([]byte, 32))
	for _, c := range []struct {
		name     string
		kiwix    string // "": has the release's .meta4; "offline": out of reach; "none": not the catalog's release
		sha256   string // srv's, when not the file's
		verified string
		err      error // or one whose text has errText
		errText  string
	}{
		{name: "the catalog's release", verified: "sha-256 verified"},
		{name: "Kiwix out of reach", kiwix: "offline", verified: "matches srv's sha-256"},
		{name: "another release", kiwix: "none", verified: "matches srv's sha-256"},
		{name: "not Kiwix's file", sha256: zeros, errText: "srv's copy is not the release Kiwix publishes"},
		{name: "bad bytes, Kiwix out of reach", kiwix: "offline", sha256: zeros, err: download.ErrHashMismatch},
	} {
		t.Run(c.name, func(t *testing.T) {
			e := newEnv(t)
			ctx := context.Background()
			src, _, _ := e.pullSource()
			e.fk.BreakAll() // nothing comes from Kiwix's mirrors
			src.SHA256 = cmp.Or(c.sha256, src.SHA256)
			entry := e.entry("devdocs_en_go")
			switch c.kiwix {
			case "offline":
				entry.Meta4URL = "http://127.0.0.1:9/nothing.meta4"
			case "none":
				entry.Meta4URL = ""
			}
			var verified string
			f, err := e.m.Pull(ctx, entry, src, func(ev Event) {
				if ev.Kind == EvDone {
					verified = ev.Msg
				}
			})
			switch {
			case c.err != nil || c.errText != "":
				if (c.err != nil && !errors.Is(err, c.err)) || !strings.Contains(fmt.Sprint(err), c.errText) {
					t.Fatalf("Pull = %v, want %v %q", err, c.err, c.errText)
				}
				if f, _ := e.m.Store.ActiveFile(ctx, catalog.ParseKey("devdocs_en_go")); f != nil {
					t.Errorf("a file that failed its check is active: %+v", f)
				}
				if dls, _ := e.m.Store.Downloads(ctx); len(dls) != 0 {
					t.Errorf("the partial download was kept: %+v", dls)
				}
			case err != nil:
				t.Fatal(err)
			case f.UUID != entry.UUID || f.SHA256 != src.SHA256 || f.Path != src.Name || verified != c.verified:
				t.Errorf("Pull = %+v, %q; want %q", f, verified, c.verified)
			}
		})
	}
}

// A pull carries on from a partial download from Kiwix of the same release.
func TestPullResumesKiwixPartial(t *testing.T) {
	e := newEnv(t)
	ctx := context.Background()
	entry := e.entry("devdocs_en_go")
	dl, meta, err := e.m.prepareDownload(ctx, entry, nil)
	if err != nil {
		t.Fatal(err)
	}
	src, p, content := e.pullSource()
	e.fk.BreakAll()
	have := 2 * meta.PieceLength
	e.partial(dl, content, have)
	if _, err := e.m.Pull(ctx, entry, src, nil); err != nil {
		t.Fatal(err)
	}
	if want := fmt.Sprintf("bytes=%d-", have); len(p.ranges) != 1 || p.ranges[0] != want {
		t.Errorf("the peer was asked for %q, want [%q]", p.ranges, want)
	}
}

// partial makes dl's partial file hold content up to have, as a checkpoint
// leaves it.
func (e *env) partial(dl *store.Download, content []byte, have int64) {
	e.t.Helper()
	h := sha256.New()
	h.Write(content[:have])
	state, _ := h.(encoding.BinaryMarshaler).MarshalBinary()
	if err := os.WriteFile(e.m.partPath(dl.UUID), content[:have], 0o644); err != nil {
		e.t.Fatal(err)
	}
	if err := e.m.Store.Checkpoint(context.Background(), dl.UUID, have, state); err != nil {
		e.t.Fatal(err)
	}
}

// older is key's catalog entry as an older release.
func (e *env) older(key string) catalog.Entry {
	old := e.entry(key)
	old.UUID, old.Date = "0ld-"+old.UUID, "2020-01-01"
	return old
}

// A pull's partial download of a release older than the catalog's is kept
// for the next pull to resume, while its ZIM is wanted.
func TestPrunePullPartialKept(t *testing.T) {
	e := newEnv(t)
	ctx := context.Background()
	e.want("devdocs_en_go")
	src, _, _ := e.pullSource()
	if _, _, err := e.m.prepareDownload(ctx, e.older("devdocs_en_go"), &src); err != nil {
		t.Fatal(err)
	}
	if plan, err := e.m.Prune(ctx, true); err != nil || len(plan) != 0 {
		t.Errorf("Prune = %+v, %v; want nothing to delete", plan, err)
	}
	e.edit(func(d *setup.Doc) { d.DeleteItem("devdocs_en_go") })
	if plan, err := e.m.Prune(ctx, true); err != nil || len(plan) != 1 {
		t.Errorf("Prune = %+v, %v; want the partial download of a ZIM no longer wanted", plan, err)
	}
}

// A pull of an older release does not throw away a partial download of the
// catalog's release from Kiwix; of a newer release (the local catalog is
// behind), it does.
func TestPullKeepsNewerKiwixPartial(t *testing.T) {
	for _, c := range []struct {
		date string // of the release pulled
		keep bool
	}{
		{"2020-01-01", true},
		{"2099-01-01", false},
	} {
		t.Run(c.date, func(t *testing.T) {
			e := newEnv(t)
			ctx := context.Background()
			dl, _, err := e.m.prepareDownload(ctx, e.entry("devdocs_en_go"), nil)
			if err != nil {
				t.Fatal(err)
			}
			src, _, content := e.pullSource()
			e.partial(dl, content, 64<<10)
			pulled := e.older("devdocs_en_go")
			pulled.Date, pulled.Meta4URL = c.date, ""
			_, _, err = e.m.prepareDownload(ctx, pulled, &src)
			got, _ := e.m.Store.DownloadByUUID(ctx, dl.UUID)
			if c.keep && (!errors.Is(err, ErrNewerPartial) || got == nil || got.VerifiedBytes != 64<<10) {
				t.Errorf("prepareDownload = %v, and the download from Kiwix is now %+v", err, got)
			}
			if !c.keep && (err != nil || got != nil) {
				t.Errorf("prepareDownload = %v, and the download from Kiwix is still %+v", err, got)
			}
		})
	}
}

// A pull that carries on from a Kiwix partial makes it the pull's, which
// prune keeps when the catalog moves on to a newer release.
func TestPullTakesOverKiwixPartial(t *testing.T) {
	e := newEnv(t)
	ctx := context.Background()
	e.want("devdocs_en_go")
	entry := e.entry("devdocs_en_go")
	dl, _, err := e.m.prepareDownload(ctx, entry, nil)
	if err != nil {
		t.Fatal(err)
	}
	src, _, content := e.pullSource()
	e.partial(dl, content, 64<<10)
	if _, _, err := e.m.prepareDownload(ctx, entry, &src); err != nil {
		t.Fatal(err)
	}
	e.fk.Bump("devdocs_en_go")
	e.sync()
	if plan, err := e.m.Prune(ctx, true); err != nil || len(plan) != 0 {
		t.Errorf("Prune = %+v, %v; want the pull's partial download kept", plan, err)
	}
}

// A download from Kiwix carries on from a pull's partial download of the
// same release.
func TestInstallResumesPullPartial(t *testing.T) {
	e := newEnv(t)
	ctx := context.Background()
	entry := e.entry("devdocs_en_go")
	src, _, content := e.pullSource()
	dl, _, err := e.m.prepareDownload(ctx, entry, &src)
	if err != nil {
		t.Fatal(err)
	}
	have := int64(128 << 10) // whole pieces of the fake's
	e.partial(dl, content, have)
	var resumed int64 = -1
	f, err := e.m.Install(ctx, entry, func(ev Event) {
		if ev.Kind == EvStart {
			resumed = ev.Done
		}
	})
	if err != nil {
		t.Fatal(err)
	}
	if resumed != have || f.SHA256 != src.SHA256 {
		t.Errorf("resumed from %d, want %d; file %+v", resumed, have, f)
	}
}
