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
	// A macOS quarantine check would go here, and would never be read.
	// Measured on macOS 26.6.2 Intel (TOR-97): a downloaded, unsigned
	// torpeek is stopped by AppleSystemPolicy before this function's first
	// instruction - a build whose very first statement wrote to stderr
	// printed nothing at all and was SIGKILLed a few seconds later, while
	// the same bytes with com.apple.quarantine cleared printed immediately.
	// Anyone who could read such a message is somebody whose run was not
	// stopped, which is why there is none. The remedy that can run is
	// packaging/macos/first-run.command: it runs before torpeek does, and
	// the macOS archives carry it. docs/licensing.md, "Gatekeeper", has the
	// arms.

	// Ctrl+C cancels the run rather than killing the process, so frames
	// already written stay and the summary still prints.
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	os.Exit(cli.Run(ctx, os.Args[1:], os.Stdout, os.Stderr))
}
