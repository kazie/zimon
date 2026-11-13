package main

import (
	"os"
	"strings"
	"testing"

	"github.com/kazie/zimon/internal/kiwix"
)

func TestContainerVersion(t *testing.T) {
	tests := []struct{ from, want string }{
		{"FROM ghcr.io/kiwix/kiwix-serve:3.8.2\n", "3.8.2"},
		{"FROM golang AS build\nFROM ghcr.io/kiwix/kiwix-serve:3.7.0-2 AS run\n", "3.7.0-2"},
		{"FROM ghcr.io/kiwix/kiwix-serve:3.8.2@sha256:abc\n", "3.8.2"},
		{"FROM --platform=$TARGETPLATFORM ghcr.io/kiwix/kiwix-serve:3.8.2\n", "3.8.2"},
		{"FROM ghcr.io/kiwix/kiwix-serve:latest\n", ""},
		{"FROM alpine\n", ""},
	}
	for _, tt := range tests {
		got, err := containerVersion([]byte(tt.from))
		if got != tt.want || (err == nil) != (tt.want != "") {
			t.Errorf("%q: got %q, %v; want %q", tt.from, got, err, tt.want)
		}
	}
}

// The bare binary fetches the kiwix-serve the container image has, for every
// platform zimon is released for.
func TestPinsFollowContainerfile(t *testing.T) {
	data, err := os.ReadFile("../../Containerfile")
	if err != nil {
		t.Fatal(err)
	}
	v, err := containerVersion(data)
	if err != nil {
		t.Fatal(err)
	}
	if v != kiwix.Version {
		t.Fatalf("the Containerfile has kiwix-serve %s, internal/kiwix/pins.go %s: run just pin-kiwix", v, kiwix.Version)
	}
	for platform, name := range platforms {
		p, ok := kiwix.Pins[platform]
		if !ok || p.Archive != "kiwix-tools_"+name+"-"+kiwix.Version+".tar.gz" || len(p.SHA256) != 64 || p.Size <= 0 ||
			strings.Trim(p.SHA256, "0123456789abcdef") != "" {
			t.Errorf("%s: pin %+v doesn't fit; run just pin-kiwix", platform, p)
		}
	}
}
