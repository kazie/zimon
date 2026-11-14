// Command zimon is a friendly manager for Kiwix ZIM files.
package main

import (
	"os"

	"github.com/kazie/zimon/internal/cli"
)

// version and commit are set by release builds with -ldflags
// "-X main.version=... -X main.commit=...". Other builds (go install, go
// build) report what Go stamps into the binary instead; see cli.Execute.
var (
	version = "dev"
	commit  string
)

func main() {
	os.Exit(cli.Execute(version, commit))
}
