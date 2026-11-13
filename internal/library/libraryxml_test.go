package library

import (
	"context"
	"errors"
	"os"
	"strings"
	"testing"
)

func TestEnsureLibraryXML(t *testing.T) {
	e := newEnv(t)
	ctx := context.Background()
	if err := e.m.EnsureLibraryXML(ctx); err != nil {
		t.Fatal(err)
	}
	data, err := os.ReadFile(e.m.Cfg.LibraryXMLPath())
	if err != nil || !strings.Contains(string(data), "<library") {
		t.Fatalf("library.xml = %q, %v", data, err)
	}

	// An existing library.xml is left alone, even when the database has
	// moved on: whoever changed the library rewrites it.
	if err := os.WriteFile(e.m.Cfg.LibraryXMLPath(), []byte("kept"), 0o644); err != nil {
		t.Fatal(err)
	}
	e.add("devdocs_en_go") // rewrites it
	if data, _ := os.ReadFile(e.m.Cfg.LibraryXMLPath()); !strings.Contains(string(data), "devdocs_en_go") {
		t.Fatalf("install did not rewrite library.xml: %q", data)
	}
	if err := os.WriteFile(e.m.Cfg.LibraryXMLPath(), []byte("kept"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := e.m.EnsureLibraryXML(ctx); err != nil {
		t.Fatal(err)
	}
	if data, _ := os.ReadFile(e.m.Cfg.LibraryXMLPath()); string(data) != "kept" {
		t.Errorf("EnsureLibraryXML overwrote an existing file: %q", data)
	}
}

func TestLibraryXMLMatches(t *testing.T) {
	e := newEnv(t)
	ctx := context.Background()
	if _, err := e.m.LibraryXMLMatches(ctx); !errors.Is(err, os.ErrNotExist) {
		t.Errorf("no library.xml: %v", err)
	}
	e.add("devdocs_en_go")
	if ok, err := e.m.LibraryXMLMatches(ctx); !ok || err != nil {
		t.Errorf("after an install: %v, %v", ok, err)
	}
	if err := os.WriteFile(e.m.Cfg.LibraryXMLPath(), []byte("<library/>"), 0o644); err != nil {
		t.Fatal(err)
	}
	if ok, err := e.m.LibraryXMLMatches(ctx); ok || err != nil {
		t.Errorf("changed by hand: %v, %v", ok, err)
	}
}
