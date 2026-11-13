//go:build unix

package lock

import (
	"context"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"
)

func TestLock(t *testing.T) {
	path := filepath.Join(t.TempDir(), "state", "lock")
	unlock, err := Acquire(context.Background(), path, func(string) { t.Error("waited for a free lock") })
	if err != nil {
		t.Fatal(err)
	}
	if b, _ := os.ReadFile(path); strings.TrimSpace(string(b)) != strconv.Itoa(os.Getpid()) {
		t.Errorf("lock file says %q", b)
	}
	if _, ok := Try(path); ok {
		t.Error("Try took a held lock")
	}

	// A second taker waits, saying who holds it, until the lock is free.
	got := make(chan error, 1)
	var who string
	go func() {
		u, err := Acquire(context.Background(), path, func(w string) { who = w })
		if err == nil {
			u()
		}
		got <- err
	}()
	time.Sleep(100 * time.Millisecond)
	unlock()
	if err := <-got; err != nil || !strings.Contains(who, "pid") {
		t.Errorf("waiter: %v, who %q", err, who)
	}

	u, ok := Try(path)
	if !ok {
		t.Fatal("Try failed on a free lock")
	}
	u()

	// Giving up while waiting.
	unlock, _ = Acquire(context.Background(), path, nil)
	defer unlock()
	ctx, cancel := context.WithTimeout(context.Background(), 50*time.Millisecond)
	defer cancel()
	if _, err := Acquire(ctx, path, func(string) {}); err == nil {
		t.Error("Acquire did not give up")
	}
}

func TestHolder(t *testing.T) {
	path := filepath.Join(t.TempDir(), "lock")
	if pid, err := Holder(path); pid != 0 || err != nil {
		t.Errorf("no lock file: %d, %v", pid, err)
	}
	unlock, err := Acquire(context.Background(), path, func(string) {})
	if err != nil {
		t.Fatal(err)
	}
	if pid, err := Holder(path); pid != os.Getpid() || err != nil {
		t.Errorf("held: %d, %v", pid, err)
	}
	unlock()
	if pid, err := Holder(path); pid != 0 || err != nil {
		t.Errorf("free again: %d, %v", pid, err)
	}
	// A pid left by a zimon that crashed, now gone: not held.
	if err := os.WriteFile(path, []byte("999999999\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if pid, err := Holder(path); pid != 0 || err != nil {
		t.Errorf("a dead holder: %d, %v", pid, err)
	}
}
