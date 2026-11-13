package config

import (
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"
)

func TestParseBytes(t *testing.T) {
	for in, want := range map[string]int64{
		"0": 0, "1024": 1024, "500MB": 500e6, "2G": 2e9, "1.5GiB": 1.5 * (1 << 30), "20 mb": 20e6, "10kB": 10e3,
	} {
		got, err := ParseBytes(in)
		if err != nil || got != want {
			t.Errorf("ParseBytes(%q) = %d, %v; want %d", in, got, err, want)
		}
	}
	for _, bad := range []string{"", "MB", "12 parsecs", "1.2.3GB"} {
		if _, err := ParseBytes(bad); err == nil {
			t.Errorf("ParseBytes(%q) should fail", bad)
		}
	}
}

func TestFormatBytes(t *testing.T) {
	for in, want := range map[int64]string{
		999: "999 B", 230400: "230 kB", 24e6: "24.0 MB", 333429922: "333 MB", 14386799616: "14.4 GB", 127418088448: "127 GB",
	} {
		if got := FormatBytes(in); got != want {
			t.Errorf("FormatBytes(%d) = %q, want %q", in, got, want)
		}
	}
}

func TestLoadSaveRoundTrip(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "config.toml")

	cfg, exists, err := Load(path)
	if err != nil || exists {
		t.Fatalf("missing file: exists=%v err=%v", exists, err)
	}
	cfg.LibraryDir = filepath.Join(dir, "lib")
	cfg.Languages = []string{"eng", "swe"}
	cfg.KeepVersions = 2
	cfg.CatalogMaxAge = Duration{6 * time.Hour}
	cfg.Download.RateLimit = ByteSize(20e6)
	if err := cfg.Save(path); err != nil {
		t.Fatal(err)
	}
	data, _ := os.ReadFile(path)
	if !strings.Contains(string(data), `rate_limit = '20MB'`) && !strings.Contains(string(data), `rate_limit = "20MB"`) {
		t.Errorf("rate_limit not human readable:\n%s", data)
	}

	got, exists, err := Load(path)
	if err != nil || !exists {
		t.Fatalf("reload: exists=%v err=%v", exists, err)
	}
	if got.LibraryDir != cfg.LibraryDir || got.KeepVersions != 2 || len(got.Languages) != 2 ||
		got.CatalogMaxAge.Duration != 6*time.Hour || got.Download.RateLimit != ByteSize(20e6) {
		t.Errorf("round trip mismatch: %+v", got)
	}
}

