// Package fakekiwix is a small stand-in for the Kiwix catalog and download
// mirrors. Tests use it to exercise zimon offline; the vhs demo tapes use it
// (via tools/fakekiwix) so recordings are reproducible.
//
// Routes:
//
//	GET  /catalog/v2/entries          OPDS feed of every book (ETag aware)
//	GET  /zim/<cat>/<file>.zim.meta4  metalink with sha-256, piece hashes and mirrors
//	GET  /zim/<cat>/<file>.zim        the file (canonical URL)
//	GET  /mirrorN/zim/<cat>/<file>.zim  the file from mirror N (1-based)
//	GET  /own/<file>                  a ZIM someone built, on a plain web server (see PutOwn)
//	POST /_fake/bump?name=&flavour=   publish a newer release of a book
//	POST /_fake/throttle?rate=6MB     change the per-connection speed limit
package fakekiwix

import (
	"bytes"
	"crypto/sha1"
	"crypto/sha256"
	"encoding/binary"
	"encoding/hex"
	"encoding/xml"
	"fmt"
	"io"
	"maps"
	"math/rand/v2"
	"net/http"
	"slices"
	"strconv"
	"strings"
	"sync"
	"time"

	"golang.org/x/time/rate"

	"github.com/kazie/zimon/internal/catalog"
	"github.com/kazie/zimon/internal/config"
	"github.com/kazie/zimon/internal/zim"
	"github.com/kazie/zimon/internal/zim/zimtest"
)

// Book is one catalog entry backed by generated content.
type Book struct {
	Name, Flavour, Title, Summary string
	Language, Category, Tags      string
	Creator, Publisher            string
	Date                          string // YYYY-MM-DD
	Size                          int64  // size of the generated file
	ListedSize                    int64  // size advertised in the catalog; 0 = Size
	ArticleCount, MediaCount      int64
	Release                       int // bumped by Bump; changes UUID, date and content
}

// Key is "name" or "name:flavour".
func (b Book) Key() string { return catalog.Key{Name: b.Name, Flavour: b.Flavour}.String() }

// UUID is stable for a given name, flavour and release.
func (b Book) UUID() string {
	sum := sha1.Sum([]byte(fmt.Sprintf("%s|%s|%d", b.Name, b.Flavour, b.Release)))
	return zim.FormatUUID(sum[:16])
}

// FileName is e.g. wikipedia_en_all_maxi_2026-08.zim.
func (b Book) FileName() string {
	base := b.Name
	if b.Flavour != "" {
		base += "_" + b.Flavour
	}
	return base + "_" + b.Date[:7] + ".zim"
}

func (b Book) category() string {
	if b.Category == "" {
		return "other"
	}
	return b.Category
}

// Server is the fake. Configure the exported fields before serving.
type Server struct {
	PieceLength int64 // piece size in the .meta4 (default 1 MiB)
	Mirrors     int   // number of mirrors (default 2)
	DirectLinks bool  // the feed links each ZIM itself rather than its .meta4, as catalogs other than Kiwix's may

	mu       sync.Mutex
	throttle int64 // bytes per second per connection; 0 = unlimited
	books    []Book
	content  map[string][]byte // by UUID
	bad      map[int]bool      // mirrors that corrupt one byte
	cutAfter map[int]int64     // mirrors that drop the connection after N bytes, once
	noRange  map[int]bool      // mirrors that ignore Range
	hits     map[string]int
	own      map[string]ownFile // by file name
}

type ownFile struct {
	body     []byte
	modified time.Time
}

// New returns a server for books.
func New(books []Book) *Server {
	return &Server{
		books:    slices.Clone(books),
		content:  map[string][]byte{},
		bad:      map[int]bool{},
		cutAfter: map[int]int64{},
		noRange:  map[int]bool{},
		hits:     map[string]int{},
		own:      map[string]ownFile{},
	}
}

// PutOwn serves body at /own/<file>, last modified at modified, with an
// ETag, as a web server serves a ZIM someone built.
func (s *Server) PutOwn(file string, body []byte, modified time.Time) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.own[file] = ownFile{body: body, modified: modified}
}

// OwnZIM is release n of a ZIM someone built of a site: real metadata (its
// Name is name), with a UUID, date and content that change with n.
func OwnZIM(name string, n int) []byte {
	sum := sha1.Sum([]byte(fmt.Sprintf("own|%s|%d", name, n)))
	return zimtest.Build(zimtest.ZIM{
		UUID: zim.FormatUUID(sum[:16]),
		Metadata: map[string]string{"Name": name, "Title": "My " + name, "Description": "Built with zimit",
			"Language": "eng", "Creator": "Me", "Date": OwnDate(n).Format(time.DateOnly)},
		Zstd:   true,
		Filler: 1<<20 + n,
	})
}

