package library

import (
	"bytes"
	"os"
	"path/filepath"
	"testing"
)

// The clone itself, where the temp dir's filesystem can clone (btrfs, XFS).
func TestCloneOf(t *testing.T) {
	dir := t.TempDir()
	src, dst := filepath.Join(dir, "a.zim"), filepath.Join(dir, "b.zim")
	if err := os.WriteFile(src, []byte("zim bytes"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := cloneOf(src, dst); err != nil {
		if _, serr := os.Stat(dst); !os.IsNotExist(serr) {
			t.Errorf("a failed clone left %s", dst)
		}
		t.Skipf("no clones here: %v", err)
	}
	if err := os.WriteFile(src, []byte("changed"), 0o644); err != nil {
		t.Fatal(err)
	}
	if got, _ := os.ReadFile(dst); !bytes.Equal(got, []byte("zim bytes")) {
		t.Errorf("the clone changed with its source: %q", got)
	}
	if err := cloneOf(src, dst); err == nil {
		t.Error("cloned over an existing file")
	}
}
