package setup

import (
	"errors"
	"fmt"
	"io/fs"
	"os"
	"regexp"
	"slices"
	"strconv"
	"strings"
	"syscall"

	"github.com/kazie/zimon/internal/collections"
	"github.com/kazie/zimon/internal/config"
)

// Template is a new library.toml.
const Template = `# What your zimon library should contain. Edit it by hand, or with
# zimon add / remove / level / budget, then run "zimon apply".
# See "zimon collections" for what you can subscribe to.

# budget = "250GB"              # size limit for the whole library
# new_items = "within-budget"   # within-budget | ask | always: what update does with new ZIMs
# region = "sweden"             # for map items (maps_en_<region>)

[collections]                   # id = "light" | "standard" | "deep"

[items]                         # name = a flavour, a level, "on" or "off"
`

// ErrReadOnly means library.toml cannot be changed by zimon, e.g. because a
// NixOS module generates it.
var ErrReadOnly = errors.New("library.toml is read-only")

// ErrNotFlat means library.toml is valid but laid out in a way zimon's line
// editor cannot change safely.
var ErrNotFlat = errors.New("zimon edits library.toml one line at a time and cannot safely change this line")

// Doc is library.toml as text, edited line by line so comments and layout
// survive. Its schema is flat on purpose: every value is one string on its
// own line, which keeps line edits safe.
type Doc struct {
	path   string
	lines  []string
	exists bool
	mode   os.FileMode
	flat   bool  // checked by editable: edits only ever write flat lines
	err    error // why an edit was refused (see editable)
}

// Open reads library.toml at path, or starts from Template when it is missing.
func Open(path string) (*Doc, error) {
	data, err := os.ReadFile(path)
	switch {
	case errors.Is(err, os.ErrNotExist):
		return newDoc(path, Template), nil
	case err != nil:
		return nil, err
	}
	d := newDoc(path, string(data))
	d.exists = true
	if st, err := os.Stat(path); err == nil {
		d.mode = st.Mode().Perm()
	}
	return d, nil
}

func newDoc(path, text string) *Doc {
	return &Doc{path: path, mode: 0o644, lines: strings.Split(strings.TrimSuffix(text, "\n"), "\n")}
}

// Exists reports whether the file was there when it was opened.
func (d *Doc) Exists() bool { return d.exists }

// Bytes is the current content.
func (d *Doc) Bytes() []byte { return []byte(strings.Join(d.lines, "\n") + "\n") }

// Setup parses the current content. It fails if an edit was refused.
func (d *Doc) Setup() (*Setup, error) {
	if d.err != nil {
		return nil, d.err
	}
	s, err := Parse(d.Bytes())
	if err != nil {
		return nil, fmt.Errorf("%s: %w", d.path, err)
	}
	return s, nil
}

// Writable says why the file can't be written, if it can't: ErrReadOnly
// when it is managed elsewhere (as by NixOS). A file not made yet can be.
func (d *Doc) Writable() error {
	if !d.exists {
		return nil
	}
	f, err := os.OpenFile(d.path, os.O_WRONLY, 0)
	if errors.Is(err, fs.ErrPermission) || errors.Is(err, syscall.EROFS) {
		return fmt.Errorf("%w: %s is managed elsewhere; change it there", ErrReadOnly, d.path)
	}
	if err != nil {
		return err
	}
	return f.Close()
}

// Save validates and writes the file atomically.
func (d *Doc) Save() error {
	if _, err := d.Setup(); err != nil {
		return err
	}
	if err := d.Writable(); err != nil {
		return err
	}
	if err := config.WriteFileAtomic(d.path, d.Bytes(), d.mode); err != nil {
		return err
	}
	d.exists = true
	return nil
}

// SetCollection subscribes to a collection at level.
func (d *Doc) SetCollection(id string, l collections.Level) { d.set("collections", id, l.String()) }

// DeleteCollection unsubscribes; it reports whether it deleted a line (an
// edit that was refused deletes nothing; Setup and Save say why).
func (d *Doc) DeleteCollection(id string) bool { return d.del("collections", id) }

