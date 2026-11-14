package buildinfo

import "testing"

func TestLine(t *testing.T) {
	long := "cfe3562a0f55364a2fde2627a8d40cc0a4b56f1d"
	for _, tt := range []struct{ version, rev, wantRev, wantLine string }{
		{"0.1.1", long, "cfe3562", "0.1.1 (cfe3562)"},                                                     // a release
		{"v0.1.1+dirty", long, "cfe3562", "v0.1.1+dirty (cfe3562)"},                                       // go build, changes not in git
		{"v0.1.2-0.20261114125422-cfe3562a0f55", long, "cfe3562", "v0.1.2-0.20261114125422-cfe3562a0f55"}, // Go's pseudo-version
		{"cfe3562", "cfe3562", "cfe3562", "cfe3562"},                                                      // Nix: named after its revision
	} {
		version, rev := Resolve(tt.version, tt.rev)
		if line := Line(version, rev); rev != tt.wantRev || line != tt.wantLine {
			t.Errorf("%q %q: got %q, %q; want %q, %q", tt.version, tt.rev, rev, line, tt.wantRev, tt.wantLine)
		}
	}
}
