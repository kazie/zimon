package cli

import (
	"strings"
	"testing"
)

func TestNeedsSetup(t *testing.T) {
	root := newRootCmd(&app{})
	root.InitDefaultCompletionCmd()
	for args, want := range map[string]bool{
		"add":             true,
		"search":          true,
		"config":          false,
		"config path":     false,
		"config edit":     false,
		"init":            false,
		"completion fish": false,
	} {
		cmd, _, err := root.Find(splitArgs(args))
		if err != nil {
			t.Fatalf("%s: %v", args, err)
		}
		if got := needsSetup(cmd); got != want {
			t.Errorf("needsSetup(%q) = %v, want %v", args, got, want)
		}
	}
	if needsSetup(root) {
		t.Error("bare zimon should not start the wizard")
	}
}

func splitArgs(s string) []string { return strings.Fields(s) }
