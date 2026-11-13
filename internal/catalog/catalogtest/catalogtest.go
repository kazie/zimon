// Package catalogtest makes catalogs of realistic size for tests and
// benchmarks: the live Kiwix catalog has about 4,000 entries.
package catalogtest

import (
	"bytes"
	"encoding/xml"
	"fmt"

	"github.com/kazie/zimon/internal/catalog"
	"github.com/kazie/zimon/internal/collections"
)

// Size is about how many entries the live catalog has.
const Size = 4000

var (
	langs = []string{"eng", "fra", "deu", "spa", "swe", "jpn", "zho", "ara", "rus", "por"}
	cats  = []string{"wikipedia", "stack_exchange", "devdocs", "gutenberg", "wiktionary", "other"}
	words = []string{"wikipedia", "linux", "cooking", "history", "science", "stack", "exchange", "docs",
		"python", "music", "games", "medicine", "travel", "physics", "anime", "repair", "maps", "books"}
)

// Entries returns n entries: every name in mini, nopic and maxi, then
// made-up ZIMs in many languages and categories until there are n.
func Entries(n int, names []string) []catalog.Entry {
	out := make([]catalog.Entry, 0, n)
	add := func(name, flavour string) {
		i := len(out)
		w1, w2 := words[i%len(words)], words[(i*7+3)%len(words)]
		out = append(out, catalog.Entry{
			UUID:     fmt.Sprintf("%08x-0000-4000-8000-%012x", i, i),
			Name:     name,
			Flavour:  flavour,
			Title:    w1 + " " + w2,
			Summary:  "Offline " + w1 + " and " + w2 + " from " + name,
			Language: langs[i%len(langs)],
			Category: cats[i%len(cats)],
			Tags:     "_pictures:yes;_ftindex:yes;" + w1,
			Date:     "2026-10-01",
			Creator:  "openZIM", Publisher: "Kiwix",
			ArticleCount: int64(i * 31), MediaCount: int64(i * 7),
			Meta4URL:   "https://download.kiwix.org/zim/" + name + ".zim.meta4",
			ApproxSize: int64(i%997+1) << 20,
		})
	}
	for _, name := range names {
		for _, fl := range []string{"mini", "nopic", "maxi"} {
			if len(out) < n {
				add(name, fl)
			}
		}
	}
	for i := 0; len(out) < n; i++ {
		add(fmt.Sprintf("site%d.example_%s_all", i, langs[i%len(langs)][:2]), "")
	}
	return out
}

// The languages and region the benchmarks use, as on a server for English,
// Swedish and Japanese readers.
var (
	Languages = []string{"eng", "swe", "jpn"}
	Region    = "sweden"
)

// BuiltinNames lists every ZIM name the built-in collections can ask for,
// in Languages and Region.
func BuiltinNames() []string {
	cols := collections.Builtin()
	ptrs := make([]*collections.Collection, len(cols))
	for i := range cols {
		ptrs[i] = &cols[i]
	}
	return collections.Candidates(ptrs, Languages, Region)
}

// BuiltinEntries is a catalog of Size entries holding every name the
// built-in collections can ask for.
func BuiltinEntries() []catalog.Entry { return Entries(Size, BuiltinNames()) }

// Feed renders entries as an OPDS acquisition feed, as the Kiwix catalog
// serves it.
func Feed(entries []catalog.Entry) []byte {
	var b bytes.Buffer
	esc := func(s string) string {
		var e bytes.Buffer
		_ = xml.EscapeText(&e, []byte(s))
		return e.String()
	}
	b.WriteString(`<?xml version="1.0" encoding="UTF-8"?>` + "\n" +
		`<feed xmlns="http://www.w3.org/2005/Atom" xmlns:dc="http://purl.org/dc/terms/">` + "\n")
	for _, e := range entries {
		fmt.Fprintf(&b, `<entry><id>urn:uuid:%s</id><title>%s</title><updated>%sT00:00:00Z</updated>`+
			`<summary>%s</summary><language>%s</language><name>%s</name><flavour>%s</flavour>`+
			`<category>%s</category><tags>%s</tags><articleCount>%d</articleCount><mediaCount>%d</mediaCount>`+
			`<author><name>%s</name></author><publisher><name>%s</name></publisher>`+
			`<link rel="http://opds-spec.org/acquisition/open-access" type="application/x-zim" href="%s" length="%d"/>`+
			"</entry>\n",
			e.UUID, esc(e.Title), e.Date, esc(e.Summary), e.Language, esc(e.Name), esc(e.Flavour),
			esc(e.Category), esc(e.Tags), e.ArticleCount, e.MediaCount, esc(e.Creator), esc(e.Publisher),
			esc(e.Meta4URL), e.ApproxSize)
	}
	b.WriteString("</feed>\n")
	return b.Bytes()
}
