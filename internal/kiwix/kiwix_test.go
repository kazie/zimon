package kiwix

import (
	"slices"
	"testing"
)

func TestArgs(t *testing.T) {
	got := Args(Options{Library: "/srv/zim/library.xml", Address: "all", Port: 8080})
	want := []string{"--library", "--monitorLibrary", "--nodatealiases", "--skipInvalid",
		"--port=8080", "--address=all", "/srv/zim/library.xml"}
	if !slices.Equal(got, want) {
		t.Errorf("got  %q\nwant %q", got, want)
	}
	got = Args(Options{Library: "l.xml", Address: "127.0.0.1", Port: 9000, AttachPID: 42})
	want = []string{"--library", "--monitorLibrary", "--nodatealiases", "--skipInvalid",
		"--port=9000", "--address=127.0.0.1", "--attachToProcess=42", "l.xml"}
	if !slices.Equal(got, want) {
		t.Errorf("attached: got  %q\nwant %q", got, want)
	}
	// No address: leave it to kiwix-serve.
	for _, a := range Args(Options{Library: "library.xml", Port: 8080}) {
		if a == "--address=" {
			t.Error("empty --address passed to kiwix-serve")
		}
	}
}