// OwnDate is when release n of an OwnZIM was made.
func OwnDate(n int) time.Time { return time.Date(2026, 10, 1, 0, 0, 0, 0, time.UTC).AddDate(0, n, 0) }

// Books returns the current catalog.
func (s *Server) Books() []Book {
	s.mu.Lock()
	defer s.mu.Unlock()
	return slices.Clone(s.books)
}

// update applies change to the book with key and returns the result.
func (s *Server) update(key string, change func(*Book)) (Book, bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	i := slices.IndexFunc(s.books, func(b Book) bool { return b.Key() == key })
	if i < 0 {
		return Book{}, false
	}
	change(&s.books[i])
	return s.books[i], true
}

// Book returns the current release of key.
func (s *Server) Book(key string) (Book, bool) {
	for _, b := range s.Books() {
		if b.Key() == key {
			return b, true
		}
	}
	return Book{}, false
}

// Content returns the generated file for b.
func (s *Server) Content(b Book) []byte {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.contentLocked(b)
}

// Bump publishes a newer release of key: new UUID, a month later, new bytes.
func (s *Server) Bump(key string) (Book, bool) {
	return s.update(key, func(b *Book) {
		b.Release++
		if t, err := time.Parse(time.DateOnly, b.Date); err == nil {
			b.Date = t.AddDate(0, 1, 0).Format(time.DateOnly)
		}
	})
}

// Republish replaces key with a new release under the same file name (same
// month), as Kiwix occasionally does.
func (s *Server) Republish(key string) (Book, bool) {
	return s.update(key, func(b *Book) { b.Release++ })
}

// Remove drops key from the catalog (as if removed upstream).
func (s *Server) Remove(key string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.books = slices.DeleteFunc(s.books, func(b Book) bool { return b.Key() == key })
}

// SetThrottle limits each connection to bps bytes per second (0 = unlimited).
func (s *Server) SetThrottle(bps int64) { s.mu.Lock(); s.throttle = bps; s.mu.Unlock() }

// BreakMirror makes mirror n (1-based; 0 is the canonical URL) send one corrupt
// byte per response.
func (s *Server) BreakMirror(n int) { s.mu.Lock(); s.bad[n] = true; s.mu.Unlock() }

// BreakAll makes every source corrupt a byte: the canonical URL and each mirror.
func (s *Server) BreakAll() {
	s.mu.Lock()
	defer s.mu.Unlock()
	for n := 0; n <= s.mirrors(); n++ {
		s.bad[n] = true
	}
}

// CutMirror makes mirror n drop its next connection after bytes bytes.
func (s *Server) CutMirror(n int, bytes int64) { s.mu.Lock(); s.cutAfter[n] = bytes; s.mu.Unlock() }

// NoRangeMirror makes mirror n ignore Range headers.
func (s *Server) NoRangeMirror(n int) { s.mu.Lock(); s.noRange[n] = true; s.mu.Unlock() }

// Hits returns how many file requests each mirror served ("mirror1", "canonical", ...).
func (s *Server) Hits() map[string]int {
	s.mu.Lock()
	defer s.mu.Unlock()
	return maps.Clone(s.hits)
}

func (s *Server) contentLocked(b Book) []byte {
	id := b.UUID()
	if c, ok := s.content[id]; ok {
		return c
	}
	c := make([]byte, max(b.Size, zim.HeaderSize))
	// A real ZIM header: magic, version 6.3, UUID. The rest is noise.
	binary.LittleEndian.PutUint32(c[0:4], zim.Magic)
	binary.LittleEndian.PutUint16(c[4:6], 6)
	binary.LittleEndian.PutUint16(c[6:8], 3)
	u, _ := zim.ParseUUID(id)
	copy(c[8:24], u)
	var seed [32]byte
	copy(seed[:], u)
	_, _ = rand.NewChaCha8(seed).Read(c[zim.HeaderSize:])
	s.content[id] = c
	return c
}

func (s *Server) pieceLength() int64 {
	if s.PieceLength > 0 {
		return s.PieceLength
	}
	return 1 << 20
}

func (s *Server) mirrors() int {
	if s.Mirrors > 0 {
		return s.Mirrors
	}
	return 2
}

