// Package api holds the HTTP API's wire types, shared by `zimon serve` and
// the client that `zimon --remote NAME` uses for an https:// remote. The
// CLI renders these types for local commands too, so a command shows the
// same thing wherever it runs.
package api

import (
	"path/filepath"
	"time"

	"github.com/kazie/zimon/internal/catalog"
	"github.com/kazie/zimon/internal/collections"
	"github.com/kazie/zimon/internal/library"
	"github.com/kazie/zimon/internal/setup"
	"github.com/kazie/zimon/internal/store"
)

// Version is the API's version. A change that an older client or server
// would misread bumps it; adding a field does not.
const Version = 1

// Status is GET /api/v1/status, the health check. It needs no token.
type Status struct {
	OK    bool   `json:"ok"`
	Kiwix string `json:"kiwix"` // up, down or missing
	API   int    `json:"api"`
}

// Info is GET /api/v1/info: the server's zimon and library.
type Info struct {
	Version     string   `json:"version"`          // zimon's
	Commit      string   `json:"commit,omitempty"` // zimon's, short; empty when unknown
	API         int      `json:"api"`
	LibraryDir  string   `json:"library_dir"`
	LibraryFile string   `json:"library_file"`
	Languages   []string `json:"languages"` // the default language filter
	Entries     int      `json:"catalog_entries"`
	Synced      string   `json:"catalog_synced,omitempty"` // RFC 3339; empty when never fetched
}

// Error is the body of every failed request.
type Error struct {
	Error   string   `json:"error"`
	Similar []string `json:"similar,omitempty"` // for a name not in the catalog: names like it
}

// Item is how scripts see a ZIM zimon knows of: `zimon list --json` and
// GET /api/v1/items. Paths are absolute.
type Item struct {
	UUID          string        `json:"uuid,omitempty"` // of the file on disk (the API serves it at /api/v1/files/UUID)
	Key           string        `json:"key"`
	Name          string        `json:"name"`
	Flavour       string        `json:"flavour"`
	Wanted        bool          `json:"wanted"`        // library.toml asks for it
	Via           []string      `json:"via,omitempty"` // collections, or "items" when hand-picked
	State         library.State `json:"state"`         // stable id, e.g. "update_available"
	Title         string        `json:"title,omitempty"`
	Path          string        `json:"path,omitempty"`
	Size          int64         `json:"size,omitempty"`
	Date          string        `json:"date,omitempty"`
	SHA256        string        `json:"sha256,omitempty"`
	Latest        string        `json:"latest_date,omitempty"`
	LatestURL     string        `json:"latest_meta4,omitempty"`
	LatestSize    int64         `json:"latest_size,omitempty"`    // approximately
	Source        string        `json:"source,omitempty"`         // a URL or file of library.toml's [sources]
	SwitchTo      string        `json:"switch_to,omitempty"`      // unwanted: the flavour library.toml wants instead
	DownloadUUID  string        `json:"download_uuid,omitempty"`  // the release a partial download is of
	Downloaded    int64         `json:"downloaded,omitempty"`     // of a partial download, verified so far
	DownloadSize  int64         `json:"download_size,omitempty"`  // of a partial download
	DownloadError string        `json:"download_error,omitempty"` // why a partial download stopped
}

// ItemOf is it as an Item, with paths under libraryDir.
func ItemOf(it *library.Item, libraryDir string) Item {
	j := Item{Key: it.Key.String(), Name: it.Key.Name, Flavour: it.Key.Flavour, Wanted: it.Wanted, State: it.State}
	if it.Wanted && it.Want != nil {
		j.Via = it.Want.Via
	}
	if !it.Wanted && it.Want != nil && it.Want.Entry != nil {
		j.SwitchTo = it.Want.Entry.Key().String()
	}
	if f := it.File; f != nil {
		j.Title, j.UUID, j.Path, j.Size, j.Date, j.SHA256 = f.Title, f.UUID, filepath.Join(libraryDir, f.Path), f.Size, f.Date, f.SHA256
	}
	if e := it.Entry; e != nil {
		j.Latest, j.LatestURL, j.LatestSize, j.Source = e.Date, e.Meta4URL, e.ApproxSize, e.Source
		if j.Title == "" {
			j.Title = e.Title
		}
	}
	if d := it.Download; d != nil {
		j.DownloadUUID, j.Downloaded, j.DownloadSize, j.DownloadError = d.UUID, d.VerifiedBytes, d.Size, d.Error
	}
	return j
}

