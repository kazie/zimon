package server

import (
	"cmp"
	"context"
	"embed"
	"html/template"
	"io/fs"
	"maps"
	"net/http"
	"path/filepath"
	"regexp"
	"slices"
	"strings"

	"github.com/kazie/zimon/internal/collections"
	"github.com/kazie/zimon/internal/config"
	"github.com/kazie/zimon/internal/library"
	"github.com/kazie/zimon/internal/setup"
	"github.com/kazie/zimon/internal/store"
)

//go:embed web
var webFS embed.FS

var (
	static, _ = fs.Sub(webFS, "web/static")
	pageTmpl  = template.Must(template.ParseFS(webFS, "web/portal.html"))
)

// portalPage is what the portal shows.
type portalPage struct {
	Problem  string    // why books can't be opened right now, if they can't
	Sections []section // one per subscribed collection with books, then the rest
	Langs    []string  // languages to search in, most books first
	Lang     string    // the one picked by default
	Books    int
	Size     string
}

type section struct {
	Title, Blurb string
	Books        []book
}

type book struct {
	Name         string // kiwix-serve's name for it in URLs
	Title        string
	Summary      string
	Note         string // why this section's collection has it
	Flavour      string
	Size, Date   string
	Langs        []string
	Illustration string // URL of its icon (see icons)
}

func (s *Server) portal(w http.ResponseWriter, r *http.Request) {
	page, err := s.portalPage(r.Context())
	if err != nil {
		s.logf("portal: %v", err)
		http.Error(w, "zimon could not read its library: "+err.Error(), http.StatusInternalServerError)
		return
	}
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	if err := pageTmpl.Execute(w, page); err != nil {
		s.logf("portal: %v", err)
	}
}

// portalPage gathers the books on disk, grouped by the collections in
// library.toml that want them. A book two collections want shows in both.
func (s *Server) portalPage(ctx context.Context) (*portalPage, error) {
	m := s.Manager
	st, err := m.LoadSetup(ctx)
	if err != nil {
		return nil, err
	}
	r := m.Resolver() // one for both, so the notes reuse the lookups
	w, err := r.Resolve(ctx, st)
	if err != nil {
		return nil, err
	}
	items, err := m.ItemsFor(ctx, w)
	if err != nil {
		return nil, err
	}
	notes, err := s.notes(ctx, r, st)
	if err != nil {
		return nil, err
	}

	page := &portalPage{Problem: s.kiwixProblem()}
	var size int64
	inSection := map[string][]book{}
	var other []book
	langs := map[string]int{}
	for _, it := range library.SortedItems(items) {
		if it.File == nil {
			continue
		}
		b := bookFor(it.File)
		page.Books++
		size += it.File.Size
		for _, l := range b.Langs {
			langs[l]++
		}
		placed := false
		if it.Want != nil { // by name: an old flavour stays with its collection until apply replaces it
			for _, via := range it.Want.Via {
				if via != setup.ViaItems {
					b.Note = notes[via][it.Key.Name]
					inSection[via] = append(inSection[via], b)
					placed = true
				}
			}
		}
		if !placed {
			other = append(other, b)
		}
	}
	page.Size = config.FormatBytes(size)

	for _, id := range st.CollectionIDs() {
		if books := inSection[id]; len(books) > 0 {
			sec := section{Title: id, Books: books}
			if c := collections.Find(m.Collections, id); c != nil {
				sec.Title, sec.Blurb = cmp.Or(c.Title, id), c.Blurb
			}
			page.Sections = append(page.Sections, sec)
		}
	}
	if len(other) > 0 {
		title := "Other"
		if len(page.Sections) == 0 {
			title = "Your books"
		}
		page.Sections = append(page.Sections, section{Title: title, Books: other})
	}
	for _, sec := range page.Sections {
		slices.SortFunc(sec.Books, func(a, b book) int { return strings.Compare(strings.ToLower(a.Title), strings.ToLower(b.Title)) })
	}

	page.Langs = slices.SortedFunc(maps.Keys(langs), func(a, b string) int {
		return cmp.Or(cmp.Compare(langs[b], langs[a]), cmp.Compare(a, b))
	})
	page.Lang = cmp.Or(firstIn(m.Cfg.Languages, langs), first(page.Langs))
	return page, nil
}

// notes maps collection ids to ZIM names to the note on why the collection
// has that ZIM.
func (s *Server) notes(ctx context.Context, r *setup.Resolver, st *setup.Setup) (map[string]map[string]string, error) {
	notes := map[string]map[string]string{}
	for _, id := range st.CollectionIDs() {
		c := collections.Find(s.Manager.Collections, id)
		if c == nil {
			continue
		}
		xs, err := r.Expand(ctx, c, st.Collections[id], st.Region, true)
		if err != nil {
			return nil, err
		}
		notes[id] = map[string]string{}
		for _, x := range xs {
			notes[id][x.Name] = x.Item.Note
		}
	}
	return notes, nil
}

func bookFor(f *store.File) book {
	b := book{
		Name: BookName(f.Path), Title: cmp.Or(f.Title, f.Name), Summary: f.Summary,
		Flavour: f.Flavour, Size: config.FormatBytes(f.Size), Date: f.Date,
	}
	for l := range strings.SplitSeq(f.Language, ",") {
		if l = strings.TrimSpace(l); l != "" {
			b.Langs = append(b.Langs, l)
		}
	}
	if f.UUID != "" {
		b.Illustration = "/_zimon/icon/" + f.UUID
	}
	return b
}

var dateSuffix = regexp.MustCompile(`_\d{4}-\d{2}$`)

// BookName is kiwix-serve's name for the ZIM at path in its URLs
// (/content/NAME/, /viewer#NAME): the file name without the date, which
// kiwix-serve --nodatealiases provides, so links survive updates.
func BookName(path string) string {
	return dateSuffix.ReplaceAllString(strings.TrimSuffix(filepath.Base(path), ".zim"), "")
}

func firstIn(want []string, have map[string]int) string {
	for _, l := range want {
		if have[l] > 0 {
			return l
		}
	}
	return ""
}

func first(s []string) string {
	if len(s) == 0 {
		return ""
	}
	return s[0]
}
