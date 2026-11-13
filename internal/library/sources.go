package library

import (
	"cmp"
	"context"
	"errors"
	"fmt"
	"io"
	"maps"
	"net/http"
	"net/url"
	"os"
	"path"
	"path/filepath"
	"regexp"
	"slices"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/kazie/zimon/internal/catalog"
	"github.com/kazie/zimon/internal/config"
	"github.com/kazie/zimon/internal/download"
	"github.com/kazie/zimon/internal/meta4"
	"github.com/kazie/zimon/internal/setup"
	"github.com/kazie/zimon/internal/store"
	"github.com/kazie/zimon/internal/zim"
)

// A ZIM of library.toml's [sources] comes from a URL or a file instead of
// a catalog. Its current release is checked like a catalog's (CheckSources)
// and cached as a catalog entry with Source set, so planning treats it like
// any other ZIM; Install then downloads it, or links or copies the file in.

// look is what checking a source found: its release, or that it has not
// changed since it was last seen.
type look struct {
	unchanged          bool
	uuid               string
	size               int64
	date               string // YYYY-MM-DD, "" when unknown
	etag, lastModified string
	meta               *zim.Metadata // a file's; nil for a URL, or when unreadable
}

// lookTimeout bounds checking one URL. Sources are checked side by side,
// so one that hangs costs this much, not one each.
const lookTimeout = 10 * time.Second

// lookURL reads the ZIM header at u with a ranged GET, conditional on
// what was seen before (nil: nothing), to learn its release and size.
func (m *Manager) lookURL(ctx context.Context, u string, seen *store.Seen) (*look, error) {
	ctx, cancel := context.WithTimeout(ctx, lookTimeout)
	defer cancel()
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, u, nil)
	if err != nil {
		return nil, err
	}
	req.Header.Set("Range", fmt.Sprintf("bytes=0-%d", zim.HeaderSize-1))
	if seen != nil && seen.Source == u {
		if seen.ETag != "" {
			req.Header.Set("If-None-Match", seen.ETag)
		}
		if seen.LastModified != "" {
			req.Header.Set("If-Modified-Since", seen.LastModified)
		}
	}
	resp, err := m.Client().Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	out := &look{etag: resp.Header.Get("ETag"), lastModified: resp.Header.Get("Last-Modified")}
	switch resp.StatusCode {
	case http.StatusNotModified:
		out.unchanged = true
		return out, nil
	case http.StatusPartialContent:
		_, total, _ := strings.Cut(resp.Header.Get("Content-Range"), "/")
		out.size, _ = strconv.ParseInt(total, 10, 64)
	case http.StatusOK:
		out.size = resp.ContentLength
	default:
		return nil, fmt.Errorf("%s answered %s", u, resp.Status)
	}
	if strings.HasPrefix(resp.Header.Get("Content-Type"), "text/html") {
		return nil, fmt.Errorf("%s is a web page, not a ZIM file (a sign-in page or a bot check?)", u)
	}
	b := make([]byte, zim.HeaderSize)
	if _, err := io.ReadFull(resp.Body, b); err != nil {
		return nil, fmt.Errorf("%s: %w", u, err)
	}
	hdr, err := zim.Parse(b)
	if err != nil {
		return nil, fmt.Errorf("%s: %w", u, err)
	}
	if out.size <= 0 {
		return nil, fmt.Errorf("%s does not say how big the file is", u)
	}
	out.uuid = hdr.UUID
	if t, err := http.ParseTime(out.lastModified); err == nil {
		out.date = t.UTC().Format(time.DateOnly)
	}
	return out, nil
}

// lookFile reads the header and metadata of the ZIM at p, unless the file
// it is (a link's target), its size and modification time are what was
// seen before. (A rebuild into the Nix store has a new path: its files'
// times are all the same.)
func lookFile(p string, seen *store.Seen) (*look, error) {
	target, err := filepath.EvalSymlinks(p)
	if err != nil {
		return nil, err
	}
	st, err := os.Stat(target)
	if err != nil {
		return nil, err
	}
	if !st.Mode().IsRegular() {
		return nil, fmt.Errorf("%s is not a file", p)
	}
	out := &look{etag: fmt.Sprintf("%d-%d %s", st.Size(), st.ModTime().UnixNano(), target), size: st.Size()}
	if seen != nil && seen.Source == p && seen.ETag == out.etag {
		out.unchanged = true
		return out, nil
	}
	hdr, err := zim.ReadFile(p)
	if err != nil {
		return nil, fmt.Errorf("%s: %w", p, err)
	}
	out.uuid = hdr.UUID
	if md, err := zim.ReadMetadata(p); err == nil {
		out.meta = md
	}
	date := ""
	if out.meta != nil {
		date = out.meta.Date
	}
	out.date = cmp.Or(catalog.DateOnly(date), st.ModTime().UTC().Format(time.DateOnly))
	return out, nil
}