// Release is the release of it on disk, as a catalog entry with what an
// Item knows of it.
func (it *Item) Release() catalog.Entry {
	return catalog.Entry{UUID: it.UUID, Name: it.Name, Flavour: it.Flavour, Title: it.Title, Date: it.Date}
}

// Items is items as Items, sorted as library.SortedItems sorts them.
func Items(items map[catalog.Key]*library.Item, libraryDir string) []Item {
	out := []Item{}
	for _, it := range library.SortedItems(items) {
		out = append(out, ItemOf(it, libraryDir))
	}
	return out
}

// ItemsByKey indexes items by key.
func ItemsByKey(items []Item) map[string]*Item {
	out := make(map[string]*Item, len(items))
	for i := range items {
		out[items[i].Key] = &items[i]
	}
	return out
}

// Entry is a catalog entry, with where it stands locally.
type Entry struct {
	UUID         string        `json:"uuid"`
	Key          string        `json:"key"`
	Name         string        `json:"name"`
	Flavour      string        `json:"flavour"`
	Title        string        `json:"title"`
	Summary      string        `json:"summary,omitempty"`
	Language     string        `json:"language"`
	Category     string        `json:"category,omitempty"`
	Tags         string        `json:"tags,omitempty"`
	ArticleCount int64         `json:"article_count,omitempty"`
	MediaCount   int64         `json:"media_count,omitempty"`
	Creator      string        `json:"creator,omitempty"`
	Publisher    string        `json:"publisher,omitempty"`
	Date         string        `json:"date"`
	Meta4URL     string        `json:"meta4,omitempty"`
	Size         int64         `json:"size"`             // approximate
	Source       string        `json:"source,omitempty"` // a URL or file of library.toml's [sources]
	State        library.State `json:"state"`
}

// EntryOf is e as an Entry, in state.
func EntryOf(e catalog.Entry, state library.State) Entry {
	return Entry{UUID: e.UUID, Key: e.Key().String(), Name: e.Name, Flavour: e.Flavour, Title: e.Title, Summary: e.Summary,
		Language: e.Language, Category: e.Category, Tags: e.Tags, ArticleCount: e.ArticleCount, MediaCount: e.MediaCount,
		Creator: e.Creator, Publisher: e.Publisher, Date: e.Date, Meta4URL: e.Meta4URL, Size: e.ApproxSize, Source: e.Source, State: state}
}

// Entries is es as Entries, each with its state among items.
func Entries(es []catalog.Entry, items map[catalog.Key]*library.Item) []Entry {
	out := []Entry{}
	for _, e := range es {
		state := library.NotInLibrary
		if it := items[e.Key()]; it != nil {
			state = it.State
		}
		out = append(out, EntryOf(e, state))
	}
	return out
}

// ExactKey is e's key with the flavour always named, "name:" for none, so
// that a request means this one flavour only (a bare name can mean them all).
func (e Entry) ExactKey() string { return catalog.Key{Name: e.Name, Flavour: e.Flavour}.Exact() }

// Query is a catalog search: GET /api/v1/search.
type Query struct {
	Text      string
	Prefix    string   // instead of Text: names ("name[:flavour]") that start with it, for shell completion
	Languages []string // any of them; none means every language
	Category  string
	Flavour   string
	MaxSize   int64
	Limit     int  // at most this many; 0 for all
	NoState   bool // leave Entry.State out (here only): the caller knows the items already
}

// Facet is a value of the catalog's categories or languages, and how many
// entries have it: GET /api/v1/categories and /api/v1/languages.
type Facet struct {
	Value string `json:"value"`
	Count int    `json:"count"`
}

// Collection is a collection, its sizes and your level.
type Collection struct {
	ID    string           `json:"id"`
	Title string           `json:"title"`
	Blurb string           `json:"blurb,omitempty"`
	Items int              `json:"items"`           // how many ZIMs it lists, at every level
	Mine  bool             `json:"mine"`            // in library.toml
	Level string           `json:"level,omitempty"` // when mine
	Sizes map[string]int64 `json:"sizes"`           // bytes at each level, approximately
}

// CollectionOf is cs as a Collection.
func CollectionOf(cs library.CollectionStatus) Collection {
	c := Collection{ID: cs.Collection.ID, Title: cs.Collection.Title, Blurb: cs.Collection.Blurb, Items: len(cs.Collection.Items),
		Mine: cs.Mine, Sizes: map[string]int64{}}
	if cs.Mine {
		c.Level = cs.Level.String()
	}
	for _, l := range collections.Levels {
		c.Sizes[l.String()] = cs.Sizes[l]
	}
	return c
}

