package server

import (
	"cmp"
	"regexp"
	"strings"

	"github.com/kazie/zimon/internal/buildinfo"
)

// project is where zimon's releases and commits are.
const project = "https://github.com/kazie/zimon"

var (
	// A release's version as goreleaser sets it: 0.1.1, or 0.2.0-rc1. Not a
	// snapshot (0.0.0-SNAPSHOT-…) or one of Go's pseudo-versions
	// (0.1.2-0.20261114125422-cfe3562a0f55), which have no release page.
	releaseVersion = regexp.MustCompile(`^\d+\.\d+\.\d+(-(alpha|beta|rc)\.?\d*)?$`)
	commitHash     = regexp.MustCompile(`^[0-9a-f]{7,40}$`)
)

// buildInfo is which zimon serves a page, for its footer, with links to the
// release notes and the commit when they are on GitHub.
type buildInfo struct {
	Version, VersionURL string
	Commit, CommitURL   string
}

func (s *Server) buildInfo() buildInfo {
	b := buildInfo{Version: cmp.Or(s.Version, "dev"), Commit: s.Commit}
	// Release builds say 0.1.1, go install says v0.1.1; the tag is v0.1.1.
	if tag := strings.TrimPrefix(b.Version, "v"); releaseVersion.MatchString(tag) {
		b.VersionURL = project + "/releases/tag/v" + tag
	}
	if commitHash.MatchString(b.Commit) {
		b.CommitURL = project + "/commit/" + b.Commit
	}
	// A version that names the commit already (a Nix build's, or one of Go's
	// pseudo-versions) shows it once, linked to the commit.
	if buildinfo.NamesCommit(b.Version, b.Commit) {
		b.VersionURL = cmp.Or(b.VersionURL, b.CommitURL)
		b.Commit, b.CommitURL = "", ""
	}
	return b
}