// withMetadata is e with what a ZIM says about itself.
func withMetadata(e catalog.Entry, md *zim.Metadata) catalog.Entry {
	if md == nil {
		return e
	}
	e.Title = cmp.Or(md.Title, e.Title)
	e.Summary = cmp.Or(md.Description, e.Summary)
	e.Language = cmp.Or(md.Language, e.Language)
	e.Creator = cmp.Or(md.Creator, e.Creator)
	e.Publisher = cmp.Or(md.Publisher, e.Publisher)
	e.Tags = cmp.Or(md.Tags, e.Tags)
	return e
}

// Found is what checking a source found: its release, and what to record
// of it (see NoteSources).
type Found struct {
	Entry     catalog.Entry // the release
	seen      store.Seen
	unchanged bool // as it was seen before: nothing to record
}

// lookSource checks the source of name, without recording anything. sha,
// if set, is a sha-256 given for the release it finds.
func (m *Manager) lookSource(ctx context.Context, name, src, sha string) (*Found, error) {
	seen, err := m.Store.Seen(ctx, name)
	if err != nil {
		return nil, err
	}
	var prev *catalog.Entry
	if seen != nil && seen.Source == src {
		fam, err := m.Store.EntriesByName(ctx, name)
		if err != nil {
			return nil, err
		}
		if len(fam) == 1 && fam[0].Source == src {
			prev = &fam[0]
		}
	} else {
		seen = nil // another source: what was seen there says nothing here
	}
	check := func(seen *store.Seen) (*look, error) {
		if config.IsURL(src) {
			return m.lookURL(ctx, src, seen)
		}
		return lookFile(src, seen)
	}
	l, err := check(seen)
	if err == nil && l.unchanged && prev == nil {
		l, err = check(nil) // the release is not known here: ask in full
	}
	switch {
	case err != nil:
		return nil, err
	case l.unchanged:
		f := &Found{Entry: *prev, seen: *seen, unchanged: sha == ""}
		if sha != "" { // for the release seen before
			f.seen.SHA256, f.seen.SHA256UUID = sha, prev.UUID
		}
		return f, nil
	}
	e := catalog.Entry{Name: name, Title: name}
	if prev != nil {
		e = *prev // keeps what the ZIM said about itself, until it is fetched again
	}
	e.UUID, e.Date, e.ApproxSize, e.Source = l.uuid, l.date, l.size, src
	e = withMetadata(e, l.meta)
	f := &Found{Entry: e, seen: store.Seen{Name: name, Source: src, ETag: l.etag, LastModified: l.lastModified}}
	switch {
	case sha != "":
		f.seen.SHA256, f.seen.SHA256UUID = sha, l.uuid
	case seen != nil && seen.SHA256UUID == l.uuid:
		f.seen.SHA256, f.seen.SHA256UUID = seen.SHA256, seen.SHA256UUID
	}
	return f, nil
}

// NoteSources records what checks found. A change records a source it
// adds only when it is carried out; a preview plans with what was found.
func (m *Manager) NoteSources(ctx context.Context, fs []*Found) error {
	for _, f := range fs {
		if f.unchanged {
			continue
		}
		if err := m.Store.PutSource(ctx, f.seen, f.Entry); err != nil {
			return fmt.Errorf("%s: %w", f.Entry.Name, err)
		}
	}
	return nil
}

