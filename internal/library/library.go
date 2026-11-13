// Package library ties the catalog, the store and the downloader together:
// it adds, updates, removes, prunes and verifies ZIMs, and keeps library.xml in sync.
package library

import (
	"context"
	"maps"
	"net/http"
	"slices"
	"sync"
	"time"

	"github.com/kazie/zimon/internal/catalog"
	"github.com/kazie/zimon/internal/collections"
	"github.com/kazie/zimon/internal/config"

	"github.com/kazie/zimon/internal/setup"
	"github.com/kazie/zimon/internal/store"
)

// Manager runs library operations.
type Manager struct {
	Cfg     *config.Config
	Store   *store.Store
	HTTP    *http.Client
	Backoff func(attempt int) time.Duration // between download rounds; nil: download.Backoff

	SetupPath   string                   // library.toml
	Collections []collections.Collection // built-in plus user collections

	xmlMu sync.Mutex // serialises library.xml rewrites
}

// State is where a key stands.
type State int

// The states. A key is in exactly one.
const (
	NotInLibrary    State = iota // in the catalog only
	Missing                      // wanted, nothing on disk yet
	Downloading                  // wanted, and a partial download exists
	UpToDate                     // the active file is the catalog's latest
	UpdateAvailable              // the catalog has a newer release
	Orphaned                     // wanted, but gone from the catalog; the file is kept
	Unwanted                     // on disk, but library.toml does not ask for it
	Held                         // in [hold]: update and apply leave it as it is (and a switch waits)
)

var stateIDs = map[State]string{
	NotInLibrary: "not_in_library", Missing: "missing", Downloading: "downloading",
	UpToDate: "up_to_date", UpdateAvailable: "update_available", Orphaned: "gone_upstream", Unwanted: "unwanted",
	Held: "held",
}

// MarshalText gives scripts (list --json) a stable identifier, independent of
// the wording String uses for people.
func (s State) MarshalText() ([]byte, error) { return []byte(stateIDs[s]), nil }

// UnmarshalText reads what MarshalText wrote. An id this zimon does not
// know (from a newer server, say) is NotInLibrary.
func (s *State) UnmarshalText(b []byte) error {
	*s = NotInLibrary
	for st, id := range stateIDs {
		if id == string(b) {
			*s = st
		}
	}
	return nil
}

// String is the human label.
func (s State) String() string {
	switch s {
	case Missing:
		return "missing"
	case Downloading:
		return "downloading"
	case UpToDate:
		return "up to date"
	case UpdateAvailable:
		return "update available"
	case Orphaned:
		return "gone upstream"
	case Unwanted:
		return "not wanted"
	case Held:
		return "held"
	default:
		return "not in library"
	}
}

// Item is the combined view of one key.
type Item struct {
	Key      catalog.Key
	Wanted   bool        // library.toml asks for exactly this key
	Want     *setup.Want // library.toml's want for this name, possibly another flavour
	State    State
	Entry    *catalog.Entry  // latest release in the catalog; nil when gone upstream
	File     *store.File     // active file on disk
	Download *store.Download // partial download, if any
}

// Items returns every key zimon knows about locally: wanted by library.toml,
// on disk or downloading.
func (m *Manager) Items(ctx context.Context) (map[catalog.Key]*Item, error) {
	s, err := m.LoadSetup(ctx)
	if err != nil {
		return nil, err
	}
	w, err := m.Wanted(ctx, s)
	if err != nil {
		return nil, err
	}
	return m.ItemsFor(ctx, w)
}

// ItemsFor is Items for an already worked-out Wanted.
func (m *Manager) ItemsFor(ctx context.Context, w *setup.Wanted) (map[catalog.Key]*Item, error) {
	items := map[catalog.Key]*Item{}
	get := func(k catalog.Key) *Item {
		if it, ok := items[k]; ok {
			return it
		}
		it := &Item{Key: k, Want: w.Names[k.Name]}
		items[k] = it
		return it
	}
	for _, want := range w.Names {
		if want.Entry != nil {
			get(want.Entry.Key()).Wanted = true
		}
	}
	files, err := m.Store.ActiveFiles(ctx)
	if err != nil {
		return nil, err
	}
	for i := range files {
		get(files[i].Key()).File = &files[i]
	}
	downloads, err := m.Store.Downloads(ctx)
	if err != nil {
		return nil, err
	}
	for i := range downloads {
		get(downloads[i].Key()).Download = &downloads[i]
	}
	entries, err := m.Store.EntriesByKeys(ctx, slices.Collect(maps.Keys(items)))
	if err != nil {
		return nil, err
	}
	for k, it := range items {
		if e, ok := entries[k]; ok {
			it.Entry = &e
		}
		if it.Want != nil && it.Want.Entry == nil {
			it.Wanted = true // wanted but gone upstream: whatever is here stays
		}
		it.State = classify(it)
	}
	for _, it := range items {
		if held(it, items) {
			it.State = Held
		}
	}
	return items, nil
}

// held reports whether it is a file [hold] keeps: the wanted flavour, or
// another one while the switch to the wanted flavour waits. A ZIM gone
// upstream is said to be so instead.
func held(it *Item, items map[catalog.Key]*Item) bool {
	w := it.Want
	if w == nil || !w.Held || w.Entry == nil {
		return false
	}
	// Another flavour of the name on disk: what a held switch keeps.
	otherOnDisk := func() bool {
		for k, o := range items {
			if k.Name == it.Key.Name && k != it.Key && o.File != nil {
				return true
			}
		}
		return false
	}
	switch {
	case it.Wanted && it.File != nil:
		return true
	case it.Wanted: // the flavour a held switch waits to bring
		return otherOnDisk()
	case it.File != nil: // the flavour the switch would replace
		wanted := items[w.Entry.Key()]
		return wanted == nil || wanted.File == nil
	}
	return false
}

func classify(it *Item) State {
	switch {
	case !it.Wanted && (it.File != nil || it.Download != nil):
		return Unwanted
	case !it.Wanted:
		return NotInLibrary
	case it.Download != nil && it.Entry != nil && it.Download.UUID == it.Entry.UUID:
		return Downloading
	case it.Entry == nil:
		return Orphaned
	case it.File == nil:
		return Missing
	case it.File.UUID != it.Entry.UUID && newer(it.Entry, it.File):
		return UpdateAvailable
	default:
		return UpToDate
	}
}

// newer reports whether release e may replace file f: never back in time,
// though a release without a known date counts (the catalog only lists the
// latest one).
func newer(e *catalog.Entry, f *store.File) bool { return e.Date == "" || e.Date >= f.Date }
