package library

import (
	"bytes"
	"cmp"
	"context"
	"errors"
	"fmt"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/kazie/zimon/internal/catalog"
	"github.com/kazie/zimon/internal/config"
	"github.com/kazie/zimon/internal/download"
	"github.com/kazie/zimon/internal/meta4"
	"github.com/kazie/zimon/internal/store"
	"github.com/kazie/zimon/internal/zim"
)

// EventKind says what an Event reports.
type EventKind int

// The events, roughly in the order a download sends them.
const (
	EvStart    EventKind = iota // a download starts (Total set, Done = resumed bytes)
	EvProgress                  // bytes arrived (Done)
	EvNote                      // something worth showing (Msg): mirror switch, retry
	EvVerify                    // final checks running
	EvDone                      // finished; Msg says how it was verified
	EvRemoved                   // an old version was deleted (File, Total = size)
)

// Event reports progress to whoever drives the Manager (CLI or TUI).
type Event struct {
	Kind  EventKind
	Key   catalog.Key
	File  string
	Done  int64
	Total int64
	Msg   string
}

// Observer receives events. It may be nil.
type Observer func(Event)

func (o Observer) emit(e Event) {
	if o != nil {
		o(e)
	}
}

// ErrNoSpace is returned when the library's filesystem is too full.
var ErrNoSpace = errors.New("not enough free disk space")

// Install downloads e, verifies it and makes it the active version of its key.
// It resumes a previous partial download of the same release. Older versions
// beyond keep_versions are deleted afterwards and library.xml is rewritten.
// A ZIM of [sources] comes from its file or URL instead, and a catalog
// entry that links the ZIM itself from that link.
func (m *Manager) Install(ctx context.Context, e catalog.Entry, obs Observer) (*store.File, error) {
	if f, err := m.installed(ctx, e); f != nil || err != nil {
		return f, err
	}
	switch {
	case e.Source != "" && !config.IsURL(e.Source):
		return m.installFile(ctx, e, obs)
	case e.Source != "" || direct(e):
		src, err := m.directSource(ctx, e)
		if err != nil {
			return nil, err
		}
		e.Meta4URL = "" // there is no .meta4 to check it with
		return m.fetch(ctx, e, src, obs)
	}
	return m.fetch(ctx, e, nil, obs)
}

// installed returns e's file when it is already installed. It redoes the
// cheap last steps in case an earlier run stopped right after recording
// the file.
func (m *Manager) installed(ctx context.Context, e catalog.Entry) (*store.File, error) {
	f, err := m.Store.ActiveFile(ctx, e.Key())
	if err != nil || f == nil || f.UUID != e.UUID {
		return nil, err
	}
	if _, err := os.Stat(m.path(f.Path)); err != nil {
		return nil, nil
	}
	if err := m.Store.DeleteDownload(ctx, e.UUID); err != nil {
		return nil, err
	}
	return f, m.WriteLibraryXML(ctx)
}

// Source is a place to download a release from instead of Kiwix's mirrors:
// another zimon's copy (zimon pull), a URL of [sources], or a catalog's
// direct link. When the release is the catalog's (the entry has its .meta4
// URL), Kiwix's .meta4 checks what it sends, if Kiwix can be reached; else
// SHA256 does, if known.
type Source struct {
	From   string       // the other zimon's name, or the URL's host, for messages
	URL    string       // the file, e.g. https://server/api/v1/files/UUID
	HTTP   *http.Client // sends the token
	Name   string       // the file's name
	Size   int64
	SHA256 string // the other zimon's (which it checked against Kiwix's if Kiwix published one), or Given
	Given  bool   // SHA256 came with zimon add --sha256
	Pull   bool   // another zimon's copy, which may be of an older release than the catalog's
	// IfRange is the URL's ETag or Last-Modified: a resumed download fails
	// rather than splice in a file published meanwhile.
	IfRange string
}

// pull reports whether src is another zimon's copy (zimon pull).
func (src *Source) pull() bool { return src != nil && src.Pull }

// ErrNewerPartial means installing an older release (a pull's, say) would
// throw away a partial download of the catalog's newer one.
var ErrNewerPartial = errors.New("a download of the catalog's newer release is under way")

