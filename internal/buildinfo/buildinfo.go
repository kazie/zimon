// Package buildinfo says which zimon this is: its version and commit, as the
// build passed them in or Go stamped them into the binary.
package buildinfo

import (
	"runtime/debug"
	"strings"
)

// Resolve completes what a build passed in with -ldflags (version and commit
// may be "dev" and ""): a binary built by go install or go build in a
// checkout carries its module version and commit. The commit comes back
// short (7 characters), or "" when unknown. Go marks uncommitted changes in
// the version itself: v0.1.1+dirty.
func Resolve(version, commit string) (string, string) {
	bi, ok := debug.ReadBuildInfo()
	if (version == "" || version == "dev") && ok && bi.Main.Version != "" && bi.Main.Version != "(devel)" {
		version = bi.Main.Version
	}
	if commit == "" && ok {
		for _, s := range bi.Settings {
			if s.Key == "vcs.revision" {
				commit = s.Value
			}
		}
	}
	if version == "" {
		version = "dev"
	}
	return version, commit[:min(len(commit), 7)]
}

// NamesCommit reports whether version names the commit already: a Nix
// build's version is its commit, and Go's pseudo-versions end in it.
func NamesCommit(version, commit string) bool {
	return commit != "" && strings.Contains(version, commit)
}

// Line is a version with its commit, as zimon --version shows it.
func Line(version, commit string) string {
	if commit == "" || NamesCommit(version, commit) {
		return version
	}
	return version + " (" + commit + ")"
}
