//go:build unix

package lock

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"golang.org/x/sys/unix"
)

// Holder returns the pid of the zimon that holds the lock at path, 0 when
// none does. It only reads, and so never gets in the way of a zimon taking
// the lock: a held lock's file has its holder's pid (Acquire writes it and
// empties it on unlock), and that process is alive. (One that crashed lost
// the lock with it; its pid, should another process get it, is a rare
// false "held".)
func Holder(path string) (int, error) {
	pid, err := pidIn(path)
	if err != nil || pid == 0 {
		return 0, err
	}
	if err := unix.Kill(pid, 0); err != nil && !errors.Is(err, unix.EPERM) {
		return 0, nil
	}
	return pid, nil
}

// pidIn is the pid written in the lock file at path, 0 when there is none.
func pidIn(path string) (int, error) {
	b, err := os.ReadFile(path)
	if errors.Is(err, os.ErrNotExist) {
		return 0, nil
	}
	if err != nil {
		return 0, err
	}
	pid, err := strconv.Atoi(strings.TrimSpace(string(b)))
	if err != nil || pid <= 0 {
		return 0, nil
	}
	return pid, nil
}

// Acquire takes the lock at path. If another zimon holds it, Acquire calls
// onWait once with who that is, and waits for it until ctx ends.
func Acquire(ctx context.Context, path string, onWait func(who string)) (unlock func(), err error) {
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return nil, err
	}
	f, err := os.OpenFile(path, os.O_RDWR|os.O_CREATE, 0o644)
	if err != nil {
		return nil, err
	}
	fd := int(f.Fd())
	if err := unix.Flock(fd, unix.LOCK_EX|unix.LOCK_NB); err != nil {
		if !errors.Is(err, unix.EWOULDBLOCK) {
			f.Close()
			return nil, err
		}
		// The lock is held, so the pid in the file is its holder's, even
		// one in another pid namespace (a container) that Holder can't see.
		who := "another zimon"
		if pid, _ := pidIn(path); pid != 0 {
			who += " (pid " + strconv.Itoa(pid) + ")"
		}
		onWait(who)
		for {
			if err := unix.Flock(fd, unix.LOCK_EX|unix.LOCK_NB); err == nil {
				break
			}
			select {
			case <-ctx.Done():
				f.Close()
				return nil, ctx.Err()
			case <-time.After(500 * time.Millisecond):
			}
		}
	}
	_ = f.Truncate(0)
	_, _ = f.WriteAt([]byte(strconv.Itoa(os.Getpid())+"\n"), 0)
	return func() {
		_ = f.Truncate(0)
		_ = unix.Flock(fd, unix.LOCK_UN)
		f.Close()
	}, nil
}