// CollectionDetail is GET /api/v1/collections/{id}: what a collection holds
// at the level shown (yours, or standard when it is not in your library).
type CollectionDetail struct {
	Collection
	Shown string           `json:"shown"` // the level the items are for
	Items []CollectionItem `json:"items"`
}

// CollectionItem is one ZIM a collection lists.
type CollectionItem struct {
	Name        string        `json:"name"`
	Key         string        `json:"key,omitempty"`  // when the catalog has it
	Size        int64         `json:"size,omitempty"` // approximately
	Tier        string        `json:"tier"`
	Included    bool          `json:"included"` // at the level shown
	Note        string        `json:"note,omitempty"`
	Gone        bool          `json:"gone,omitempty"`         // not in the catalog
	NeedsRegion bool          `json:"needs_region,omitempty"` // a map that needs region in library.toml
	Off         bool          `json:"off,omitempty"`          // switched off in library.toml
	State       library.State `json:"state"`
}

// CollectionDetailOf is d as a CollectionDetail.
func CollectionDetailOf(d *library.CollectionDetail) CollectionDetail {
	out := CollectionDetail{Collection: CollectionOf(d.CollectionStatus), Shown: d.Shown.String(), Items: []CollectionItem{}}
	for _, x := range d.Items {
		it := CollectionItem{Name: x.Name, Tier: x.Item.Tier.String(), Included: d.Shown.Includes(x.Item.Tier),
			Note: x.Item.Note, Gone: x.Gone, NeedsRegion: x.NeedsRegion, Off: x.Off}
		if x.Entry != nil {
			it.Key, it.Size = x.Entry.Key().String(), x.Entry.ApproxSize
		}
		if x.Local != nil {
			it.State = x.Local.State
		}
		out.Items = append(out.Items, it)
	}
	return out
}

// Budget is GET /api/v1/budget: what the library takes, by collection,
// against the budget and the free disk.
type Budget struct {
	Uses   []Use `json:"uses"`
	Extra  int64 `json:"extra"` // on disk, not wanted
	Disk   int64 `json:"disk"`
	Total  int64 `json:"total"`  // when complete
	Budget int64 `json:"budget"` // 0: none
	Free   int64 `json:"free"`   // 0: unknown
}

// Use is what one collection, or the hand-picked ZIMs, take.
type Use struct {
	Via    string `json:"via"` // a collection id, or "items" for hand-picked ZIMs
	Level  string `json:"level,omitempty"`
	Disk   int64  `json:"disk"`
	Wanted int64  `json:"wanted"`
}

// BudgetOf is rep as a Budget, with free bytes of disk.
func BudgetOf(rep *library.BudgetReport, free int64) Budget {
	b := Budget{Uses: []Use{}, Extra: rep.Extra, Disk: rep.Disk, Total: rep.Total, Budget: rep.Budget, Free: free}
	for _, u := range rep.Uses {
		j := Use{Via: u.Via, Disk: u.Disk, Wanted: u.Wanted}
		if u.Via != setup.ViaItems {
			j.Level = u.Level.String()
		}
		b.Uses = append(b.Uses, j)
	}
	return b
}

// Job is a change asked for through the API, queued, running or finished.
type Job struct {
	ID       int64    `json:"id"`
	Kind     string   `json:"kind"`
	Summary  string   `json:"summary"`
	By       string   `json:"by,omitempty"` // the token's name, or "schedule"
	State    string   `json:"state"`        // queued, running, done, failed, canceled or interrupted
	Error    string   `json:"error,omitempty"`
	Log      []string `json:"log,omitempty"`
	Created  string   `json:"created"` // RFC 3339
	Started  string   `json:"started,omitempty"`
	Finished string   `json:"finished,omitempty"`
}

// JobOf is j as a Job.
func JobOf(j store.Job) Job {
	return Job{ID: j.ID, Kind: j.Kind, Summary: j.Summary, By: j.By, State: j.State, Error: j.Error, Log: j.Log,
		Created: Time(j.Created), Started: Time(j.Started), Finished: Time(j.Finished)}
}

// Time is t in RFC 3339, UTC, or "" for the zero time.
func Time(t time.Time) string {
	if t.IsZero() {
		return ""
	}
	return t.UTC().Format(time.RFC3339)
}
