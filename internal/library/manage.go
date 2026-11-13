package library

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"maps"
	"os"
	"path/filepath"
	"slices"
	"strings"

	"github.com/kazie/zimon/internal/catalog"
	"github.com/kazie/zimon/internal/store"
)

// Removed describes a file zimon deleted (or would delete).
type Removed struct {
	Key    catalog.Key
	Path   string // absolute
	Size   int64
	Reason string

	apply func(context.Context) error // does the deleting
}

// applyAll carries out a plan. Once files start disappearing it finishes even
// on Ctrl-C, or the database would point at files that are gone.
func applyAll(ctx context.Context, plan []Removed) error {
	ctx = context.WithoutCancel(ctx)
	for _, r := range plan {
		if err := r.apply(ctx); err != nil {
			return err
		}
	}
	return nil
}

func keySet(keys []catalog.Key) map[catalog.Key]bool {
	set := make(map[catalog.Key]bool, len(keys))
	for _, k := range keys {
		set[k] = true
	}
	return set
}

// Remove deletes the files (every version) and partial downloads of keys,
// returning what it deleted. With keepFiles the ZIMs stay on disk but zimon
// forgets them. library.toml is not touched.
func (m *Manager) Remove(ctx context.Context, keys []catalog.Key, keepFiles bool) ([]Removed, error) {
	want := keySet(keys)
	files, err := m.Store.Files(ctx)
	if err != nil {
		return nil, err
	}
	downloads, err := m.Store.Downloads(ctx)
	if err != nil {
		return nil, err
	}
	var plan, forget []Removed // forget: --keep-file rows, not shown as deletions
	for _, f := range files {
		if want[f.Key()] {
			r := m.fileRemoval(f, f.State, keepFiles)
			if keepFiles {
				forget = append(forget, r)
			} else {
				plan = append(plan, r)
			}
		}
	}
	for _, d := range downloads {
		if want[d.Key()] {
			plan = append(plan, m.downloadRemoval(d, "partial download"))
		}
	}
	// Like Install: library.xml must never list a file that is gone.
	if err := m.writeLibraryXML(context.WithoutCancel(ctx), want); err != nil {
		return plan, err
	}
	if err := applyAll(ctx, append(plan, forget...)); err != nil {
		return plan, err
	}
	return plan, m.WriteLibraryXML(context.WithoutCancel(ctx))
}

// Prune deletes superseded versions beyond keep_versions, partial downloads
// that can no longer finish (not wanted, or the catalog moved on or dropped
// the ZIM) and stray files in the partial directory. While library.toml has
// a typo (see typo), unwanted partial downloads stay. With dryRun it only
// reports.
func (m *Manager) Prune(ctx context.Context, dryRun bool) ([]Removed, error) {
	plan, err := m.prunePlan(ctx)
	if err != nil || dryRun {
		return plan, err
	}
	return plan, applyAll(ctx, plan)
}

// PruneConfirmed deletes what is still prunable among confirmed (by path):
// what the user agreed to, minus anything that changed since.
func (m *Manager) PruneConfirmed(ctx context.Context, confirmed []Removed) ([]Removed, error) {
	plan, err := m.prunePlan(ctx)
	if err != nil {
		return nil, err
	}
	ok := map[string]bool{}
	for _, r := range confirmed {
		ok[r.Path] = true
	}
	plan = slices.DeleteFunc(plan, func(r Removed) bool { return !ok[r.Path] })
	return plan, applyAll(ctx, plan)
}

func (m *Manager) prunePlan(ctx context.Context) ([]Removed, error) {
	var plan []Removed
	files, err := m.Store.Files(ctx) // grouped by key, newest first
	if err != nil {
		return nil, err
	}
	for start := 0; start < len(files); {
		end := start + 1
		for end < len(files) && files[end].Key() == files[start].Key() {
			end++
		}
		plan = append(plan, m.oldVersions(files[start:end])...)
		start = end
	}

	s, err := m.LoadSetup(ctx)
	if err != nil {
		return nil, err
	}
	w, err := m.Wanted(ctx, s)
	if err != nil {
		return nil, err
	}
	items, err := m.ItemsFor(ctx, w)
	if err != nil {
		return nil, err
	}
	downloads, err := m.Store.Downloads(ctx)
	if err != nil {
		return nil, err
	}
	// While library.toml has a typo, "not wanted" may be the typo's doing:
	// keep those partial downloads, as plans keep their files.
	here := map[string]bool{}
	for _, f := range files {
		here[f.Name] = true
	}
	for _, d := range downloads {
		here[d.Name] = true
	}
	held := hasTypo(w, func(name string) bool { return here[name] })
	owned := map[string]bool{} // part files that belong to a download row
	for _, d := range downloads {
		owned[m.partPath(d.UUID)] = true
		it := items[d.Key()]
		unwanted := it == nil || !it.Wanted
		if unwanted && held {
			continue
		}
		// A pull's partial may be of an older release than the catalog's.
		if unwanted || it.Entry == nil || (it.Entry.UUID != d.UUID && !d.Pulled) {
			plan = append(plan, m.downloadRemoval(d, "stale partial download"))
		}
	}
	entries, _ := os.ReadDir(m.Cfg.PartialDir())
	for _, de := range entries {
		p := filepath.Join(m.Cfg.PartialDir(), de.Name())
		if !de.IsDir() && !owned[p] {
			plan = append(plan, Removed{Path: p, Size: fileSize(p), Reason: "stray partial file",
				apply: func(context.Context) error { return removeIfExists(p) }})
		}
	}
	slices.SortFunc(plan, func(a, b Removed) int { return strings.Compare(a.Path, b.Path) })
	return plan, nil
}

