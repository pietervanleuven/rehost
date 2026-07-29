package main

import (
	"context"
	"os"
	"os/signal"
	"syscall"

	"github.com/pietervanleuven/rehost/internal/cli"
)

// Set at build time via goreleaser ldflags.
var (
	version = "dev"
	commit  = "none"
	date    = "unknown"
)

func main() {
	// Ctrl-C/TERM cancel the context every layer already threads, so
	// in-flight SSH sessions and remote pipelines shut down and local
	// cleanup (partial-dump removal, temp files) runs instead of the
	// process being cut down mid-write. A second signal kills hard.
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	if err := cli.Execute(ctx, cli.BuildInfo{Version: version, Commit: commit, Date: date}); err != nil {
		os.Exit(1)
	}
}
