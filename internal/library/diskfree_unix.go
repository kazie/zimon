//go:build unix

package library

import "golang.org/x/sys/unix"

// freeBytes reports the space available to unprivileged users on dir's filesystem.
func freeBytes(dir string) (int64, bool) {
	var st unix.Statfs_t
	if err := unix.Statfs(dir, &st); err != nil {
		return 0, false
	}
	return int64(st.Bavail) * int64(st.Bsize), true //nolint:unconvert // Bsize is uint32 on darwin
}
