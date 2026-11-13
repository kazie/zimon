// Command zimon is a friendly manager for Kiwix ZIM files.
package main

import (
	"os"

	"github.com/kazie/zimon/internal/cli"
)

// version is set by release builds with -ldflags "-X main.version=...".
// Other builds (go install, go build) report the version Go stamps into the
// binary instead; see cli.resolveVersion.
var version = "dev"

func main() {
	os.Exit(cli.Execute(version))
}
