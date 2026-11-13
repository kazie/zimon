package library

import (
	"context"
	"encoding/xml"
	"os"

	"github.com/kazie/zimon/internal/catalog"
	"github.com/kazie/zimon/internal/config"
)

// libraryXML is the kiwix library format read by kiwix-serve --library.
type libraryXML struct {
	XMLName xml.Name  `xml:"library"`
	Version string    `xml:"version,attr"`
	Books   []bookXML `xml:"book"`
}

type bookXML struct {
	ID           string `xml:"id,attr"`
	Path         string `xml:"path,attr"`
	Title        string `xml:"title,attr,omitempty"`
	Description  string `xml:"description,attr,omitempty"`
	Language     string `xml:"language,attr,omitempty"`
	Creator      string `xml:"creator,attr,omitempty"`
	Publisher    string `xml:"publisher,attr,omitempty"`
	Name         string `xml:"name,attr,omitempty"`
	Flavour      string `xml:"flavour,attr,omitempty"`
	Tags         string `xml:"tags,attr,omitempty"`
	Date         string `xml:"date,attr,omitempty"`
	ArticleCount int64  `xml:"articleCount,attr,omitempty"`
	MediaCount   int64  `xml:"mediaCount,attr,omitempty"`
	Size         int64  `xml:"size,attr,omitempty"` // KiB
}

// renderLibraryXML renders library.xml for the active files, leaving out the
// keys in skip. Paths are relative to the library dir, where library.xml lives.
func (m *Manager) renderLibraryXML(ctx context.Context, skip map[catalog.Key]bool) ([]byte, error) {
	files, err := m.Store.ActiveFiles(ctx)
	if err != nil {
		return nil, err
	}
	lib := libraryXML{Version: "20110515"}
	for _, f := range files {
		if skip[f.Key()] {
			continue
		}
		lib.Books = append(lib.Books, bookXML{
			ID: f.ZimUUID, Path: f.Path, Title: f.Title, Description: f.Summary, Language: f.Language,
			Creator: f.Creator, Publisher: f.Publisher, Name: f.Name, Flavour: f.Flavour, Tags: f.Tags,
			Date: f.Date, ArticleCount: f.ArticleCount, MediaCount: f.MediaCount, Size: (f.Size + 1023) / 1024,
		})
	}
	out, err := xml.MarshalIndent(lib, "", "  ")
	if err != nil {
		return nil, err
	}
	return append([]byte(xml.Header), append(out, '\n')...), nil
}

// EnsureLibraryXML writes library.xml if it does not exist. Every command
// that changes the library keeps it current (under the state lock), so a
// reader like `zimon serve` must not overwrite it with a possibly older view.
func (m *Manager) EnsureLibraryXML(ctx context.Context) error {
	if _, err := os.Stat(m.Cfg.LibraryXMLPath()); err == nil {
		return nil
	}
	return m.WriteLibraryXML(ctx)
}

// LibraryXMLMatches reports whether library.xml lists what the database
// has, as the next write would. A missing library.xml is os.ErrNotExist.
func (m *Manager) LibraryXMLMatches(ctx context.Context) (bool, error) {
	have, err := os.ReadFile(m.Cfg.LibraryXMLPath())
	if err != nil {
		return false, err
	}
	want, err := m.renderLibraryXML(ctx, nil)
	return string(have) == string(want), err
}

// WriteLibraryXML atomically rewrites library.xml, so kiwix-serve
// --monitorLibrary never reads half a file.
func (m *Manager) WriteLibraryXML(ctx context.Context) error { return m.writeLibraryXML(ctx, nil) }

func (m *Manager) writeLibraryXML(ctx context.Context, skip map[catalog.Key]bool) error {
	m.xmlMu.Lock()
	defer m.xmlMu.Unlock()
	data, err := m.renderLibraryXML(ctx, skip)
	if err != nil {
		return err
	}
	if old, err := os.ReadFile(m.Cfg.LibraryXMLPath()); err == nil && string(old) == string(data) {
		return nil // unchanged; don't wake kiwix-serve
	}
	return config.WriteFileAtomic(m.Cfg.LibraryXMLPath(), data, 0o644)
}