// ServeHTTP implements http.Handler.
func (s *Server) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	p := r.URL.Path
	switch {
	case p == "/catalog/v2/entries":
		s.serveFeed(w, r)
	case p == "/_fake/bump" && r.Method == http.MethodPost:
		b, ok := s.Bump(catalog.Key{Name: r.URL.Query().Get("name"), Flavour: r.URL.Query().Get("flavour")}.String())
		if !ok {
			http.NotFound(w, r)
			return
		}
		fmt.Fprintf(w, "%s is now %s (%s)\n", b.Key(), b.FileName(), b.UUID())
	case p == "/_fake/throttle" && r.Method == http.MethodPost:
		bps, err := config.ParseBytes(r.URL.Query().Get("rate"))
		if err != nil {
			http.Error(w, err.Error(), http.StatusBadRequest)
			return
		}
		s.SetThrottle(bps)
	case strings.HasPrefix(p, "/own/"):
		s.serveOwn(w, r)
	case strings.HasSuffix(p, ".zim.meta4"):
		s.serveMeta4(w, r)
	case strings.HasSuffix(p, ".zim"):
		s.serveFile(w, r)
	default:
		http.NotFound(w, r)
	}
}

func baseURL(r *http.Request) string { return "http://" + r.Host }

func (s *Server) etag() string {
	h := sha256.New()
	for _, b := range s.Books() {
		fmt.Fprintf(h, "%s|%d|%d\n", b.Key(), b.Release, b.Size)
	}
	return `"` + hex.EncodeToString(h.Sum(nil))[:16] + `"`
}

func (s *Server) serveFeed(w http.ResponseWriter, r *http.Request) {
	tag := s.etag()
	w.Header().Set("ETag", tag)
	if r.Header.Get("If-None-Match") == tag {
		w.WriteHeader(http.StatusNotModified)
		return
	}
	s.mu.Lock()
	s.hits["feed"]++ // a feed sent in full
	s.mu.Unlock()
	books := s.Books()
	var buf bytes.Buffer
	buf.WriteString(`<?xml version="1.0" encoding="UTF-8"?>
<feed xmlns="http://www.w3.org/2005/Atom" xmlns:dc="http://purl.org/dc/terms/" xmlns:opds="https://specs.opds.io/opds-1.2" xmlns:opensearch="http://a9.com/-/spec/opensearch/1.1/">
  <id>fakekiwix</id>
  <title>All entries</title>
  <updated>` + time.Now().UTC().Format(time.RFC3339) + `</updated>
  <totalResults>` + strconv.Itoa(len(books)) + `</totalResults>
`)
	for _, b := range books {
		listed := b.ListedSize
		if listed == 0 {
			listed = max(b.Size, zim.HeaderSize)
		}
		meta4 := fmt.Sprintf("%s/zim/%s/%s.meta4", baseURL(r), b.category(), b.FileName())
		if s.DirectLinks {
			meta4 = strings.TrimSuffix(meta4, ".meta4")
		}
		fmt.Fprintf(&buf, `  <entry>
    <id>urn:uuid:%s</id>
    <title>%s</title>
    <updated>%sT00:00:00Z</updated>
    <summary>%s</summary>
    <language>%s</language>
    <name>%s</name>
    <flavour>%s</flavour>
    <category>%s</category>
    <tags>%s</tags>
    <articleCount>%d</articleCount>
    <mediaCount>%d</mediaCount>
    <author><name>%s</name></author>
    <publisher><name>%s</name></publisher>
    <dc:issued>%sT00:00:00Z</dc:issued>
    <link rel="http://opds-spec.org/acquisition/open-access" type="application/x-zim" href="%s" length="%d" />
  </entry>
`, b.UUID(), esc(b.Title), b.Date, esc(b.Summary), esc(b.Language), esc(b.Name), esc(b.Flavour),
			esc(b.category()), esc(b.Tags), b.ArticleCount, b.MediaCount, esc(b.Creator), esc(b.Publisher),
			b.Date, esc(meta4), listed)
	}
	buf.WriteString("</feed>\n")
	w.Header().Set("Content-Type", "application/atom+xml;profile=opds-catalog;kind=acquisition;charset=utf-8")
	_, _ = w.Write(buf.Bytes())
}

func (s *Server) findFile(name string) (Book, bool) {
	for _, b := range s.Books() {
		if b.FileName() == name {
			return b, true
		}
	}
	return Book{}, false
}

