// Package config loads and saves zimon's TOML config and knows the default paths.
package config

import (
	"bytes"
	"cmp"
	"errors"
	"fmt"
	"maps"
	"os"
	"path/filepath"
	"reflect"
	"slices"
	"strings"
	"time"

	"github.com/pelletier/go-toml/v2"
	"golang.org/x/text/language"
)

// DefaultCatalogURL is the Kiwix OPDS v2 root meant for programs.
// (browse.library.kiwix.org sits behind a browser-only bot check.)
const DefaultCatalogURL = "https://opds.library.kiwix.org/catalog/v2"

// Serve defaults: only this machine can reach kiwix-serve until the user
// opts in with address = "all" (or puts a reverse proxy in front).
const (
	DefaultServeAddress = "127.0.0.1"
	DefaultServePort    = 8080
)

// Config is the on-disk configuration.
type Config struct {
	LibraryDir    string         `toml:"library_dir" comment:"Where ZIM files and library.xml live."`
	StateDir      string         `toml:"state_dir" comment:"Where zimon keeps its database and lock file."`
	LibraryFile   string         `toml:"library_file" comment:"What the library should contain (collections, levels, hand-picked ZIMs). Empty means library.toml next to this file."`
	Languages     []string       `toml:"languages" comment:"Languages shown by default in search and browse (ISO 639-3, e.g. eng, swe). Empty means all."`
	KeepVersions  int            `toml:"keep_versions" comment:"How many versions of each ZIM to keep after an update (1 = only the newest)."`
	CatalogURL    string         `toml:"catalog_url" comment:"Kiwix OPDS v2 catalog root."`
	Catalogs      []string       `toml:"catalogs" comment:"More OPDS catalogs to take ZIMs from, e.g. another kiwix-serve's /catalog/v2. On a clash the newest release wins, then the first catalog."`
	CatalogMaxAge Duration       `toml:"catalog_max_age" comment:"Refresh the catalog automatically when it is older than this."`
	Download      DownloadConfig `toml:"download"`
	Serve         ServeConfig    `toml:"serve"`
	Remote        RemoteConfig   `toml:"remote"`
	Schedule      ScheduleConfig `toml:"schedule"`
}

// RemoteConfig holds settings for running commands on a remote (zimon remote).
type RemoteConfig struct {
	SSHCommand string `toml:"ssh_command" comment:"The ssh program, with any options, e.g. \"ssh -F ~/.ssh/zimon_config\"."`
}

// ScheduleConfig is when zimon serve updates the library by itself.
type ScheduleConfig struct {
	Update string `toml:"update" comment:"How often zimon serve runs update: off, daily, weekly or monthly (on the 1st). A missed run happens soon after zimon serve starts."`
	Time   string `toml:"time" comment:"At what time, HH:MM local time (plus up to 10 minutes, so servers don't all ask Kiwix at once)."`
}

// How often a schedule runs.
const (
	ScheduleOff     = "off"
	ScheduleDaily   = "daily"
	ScheduleWeekly  = "weekly"
	ScheduleMonthly = "monthly"
)

// At is Time as hours and minutes; Normalize has checked it.
func (s ScheduleConfig) At() (hour, minute int) {
	t, _ := time.Parse("15:04", s.Time)
	return t.Hour(), t.Minute()
}

// DownloadConfig holds download settings.
type DownloadConfig struct {
	RateLimit ByteSize `toml:"rate_limit" comment:"Max download speed, e.g. \"20MB\" (per second). 0 means unlimited."`
}

// ServeConfig holds kiwix-serve settings.
type ServeConfig struct {
	Address        string `toml:"address" comment:"Address zimon serve listens on: 127.0.0.1 = this machine only, all = every network interface, or one IP."`
	Port           int    `toml:"port"`
	AdminPort      int    `toml:"admin_port" comment:"Port of the admin page, on the same address. 0 means port + 1. Books never share its port, so their scripts can't reach it."`
	KiwixServePath string `toml:"kiwix_serve_path" comment:"Path to kiwix-serve. Empty means look it up on PATH."`
}

// Default returns the built-in defaults.
func Default() *Config {
	return &Config{
		LibraryDir:    filepath.Join(dataHome(), "zimon", "library"),
		StateDir:      filepath.Join(dataHome(), "zimon"),
		Languages:     defaultLanguages(),
		KeepVersions:  1,
		CatalogURL:    DefaultCatalogURL,
		CatalogMaxAge: Duration{24 * time.Hour},
		Serve:         ServeConfig{Address: DefaultServeAddress, Port: DefaultServePort},
		Remote:        RemoteConfig{SSHCommand: "ssh"},
		Schedule:      ScheduleConfig{Update: ScheduleOff, Time: "03:30"},
	}
}

