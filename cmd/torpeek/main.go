// Command torpeek previews a video torrent without downloading it: frames
// spread across each video file, plus a technical summary.
//
// See REQUIREMENTS.md for what it is meant to do and ARCHITECTURE.md for how
// it is put together.
package main

import (
	"context"
	"os"
	"os/signal"
	"syscall"

	"github.com/madmurdok/torpeek/internal/cli"
)

func main() {
	// Ctrl+C cancels the run rather than killing the process, so frames
	// already written stay and the summary still prints.
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	os.Exit(cli.Run(ctx, os.Args[1:], os.Stdout, os.Stderr))
}
