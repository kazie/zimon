// Package catalog fetches and parses the Kiwix OPDS v2 catalog.
package catalog

import (
	"context"
	"encoding/xml"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"regexp"
	"strconv"
	"strings"
	"time"
)

// Key identifies a ZIM across releases: the catalog keeps one entry (the
// latest) per name+flavour. Flavour may be empty.
type Key struct {
	Name    string
	Flavour string
}

// ParseKey parses "name" or "name:flavour".
func ParseKey(s string) Key {
	k, _ := SplitKey(s)
	return k
}

// SplitKey is ParseKey that also says whether a flavour was given ("name:"
// names the empty flavour, unlike "name").
func SplitKey(s string) (k Key, hasFlavour bool) {
	name, flavour, found := strings.Cut(s, ":")
	return Key{Name: strings.TrimSpace(name), Flavour: strings.TrimSpace(flavour)}, found
}

func (k Key) String() string {
	if k.Flavour == "" {
		return k.Name
	}
	return k.Name + ":" + k.Flavour
}

// Exact is k with its flavour always given: "name:" for the empty one,
// which SplitKey reads back exactly.
func (k Key) Exact() string { return k.Name + ":" + k.Flavour }

// Entry is one book in the catalog.
type Entry struct {
	UUID         string
	Name         string
	Flavour      string
	Title        string
	Summary      string
	Language     string // comma-separated ISO 639-3 codes, e.g. "eng" or "eng,jpn"
	Category     string
	Tags         string
	ArticleCount int64
	MediaCount   int64
	Creator      string
	Publisher    string
	Date         string // YYYY-MM-DD
	Meta4URL     string
	ApproxSize   int64  // from the catalog; the .meta4 has the exact size
	Source       string // for a ZIM of library.toml's [sources]: its URL or file; "" for a catalog's
	Catalog      string // the URL of the catalog it came from; "" for a source's
}

// Key is the entry's name and flavour.
func (e Entry) Key() Key { return Key{Name: e.Name, Flavour: e.Flavour} }

var safeID = regexp.MustCompile(`^[A-Za-z0-9-]{1,64}$`)

// SafeID reports whether s can be a release id, which names files (a
// partial download, a book's icon): no path, nothing that climbs out of a
// folder.
func SafeID(s string) bool { return safeID.MatchString(s) }

// SafeName reports whether s can be a ZIM name or flavour, which name the
// ZIM's file: letters, digits and . _ -, starting with a letter or digit.
func SafeName(s string) bool {
	if s == "" || len(s) > 200 {
		return false
	}
	for i := range len(s) { // a byte loop: a catalog has thousands to check
		switch c := s[i]; {
		case 'a' <= c && c <= 'z', 'A' <= c && c <= 'Z', '0' <= c && c <= '9':
		case i > 0 && (c == '.' || c == '_' || c == '-'):
		default:
			return false
		}
	}
	return true
}

// Safe reports whether e's release id, name and flavour are fit for file
// names. A catalog or a remote that sends anything else is not trusted
// with the disk.
func (e Entry) Safe() bool {
	return SafeID(e.UUID) && SafeName(e.Name) && (e.Flavour == "" || SafeName(e.Flavour))
}

// Result of a catalog fetch.
type Result struct {
	Entries     []Entry
	ETag        string
	NotModified bool
}

// Client fetches the catalog.
type Client struct {
	HTTP       *http.Client
	BaseURL    string // e.g. https://opds.library.kiwix.org/catalog/v2
	MayBeEmpty bool   // a catalog with no entries is not an error
}

// fetchTimeout bounds a whole catalog fetch (~0.5 MB gzipped), so a server
// that stalls mid-response cannot hang an unattended update.
const fetchTimeout = 3 * time.Minute

// Fetch downloads the whole catalog in one request. If etag matches the
// server's current version it returns NotModified without entries.
func (c *Client) Fetch(ctx context.Context, etag string) (*Result, error) {
	ctx, cancel := context.WithTimeout(ctx, fetchTimeout)
	defer cancel()
	base := strings.TrimRight(c.BaseURL, "/")
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, base+"/entries?count=-1", nil)
	if err != nil {
		return nil, err
	}
	if etag != "" {
		req.Header.Set("If-None-Match", etag)
	}
	hc := c.HTTP
	if hc == nil {
		hc = http.DefaultClient
	}
	resp, err := hc.Do(req)
	if err != nil {
		return nil, fmt.Errorf("fetch catalog: %w", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode == http.StatusNotModified {
		return &Result{ETag: etag, NotModified: true}, nil
	}
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("fetch catalog: %s returned %s", req.URL, resp.Status)
	}
	if ct := resp.Header.Get("Content-Type"); strings.HasPrefix(ct, "text/html") {
		return nil, fmt.Errorf("fetch catalog: %s returned an HTML page, not an OPDS feed (is catalog_url right?)", req.URL)
	}
	entries, err := Parse(resp.Body, resp.Request.URL)
	if err != nil {
		return nil, err
	}
	if len(entries) == 0 && !c.MayBeEmpty {
		// A hiccup upstream; replacing the cache would make every tracked ZIM look gone.
		return nil, fmt.Errorf("fetch catalog: %s returned no entries", req.URL)
	}
	return &Result{Entries: entries, ETag: resp.Header.Get("ETag")}, nil
}