// defaultLanguages is the system language plus English, as the ISO 639-3
// codes the Kiwix catalog uses: LANG=sv_SE.UTF-8 gives [swe eng].
func defaultLanguages() []string {
	locale := cmp.Or(os.Getenv("LC_ALL"), os.Getenv("LC_MESSAGES"), os.Getenv("LANG"))
	if i := strings.IndexAny(locale, ".@"); i >= 0 { // drop ".UTF-8", "@euro"
		locale = locale[:i]
	}
	eng := []string{"eng"}
	if locale == "" || locale == "C" || locale == "POSIX" {
		return eng
	}
	tag, err := language.Parse(strings.ReplaceAll(locale, "_", "-"))
	if err != nil {
		return eng
	}
	base, conf := tag.Base()
	code := base.ISO3()
	if conf == language.No || code == "und" || code == "eng" {
		return eng
	}
	return []string{code, "eng"}
}

// Path returns the config file location: $ZIMON_CONFIG, else $XDG_CONFIG_HOME/zimon/config.toml.
func Path() string {
	if p := os.Getenv("ZIMON_CONFIG"); p != "" {
		return p
	}
	return filepath.Join(configHome(), "zimon", "config.toml")
}

// Load reads the config at path. A missing file is not an error: it returns
// the defaults and exists=false.
func Load(path string) (cfg *Config, exists bool, err error) {
	cfg = Default()
	data, err := os.ReadFile(path)
	if errors.Is(err, os.ErrNotExist) {
		return cfg, false, nil
	}
	if err != nil {
		return nil, false, err
	}
	if err := DecodeStrict(data, cfg); err != nil {
		return nil, true, fmt.Errorf("%s: %w", path, err)
	}
	if err := cfg.Normalize(); err != nil {
		return nil, true, fmt.Errorf("%s: %w", path, err)
	}
	return cfg, true, nil
}

// DecodeStrict decodes TOML into v, rejecting keys v has no field for (a
// typo should be an error, not silently ignored), with a readable message.
// Keys must be spelled exactly as v's toml tags: TOML keys are
// case-sensitive, though the decoder alone would accept "Budget" for
// "budget", and zimon's line editor would then never find it.
func DecodeStrict(data []byte, v any) error {
	dec := toml.NewDecoder(bytes.NewReader(data))
	dec.DisallowUnknownFields()
	err := dec.Decode(v)
	if sm, ok := errors.AsType[*toml.StrictMissingError](err); ok {
		return errors.New(sm.String())
	}
	if err != nil {
		return err
	}
	var raw map[string]any
	if err := toml.Unmarshal(data, &raw); err != nil {
		return err
	}
	return checkKeyCase(raw, reflect.TypeOf(v), "")
}

// checkKeyCase walks decoded TOML alongside the type it was decoded into,
// and reports a key that matched a field only when case is ignored.
func checkKeyCase(v any, t reflect.Type, path string) error {
	for t.Kind() == reflect.Pointer {
		t = t.Elem()
	}
	switch v := v.(type) {
	case []any: // an array (of tables)
		if t.Kind() == reflect.Slice || t.Kind() == reflect.Array {
			for _, x := range v {
				if err := checkKeyCase(x, t.Elem(), path); err != nil {
					return err
				}
			}
		}
	case map[string]any: // a table
		for _, k := range slices.Sorted(maps.Keys(v)) { // the same error every time
			et, err := keyType(t, k, path)
			if err != nil {
				return err
			}
			if et == nil {
				continue
			}
			if err := checkKeyCase(v[k], et, path+k+"."); err != nil {
				return err
			}
		}
	}
	return nil
}

// keyType is the type key decodes into in table type t, or nil when the
// decoder ignores it.
func keyType(t reflect.Type, key, path string) (reflect.Type, error) {
	switch t.Kind() {
	case reflect.Map:
		return t.Elem(), nil
	case reflect.Struct:
		folded := ""
		for f := range t.Fields() {
			name, _, _ := strings.Cut(f.Tag.Get("toml"), ",")
			if !f.IsExported() || name == "-" {
				continue
			}
			name = cmp.Or(name, f.Name)
			if name == key {
				return f.Type, nil
			}
			if folded == "" && strings.EqualFold(name, key) {
				folded = name
			}
		}
		if folded != "" {
			return nil, fmt.Errorf("%s%s: TOML keys are case-sensitive; write %s%s", path, key, path, folded)
		}
	}
	return nil, nil
}

// Save writes the config to path atomically.
func (c *Config) Save(path string) error {
	data, err := toml.Marshal(c)
	if err != nil {
		return err
	}
	header := []byte("# zimon configuration. See `zimon config --help`.\n\n")
	return WriteFileAtomic(path, append(header, data...), 0o644)
}

