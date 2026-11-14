package store

import (
	"context"
	"path/filepath"
	"testing"
	"time"
)

func TestSessions(t *testing.T) {
	ctx := context.Background()
	path := filepath.Join(t.TempDir(), "zimon.db")
	s := openAt(t, path)
	if err := s.AddToken(ctx, "laptop", "aaa"); err != nil {
		t.Fatal(err)
	}
	soon := time.Now().Add(time.Hour).Unix()
	if err := s.Set(ctx, sessionKey+"old", `{"name":"laptop","token":"aaa","expires":1}`); err != nil {
		t.Fatal(err)
	}
	if err := s.Set(ctx, "other", "kept"); err != nil {
		t.Fatal(err)
	}
	for id, ss := range map[string]Session{
		"live":  {Name: "laptop", TokenHash: "aaa", Expires: soon},
		"local": {Name: "local", Expires: soon},
	} {
		if err := s.PutSession(ctx, id, ss); err != nil {
			t.Fatal(err)
		}
	}
	// A new session takes the expired ones away, and nothing else.
	if v, _ := s.Get(ctx, sessionKey+"old"); v != "" {
		t.Errorf("an expired session is still kept: %s", v)
	}
	if v, _ := s.Get(ctx, "other"); v != "kept" {
		t.Errorf("another key went: %q", v)
	}
	s.Close()

	// Sessions outlive the process: a restart opens the same database.
	s = openAt(t, path)
	if got, err := s.Session(ctx, "live"); err != nil || got == nil || *got != (Session{Name: "laptop", TokenHash: "aaa", Expires: soon}) {
		t.Errorf("live: %+v, %v", got, err)
	}
	if got, err := s.Session(ctx, "nope"); err != nil || got != nil {
		t.Errorf("nope: %+v, %v; want none", got, err)
	}
	if err := s.DeleteSession(ctx, "local"); err != nil {
		t.Fatal(err)
	}
	if got, _ := s.Session(ctx, "local"); got != nil {
		t.Errorf("a deleted session is still there: %+v", got)
	}
	// Removing the token ends its sessions.
	if _, err := s.RemoveToken(ctx, "laptop"); err != nil {
		t.Fatal(err)
	}
	if got, err := s.Session(ctx, "live"); err != nil || got != nil {
		t.Errorf("after zimon token remove: %+v, %v", got, err)
	}
}