type atomFeed struct {
	Entries []atomEntry `xml:"entry"`
}

type atomEntry struct {
	ID           string     `xml:"id"`
	Title        string     `xml:"title"`
	Updated      string     `xml:"updated"`
	Issued       string     `xml:"http://purl.org/dc/terms/ issued"`
	Summary      string     `xml:"summary"`
	Language     string     `xml:"language"`
	Name         string     `xml:"name"`
	Flavour      string     `xml:"flavour"`
	Category     string     `xml:"category"`
	Tags         string     `xml:"tags"`
	ArticleCount int64      `xml:"articleCount"`
	MediaCount   int64      `xml:"mediaCount"`
	Author       atomPerson `xml:"author"`
	Publisher    atomPerson `xml:"publisher"`
	Links        []atomLink `xml:"link"`
}

type atomPerson struct {
	Name string `xml:"name"`
}

type atomLink struct {
	Rel    string `xml:"rel,attr"`
	Type   string `xml:"type,attr"`
	Href   string `xml:"href,attr"`
	Length string `xml:"length,attr"`
}

const acquisitionRel = "http://opds-spec.org/acquisition/open-access"

// Parse reads an OPDS acquisition feed. Relative links resolve against base (may be nil).
func Parse(r io.Reader, base *url.URL) ([]Entry, error) {
	var feed atomFeed
	if err := xml.NewDecoder(r).Decode(&feed); err != nil {
		return nil, fmt.Errorf("parse catalog: %w", err)
	}
	entries := make([]Entry, 0, len(feed.Entries))
	for _, ae := range feed.Entries {
		e := Entry{
			UUID:         strings.TrimPrefix(strings.TrimSpace(ae.ID), "urn:uuid:"),
			Name:         strings.TrimSpace(ae.Name),
			Flavour:      strings.TrimSpace(ae.Flavour),
			Title:        strings.TrimSpace(ae.Title),
			Summary:      strings.TrimSpace(ae.Summary),
			Language:     strings.TrimSpace(ae.Language),
			Category:     strings.TrimSpace(ae.Category),
			Tags:         strings.TrimSpace(ae.Tags),
			ArticleCount: ae.ArticleCount,
			MediaCount:   ae.MediaCount,
			Creator:      strings.TrimSpace(ae.Author.Name),
			Publisher:    strings.TrimSpace(ae.Publisher.Name),
			Date:         DateOnly(ae.Updated, ae.Issued),
		}
		for _, l := range ae.Links {
			if l.Rel != acquisitionRel || !strings.HasPrefix(l.Type, "application/x-zim") {
				continue
			}
			e.Meta4URL = resolve(base, l.Href)
			if n, err := strconv.ParseInt(l.Length, 10, 64); err == nil && n > 0 {
				e.ApproxSize = n // else unknown (0): a negative size would undo budgets
			}
			break
		}
		if e.Meta4URL == "" || !e.Safe() {
			continue // not downloadable; nothing zimon can do with it
		}
		entries = append(entries, e)
	}
	return entries, nil
}

// DateOnly is the first of values that is a real date (RFC 3339, or
// starting YYYY-MM-DD), as YYYY-MM-DD; "" when none is.
func DateOnly(values ...string) string {
	for _, v := range values {
		v = strings.TrimSpace(v)
		if v == "" {
			continue
		}
		if t, err := time.Parse(time.RFC3339, v); err == nil {
			return t.UTC().Format(time.DateOnly)
		}
		// Dates decide which release is newer: only real ones count.
		if len(v) >= 10 {
			if _, err := time.Parse(time.DateOnly, v[:10]); err == nil {
				return v[:10]
			}
		}
	}
	return ""
}

func resolve(base *url.URL, href string) string {
	if base == nil {
		return href
	}
	u, err := url.Parse(href)
	if err != nil {
		return href
	}
	return base.ResolveReference(u).String()
}
