package api

import "testing"

func TestLogLine(t *testing.T) {
	for _, c := range []struct {
		ev   Event
		want string
	}{
		{Event{Type: EvProgress, Key: "x", Done: 1}, ""},
		{Event{Type: EvNote, Line: "mirror 1 failed"}, "mirror 1 failed"},
		{Event{Type: EvSaved, State: Saved}, "Saved library.toml"},
		{Event{Type: EvSaved, State: SavedUnchanged}, "library.toml already says so"},
		{Event{Type: EvPruned, Reason: "old version", Line: "/lib/x_2026-09.zim"}, "Removed old version /lib/x_2026-09.zim"},
		{Event{Type: EvVerified, Key: "x"}, ""},
		{Event{Type: EvVerified, Key: "x", Error: "sha-256 mismatch"}, "x: sha-256 mismatch"},
		{Event{Type: EvCatalog, State: CatalogRefreshing}, ""},
		{Event{Type: EvCatalog, State: CatalogChanged, Total: 12}, "Catalog refreshed: 12 entries"},
		{Event{Type: EvStep, Step: &StepResult{Kind: StepAdd, Key: "x", Error: "no mirror"}}, "x: no mirror"},
		{Event{Type: EvStep, Step: &StepResult{Kind: StepRemove, Key: "x", Removed: []Removed{{Size: 2_000_000}}}}, "Removed x, freeing 2.00 MB"},
		{Event{Type: EvStep, Step: &StepResult{Kind: StepRemove, Key: "x", KeptFiles: true}}, "Forgot x; its files stay on disk"},
		{Event{Type: EvStep, Step: &StepResult{Kind: StepUpdate, Key: "x", Path: "/lib/x_2026-11.zim", Size: 2_000_000,
			OldVersions: []Removed{{Path: "x_2026-10.zim", Size: 1_000_000}}}}, "x: x_2026-11.zim (2.00 MB), freeing 1.00 MB"},
	} {
		if got := c.ev.LogLine(); got != c.want {
			t.Errorf("%+v: LogLine() = %q, want %q", c.ev, got, c.want)
		}
	}
}
