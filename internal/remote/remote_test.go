package remote

import (
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
)

func TestParseURL(t *testing.T) {
	for _, c := range []struct {
		url  string
		want Target
		err  string
	}{
		{"ssh://nas", Target{Dest: "nas"}, ""},
		{"ssh://kazie@nas.example", Target{Dest: "kazie@nas.example"}, ""},
		{"ssh://kazie@nas.example:2222", Target{Dest: "kazie@nas.example", Port: "2222"}, ""},
		{"ssh://[fe80::1]:22", Target{Dest: "fe80::1", Port: "22"}, ""},
		{"ssh://nas/", Target{Dest: "nas"}, ""},
		{"https://zim.example", Target{}, "give ssh://"},
		{"nas.example", Target{}, "give ssh://[user@]host[:port]"},
		{"kazie@nas", Target{}, "give ssh://[user@]host[:port]"},
		{"ssh://", Target{}, "give ssh://[user@]host[:port]"},
		{"ssh://nas/srv/zim", Target{}, "only [user@]host[:port]"},
		{"ssh://-oProxyCommand=touch%20%2Ftmp%2Fx@host", Target{}, "may not start with -"},
		{"ssh://-host", Target{}, "may not start with -"},
	} {
		got, err := ParseURL(c.url)
		if c.err != "" {
			if err == nil || !strings.Contains(err.Error(), c.err) {
				t.Errorf("ParseURL(%q) error = %v, want %q", c.url, err, c.err)
			}
			continue
		}
		if err != nil || got != c.want {
			t.Errorf("ParseURL(%q) = %+v, %v; want %+v", c.url, got, err, c.want)
		}
	}
}

func TestParseHTTP(t *testing.T) {
	for _, c := range []struct {
		url, want, err string
	}{
		{"https://zim.example", "https://zim.example", ""},
		{"https://zim.example/", "https://zim.example", ""},
		{"http://127.0.0.1:8081", "http://127.0.0.1:8081", ""},
		{"https://example.org/zimon/", "https://example.org/zimon", ""},
		{"https://", "", "give https://host"},
		{"https://me@zim.example", "", "without a user"},
		{"https://zim.example/?x=1", "", "without a user, query or #"},
		{"https://zim.example/#top", "", "without a user, query or #"},
	} {
		got, err := ParseHTTP(c.url)
		if c.err != "" {
			if err == nil || !strings.Contains(err.Error(), c.err) {
				t.Errorf("ParseHTTP(%q) error = %v, want %q", c.url, err, c.err)
			}
			continue
		}
		if err != nil || got.String() != c.want {
			t.Errorf("ParseHTTP(%q) = %v, %v; want %s", c.url, got, err, c.want)
		}
	}
	for url, want := range map[string]bool{"https://x": true, "http://x": true, "ssh://x": false, "x": false} {
		if got := (Remote{URL: url}).HTTP(); got != want {
			t.Errorf("Remote{URL: %q}.HTTP() = %v", url, got)
		}
		if err := CheckURL(url); (err == nil) != (url != "x") {
			t.Errorf("CheckURL(%q) = %v", url, err)
		}
	}
}

func TestSSH(t *testing.T) {
	t.Setenv("HOME", "/home/me")
	for _, c := range []struct {
		name string
		r    Remote
		ssh  string
		mode Mode
		args []string
		want []string
	}{
		{"plain", Remote{URL: "ssh://nas"}, "ssh", Pipe, []string{"list"},
			[]string{"ssh", "nas", "--", "zimon list"}},
		{"terminal and port", Remote{URL: "ssh://me@nas:2222"}, "ssh", Terminal, []string{"browse"},
			[]string{"ssh", "-t", "-o", "LogLevel=ERROR", "-p", "2222", "me@nas", "--", "zimon browse"}},
		{"quoting for the remote shell", Remote{URL: "ssh://nas"}, "ssh", Pipe, []string{"search", "stack exchange", "it's"},
			[]string{"ssh", "nas", "--", `zimon search 'stack exchange' 'it'\''s'`}},
		{"a command of its own", Remote{URL: "ssh://nas", Command: "podman exec -i zimon zimon"}, "ssh", Pipe, []string{"update", "--yes"},
			[]string{"ssh", "nas", "--", "podman exec -i zimon zimon update --yes"}},
		{"ssh with options", Remote{URL: "ssh://nas"}, "ssh -F /etc/zimon/ssh_config", Pipe, []string{"--version"},
			[]string{"ssh", "-F", "/etc/zimon/ssh_config", "nas", "--", "zimon --version"}},
		{"batch, for completion and remote test", Remote{URL: "ssh://nas"}, "ssh", Batch, []string{"__complete", "remove", ""},
			[]string{"ssh", "-n", "-o", "BatchMode=yes", "-o", "ConnectTimeout=5", "nas", "--", "zimon __complete remove ''"}},
		{"~ in ssh options", Remote{URL: "ssh://nas"}, "ssh -F ~/.ssh/zimon", Pipe, nil,
			[]string{"ssh", "-F", "/home/me/.ssh/zimon", "nas", "--", "zimon"}},
	} {
		got, err := c.r.SSH(c.ssh, c.mode, c.args)
		if err != nil || !slices.Equal(got, c.want) {
			t.Errorf("%s: SSH = %q, %v; want %q", c.name, got, err, c.want)
		}
	}
	if _, err := (Remote{URL: "https://zim"}).SSH("ssh", Pipe, nil); err == nil {
		t.Error("an https remote gave an ssh command")
	}
}

func TestLoadSave(t *testing.T) {
	path := Path(filepath.Join(t.TempDir(), "zimon", "config.toml"))
	if rs, err := Load(path); err != nil || len(rs) != 0 {
		t.Fatalf("Load of a missing file = %v, %v", rs, err)
	}
	want := []Remote{
		{Name: "nas", URL: "ssh://kazie@nas:2222", Command: "sudo -u zimon zimon"},
		{Name: "box", URL: "ssh://box"},
	}
	if err := Save(path, want); err != nil {
		t.Fatal(err)
	}
	st, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	if st.Mode().Perm() != 0o600 {
		t.Errorf("remotes.toml mode = %v, want 0600", st.Mode().Perm())
	}
	got, err := Load(path)
	if err != nil {
		t.Fatal(err)
	}
	if !slices.Equal(got, []Remote{want[1], want[0]}) { // sorted by name
		t.Errorf("Load = %+v", got)
	}
	if r, ok := Find(got, "nas"); !ok || r.Command != "sudo -u zimon zimon" {
		t.Errorf("Find(nas) = %+v, %v", r, ok)
	}
	if _, ok := Find(got, "nope"); ok {
		t.Error("Find found a remote that is not there")
	}

	// A typo in the file is an error, not silently ignored.
	if err := os.WriteFile(path, []byte("[nas]\nurll = \"ssh://nas\"\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := Load(path); err == nil || !strings.Contains(err.Error(), "urll") {
		t.Errorf("Load of a typo: %v", err)
	}
}
