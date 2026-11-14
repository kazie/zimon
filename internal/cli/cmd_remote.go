package cli

import (
	"bytes"
	"cmp"
	"context"
	"errors"
	"fmt"
	"net"
	"os"
	"os/exec"
	"regexp"
	"slices"
	"strings"

	"github.com/spf13/cobra"

	"github.com/kazie/zimon/internal/buildinfo"
	"github.com/kazie/zimon/internal/client"
	"github.com/kazie/zimon/internal/config"
	"github.com/kazie/zimon/internal/remote"
	"github.com/kazie/zimon/internal/shell"
	"github.com/kazie/zimon/internal/ui"
)

func newRemoteCmd(a *app) *cobra.Command {
	cmd := &cobra.Command{
		Use:   "remote",
		Short: "Manage remotes: zimon on other machines, over ssh or https",
		Long: `A remote is zimon on another machine, such as a NAS or a server. With
--remote NAME (or ZIMON_REMOTE=NAME) commands work on its library instead
of this one.

An ssh:// remote runs every command there, prompts, progress bars and browse
included. ssh does the logging in, so set up keys (ssh-copy-id) and
~/.ssh/config as you would for any server. The ssh program itself is
remote.ssh_command in the config.

An https:// remote is zimon serve, reached through its API with a token
(zimon token add on the server makes one; add asks for it). The command runs
here and asks the server; a change runs there as a job, shown here as it
goes. Ctrl-C stops showing it, but the job carries on (see zimon jobs).
Every command but serve, init, config and token works this way, browse and
shell completion included.

Remotes live in remotes.toml next to config.toml.`,
		Example: `  zimon remote add nas ssh://you@nas.local
  zimon remote add box ssh://box:2222 --command "podman exec -i zimon zimon"
  zimon remote add home https://zim-admin.example.org
  zimon remote test nas
  zimon --remote nas list
  ZIMON_REMOTE=nas zimon update`,
		Args: cobra.NoArgs,
		RunE: func(_ *cobra.Command, _ []string) error { return a.listRemotes() },
	}

	var command string
	add := &cobra.Command{
		Use:   "add NAME ssh://[user@]host[:port] | https://host",
		Short: "Add a remote",
		Long: `Add a remote. For an https:// one (zimon serve, or a proxy in front of it),
add asks for a token, made on the server with zimon token add, and checks
that the server takes it. Without a terminal it reads the token from stdin.`,
		Args: cobra.ExactArgs(2),
		RunE: func(cmd *cobra.Command, args []string) error {
			name, url := args[0], args[1]
			if !remoteName.MatchString(name) {
				return fmt.Errorf("%q: a remote's name is letters, digits, - and _", name)
			}
			if err := remote.CheckURL(url); err != nil {
				return err
			}
			path, remotes, err := a.remotes()
			if err != nil {
				return err
			}
			if _, ok := remote.Find(remotes, name); ok {
				return fmt.Errorf("there is a remote %s already; zimon remote remove %s first", name, name)
			}
			r := remote.Remote{Name: name, URL: url, Command: command}
			if r.HTTP() {
				if r.Token, err = a.askToken(cmd.Context(), r); err != nil {
					return err
				}
			}
			if err := remote.Save(path, append(remotes, r)); err != nil {
				return err
			}
			a.out.Success("Added remote %s %s", ui.Bold.Render(name), ui.Dim.Render("("+url+")"))
			a.out.Hint("Try it: zimon remote test %s   Use it: zimon --remote %s list", name, name)
			return nil
		},
	}
	add.Flags().StringVar(&command, "command", "", `how to run zimon there (default "zimon"), e.g. "sudo -u zimon zimon"`)

	cmd.AddCommand(add, &cobra.Command{
		Use:     "list",
		Aliases: []string{"ls"},
		Short:   "List the remotes",
		Args:    cobra.NoArgs,
		RunE:    func(_ *cobra.Command, _ []string) error { return a.listRemotes() },
	}, &cobra.Command{
		Use:               "remove NAME",
		Aliases:           []string{"rm"},
		Short:             "Remove a remote (nothing on it changes)",
		Args:              cobra.ExactArgs(1),
		ValidArgsFunction: a.completeRemoteArg,
		RunE: func(_ *cobra.Command, args []string) error {
			r, remotes, path, err := a.findRemote(args[0])
			if err != nil {
				return err
			}
			if err := remote.Save(path, slices.DeleteFunc(remotes, func(x remote.Remote) bool { return x.Name == r.Name })); err != nil {
				return err
			}
			a.out.Success("Removed remote %s", ui.Bold.Render(r.Name))
			return nil
		},
	}, &cobra.Command{
		Use:               "test NAME",
		Short:             "Check that zimon answers on a remote",
		Args:              cobra.ExactArgs(1),
		ValidArgsFunction: a.completeRemoteArg,
		RunE: func(cmd *cobra.Command, args []string) error {
			r, _, _, err := a.findRemote(args[0])
			if err != nil {
				return err
			}
			version, ssh, err := a.askVersion(cmd.Context(), r)
			switch {
			case cmd.Context().Err() != nil:
				return cmd.Context().Err()
			case ssh == nil && err != nil:
				return err
			case err != nil:
				a.out.Fail("%s does not answer: %v", ui.Bold.Render(r.Name), err)
				for line := range strings.Lines(ssh.stderr) {
					a.out.Hint("  %s", strings.TrimRight(line, "\n"))
				}
				a.out.Hint("The command was: %s", shell.Quote(ssh.argv))
				return errSilent
			}
			a.out.Success("%s answers: %s", ui.Bold.Render(r.Name), version)
			return nil
		},
	})
	return cmd
}