// CheckSources looks for new releases at library.toml's [sources]: at the
// ones not checked yet, or at all of them with all. A source that cannot
// be checked keeps what was seen of it; problems says why.
func (m *Manager) CheckSources(ctx context.Context, all bool) (problems []error, err error) {
	s, err := m.LoadSetup(ctx)
	if err != nil {
		return nil, err
	}
	var names []string
	for _, name := range slices.Sorted(maps.Keys(s.Sources)) {
		if !all {
			if seen, err := m.Store.Seen(ctx, name); err != nil {
				return nil, err
			} else if seen != nil && seen.Source == s.Sources[name] {
				continue
			}
		}
		names = append(names, name)
	}
	type result struct {
		f   *Found
		err error
	}
	results := make([]result, len(names))
	var wg sync.WaitGroup
	for i, name := range names {
		wg.Go(func() {
			f, err := m.lookSource(ctx, name, s.Sources[name], "")
			results[i] = result{f, err}
		})
	}
	wg.Wait()
	if ctx.Err() != nil {
		return nil, ctx.Err()
	}
	for i, r := range results {
		err := r.err
		if err == nil {
			err = m.NoteSources(ctx, []*Found{r.f})
		}
		if err != nil {
			problems = append(problems, fmt.Errorf("could not check %s: %w", names[i], err))
		}
	}
	return problems, nil
}

// ForgetSources forgets what was seen of sources library.toml no longer
// has. Call it under the state lock: a zimon add records its source before
// it saves library.toml.
func (m *Manager) ForgetSources(ctx context.Context) error {
	s, err := m.LoadSetup(ctx)
	if err != nil {
		return err
	}
	return m.Store.KeepSources(ctx, slices.Sorted(maps.Keys(s.Sources)))
}

// AddSource checks a ZIM to add from a URL or a file (an absolute path),
// without recording anything. It is named name or, when that is "", after
// the file's own name (a file) or its file name. sha, if set, is the
// sha-256 it must have. A Change then adds what it found (see AddChange and
// Change.Sources).
func (m *Manager) AddSource(ctx context.Context, from, name, sha string) (*Found, error) {
	file, err := setup.CheckSource(from)
	if err != nil {
		return nil, err
	}
	if sha, err = meta4.ParseSHA256(sha); err != nil {
		return nil, fmt.Errorf("--sha256 %w", err)
	}
	if name == "" && file {
		if md, err := zim.ReadMetadata(from); err == nil && setup.CheckSourceName(md.Name) == nil {
			name = md.Name
		}
	}
	if name == "" {
		name = nameOfFile(from)
	}
	if err := setup.CheckSourceName(name); err != nil {
		return nil, fmt.Errorf("%q: %w; choose one with --name", name, err)
	}
	fam, err := m.Store.EntriesByName(ctx, name)
	if err != nil {
		return nil, err
	}
	if slices.ContainsFunc(fam, func(e catalog.Entry) bool { return e.Source == "" }) {
		msg := fmt.Sprintf("%s is in the catalog, so zimon add %s gets it from there", name, name)
		if file {
			msg += fmt.Sprintf("; a copy of its latest release in %s is checked and kept instead of downloaded", m.Cfg.LibraryDir)
		}
		return nil, errors.New(msg + "; or choose another name with --name")
	}
	f, err := m.lookSource(ctx, name, from, sha)
	if err != nil {
		return nil, err
	}
	switch known, err := m.Store.EntryByUUID(ctx, f.Entry.UUID); {
	case err != nil:
		return nil, err
	case known != nil && known.Name != name && known.Source != "":
		return nil, fmt.Errorf("that is the release %s already comes from (%s); zimon add %s", known.Name, known.Source, known.Name)
	case known != nil && known.Name != name:
		return nil, fmt.Errorf("that is a release of %s from the catalog: zimon add %s", known.Key(), known.Key().Exact())
	}
	switch file, err := m.Store.FileOfRelease(ctx, f.Entry.UUID); {
	case err != nil:
		return nil, err
	case file != nil && file.Name != name:
		return nil, fmt.Errorf("that release is in your library already, as %s", file.Key())
	}
	return f, nil
}

// dateSuffix is a release date at the end of a file name. (The portal's
// BookName strips only _YYYY-MM, as kiwix-serve does for its book names; a
// source's name may drop a day too.)
var dateSuffix = regexp.MustCompile(`_\d{4}-\d{2}(-\d{2})?$`)

