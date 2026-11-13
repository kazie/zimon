package cli

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"flag"
	"fmt"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/rogpeppe/go-internal/testscript"

	"github.com/kazie/zimon/internal/api"
	"github.com/kazie/zimon/internal/collections"
	"github.com/kazie/zimon/internal/config"
	"github.com/kazie/zimon/internal/fakekiwix"
	"github.com/kazie/zimon/internal/kiwix"
	"github.com/kazie/zimon/internal/library"
	"github.com/kazie/zimon/internal/server"
	"github.com/kazie/zimon/internal/store"
)

var updateScripts = flag.Bool("update", false, "rewrite the expected output in testdata/script from the actual output")

// TestMain lets the scripts run the real zimon as a separate process.
func TestMain(m *testing.M) {
	testscript.Main(m, map[string]func(){
		"zimon": func() {
			// Retrying a mirror right away keeps the broken-mirror scripts fast.
			// kiwix-serve, when zimon serve has to fetch it, is the fake's.
			os.Exit(run(&app{version: "test", backoff: func(int) time.Duration { return 0 }, kiwix: fakeKiwixTools()}))
		},
	})
}

// fakeKiwixTools fetches the fake's kiwix-tools archive, pinned for this
// system, from the script's fakekiwix.
func fakeKiwixTools() kiwix.Fetcher {
	archive := fakekiwix.KiwixTools()
	sum := sha256.Sum256(archive)
	return kiwix.Fetcher{
		BaseURL: os.Getenv("FAKE_URL") + "/release/kiwix-tools/",
		Pins: map[string]kiwix.Pin{runtime.GOOS + "/" + runtime.GOARCH: {
			Archive: fakekiwix.KiwixToolsArchive, Size: int64(len(archive)), SHA256: hex.EncodeToString(sum[:])}},
	}
}

// TestScripts runs the end-to-end scenarios in testdata/script. Each script
// gets its own fake Kiwix (DemoBooks, no throttle) and a home directory in
// $WORK with a config that points at it. $LIB is the library directory, $TOML
// library.toml and $DB the database. Commands:
//
//	fake bump NAME[:FLAVOUR]   publish a newer release
//	fake remove NAME[:FLAVOUR] drop it from the catalog
//	fake break-mirror N|all    mirror N corrupts a byte (1-based; 0 is the canonical URL)
//	fake downloads N           the fake has served exactly N file downloads so far
//	fake kiwix-tools broken|whole  its kiwix-tools archive arrives with a byte wrong, or whole
//	server start               run zimon serve in this process (see cmdServer)
//	server refresh             queue a catalog refresh job there, and wait for it
//
// $ZIMONBIN is the folder with zimon in it: PATH=$ZIMONBIN hides any
// kiwix-serve installed here. $KIWIX_VERSION is the one zimon serve fetches.
//
// Run one with: go test ./internal/cli -run 'TestScripts/^add$'
// Add -update to rewrite the files that cmp compares against.
func TestScripts(t *testing.T) {
	testscript.Run(t, testscript.Params{
		Dir:                 "testdata/script",
		Setup:               setupScript,
		Cmds:                map[string]func(*testscript.TestScript, bool, []string){"fake": cmdFake, "server": cmdServer},
		RequireExplicitExec: true,
		RequireUniqueNames:  true,
		UpdateScripts:       *updateScripts,
	})
}

type (
	fakeKey   struct{}
	serverKey struct{}
)