// sshRun is how an ssh command went: the command, and what it printed on stderr.
type sshRun struct {
	argv   []string
	stderr string
}

// reason is ssh's last line on stderr, which says why it failed, as
// "Permission denied (publickey)."
func (s *sshRun) reason() string {
	return s.stderr[strings.LastIndexByte(s.stderr, '\n')+1:]
}

// askVersion asks a remote which zimon it runs ("zimon 1.2"). For an ssh
// remote that it could ask, it also says how the ssh command went; an error
// from running it is ssh's own, which doesn't name the remote.
func (a *app) askVersion(ctx context.Context, r remote.Remote) (version string, ssh *sshRun, err error) {
	if r.HTTP() {
		c, err := client.New(r, a.httpClient())
		if err != nil {
			return "", nil, err
		}
		info, err := c.Info(ctx)
		if err != nil {
			return "", nil, err
		}
		return "zimon " + buildinfo.Line(info.Version, info.Commit), nil, nil
	}
	argv, err := r.SSH(a.cfg.Remote.SSHCommand, remote.Batch, []string{"--version"})
	if err != nil {
		return "", nil, err
	}
	ssh = &sshRun{argv: argv}
	var stdout, stderr bytes.Buffer
	c := exec.CommandContext(ctx, ssh.argv[0], ssh.argv[1:]...)
	c.Stdout, c.Stderr = &stdout, &stderr
	err = c.Run()
	ssh.stderr = strings.TrimSpace(stderr.String())
	if err != nil {
		return "", ssh, err
	}
	return strings.TrimSpace(stdout.String()), ssh, nil
}

var remoteName = regexp.MustCompile(`^[A-Za-z0-9_-]+$`)

// remotes reads remotes.toml, returning its path too.
func (a *app) remotes() (string, []remote.Remote, error) {
	path := remote.Path(cmp.Or(a.cfgPath, config.Path())) // completion runs before the usual setup
	remotes, err := remote.Load(path)
	return path, remotes, err
}

// findRemote reads remotes.toml and returns the remote called name, with
// every remote and the file's path.
func (a *app) findRemote(name string) (remote.Remote, []remote.Remote, string, error) {
	path, remotes, err := a.remotes()
	if err != nil {
		return remote.Remote{}, nil, "", err
	}
	if r, ok := remote.Find(remotes, name); ok {
		return r, remotes, path, nil
	}
	return remote.Remote{}, nil, "", fmt.Errorf("no remote %q in %s; add it with: zimon remote add %s ssh://[user@]host", name, ui.Tilde(path), name)
}

// askToken asks for r's API token, and checks that r takes it.
func (a *app) askToken(ctx context.Context, r remote.Remote) (string, error) {
	if r.Command != "" {
		return "", errors.New("--command is for ssh:// remotes")
	}
	u, err := remote.ParseHTTP(r.URL)
	if err != nil {
		return "", err
	}
	if u.Scheme == "http" && !loopback(u.Hostname()) {
		a.out.Warn("%s is plain http: the token crosses the network unencrypted. Use https:// unless you trust every machine on the way.", r.URL)
	}
	if r.Token, err = a.out.AskSecret("Token for " + r.Name + " (zimon token add on the server makes one):"); err != nil {
		return "", err
	}
	if r.Token == "" {
		return "", errors.New("no token given; nothing saved")
	}
	c, err := client.New(r, a.httpClient())
	if err != nil {
		return "", err
	}
	if _, err := c.Info(ctx); err != nil {
		return "", fmt.Errorf("%w\n  nothing saved", err)
	}
	return r.Token, nil
}

// loopback reports whether host is this machine.
func loopback(host string) bool {
	if host == "localhost" {
		return true
	}
	ip := net.ParseIP(host)
	return ip != nil && ip.IsLoopback()
}