// nameOfFile is a ZIM name from a URL's or path's file name:
// mysite_en_all_2026-01.zim gives mysite_en_all.
func nameOfFile(from string) string {
	if u, err := url.Parse(from); err == nil && config.IsURL(from) {
		from = u.Path
	}
	return dateSuffix.ReplaceAllString(strings.TrimSuffix(path.Base(filepath.ToSlash(from)), ".zim"), "")
}

// Client is the Manager's HTTP client, or http.DefaultClient.
func (m *Manager) Client() *http.Client { return cmp.Or(m.HTTP, http.DefaultClient) }

// direct reports whether e is a catalog entry whose link is the ZIM itself
// rather than a .meta4 (catalogs other than Kiwix's may have them).
func direct(e catalog.Entry) bool {
	return e.Meta4URL != "" && !strings.HasSuffix(e.Meta4URL, ".meta4")
}

// directSource is the Source to download e from: its URL source, or its
// catalog's direct link. It looks at the URL first: the file's size is
// exact (a catalog's may be rounded), and a source must still have the
// release that was planned, or a resumed download would splice two.
func (m *Manager) directSource(ctx context.Context, e catalog.Entry) (*Source, error) {
	u := cmp.Or(e.Source, e.Meta4URL)
	l, err := m.lookURL(ctx, u, nil)
	switch {
	case err != nil:
		return nil, err
	case e.Source != "" && l.uuid != e.UUID:
		return nil, fmt.Errorf("%s has another release since zimon checked it; run zimon update to look again", u)
	}
	src := &Source{URL: u, HTTP: m.Client(), Name: fileNameOf(u, e), Size: l.size, From: download.Host(u), IfRange: l.lastModified}
	if l.etag != "" && !strings.HasPrefix(l.etag, "W/") { // a weak ETag can't go in If-Range
		src.IfRange = l.etag
	}
	if sha, ok, err := m.givenSHA256(ctx, e); err != nil {
		return nil, err
	} else if ok {
		src.SHA256, src.Given = sha, true
	}
	return src, nil
}

// givenSHA256 is the sha-256 given (zimon add --sha256) for e's release of
// a source, if one was.
func (m *Manager) givenSHA256(ctx context.Context, e catalog.Entry) (string, bool, error) {
	if e.Source == "" {
		return "", false, nil
	}
	seen, err := m.Store.Seen(ctx, e.Name)
	if err != nil || seen == nil || seen.SHA256 == "" || seen.SHA256UUID != e.UUID {
		return "", false, err
	}
	return seen.SHA256, true, nil
}

// fileNameOf is the name a ZIM from u gets in the library folder.
func fileNameOf(u string, e catalog.Entry) string {
	if pu, err := url.Parse(u); err == nil {
		if base := path.Base(pu.Path); strings.HasSuffix(base, ".zim") && base != ".zim" {
			return base
		}
	}
	return e.Name + "_" + cmp.Or(e.Date, "undated") + ".zim"
}

// inLibrary returns p relative to the library dir, when it is in it (and
// not in its partial downloads).
func (m *Manager) inLibrary(p string) (string, bool) {
	dir, err := filepath.EvalSymlinks(m.Cfg.LibraryDir)
	if err != nil {
		dir = m.Cfg.LibraryDir
	}
	rel, err := filepath.Rel(dir, p)
	if err != nil || !filepath.IsLocal(rel) {
		return "", false
	}
	top, _, _ := strings.Cut(filepath.ToSlash(rel), "/")
	return rel, top != filepath.Base(m.Cfg.PartialDir())
}