func TestLoadRejectsUnknownKeys(t *testing.T) {
	path := filepath.Join(t.TempDir(), "config.toml")
	if err := os.WriteFile(path, []byte("library_dirr = \"/x\"\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if _, _, err := Load(path); err == nil {
		t.Fatal("expected an error for a misspelt key")
	}
}

func TestDecodeStrictWantsExactKeys(t *testing.T) {
	type item struct {
		Name string `toml:"name"`
	}
	type doc struct {
		Budget   ByteSize          `toml:"budget"`
		Download DownloadConfig    `toml:"download"`
		Items    map[string]string `toml:"items"`
		Entry    []item            `toml:"entry"`
		Plain    string
	}
	for _, c := range []struct {
		text, err string // err: "" when it decodes
	}{
		{"budget = \"1GB\"\nPlain = \"x\"\n[download]\nrate_limit = \"1MB\"\n[items]\nAnyCase = \"on\"\n[[entry]]\nname = \"a\"\n", ""},
		{"Budget = \"1GB\"\n", "Budget: TOML keys are case-sensitive; write budget"},
		{"[Download]\n", "Download: TOML keys are case-sensitive; write download"},
		{"[download]\nRate_Limit = \"1MB\"\n", "download.Rate_Limit: TOML keys are case-sensitive; write download.rate_limit"},
		{"[[entry]]\nNAME = \"a\"\n", "entry.NAME: TOML keys are case-sensitive; write entry.name"},
		{"plain = \"x\"\n", "plain: TOML keys are case-sensitive; write Plain"},
		// Every key is checked, not just the first.
		{"Plain = \"x\"\nbudget = \"1GB\"\n[download]\nRate_Limit = \"1MB\"\n", "download.Rate_Limit"},
		{"budgett = \"1GB\"\n", "budgett"}, // unknown: the decoder's own message
	} {
		var d doc
		err := DecodeStrict([]byte(c.text), &d)
		if c.err == "" && err != nil || c.err != "" && (err == nil || !strings.Contains(err.Error(), c.err)) {
			t.Errorf("%q: %v, want %q", c.text, err, c.err)
		}
	}
}

func TestExpandHome(t *testing.T) {
	home, _ := os.UserHomeDir()
	path := filepath.Join(t.TempDir(), "config.toml")
	if err := os.WriteFile(path, []byte("library_dir = \"~/zims\"\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	cfg, _, err := Load(path)
	if err != nil {
		t.Fatal(err)
	}
	if cfg.LibraryDir != filepath.Join(home, "zims") {
		t.Errorf("LibraryDir = %q", cfg.LibraryDir)
	}
}

func TestByteSizeRoundTrip(t *testing.T) {
	for _, in := range []string{"0", "20MB", "1GiB", "20MiB", "1.5GB", "123"} {
		n, err := ParseBytes(in)
		if err != nil {
			t.Fatal(err)
		}
		text, _ := ByteSize(n).MarshalText()
		back, err := ParseBytes(string(text))
		if err != nil || back != n {
			t.Errorf("%s -> %d -> %q -> %d (%v)", in, n, text, back, err)
		}
	}
}

func TestRelativePathsRejected(t *testing.T) {
	path := filepath.Join(t.TempDir(), "config.toml")
	if err := os.WriteFile(path, []byte("library_dir = \"zims\"\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if _, _, err := Load(path); err == nil {
		t.Fatal("a relative library_dir should be rejected")
	}
}

func TestDefaultLanguages(t *testing.T) {
	for _, tc := range []struct {
		all, messages, lang string
		want                []string
	}{
		{lang: "sv_SE.UTF-8", want: []string{"swe", "eng"}},
		{lang: "nb_NO.UTF-8", want: []string{"nob", "eng"}},
		{lang: "de_DE@euro", want: []string{"deu", "eng"}},
		{lang: "en_US.UTF-8", want: []string{"eng"}},
		{lang: "C.UTF-8", want: []string{"eng"}},
		{lang: "POSIX", want: []string{"eng"}},
		{lang: "", want: []string{"eng"}},
		{lang: "not a locale!", want: []string{"eng"}},
		{all: "fr_FR.UTF-8", lang: "sv_SE.UTF-8", want: []string{"fra", "eng"}},
		{messages: "fi_FI.UTF-8", lang: "sv_SE.UTF-8", want: []string{"fin", "eng"}},
	} {
		t.Setenv("LC_ALL", tc.all)
		t.Setenv("LC_MESSAGES", tc.messages)
		t.Setenv("LANG", tc.lang)
		if got := defaultLanguages(); !slices.Equal(got, tc.want) {
			t.Errorf("LC_ALL=%q LC_MESSAGES=%q LANG=%q: got %v, want %v", tc.all, tc.messages, tc.lang, got, tc.want)
		}
	}
}

func TestServeAddress(t *testing.T) {
	path := filepath.Join(t.TempDir(), "config.toml")
	for content, want := range map[string]string{
		"":                                     DefaultServeAddress, // not set: this machine only
		"[serve]\naddress = \"\"\n":            DefaultServeAddress,
		"[serve]\naddress = \"all\"\n":         "all",
		"[serve]\naddress = \"192.168.1.5\"\n": "192.168.1.5",
	} {
		if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
			t.Fatal(err)
		}
		cfg, _, err := Load(path)
		if err != nil {
			t.Fatal(err)
		}
		if cfg.Serve.Address != want {
			t.Errorf("%q: address = %q, want %q", content, cfg.Serve.Address, want)
		}
	}
}

func TestDurationMustBeAString(t *testing.T) {
	path := filepath.Join(t.TempDir(), "config.toml")
	if err := os.WriteFile(path, []byte("catalog_max_age = 3600\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if _, _, err := Load(path); err == nil {
		t.Fatal("a bare number for a duration should be rejected")
	}
	if err := os.WriteFile(path, []byte("catalog_max_age = \"6h\"\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if cfg, _, err := Load(path); err != nil || cfg.CatalogMaxAge.Duration != 6*time.Hour {
		t.Fatalf("got %v, %v", cfg, err)
	}
}

// A file behind a symbolic link is written through it; the link stays.
func TestWriteFileAtomicFollowsLinks(t *testing.T) {
	dir := t.TempDir()
	target, link := filepath.Join(dir, "dotfiles", "library.toml"), filepath.Join(dir, "library.toml")
	for _, c := range []struct{ path, data string }{
		{target, "old\n"}, // set up the target, then write through the link
		{link, "new\n"},
	} {
		if c.path == link {
			if err := os.Symlink(target, link); err != nil {
				t.Fatal(err)
			}
		}
		if err := os.MkdirAll(filepath.Dir(c.path), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := WriteFileAtomic(c.path, []byte(c.data), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	if st, err := os.Lstat(link); err != nil || st.Mode()&os.ModeSymlink == 0 {
		t.Fatalf("the link became %v, %v", st.Mode(), err)
	}
	if b, _ := os.ReadFile(target); string(b) != "new\n" {
		t.Errorf("target has %q", b)
	}
}
