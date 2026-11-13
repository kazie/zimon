// Package remote keeps the remotes, zimon on other machines, in
// remotes.toml next to config.toml, and runs zimon commands on them over
// ssh. (An https:// remote is reached through its API; see internal/client.)
//
//	[server]                          # the name, as in --remote server
//	url = "ssh://you@server.example"  # ssh://[user@]host[:port]
//	command = "zimon"                 # how to run zimon there
//
//	[box]
//	url = "https://zim-admin.example" # zimon serve's admin port, or its proxy
//	token = "zimon_…"                 # from zimon token add on the server
package remote

import (
	"cmp"
	"errors"
	"fmt"
	"maps"
	"net/url"
	"os"
	"path/filepath"
	"slices"
	"strings"

	"github.com/pelletier/go-toml/v2"

	"github.com/kazie/zimon/internal/config"
	"github.com/kazie/zimon/internal/shell"
)

// DefaultCommand runs zimon on the remote when a remote names no command.
const DefaultCommand = "zimon"

// Remote is one entry of remotes.toml.
type Remote struct {
	Name    string `toml:"-"`
	URL     string `toml:"url"`
	Command string `toml:"command,omitempty"` // ssh: e.g. "podman exec -i zimon zimon"; empty means DefaultCommand
	Token   string `toml:"token,omitempty"`   // https: the API token
}

// HTTP reports whether r is reached through zimon serve's API (an http://
// or https:// URL) rather than over ssh.
func (r Remote) HTTP() bool { return config.IsURL(r.URL) }

// CheckURL checks a remote's URL: ssh://[user@]host[:port], or
// https://host[:port][/path] for zimon serve (http:// too).
func CheckURL(raw string) error {
	if !config.IsURL(raw) {
		_, err := ParseURL(raw)
		return err
	}
	_, err := ParseHTTP(raw)
	return err
}

// ParseHTTP reads https://host[:port][/path], where zimon serve's API is
// under path/api/v1. The URL it returns ends without a slash.
func ParseHTTP(raw string) (*url.URL, error) {
	u, err := url.Parse(raw)
	switch {
	case err != nil:
		return nil, err
	case (u.Scheme != "https" && u.Scheme != "http") || u.Host == "" || u.Opaque != "":
		return nil, fmt.Errorf("%s: give https://host[:port]", raw)
	case u.User != nil, u.RawQuery != "", u.Fragment != "", u.ForceQuery:
		return nil, fmt.Errorf("%s: give https://host[:port], without a user, query or #", raw)
	}
	u.Path = strings.TrimSuffix(u.Path, "/")
	u.RawPath = ""
	return u, nil
}

// Path is remotes.toml in the same directory as the config file.
func Path(configPath string) string {
	return filepath.Join(filepath.Dir(configPath), "remotes.toml")
}

// Load reads the remotes at path, sorted by name. A missing file means none.
func Load(path string) ([]Remote, error) {
	data, err := os.ReadFile(path)
	if errors.Is(err, os.ErrNotExist) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	var byName map[string]Remote
	if err := config.DecodeStrict(data, &byName); err != nil {
		return nil, fmt.Errorf("%s: %w", path, err)
	}
	out := make([]Remote, 0, len(byName))
	for _, name := range slices.Sorted(maps.Keys(byName)) {
		r := byName[name]
		r.Name = name
		out = append(out, r)
	}
	return out, nil
}

// Save writes remotes to path, readable by the owner only: a remote may
// carry a token.
func Save(path string, remotes []Remote) error {
	byName := map[string]Remote{}
	for _, r := range remotes {
		byName[r.Name] = r
	}
	data, err := toml.Marshal(byName)
	if err != nil {
		return err
	}
	header := []byte("# zimon remotes: zimon remote add|list|remove|test, and --remote NAME.\n\n")
	return config.WriteFileAtomic(path, append(header, data...), 0o600)
}

// Find returns the remote called name.
func Find(remotes []Remote, name string) (Remote, bool) {
	i := slices.IndexFunc(remotes, func(r Remote) bool { return r.Name == name })
	if i < 0 {
		return Remote{}, false
	}
	return remotes[i], true
}

// Target is where ssh connects to.
type Target struct {
	Dest string // [user@]host
	Port string // empty for ssh's default
}

// ParseURL reads ssh://[user@]host[:port].
func ParseURL(raw string) (Target, error) {
	u, err := url.Parse(raw)
	switch {
	case err != nil:
		return Target{}, err
	case u.Scheme != "ssh" || u.Hostname() == "" || u.Opaque != "":
		return Target{}, fmt.Errorf("%s: give ssh://[user@]host[:port] or https://host", raw)
	case u.Path != "" && u.Path != "/", u.RawQuery != "", u.Fragment != "":
		return Target{}, fmt.Errorf("%s: an ssh remote is only [user@]host[:port]", raw)
	}
	t := Target{Dest: u.Hostname(), Port: u.Port()}
	if user := u.User.Username(); user != "" {
		t.Dest = user + "@" + t.Dest
	}
	if strings.HasPrefix(t.Dest, "-") { // ssh would take it for an option
		return Target{}, fmt.Errorf("%s: a user or host may not start with -", raw)
	}
	return t, nil
}

// Mode is how ssh talks to the user.
type Mode int

const (
	// Pipe passes zimon's input and output through: for scripts and pipes.
	Pipe Mode = iota
	// Terminal gives the remote command a terminal (ssh -t), so prompts,
	// progress bars and browse work.
	Terminal
	// Batch never asks for anything and gives up on a dead host quickly: for
	// shell completion and remote test.
	Batch
)

// SSH is the command line that runs zimon with args on r: ssh (sshCommand,
// which may carry options, split on spaces, with ~/ expanded), the options
// mode needs, and the remote command quoted for the remote shell.
func (r Remote) SSH(sshCommand string, mode Mode, args []string) ([]string, error) {
	t, err := ParseURL(r.URL)
	if err != nil {
		return nil, err
	}
	argv := strings.Fields(sshCommand)
	for i, f := range argv {
		if argv[i], err = config.ExpandHome(f); err != nil {
			return nil, err
		}
	}
	// Options already in sshCommand come first and win.
	switch mode {
	case Terminal:
		// Without LogLevel=ERROR, ssh -t says "Connection to ... closed"
		// after every command.
		argv = append(argv, "-t", "-o", "LogLevel=ERROR")
	case Batch:
		argv = append(argv, "-n", "-o", "BatchMode=yes", "-o", "ConnectTimeout=5")
	}
	if t.Port != "" {
		argv = append(argv, "-p", t.Port)
	}
	command := cmp.Or(r.Command, DefaultCommand)
	if len(args) > 0 {
		command += " " + shell.Quote(args)
	}
	return append(argv, t.Dest, "--", command), nil
}