func setupScript(env *testscript.Env) error {
	fk := fakekiwix.New(fakekiwix.DemoBooks())
	srv := httptest.NewServer(fk)
	env.Defer(srv.Close)
	env.Values[fakeKey{}] = fk
	env.Values[serverKey{}] = &scriptServer{}

	home := filepath.Join(env.WorkDir, "home")
	cfgDir, dataDir := filepath.Join(home, ".config"), filepath.Join(home, ".local", "share")
	cfgPath := filepath.Join(cfgDir, "zimon", "config.toml")
	for k, v := range map[string]string{
		"HOME":            home,
		"XDG_CONFIG_HOME": cfgDir,
		"XDG_DATA_HOME":   dataDir,
		"ZIMON_CONFIG":    cfgPath,
		"LIB":             filepath.Join(dataDir, "zimon", "library"),
		"TOML":            filepath.Join(cfgDir, "zimon", "library.toml"),
		"DB":              filepath.Join(dataDir, "zimon", "zimon.db"),
		// Plain, stable output. Under testscript stdout is never a terminal,
		// so tables are not cut to a width either.
		"NO_COLOR": "1",
		// Anything that tries a real host (the live Kiwix catalog, say) fails
		// at once. Loopback, where the fake listens, is never proxied.
		"HTTP_PROXY":  "http://127.0.0.1:9",
		"HTTPS_PROXY": "http://127.0.0.1:9",
		// Under -race every process sleeps a second at exit; the scripts run
		// zimon a few hundred times. Options already set still win.
		"GORACE": strings.TrimSpace("atexit_sleep_ms=0 " + env.Getenv("GORACE")),
	} {
		env.Setenv(k, v)
	}
	if err := os.MkdirAll(filepath.Dir(cfgPath), 0o755); err != nil {
		return err
	}
	// The remote scripts bring a stand-in ssh at $WORK/bin/ssh.
	env.Setenv("CATALOG_URL", srv.URL+"/catalog/v2")
	env.Setenv("FAKE_URL", srv.URL)
	env.Setenv("KIWIX_VERSION", kiwix.Version)
	if zimon, err := exec.LookPath("zimon"); err == nil { // testscript puts it first on PATH
		env.Setenv("ZIMONBIN", filepath.Dir(zimon))
	}
	cfg := fmt.Sprintf("catalog_url = %q\n\n[remote]\nssh_command = %q\n", srv.URL+"/catalog/v2", filepath.Join(env.WorkDir, "bin", "ssh"))
	return os.WriteFile(cfgPath, []byte(cfg), 0o644)
}

func cmdFake(ts *testscript.TestScript, neg bool, args []string) {
	if neg {
		ts.Fatalf("fake does not support !")
	}
	usage := "usage: fake bump|remove NAME[:FLAVOUR], fake break-mirror N|all, fake downloads N, fake kiwix-tools broken|whole, " +
		"fake own FILE NAME N (serve release N of a ZIM named NAME at $FAKE_URL/own/FILE), fake zim PATH NAME N (write one)"
	if len(args) < 2 {
		ts.Fatalf("%s", usage)
	}
	fk := ts.Value(fakeKey{}).(*fakekiwix.Server)
	if cmd := args[0]; cmd == "own" || cmd == "zim" {
		if len(args) != 4 {
			ts.Fatalf("%s", usage)
		}
		n, err := strconv.Atoi(args[3])
		ts.Check(err)
		body := fakekiwix.OwnZIM(args[2], n)
		if cmd == "own" {
			fk.PutOwn(args[1], body, fakekiwix.OwnDate(n))
		} else {
			ts.Check(os.WriteFile(ts.MkAbs(args[1]), body, 0o644))
		}
		return
	}
	if len(args) != 2 {
		ts.Fatalf("%s", usage)
	}
	switch cmd, arg := args[0], args[1]; cmd {
	case "bump":
		b, ok := fk.Bump(arg)
		if !ok {
			ts.Fatalf("fake: no book %s", arg)
		}
		ts.Logf("fake: %s is now %s", arg, b.FileName())
	case "remove":
		if _, ok := fk.Book(arg); !ok {
			ts.Fatalf("fake: no book %s", arg)
		}
		fk.Remove(arg)
	case "break-mirror":
		if arg == "all" {
			fk.BreakAll()
			return
		}
		n, err := strconv.Atoi(arg)
		ts.Check(err)
		fk.BreakMirror(n)
	case "kiwix-tools":
		if arg != "broken" && arg != "whole" {
			ts.Fatalf("%s", usage)
		}
		fk.BreakKiwixTools(arg == "broken")
	case "downloads":
		want, err := strconv.Atoi(arg)
		ts.Check(err)
		got := 0
		for what, n := range fk.Hits() {
			if what != "feed" { // catalog fetches are not downloads
				got += n
			}
		}
		if got != want {
			ts.Fatalf("fake served %d downloads, want %d", got, want)
		}
	default:
		ts.Fatalf("fake: unknown command %q", cmd)
	}
}

