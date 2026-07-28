package main

import (
	"os"

	"github.com/pietervanleuven/rehost/internal/cli"
)

// Set at build time via goreleaser ldflags.
var (
	version = "dev"
	commit  = "none"
	date    = "unknown"
)

func main() {
	if err := cli.Execute(cli.BuildInfo{Version: version, Commit: commit, Date: date}); err != nil {
		os.Exit(1)
	}
}
