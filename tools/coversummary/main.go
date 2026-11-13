// Command coversummary prints the coverage of each package from a Go cover
// profile, such as the one `just cover` and CI write with
//
//	go test -coverpkg=./internal/... -coverprofile=cover.out ./...
//	go run ./tools/coversummary cover.out
//
// With -coverpkg a block shows up once per test binary; it counts as covered
// when any of them ran it.
package main

import (
	"bufio"
	"fmt"
	"io"
	"maps"
	"os"
	"path"
	"runtime/debug"
	"slices"
	"strconv"
	"strings"
)

func main() {
	if len(os.Args) != 2 {
		fmt.Fprintln(os.Stderr, "usage: coversummary cover.out")
		os.Exit(2)
	}
	if err := run(os.Args[1], os.Stdout); err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
}

func run(name string, w io.Writer) error {
	f, err := os.Open(name)
	if err != nil {
		return err
	}
	defer f.Close()
	pkgs, err := summarize(f)
	if err != nil {
		return fmt.Errorf("%s: %w", name, err)
	}
	prefix := ""
	if bi, ok := debug.ReadBuildInfo(); ok && bi.Main.Path != "" {
		prefix = bi.Main.Path + "/"
	}
	write(w, pkgs, prefix)
	return nil
}

// count is a package's statements, and how many of them ran.
type count struct{ stmts, covered int }

// summarize reads a cover profile and counts statements per package.
func summarize(r io.Reader) (map[string]count, error) {
	type block struct {
		pkg   string
		stmts int
		ran   bool // by any test binary
	}
	blocks := map[string]block{}
	sc := bufio.NewScanner(r)
	for n := 1; sc.Scan(); n++ {
		line := sc.Text()
		if (n == 1 && strings.HasPrefix(line, "mode:")) || line == "" {
			continue
		}
		// file.go:12.34,15.2 3 1
		loc, rest, ok1 := strings.Cut(line, " ")
		stmtsText, hitsText, ok2 := strings.Cut(rest, " ")
		file, _, ok3 := strings.Cut(loc, ":")
		stmts, err1 := strconv.Atoi(stmtsText)
		hits, err2 := strconv.Atoi(hitsText)
		if !ok1 || !ok2 || !ok3 || err1 != nil || err2 != nil {
			return nil, fmt.Errorf("line %d: not a cover profile line: %q", n, line)
		}
		blocks[loc] = block{pkg: path.Dir(file), stmts: stmts, ran: blocks[loc].ran || hits > 0}
	}
	if err := sc.Err(); err != nil {
		return nil, err
	}
	pkgs := map[string]count{}
	for _, b := range blocks {
		c := pkgs[b.pkg]
		c.stmts += b.stmts
		if b.ran {
			c.covered += b.stmts
		}
		pkgs[b.pkg] = c
	}
	return pkgs, nil
}

// write prints one line per package, sorted, and the total, with prefix
// (the module path) left off the package names.
func write(w io.Writer, pkgs map[string]count, prefix string) {
	var total count
	width := len("total")
	for p, c := range pkgs {
		total.stmts += c.stmts
		total.covered += c.covered
		width = max(width, len(strings.TrimPrefix(p, prefix)))
	}
	line := func(name string, c count) {
		pc := 0.0
		if c.stmts > 0 {
			pc = 100 * float64(c.covered) / float64(c.stmts)
		}
		fmt.Fprintf(w, "%-*s %5.1f%%\n", width, name, pc)
	}
	for _, p := range slices.Sorted(maps.Keys(pkgs)) {
		line(strings.TrimPrefix(p, prefix), pkgs[p])
	}
	line("total", total)
}