// scriptServer is the zimon serve a script started, if any.
type scriptServer struct {
	url, token string
}

// cmdServer runs zimon serve (without kiwix-serve) in the test process,
// with a home of its own in $WORK/server, as the https remote scenarios'
// server. "server start" sets $SERVER_CONFIG (run zimon there with
// --config $SERVER_CONFIG), $SERVER_URL, and $TOKEN, an API token that
// $WORK/server/token holds too. "server refresh" queues a catalog refresh
// job there through the API and waits for it.
func cmdServer(ts *testscript.TestScript, neg bool, args []string) {
	if neg || len(args) != 1 {
		ts.Fatalf("usage: server start|refresh")
	}
	srv := ts.Value(serverKey{}).(*scriptServer)
	switch args[0] {
	case "start":
		srv.url, srv.token = startServer(ts)
	case "refresh":
		if srv.url == "" {
			ts.Fatalf("server: start it first")
		}
		ts.Check(refreshJob(srv.url, srv.token))
	default:
		ts.Fatalf("server: unknown command %q", args[0])
	}
}

func startServer(ts *testscript.TestScript) (url, token string) {
	dir := ts.MkAbs("server")
	cfgPath := filepath.Join(dir, "config.toml")
	cfg := fmt.Sprintf("catalog_url = %q\nlibrary_dir = %q\nstate_dir = %q\nlibrary_file = %q\nlanguages = [\"eng\"]\n",
		ts.Getenv("CATALOG_URL"), filepath.Join(dir, "library"), filepath.Join(dir, "state"), filepath.Join(dir, "library.toml"))
	ts.Check(os.MkdirAll(dir, 0o755))
	ts.Check(os.WriteFile(cfgPath, []byte(cfg), 0o644))
	c, _, err := config.Load(cfgPath)
	ts.Check(err)
	st, err := store.Open(c.DBPath())
	ts.Check(err)
	cols, err := collections.Load(filepath.Join(dir, "collections.toml"))
	ts.Check(err)
	s := server.New(&library.Manager{Cfg: c, Store: st, HTTP: http.DefaultClient, SetupPath: c.LibraryFile, Collections: cols}, "")
	s.Version = "test"
	token, hash := server.NewToken()
	ts.Check(st.AddToken(context.Background(), "script", hash))
	ts.Check(os.WriteFile(filepath.Join(dir, "token"), []byte(token+"\n"), 0o600))

	ln, err := net.Listen("tcp", "127.0.0.1:0")
	ts.Check(err)
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() { done <- s.Run(ctx, ln, nil) }()
	ts.Defer(func() {
		cancel()
		if err := <-done; err != nil {
			ts.Logf("server: %v", err)
		}
		st.Close()
	})
	url = "http://" + ln.Addr().String()
	ts.Setenv("SERVER_CONFIG", cfgPath)
	ts.Setenv("SERVER_URL", url)
	ts.Setenv("TOKEN", token)
	return url, token
}

// refreshJob asks the server at url for a catalog refresh and waits until
// the job is done.
func refreshJob(url, token string) error {
	call := func(method, path string, out any) error {
		req, err := http.NewRequest(method, url+path, nil)
		if err != nil {
			return err
		}
		req.Header.Set("Authorization", "Bearer "+token)
		resp, err := http.DefaultClient.Do(req)
		if err != nil {
			return err
		}
		defer resp.Body.Close()
		if resp.StatusCode >= 300 {
			return fmt.Errorf("%s %s: %s", method, path, resp.Status)
		}
		return json.NewDecoder(resp.Body).Decode(out)
	}
	var j api.Job
	if err := call(http.MethodPost, "/api/v1/refresh", &j); err != nil {
		return err
	}
	for range 500 {
		if err := call(http.MethodGet, "/api/v1/jobs/"+strconv.FormatInt(j.ID, 10), &j); err != nil {
			return err
		}
		if j.Finished != "" {
			if j.State != store.JobDone {
				return fmt.Errorf("refresh job %s: %s", j.State, j.Error)
			}
			return nil
		}
		time.Sleep(10 * time.Millisecond)
	}
	return fmt.Errorf("refresh job still %s", j.State)
}