func (s *Server) serveMeta4(w http.ResponseWriter, r *http.Request) {
	name := strings.TrimSuffix(r.URL.Path[strings.LastIndexByte(r.URL.Path, '/')+1:], ".meta4")
	b, ok := s.findFile(name)
	if !ok {
		http.NotFound(w, r)
		return
	}
	content := s.Content(b)
	sum := sha256.Sum256(content)
	pl := s.pieceLength()
	var buf bytes.Buffer
	fmt.Fprintf(&buf, `<?xml version="1.0" encoding="UTF-8"?>
<metalink xmlns="urn:ietf:params:xml:ns:metalink">
  <generator>fakekiwix</generator>
  <file name="%s">
    <size>%d</size>
    <hash type="sha-256">%s</hash>
    <pieces length="%d" type="sha-1">
`, esc(name), len(content), hex.EncodeToString(sum[:]), pl)
	for off := int64(0); off < int64(len(content)); off += pl {
		ps := sha1.Sum(content[off:min(off+pl, int64(len(content)))])
		fmt.Fprintf(&buf, "      <hash>%s</hash>\n", hex.EncodeToString(ps[:]))
	}
	buf.WriteString("    </pieces>\n")
	path := "/zim/" + b.category() + "/" + name
	for i := 1; i <= s.mirrors(); i++ {
		fmt.Fprintf(&buf, `    <url location="m%d" priority="%d">%s/mirror%d%s</url>`+"\n", i, i, baseURL(r), i, path)
	}
	buf.WriteString("  </file>\n</metalink>\n")
	w.Header().Set("Content-Type", "application/metalink4+xml")
	_, _ = w.Write(buf.Bytes())
}

func (s *Server) serveFile(w http.ResponseWriter, r *http.Request) {
	path := r.URL.Path
	mirror, label := 0, "canonical"
	if rest, ok := strings.CutPrefix(path, "/mirror"); ok {
		i := strings.IndexByte(rest, '/')
		n, err := strconv.Atoi(rest[:max(i, 0)])
		if i < 0 || err != nil || n < 1 || n > s.mirrors() {
			http.NotFound(w, r)
			return
		}
		mirror, label, path = n, "mirror"+rest[:i], rest[i:]
	}
	b, ok := s.findFile(path[strings.LastIndexByte(path, '/')+1:])
	if !ok {
		http.NotFound(w, r)
		return
	}
	s.mu.Lock()
	s.hits[label]++
	content := s.contentLocked(b)
	bad, noRange, throttle := s.bad[mirror], s.noRange[mirror], s.throttle
	cut, hasCut := s.cutAfter[mirror]
	if hasCut {
		delete(s.cutAfter, mirror)
	}
	s.mu.Unlock()

	if bad {
		content = append([]byte(nil), content...)
		content[len(content)/2] ^= 0xff
	}
	if noRange {
		r.Header.Del("Range")
	}
	var out io.Writer = w
	if throttle > 0 {
		out = &throttled{w: out, l: rate.NewLimiter(rate.Limit(throttle), int(min(throttle, 64<<10))), r: r}
	}
	if hasCut {
		out = &cutter{w: out, left: cut}
	}
	w.Header().Set("Content-Type", "application/octet-stream")
	http.ServeContent(writerOverride{ResponseWriter: w, w: out}, r, b.FileName(), time.Time{}, bytes.NewReader(content))
}

// serveOwn serves a file of PutOwn's. A download counts as a hit; a look at
// the header (the first 80 bytes) does not.
func (s *Server) serveOwn(w http.ResponseWriter, r *http.Request) {
	file := strings.TrimPrefix(r.URL.Path, "/own/")
	s.mu.Lock()
	f, ok := s.own[file]
	if ok && r.Header.Get("Range") != fmt.Sprintf("bytes=0-%d", zim.HeaderSize-1) {
		s.hits["own"]++
	}
	s.mu.Unlock()
	if !ok {
		http.NotFound(w, r)
		return
	}
	sum := sha256.Sum256(f.body)
	w.Header().Set("ETag", `"`+hex.EncodeToString(sum[:8])+`"`)
	w.Header().Set("Content-Type", "application/octet-stream")
	http.ServeContent(w, r, file, f.modified, bytes.NewReader(f.body))
}

type writerOverride struct {
	http.ResponseWriter
	w io.Writer
}

func (o writerOverride) Write(p []byte) (int, error) { return o.w.Write(p) }

type throttled struct {
	w io.Writer
	l *rate.Limiter
	r *http.Request
}

func (t *throttled) Write(p []byte) (int, error) {
	written := 0
	for len(p) > 0 {
		n := min(len(p), t.l.Burst())
		if err := t.l.WaitN(t.r.Context(), n); err != nil {
			return written, err
		}
		m, err := t.w.Write(p[:n])
		written += m
		if err != nil {
			return written, err
		}
		if f, ok := t.w.(http.Flusher); ok {
			f.Flush()
		}
		p = p[n:]
	}
	return written, nil
}

type cutter struct {
	w    io.Writer
	left int64
}

func (c *cutter) Write(p []byte) (int, error) {
	if int64(len(p)) > c.left {
		_, _ = c.w.Write(p[:c.left])
		if f, ok := c.w.(http.Flusher); ok {
			f.Flush()
		}
		panic(http.ErrAbortHandler) // drop the connection mid-body
	}
	c.left -= int64(len(p))
	return c.w.Write(p)
}

func esc(s string) string {
	var b strings.Builder
	_ = xml.EscapeText(&b, []byte(s))
	return b.String()
}