// meta describes src as a .meta4 would, with src as the only mirror.
func (src *Source) meta() *meta4.File {
	return &meta4.File{Name: src.Name, Size: src.Size, SHA256: src.SHA256, Mirrors: []meta4.Mirror{{URL: src.URL}}}
}

// over is Kiwix's .meta4 for src's release, with src as the only mirror:
// Kiwix's piece hashes and sha-256 check what src sends.
func (src *Source) over(kiwix *meta4.File) *meta4.File {
	m := *kiwix
	m.Mirrors, m.SHA256 = src.meta().Mirrors, cmp.Or(m.SHA256, src.SHA256)
	return &m
}

// Pull is Install with src as the only place to download e from. e is the
// release src has, which may be older than the catalog's.
func (m *Manager) Pull(ctx context.Context, e catalog.Entry, src Source, obs Observer) (*store.File, error) {
	if f, err := m.installed(ctx, e); f != nil || err != nil {
		return f, err
	}
	return m.fetch(ctx, e, &src, obs)
}

func (m *Manager) fetch(ctx context.Context, e catalog.Entry, src *Source, obs Observer) (*store.File, error) {
	k := e.Key()
	if !catalog.SafeID(e.UUID) { // it names the partial download
		return nil, fmt.Errorf("%s: %q is no release id to name a file after", k, e.UUID)
	}
	if err := os.MkdirAll(m.Cfg.LibraryDir, 0o755); err != nil {
		return nil, err
	}

	dl, meta, err := m.prepareDownload(ctx, e, src)
	if err != nil {
		return nil, err
	}
	if !download.Resumes(meta, dl.VerifiedBytes) { // the download starts over: say so
		dl.VerifiedBytes, dl.HashState = 0, nil
	}
	if f, err := m.adopt(ctx, e, dl, meta, obs); err != nil || f != nil {
		return f, err
	}
	partPath := m.partPath(dl.UUID)
	// Bookkeeping that must land even on Ctrl-C (checkpoints, the final
	// install once the file is in place) uses bg.
	bg := context.WithoutCancel(ctx)
	// discard throws the partial away, .meta4 included: if the bytes no longer
	// match, the .meta4 may be stale too, so the next try fetches a fresh one.
	discard := func(err error) error {
		_ = m.downloadRemoval(*dl, "").apply(bg)
		return err
	}

	if err := m.roomFor(meta.Name, meta.Size-dl.VerifiedBytes, meta.Size); err != nil {
		return nil, err
	}

	obs.emit(Event{Kind: EvStart, Key: k, File: meta.Name, Done: dl.VerifiedBytes, Total: meta.Size})
	d := download.Downloader{HTTP: m.Client(), Backoff: m.Backoff}
	var fallback []string
	var ifRange string
	if src != nil {
		d.HTTP, ifRange = src.HTTP, src.IfRange
	} else {
		fallback = []string{strings.TrimSuffix(e.Meta4URL, ".meta4")}
	}
	sum, err := d.Run(ctx, download.Job{
		File:      meta,
		Fallback:  fallback,
		IfRange:   ifRange,
		PartPath:  partPath,
		Offset:    dl.VerifiedBytes,
		HashState: dl.HashState,
		RateLimit: int64(m.Cfg.Download.RateLimit),
		Progress: func(done int64) {
			obs.emit(Event{Kind: EvProgress, Key: k, File: meta.Name, Done: done, Total: meta.Size})
		},
		Checkpoint: func(offset int64, state []byte) error {
			return m.Store.Checkpoint(bg, dl.UUID, offset, state)
		},
		Notify: func(msg string) { obs.emit(Event{Kind: EvNote, Key: k, File: meta.Name, Msg: msg}) },
	})
	if err != nil {
		if errors.Is(err, download.ErrHashMismatch) {
			return nil, fmt.Errorf("%s: %w (the partial file was discarded; run again to retry)", meta.Name, discard(err))
		}
		if !errors.Is(err, context.Canceled) {
			_ = m.Store.SetDownloadError(bg, dl.UUID, err.Error())
		}
		return nil, err
	}

	obs.emit(Event{Kind: EvVerify, Key: k, File: meta.Name, Done: meta.Size, Total: meta.Size})
	hdr, err := zim.ReadFile(partPath)
	if err != nil {
		return nil, fmt.Errorf("%s: %w", meta.Name, discard(err))
	}
	switch {
	case hdr.UUID != e.UUID && e.Source != "":
		return nil, fmt.Errorf("%s: %w", meta.Name, discard(fmt.Errorf("%s changed while it downloaded; run zimon update to look again", e.Source)))
	case hdr.UUID != e.UUID:
		obs.emit(Event{Kind: EvNote, Key: k, File: meta.Name,
			Msg: fmt.Sprintf("file UUID %s differs from the catalog's %s; using the file's", hdr.UUID, e.UUID)})
	case e.Source != "":
		e = m.describe(bg, e, partPath, obs)
	}

	if meta.Name != filepath.Base(meta.Name) || strings.HasPrefix(meta.Name, ".") { // a remote's, say
		return nil, fmt.Errorf("%s: %w", meta.Name, discard(errors.New("not a file name for the library folder")))
	}
	name := m.freeName(meta.Name, e.UUID)
	if err := os.Rename(partPath, m.path(name)); err != nil {
		return nil, err
	}
	syncDir(m.Cfg.LibraryDir)
	verified := "sha-256 verified"
	switch kiwix, err := meta4.Parse(bytes.NewReader(dl.Meta4)); {
	case err == nil && kiwix.SHA256 != "":
	case src != nil && src.Given && meta.SHA256 != "":
		verified = "matches the sha-256 given"
	case src != nil && meta.SHA256 != "":
		verified = "matches " + src.From + "'s sha-256"
	case src != nil:
		verified = "not verified: " + src.From + " has no checksum"
	default:
		verified = "not verified: no checksum was published"
	}
	return m.record(bg, e, placed{name: name, want: meta.Name, size: meta.Size, sum: sum, zimUUID: hdr.UUID, how: verified}, obs)
}