func (a *app) listRemotes() error {
	path, remotes, err := a.remotes()
	if err != nil {
		return err
	}
	if len(remotes) == 0 {
		a.out.Status("No remotes yet.")
		a.out.Hint("Add one with: zimon remote add NAME ssh://[user@]host")
		return nil
	}
	t := ui.NewTable("NAME", "URL", "COMMAND")
	for _, r := range remotes {
		command := cmp.Or(r.Command, remote.DefaultCommand)
		if r.HTTP() {
			command = ui.Dim.Render("(its API)")
		}
		t.Add(ui.Bold.Render(r.Name), r.URL, command)
	}
	t.Render(a.out.Out, a.out.Width())
	a.out.Hint("in %s · use one with: zimon --remote NAME <command>", ui.Tilde(path))
	return nil
}

// localOnly are the commands that never run on a remote: managing remotes,
// pull (which names its remote), completion scripts, the man page and help
// (and --version, -h and --help).
var localOnly = []string{"remote", "pull", "completion", "help", "man"}

// remoteCall is a command line to run on a remote.
type remoteCall struct {
	name, cfgPath string
	args          []string // for zimon there
	completion    bool     // shell completion: no terminal, no prompts
}

// parseRemoteCall works out from zimon's arguments whether they run on a
// remote: picked with --remote NAME or $ZIMON_REMOTE (an empty --remote
// keeps it here), and not a local-only command. It looks before cobra does,
// so a newer zimon there may take commands and flags this one does not know.
func parseRemoteCall(args []string) (remoteCall, bool) {
	c := remoteCall{}
	words := args
	if len(args) > 0 && (args[0] == cobra.ShellCompRequestCmd || args[0] == cobra.ShellCompNoDescRequestCmd) {
		c.completion, words = true, args[1:]
	}
	name, named, cfgPath, rest := splitLocal(words)
	if !named {
		name = os.Getenv("ZIMON_REMOTE")
	}
	command := slices.IndexFunc(rest, func(w string) bool { return !strings.HasPrefix(w, "-") })
	switch {
	case name == "", command < 0, slices.Contains(localOnly, rest[command]):
		return c, false
	case c.completion && command == len(rest)-1:
		return c, false // completing the command's name: they are the same everywhere
	case !c.completion && slices.ContainsFunc(rest, func(w string) bool { return w == "-h" || w == "--help" || w == "--version" }):
		return c, false
	}
	c.name, c.cfgPath, c.args = name, cfgPath, rest
	if c.completion {
		c.args = append([]string{args[0]}, rest...)
	}
	return c, true
}

// remoteFor reads the config and remotes.toml, and returns c's remote.
func (a *app) remoteFor(c remoteCall) (remote.Remote, error) {
	a.cfgPath = c.cfgPath
	if err := a.loadConfig(); err != nil {
		return remote.Remote{}, err
	}
	r, _, _, err := a.findRemote(c.name)
	return r, err
}

// forward replaces zimon with ssh running c on r; it returns only when
// that fails.
func (a *app) forward(c remoteCall, r remote.Remote) error {
	mode := remote.Pipe
	switch {
	case c.completion:
		mode = remote.Batch // never read the shell's terminal or wait on a password
	case a.out.InTTY && a.out.OutTTY:
		mode = remote.Terminal
	}
	argv, err := r.SSH(a.cfg.Remote.SSHCommand, mode, c.args)
	if err != nil {
		return err
	}
	return remote.Exec(argv)
}

// splitLocal takes the flags that only mean something here, --remote and
// --config, out of args, returning their values, whether --remote was there,
// and the rest.
func splitLocal(args []string) (remote string, named bool, cfgPath string, rest []string) {
	for i := 0; i < len(args); i++ {
		arg := args[i]
		if arg == "--" {
			return remote, named, cfgPath, append(rest, args[i:]...)
		}
		switch {
		case strings.HasPrefix(arg, "--remote="):
			remote, named = arg[len("--remote="):], true
		case arg == "--remote":
			named = true
			if i+1 < len(args) {
				i++
				remote = args[i]
			}
		case strings.HasPrefix(arg, "--config="):
			cfgPath = arg[len("--config="):]
		case arg == "--config":
			if i+1 < len(args) {
				i++
				cfgPath = args[i]
			}
		default:
			rest = append(rest, arg)
		}
	}
	return remote, named, cfgPath, rest
}

func (a *app) completeRemotes(_ *cobra.Command, _ []string, _ string) ([]string, cobra.ShellCompDirective) {
	return a.remoteNames(false), cobra.ShellCompDirectiveNoFileComp
}

// remoteNames completes remote names, with their URLs; only https:// ones
// when httpOnly.
func (a *app) remoteNames(httpOnly bool) []string {
	_, remotes, _ := a.remotes()
	var out []string
	for _, r := range remotes {
		if r.HTTP() || !httpOnly {
			out = append(out, r.Name+"\t"+r.URL)
		}
	}
	return out
}

func (a *app) completeRemoteArg(cmd *cobra.Command, args []string, toComplete string) ([]string, cobra.ShellCompDirective) {
	if len(args) > 0 {
		return nil, cobra.ShellCompDirectiveNoFileComp
	}
	return a.completeRemotes(cmd, args, toComplete)
}