// Normalize expands ~ in paths, applies defaults and checks the result, the
// same way Load does.
func (c *Config) Normalize() error {
	var err error
	if c.LibraryDir, err = ExpandHome(c.LibraryDir); err != nil {
		return err
	}
	if c.StateDir, err = ExpandHome(c.StateDir); err != nil {
		return err
	}
	if c.LibraryFile, err = ExpandHome(c.LibraryFile); err != nil {
		return err
	}
	if c.LibraryFile != "" && !filepath.IsAbs(c.LibraryFile) {
		return fmt.Errorf("library_file must be an absolute path or start with ~/ (got %q)", c.LibraryFile)
	}
	for key, dir := range map[string]string{"library_dir": c.LibraryDir, "state_dir": c.StateDir} {
		if !filepath.IsAbs(dir) {
			return fmt.Errorf("%s must be an absolute path or start with ~/ (got %q)", key, dir)
		}
	}
	if c.KeepVersions < 1 {
		c.KeepVersions = 1
	}
	if c.CatalogURL == "" {
		c.CatalogURL = DefaultCatalogURL
	}
	for _, u := range c.Catalogs {
		if !IsURL(u) {
			return fmt.Errorf("catalogs: %q is not an http(s) URL", u)
		}
	}
	if c.Serve.Port == 0 {
		c.Serve.Port = DefaultServePort
	}
	if c.Serve.Address == "" {
		c.Serve.Address = DefaultServeAddress
	}
	if strings.TrimSpace(c.Remote.SSHCommand) == "" {
		c.Remote.SSHCommand = "ssh"
	}
	switch c.Schedule.Update {
	case "":
		c.Schedule.Update = ScheduleOff
	case ScheduleOff, ScheduleDaily, ScheduleWeekly, ScheduleMonthly:
	default:
		return fmt.Errorf("schedule.update = %q: use off, daily, weekly or monthly", c.Schedule.Update)
	}
	if c.Schedule.Time == "" {
		c.Schedule.Time = "03:30"
	}
	if _, err := time.Parse("15:04", c.Schedule.Time); err != nil {
		return fmt.Errorf("schedule.time = %q: use HH:MM, e.g. \"03:30\"", c.Schedule.Time)
	}
	return nil
}

// IsURL reports whether s is an http or https URL (the scheme in any case).
func IsURL(s string) bool {
	scheme, _, ok := strings.Cut(s, "://")
	return ok && (strings.EqualFold(scheme, "https") || strings.EqualFold(scheme, "http"))
}

// CatalogURLs are the catalogs to fetch: catalog_url, then catalogs.
func (c *Config) CatalogURLs() []string { return append([]string{c.CatalogURL}, c.Catalogs...) }

// DBPath is the SQLite database location.
func (c *Config) DBPath() string { return filepath.Join(c.StateDir, "zimon.db") }

// LockPath is the lock file that serialises mutating commands.
func (c *Config) LockPath() string { return filepath.Join(c.StateDir, "zimon.lock") }

// ServeLockPath is the lock file that keeps to one zimon serve per state_dir.
func (c *Config) ServeLockPath() string { return filepath.Join(c.StateDir, "serve.lock") }

// LibraryXMLPath is the kiwix-serve library file.
func (c *Config) LibraryXMLPath() string { return filepath.Join(c.LibraryDir, "library.xml") }

// PartialDir holds in-progress downloads. It sits inside the library dir so the
// final rename stays on one filesystem.
func (c *Config) PartialDir() string { return filepath.Join(c.LibraryDir, ".partial") }

// NearestDir is dir, or its nearest parent that exists (the root at worst).
func NearestDir(dir string) string {
	for {
		if _, err := os.Stat(dir); err == nil || filepath.Dir(dir) == dir {
			return dir
		}
		dir = filepath.Dir(dir)
	}
}

// ExpandHome turns "~" and "~/..." into paths under the home directory.
func ExpandHome(p string) (string, error) {
	if p == "~" || (len(p) > 1 && p[:2] == "~/") {
		home, err := os.UserHomeDir()
		if err != nil {
			return "", err
		}
		return filepath.Join(home, p[1:]), nil
	}
	return p, nil
}

func dataHome() string {
	if d := os.Getenv("XDG_DATA_HOME"); d != "" {
		return d
	}
	home, _ := os.UserHomeDir()
	return filepath.Join(home, ".local", "share")
}

// configHome is $XDG_CONFIG_HOME or ~/.config, on every OS (macOS included),
// as the docs promise.
func configHome() string {
	if d := os.Getenv("XDG_CONFIG_HOME"); d != "" {
		return d
	}
	home, _ := os.UserHomeDir()
	return filepath.Join(home, ".config")
}

// WriteFileAtomic writes data to a temp file next to path, fsyncs it and
// renames it into place, making the folder if needed. A symbolic link at
// path stays one: the file it points to is written (into a dotfiles
// repository, say).
func WriteFileAtomic(path string, data []byte, perm os.FileMode) error {
	if target, err := filepath.EvalSymlinks(path); err == nil {
		path = target
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return err
	}
	tmp, err := os.CreateTemp(filepath.Dir(path), "."+filepath.Base(path)+".*")
	if err != nil {
		return err
	}
	defer os.Remove(tmp.Name())
	if _, err := tmp.Write(data); err != nil {
		tmp.Close()
		return err
	}
	if err := tmp.Chmod(perm); err != nil {
		tmp.Close()
		return err
	}
	if err := tmp.Sync(); err != nil {
		tmp.Close()
		return err
	}
	if err := tmp.Close(); err != nil {
		return err
	}
	return os.Rename(tmp.Name(), path)
}