// placed is a file put in the library folder, to record.
type placed struct {
	name, want   string // its name there, and the one it should have (see record)
	size         int64
	sum, zimUUID string
	how          string // how it was checked, in words
}

// record makes the file p the active version of e's key, then deletes the
// download row and versions beyond keep_versions. When its name is a
// stand-in for the one it wants, which the old version had until now, the
// file then gets that name: kiwix-serve names a book after its file, and
// book URLs should not change with each update.
func (m *Manager) record(ctx context.Context, e catalog.Entry, p placed, obs Observer) (*store.File, error) {
	k, name := e.Key(), p.name
	f := store.FileFromEntry(e)
	f.ZimUUID, f.Path, f.Size, f.SHA256, f.State = p.zimUUID, name, p.size, p.sum, store.StateActive
	if err := m.Store.InstallFile(ctx, f); err != nil {
		return nil, err
	}
	// library.xml lists active files only: point it at the new file before any
	// old one is deleted, so a crash in between never leaves it dangling.
	if err := m.WriteLibraryXML(ctx); err != nil {
		return nil, err
	}
	if err := m.Store.DeleteDownload(ctx, e.UUID); err != nil {
		return nil, err
	}
	files, err := m.Store.FilesByKey(ctx, k)
	if err != nil {
		return nil, err
	}
	// The new file is in: an old one that won't go is prune's to try again,
	// not a failed install.
	for _, r := range m.oldVersions(files) {
		if err := r.apply(ctx); err != nil {
			obs.emit(Event{Kind: EvNote, Key: k, File: filepath.Base(r.Path), Msg: "kept the old version: " + err.Error() + " (zimon prune tries again)"})
			continue
		}
		obs.emit(Event{Kind: EvRemoved, Key: k, File: filepath.Base(r.Path), Total: r.Size})
	}
	if name != p.want {
		if err := m.rename(ctx, &f, p.want); err != nil {
			obs.emit(Event{Kind: EvNote, Key: k, File: name, Msg: "kept the name " + name + ": " + err.Error()})
		}
	}
	obs.emit(Event{Kind: EvDone, Key: k, File: p.want, Done: p.size, Total: p.size, Msg: p.how})
	return &f, nil
}

