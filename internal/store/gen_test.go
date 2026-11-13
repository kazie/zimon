package store

import (
	"os"
	"path/filepath"
	"regexp"
	"testing"
)

// sqlc's SQLite parser can leave a parameter unreplaced (it did after an
// ESCAPE), which then reaches SQLite as a column name: none may be left.
func TestGeneratedQueriesHaveNoNamedParameters(t *testing.T) {
	files, err := filepath.Glob("gen/*.go")
	if err != nil || len(files) == 0 {
		t.Fatalf("no generated code: %v", err)
	}
	named := regexp.MustCompile(`@[a-z_]+`)
	for _, f := range files {
		b, err := os.ReadFile(f)
		if err != nil {
			t.Fatal(err)
		}
		if m := named.Find(b); m != nil {
			t.Errorf("%s still has %s: rewrite the query in queries/ (see sqlc.yaml)", f, m)
		}
	}
}