// SetItem sets a hand-picked choice: a flavour, a level, On or Off.
func (d *Doc) SetItem(name, choice string) { d.set("items", name, choice) }

// DeleteItem removes a name from [items]; it reports whether it deleted a
// line, like DeleteCollection.
func (d *Doc) DeleteItem(name string) bool { return d.del("items", name) }

// SetHold holds name at the release dated date (a note for the reader).
func (d *Doc) SetHold(name, date string) { d.set("hold", name, date) }

// DeleteHold removes name from [hold]; it reports whether it deleted a line,
// like DeleteCollection.
func (d *Doc) DeleteHold(name string) bool { return d.del("hold", name) }

// SetSource says where name comes from: a URL or a file's absolute path.
func (d *Doc) SetSource(name, source string) { d.set("sources", name, source) }

// DeleteSource removes name from [sources]; it reports whether it deleted a
// line, like DeleteCollection.
func (d *Doc) DeleteSource(name string) bool { return d.del("sources", name) }

// nameTables are the tables keyed by ZIM name.
var nameTables = []string{"items", "hold", "sources"}

// ForgetName drops what library.toml says about how to keep name, its
// [hold] and [sources] lines: for a ZIM that leaves the library. [items] is
// the caller's.
func (d *Doc) ForgetName(name string) {
	d.del("hold", name)
	d.del("sources", name)
}

// SetBudget sets the size budget; 0 removes it.
func (d *Doc) SetBudget(bytes int64) {
	if bytes <= 0 {
		d.del("", "budget")
		return
	}
	text, _ := config.ByteSize(bytes).MarshalText()
	d.set("", "budget", string(text))
}

var (
	headerRe = regexp.MustCompile(`^\s*\[\s*([A-Za-z0-9_-]+)\s*\]\s*(#.*)?$`)
	keyRe    = regexp.MustCompile(`^\s*(` + keyPat + `)\s*=`)
	bareRe   = regexp.MustCompile(`^[A-Za-z0-9_-]+$`)
	// flatRe matches a blank line, a comment, or key = value on one line.
	flatRe = regexp.MustCompile(`^\s*(?:(?:` + keyPat + `)\s*=\s*(?:` + strPat + `|[A-Za-z0-9_.+-]+)\s*)?(?:#.*)?$`)
)

// The TOML the line editor understands: a one-line basic or literal string,
// and a key (one of those, or bare).
const (
	strPat = `"(?:[^"\\]|\\.)*"|'[^']*'`
	keyPat = strPat + `|[A-Za-z0-9_-]+`
)

// Editable says why the line editor can't safely change the file (an
// ErrNotFlat), or nil when it can: every line must be blank, a comment, a
// [table] header, or one key = "value". Other valid TOML (dotted keys,
// inline tables, multi-line strings) reads fine, but a line edit could
// change the wrong thing. Check it before editing; edits of a file that
// isn't editable do nothing, and Setup and Save report why.
func (d *Doc) Editable() error {
	d.editable()
	return d.err
}

func (d *Doc) editable() bool {
	if d.flat || d.err != nil {
		return d.flat
	}
	for i, l := range d.lines {
		if !flatRe.MatchString(l) && !headerRe.MatchString(l) {
			d.err = fmt.Errorf("%s line %d: %w: write %q as one key = \"value\" line, like the others",
				d.path, i+1, ErrNotFlat, strings.TrimSpace(l))
			return false
		}
	}
	d.flat = true
	return true
}

// section returns the line range of table ("" is the top level before any
// header): the header's index (-1 for the top level) and the end.
func (d *Doc) section(table string) (header, end int) {
	header, cur := -1, ""
	if table != "" {
		header = -2 // not found yet
	}
	for i, l := range d.lines {
		m := headerRe.FindStringSubmatch(l)
		if m == nil {
			continue
		}
		if cur == table && header != -2 {
			return header, i
		}
		cur = m[1]
		if cur == table {
			header = i
		}
	}
	if header == -2 {
		return -2, -2
	}
	return header, len(d.lines)
}

// find returns the line index of key in table, or -1.
func (d *Doc) find(table, key string) int {
	header, end := d.section(table)
	if header == -2 {
		return -1
	}
	for i := header + 1; i < end; i++ {
		if k, ok := lineKey(d.lines[i]); ok && k == key {
			return i
		}
	}
	return -1
}