// rename gives the active file f the name to, if it is free. The file has
// both names until library.xml lists the new one, so kiwix-serve never
// looks for a file that isn't there.
func (m *Manager) rename(ctx context.Context, f *store.File, to string) error {
	if _, err := os.Lstat(m.path(to)); !errors.Is(err, os.ErrNotExist) {
		return nil // an older version kept (keep_versions), or another file
	}
	from := f.Path
	if err := os.Link(m.path(from), m.path(to)); err != nil {
		return err
	}
	if err := m.Store.SetFilePath(ctx, f.UUID, to); err != nil {
		_ = os.Remove(m.path(to))
		return err
	}
	f.Path = to
	if err := m.WriteLibraryXML(ctx); err != nil {
		return err
	}
	syncDir(m.Cfg.LibraryDir)
	return os.Remove(m.path(from))
}

// adopt uses a copy of e that is already in the library dir, left there by
// `remove --keep-file` or found after the database was lost, when its
// sha-256 matches. It returns nil (and no error) when there is nothing to
// adopt, and the caller downloads as usual.
func (m *Manager) adopt(ctx context.Context, e catalog.Entry, dl *store.Download, meta *meta4.File, obs Observer) (*store.File, error) {
	p := m.path(meta.Name)
	if st, err := os.Stat(p); err != nil || !st.Mode().IsRegular() || st.Size() != meta.Size || dl.VerifiedBytes > 0 || meta.SHA256 == "" {
		return nil, nil
	}
	files, err := m.Store.Files(ctx)
	if err != nil {
		return nil, err
	}
	for _, f := range files {
		if f.Path == meta.Name {
			return nil, nil // another release's file
		}
	}
	k := e.Key()
	obs.emit(Event{Kind: EvNote, Key: k, File: meta.Name, Msg: meta.Name + " is already in the library folder; checking it instead of downloading"})
	sum, err := m.hashPath(ctx, p, k, meta.Name, meta.Size, obs)
	if err != nil {
		return nil, ctx.Err() // unreadable: download instead, unless interrupted
	}
	hdr, err := zim.ReadFile(p)
	if sum != meta.SHA256 || err != nil {
		return nil, nil
	}
	return m.record(context.WithoutCancel(ctx), e, placed{name: meta.Name, want: meta.Name, size: meta.Size, sum: sum,
		zimUUID: hdr.UUID, how: "found on disk, sha-256 verified"}, obs)
}

// prepareDownload returns the download row for e, creating it (and
// fetching the .meta4) if this release has not been started before. Partial
// downloads of other releases of the same key are dropped: e supersedes
// them, so each key has at most one download in flight. But installing an
// older release than the catalog's (a pull's) keeps a partial download of
// the catalog's: ErrNewerPartial.
func (m *Manager) prepareDownload(ctx context.Context, e catalog.Entry, src *Source) (*store.Download, *meta4.File, error) {
	if err := os.MkdirAll(m.Cfg.PartialDir(), 0o755); err != nil {
		return nil, nil, err
	}
	others, err := m.Store.Downloads(ctx)
	if err != nil {
		return nil, nil, err
	}
	var latest map[catalog.Key]catalog.Entry
	for _, d := range others {
		if d.Key() != e.Key() || d.UUID == e.UUID {
			continue
		}
		if d.VerifiedBytes > 0 && fileSize(m.partPath(d.UUID)) > 0 {
			if latest == nil {
				if latest, err = m.Store.EntriesByKeys(ctx, []catalog.Key{e.Key()}); err != nil {
					return nil, nil, err
				}
			}
			if l := latest[e.Key()]; l.UUID == d.UUID && l.Date > e.Date {
				return nil, nil, fmt.Errorf("%w (%s of %s)", ErrNewerPartial,
					config.FormatBytes(d.VerifiedBytes), config.FormatBytes(d.Size))
			}
		}
		if err := m.downloadRemoval(d, "").apply(ctx); err != nil {
			return nil, nil, err
		}
	}
	dl, err := m.Store.DownloadByUUID(ctx, e.UUID)
	if err != nil {
		return nil, nil, err
	}
	if dl != nil {
		if _, err := os.Stat(m.partPath(dl.UUID)); err != nil {
			dl.VerifiedBytes, dl.HashState = 0, nil
		}
		if meta, err := m.carryOn(ctx, e, dl, src); err != nil || meta != nil {
			return dl, meta, err
		}
		_ = m.Store.DeleteDownload(ctx, dl.UUID) // unreadable; start over
	}
	start := store.Download{UUID: e.UUID, Name: e.Name, Flavour: e.Flavour, Pulled: src.pull(), Meta4: []byte{}}
	var meta *meta4.File
	switch kiwix, raw, err := m.kiwixMeta4(ctx, e, src); {
	case err != nil:
		return nil, nil, err
	case src == nil:
		meta, start.Meta4 = kiwix, raw
	case kiwix != nil:
		meta, start.Meta4 = src.over(kiwix), raw
	default:
		meta = src.meta()
	}
	start.Filename, start.Size = meta.Name, meta.Size
	dl, err = m.Store.StartDownload(ctx, start)
	if err != nil {
		return nil, nil, err
	}
	return dl, meta, nil
}

