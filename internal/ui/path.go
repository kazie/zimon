package ui

import (
	"os"
	"path/filepath"
	"strings"
)

// Tilde shortens paths under the home directory to ~/..., for display only.
func Tilde(path string) string {
	home, err := os.UserHomeDir()
	if err != nil || home == "" || home == "/" {
		return path
	}
	if path == home {
		return "~"
	}
	if rest, ok := strings.CutPrefix(path, home+string(filepath.Separator)); ok {
		return "~" + string(filepath.Separator) + rest
	}
	return path
}