// oldVersions plans removing the files of one key (newest first) beyond the
// active one and the keep_versions-1 newest superseded ones.
func (m *Manager) oldVersions(files []store.File) []Removed {
	var plan []Removed
	kept := 0
	for _, f := range files {
		if f.State == store.StateActive {
			continue
		}
		if kept < m.Cfg.KeepVersions-1 {
			kept++
			continue
		}
		plan = append(plan, m.fileRemoval(f, "old version", false))
	}
	return plan
}

// fileRemoval plans forgetting f and, unless keepBytes, deleting it.
func (m *Manager) fileRemoval(f store.File, reason string, keepBytes bool) Removed {
	p := m.path(f.Path)
	return Removed{Key: f.Key(), Path: p, Size: f.Size, Reason: reason, apply: func(ctx context.Context) error {
		if !keepBytes {
			if err := removeIfExists(p); err != nil {
				return err
			}
		}
		return m.Store.DeleteFile(ctx, f.UUID)
	}}
}

// downloadRemoval plans deleting a download's partial file and its row.
func (m *Manager) downloadRemoval(d store.Download, reason string) Removed {
	p := m.partPath(d.UUID)
	return Removed{Key: d.Key(), Path: p, Size: fileSize(p), Reason: reason, apply: func(ctx context.Context) error {
		if err := removeIfExists(p); err != nil {
			return err
		}
		return m.Store.DeleteDownload(ctx, d.UUID)
	}}
}

func removeIfExists(p string) error {
	if err := os.Remove(p); err != nil && !errors.Is(err, os.ErrNotExist) {
		return err
	}
	return nil
}

// VerifyResult is the outcome of re-hashing one file.
type VerifyResult struct {
	File store.File
	Err  error // nil when the file matches its recorded sha-256
}

// Verify re-hashes the active files of keys (all when empty) from disk.
func (m *Manager) Verify(ctx context.Context, keys []catalog.Key, obs Observer) ([]VerifyResult, error) {
	files, err := m.Store.ActiveFiles(ctx)
	if err != nil {
		return nil, err
	}
	want := keySet(keys)
	var out []VerifyResult
	for _, f := range files {
		if len(want) > 0 && !want[f.Key()] {
			continue
		}
		res := VerifyResult{File: f}
		sum, err := m.hashPath(ctx, m.path(f.Path), f.Key(), f.Path, f.Size, obs)
		switch {
		case ctx.Err() != nil:
			return out, ctx.Err()
		case err != nil:
			res.Err = err
		case f.SHA256 != "" && sum != f.SHA256:
			res.Err = fmt.Errorf("sha-256 mismatch: file is %s, expected %s", sum, f.SHA256)
		default:
			if err := m.Store.MarkVerified(ctx, f.UUID); err != nil {
				return out, err
			}
		}
		out = append(out, res)
	}
	return out, nil
}

// hashPath returns the sha-256 of the file at path, reporting progress as
// key and name.
func (m *Manager) hashPath(ctx context.Context, path string, key catalog.Key, name string, size int64, obs Observer) (string, error) {
	return m.hash(ctx, path, nil, key, name, size, obs)
}

// hash is hashPath that also copies the file to w, if set.
func (m *Manager) hash(ctx context.Context, path string, w io.Writer, key catalog.Key, name string, size int64, obs Observer) (string, error) {
	fh, err := os.Open(path)
	if err != nil {
		return "", err
	}
	defer fh.Close()
	obs.emit(Event{Kind: EvStart, Key: key, File: name, Total: size})
	h := sha256.New()
	buf := make([]byte, 4<<20)
	var done int64
	for {
		if ctx.Err() != nil {
			return "", ctx.Err()
		}
		n, err := fh.Read(buf)
		h.Write(buf[:n])
		if w != nil {
			if _, err := w.Write(buf[:n]); err != nil {
				return "", err
			}
		}
		done += int64(n)
		obs.emit(Event{Kind: EvProgress, Key: key, File: name, Done: done, Total: size})
		if err == io.EOF {
			break
		}
		if err != nil {
			return "", err
		}
	}
	obs.emit(Event{Kind: EvDone, Key: key, File: name, Done: done, Total: size})
	return hex.EncodeToString(h.Sum(nil)), nil
}

func fileSize(p string) int64 {
	if st, err := os.Stat(p); err == nil {
		return st.Size()
	}
	return 0
}

// SortedItems returns items ordered by key.
func SortedItems(items map[catalog.Key]*Item) []*Item {
	out := make([]*Item, 0, len(items))
	for _, k := range slices.SortedFunc(maps.Keys(items), func(a, b catalog.Key) int { return strings.Compare(a.String(), b.String()) }) {
		out = append(out, items[k])
	}
	return out
}