// kiwixMeta4 fetches Kiwix's .meta4 for e. For a pull it is only a check:
// none when e is not the catalog's release or Kiwix is out of reach. It
// fails when src's sha-256 is not Kiwix's.
func (m *Manager) kiwixMeta4(ctx context.Context, e catalog.Entry, src *Source) (*meta4.File, []byte, error) {
	if src == nil {
		return meta4.Fetch(ctx, m.Client(), e.Meta4URL)
	}
	if e.Meta4URL == "" || direct(e) { // a direct link is the ZIM itself
		return nil, []byte{}, nil
	}
	ctx, cancel := context.WithTimeout(ctx, 15*time.Second)
	defer cancel()
	kiwix, raw, err := meta4.Fetch(ctx, m.Client(), e.Meta4URL)
	switch {
	case err != nil:
		return nil, []byte{}, nil
	case kiwix.SHA256 != "" && src.SHA256 != "" && kiwix.SHA256 != src.SHA256:
		return nil, nil, fmt.Errorf("%s's copy is not the release Kiwix publishes: its sha-256 differs", src.From)
	}
	return kiwix, raw, nil
}

// carryOn is the .meta4 to resume dl with: Kiwix's, with src as the only
// mirror for a pull; or src's alone, for a pull that had no Kiwix .meta4.
// A download from Kiwix fetches the .meta4 a pull's partial lacks. It
// records whether dl is now a pull's. It returns nil when dl's .meta4 is
// unreadable, to start over.
func (m *Manager) carryOn(ctx context.Context, e catalog.Entry, dl *store.Download, src *Source) (*meta4.File, error) {
	kiwix, err := meta4.Parse(bytes.NewReader(dl.Meta4))
	var meta *meta4.File
	switch {
	case src != nil && err != nil:
		meta = src.meta()
	case src != nil:
		meta = src.over(kiwix)
	case err == nil:
		meta = kiwix
	case !dl.Pulled:
		return nil, nil
	default:
		if kiwix, dl.Meta4, err = meta4.Fetch(ctx, m.Client(), e.Meta4URL); err != nil {
			return nil, err
		}
		meta = kiwix
	}
	if pulled := src.pull(); pulled != dl.Pulled { // a new .meta4, if fetched, goes with it
		dl.Pulled = pulled
		if err := m.Store.SetDownloadSource(ctx, dl.UUID, pulled, dl.Meta4); err != nil {
			return nil, err
		}
	}
	return meta, nil
}

// path turns a library-relative path into an absolute one.
func (m *Manager) path(rel string) string { return filepath.Join(m.Cfg.LibraryDir, rel) }

// partPath is where the bytes of release uuid go while they come in. It is
// named by release, so two releases that share a file name never share a
// partial file.
func (m *Manager) partPath(uuid string) string {
	return filepath.Join(m.Cfg.PartialDir(), uuid+".part")
}

// freeName returns name, or a variant with a UUID suffix when name is already
// taken in the library (Kiwix sometimes republishes a ZIM in the same month
// under the same file name, or the user kept a file with remove --keep-file).
// The old file must survive until the new one is in place, and pruning the
// old one must not delete the new one.
func (m *Manager) freeName(name, uuid string) string {
	if _, err := os.Lstat(m.path(name)); errors.Is(err, os.ErrNotExist) {
		return name
	}
	return strings.TrimSuffix(name, ".zim") + "." + uuid[:min(8, len(uuid))] + ".zim"
}

func syncDir(dir string) {
	if d, err := os.Open(dir); err == nil {
		_ = d.Sync()
		d.Close()
	}
}