func lineKey(line string) (string, bool) {
	m := keyRe.FindStringSubmatch(line)
	if m == nil {
		return "", false
	}
	k := m[1]
	switch k[0] {
	case '"':
		u, err := strconv.Unquote(k)
		return u, err == nil
	case '\'':
		return k[1 : len(k)-1], true
	}
	return k, true
}

func quoteKey(k string) string {
	if bareRe.MatchString(k) {
		return k
	}
	return quote(k)
}

func (d *Doc) set(table, key, value string) {
	if !d.editable() {
		return
	}
	if i := d.find(table, key); i >= 0 {
		d.lines[i] = replaceValue(d.lines[i], value)
		return
	}
	k := quoteKey(key)
	if slices.Contains(nameTables, table) {
		k = quote(key) // ZIM names often need quotes; keep them uniform
	}
	line := k + " = " + quote(value)
	header, end := d.section(table)
	if header == -2 { // no such table yet
		if n := len(d.lines); n > 0 && strings.TrimSpace(d.lines[n-1]) != "" {
			d.lines = append(d.lines, "")
		}
		d.lines = append(d.lines, "["+table+"]", line)
		return
	}
	// After the table's last key, or right after its header.
	at := header + 1
	for i := header + 1; i < end; i++ {
		if _, ok := lineKey(d.lines[i]); ok {
			at = i + 1
		}
	}
	if table == "" && at == 0 {
		// No top-level keys yet: put it after the opening comments, just
		// before the first table, keeping a blank line in between.
		at = end
		for at > 0 && strings.TrimSpace(d.lines[at-1]) == "" {
			at--
		}
		if at < len(d.lines) && strings.TrimSpace(d.lines[at]) == "" {
			d.lines = slices.Insert(d.lines, at, line)
		} else {
			d.lines = slices.Insert(d.lines, at, line, "")
		}
		return
	}
	d.lines = slices.Insert(d.lines, at, line)
}

func (d *Doc) del(table, key string) bool {
	if !d.editable() {
		return false
	}
	i := d.find(table, key)
	if i < 0 {
		return false
	}
	d.lines = slices.Delete(d.lines, i, i+1)
	return true
}

// replaceValue swaps the value of a "key = value  # comment" line, keeping
// the key as written and any trailing comment.
func replaceValue(line, value string) string {
	m := keyRe.FindStringIndex(line)
	prefix, rest := line[:m[1]], line[m[1]:]
	v := strings.TrimLeft(rest, " \t")
	end := len(v)
	switch {
	case strings.HasPrefix(v, `"`):
		for i := 1; i < len(v); i++ {
			if v[i] == '\\' {
				i++
			} else if v[i] == '"' {
				end = i + 1
				break
			}
		}
	case strings.HasPrefix(v, "'"):
		if j := strings.IndexByte(v[1:], '\''); j >= 0 {
			end = j + 2
		}
	default:
		if j := strings.IndexByte(v, '#'); j >= 0 {
			end = j
		}
	}
	comment := v[end:]
	if strings.TrimSpace(comment) == "" {
		comment = ""
	} else if !strings.HasPrefix(comment, " ") && !strings.HasPrefix(comment, "\t") {
		comment = " " + comment
	}
	return prefix + " " + quote(value) + strings.TrimRight(comment, " \t")
}

// quote writes s as a TOML basic string. (strconv.Quote's \x and \a
// escapes are not TOML.)
func quote(s string) string {
	var b strings.Builder
	b.WriteByte('"')
	for _, r := range s {
		switch r {
		case '"':
			b.WriteString(`\"`)
		case '\\':
			b.WriteString(`\\`)
		case '\t':
			b.WriteString(`\t`)
		case '\n':
			b.WriteString(`\n`)
		case '\r':
			b.WriteString(`\r`)
		default:
			if r < 0x20 || r == 0x7f {
				fmt.Fprintf(&b, `\u%04X`, r)
			} else {
				b.WriteRune(r)
			}
		}
	}
	b.WriteByte('"')
	return b.String()
}