// installFile installs e from its file (a link's target, if it is a
// symbolic link): in place when it is in the library folder, where it
// becomes the library's own; else as a clone, where the filesystem can
// make one, or a copy, so changing the file later never changes the
// library's.
func (m *Manager) installFile(ctx context.Context, e catalog.Entry, obs Observer) (_ *store.File, err error) {
	k := e.Key()
	src, err := filepath.EvalSymlinks(e.Source)
	if err != nil {
		return nil, err
	}
	if hdr, err := zim.ReadFile(src); err != nil {
		return nil, fmt.Errorf("%s: %w", e.Source, err)
	} else if hdr.UUID != e.UUID {
		return nil, fmt.Errorf("%s changed since zimon checked it; run zimon update to look again", e.Source)
	}
	st, err := os.Stat(src)
	if err != nil {
		return nil, err
	}
	if err := os.MkdirAll(m.Cfg.LibraryDir, 0o755); err != nil {
		return nil, err
	}
	want, how := filepath.Base(e.Source), "in the library folder"
	name, inPlace := m.inLibrary(src)
	var sum string
	if inPlace {
		want = name
		sum, err = m.hashPath(ctx, src, k, name, st.Size(), obs)
	} else {
		name = m.freeName(want, e.UUID)
		sum, how, err = m.bringIn(ctx, e, src, name, st.Size(), obs)
		defer func() {
			if err != nil {
				_ = os.Remove(m.path(name))
			}
		}()
	}
	if err != nil {
		return nil, err
	}
	// The copy's own header and size: the file may have changed meanwhile.
	hdr, err := zim.ReadFile(m.path(name))
	if err != nil {
		return nil, err
	}
	if hdr.UUID != e.UUID {
		return nil, fmt.Errorf("%s changed while zimon took it in; run zimon update to look again", e.Source)
	}
	size := fileSize(m.path(name))
	switch wantSum, given, err := m.givenSHA256(ctx, e); {
	case err != nil:
		return nil, err
	case given && sum != wantSum:
		return nil, fmt.Errorf("%s: %w: it is %s, not the %s given", e.Source, download.ErrHashMismatch, sum, wantSum)
	case given:
		how += ", matches the sha-256 given"
	}
	bg := context.WithoutCancel(ctx)
	e = m.describe(bg, e, m.path(name), obs)
	return m.record(bg, e, placed{name: name, want: want, size: size, sum: sum, zimUUID: hdr.UUID, how: how}, obs)
}

// describe is e with what the ZIM at p says about itself (title and so
// on), noted in the entry of e's release of a [sources] ZIM.
func (m *Manager) describe(ctx context.Context, e catalog.Entry, p string, obs Observer) catalog.Entry {
	md, _ := zim.ReadMetadata(p)
	e = withMetadata(e, md)
	if err := m.Store.NoteMetadata(ctx, e); err != nil {
		obs.emit(Event{Kind: EvNote, Key: e.Key(), Msg: "could not note what the ZIM says about itself: " + err.Error()})
	}
	return e
}

// cloneFile is cloneOf; tests make it fail, as on a filesystem that can't
// clone.
var cloneFile = cloneOf

// bringIn clones the file at src into the library folder as name, or
// copies it when it can't be cloned, and returns its sha-256 and how it
// came.
func (m *Manager) bringIn(ctx context.Context, e catalog.Entry, src, name string, size int64, obs Observer) (sum, how string, err error) {
	k, dst := e.Key(), m.path(name)
	if err := cloneFile(src, dst); err == nil {
		if sum, err = m.hashPath(ctx, dst, k, name, size, obs); err != nil {
			_ = os.Remove(dst)
		}
		syncDir(m.Cfg.LibraryDir)
		return sum, "cloned from " + e.Source, err
	}
	if err := m.roomFor(e.Source, size, size); err != nil {
		return "", "", err
	}
	if err := os.MkdirAll(m.Cfg.PartialDir(), 0o755); err != nil {
		return "", "", err
	}
	part := m.partPath(e.UUID)
	out, err := os.Create(part)
	if err != nil {
		return "", "", err
	}
	defer os.Remove(part) // gone by then when all went well
	sum, err = m.hash(ctx, src, out, k, name, size, obs)
	if err == nil {
		err = out.Sync()
	}
	if cerr := out.Close(); err == nil {
		err = cerr
	}
	if err == nil {
		err = os.Rename(part, dst)
	}
	syncDir(m.Cfg.LibraryDir)
	return sum, "copied from " + e.Source, err
}

// roomFor fails with ErrNoSpace unless the library's disk has room for
// need more bytes of what, a file of size, with a margin.
func (m *Manager) roomFor(what string, need, size int64) error {
	free, ok := freeBytes(m.Cfg.LibraryDir)
	if !ok || free >= need+max(int64(64<<20), size/100) {
		return nil
	}
	return fmt.Errorf("%w: %s needs %s more, but only %s is free in %s",
		ErrNoSpace, what, config.FormatBytes(need), config.FormatBytes(free), m.Cfg.LibraryDir)
}
