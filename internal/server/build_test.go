package server

import (
	"net/http"
	"strings"
	"testing"
)

func TestBuildInfo(t *testing.T) {
	tests := []struct {
		version, commit string
		want            buildInfo
	}{
		{"0.1.1", "cfe3562", buildInfo{Version: "0.1.1", VersionURL: project + "/releases/tag/v0.1.1",
			Commit: "cfe3562", CommitURL: project + "/commit/cfe3562"}},
		{"v0.1.1", "", buildInfo{Version: "v0.1.1", VersionURL: project + "/releases/tag/v0.1.1"}}, // go install
		{"0.2.0-rc1", "abc1234", buildInfo{Version: "0.2.0-rc1", VersionURL: project + "/releases/tag/v0.2.0-rc1",
			Commit: "abc1234", CommitURL: project + "/commit/abc1234"}},
		{"v0.1.1+dirty", "cfe3562", buildInfo{Version: "v0.1.1+dirty", Commit: "cfe3562", CommitURL: project + "/commit/cfe3562"}},
		{"v0.1.2-0.20261114125422-cfe3562a0f55", "cfe3562", buildInfo{Version: "v0.1.2-0.20261114125422-cfe3562a0f55",
			VersionURL: project + "/commit/cfe3562"}}, // Go's pseudo-version: no release page
		{"0.0.0-SNAPSHOT-119e5e6", "119e5e6", buildInfo{Version: "0.0.0-SNAPSHOT-119e5e6",
			VersionURL: project + "/commit/119e5e6"}}, // goreleaser --snapshot
		{"abc1234", "abc1234", buildInfo{Version: "abc1234", VersionURL: project + "/commit/abc1234"}}, // Nix
		{"", "", buildInfo{Version: "dev"}},
	}
	for _, tt := range tests {
		s := &Server{Version: tt.version, Commit: tt.commit}
		if got := s.buildInfo(); got != tt.want {
			t.Errorf("%q %q:\n got %+v\nwant %+v", tt.version, tt.commit, got, tt.want)
		}
	}
}

// Both pages say which zimon serves them.
func TestPagesShowTheBuild(t *testing.T) {
	s, h, token := newAdmin(t)
	s.Version, s.Commit = "0.1.1", "cfe3562"
	want := `zimon <a href="` + project + `/releases/tag/v0.1.1">0.1.1</a> (<a href="` + project + `/commit/cfe3562">cfe3562</a>)`
	if body := get(t, s.Handler(), http.MethodGet, "/").Body.String(); !strings.Contains(body, want) {
		t.Errorf("the start page doesn't show the build:\n%s", body)
	}
	signedIn := login(h, token, "192.0.2.1:1234")
	if body := withCookie(h, http.MethodGet, "/admin", signedIn).Body.String(); !strings.Contains(body, want) || !strings.Contains(body, "admin.js") {
		t.Errorf("the admin page doesn't show the build:\n%s", body)
	}
}
