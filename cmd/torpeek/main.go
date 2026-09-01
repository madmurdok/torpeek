// Command torpeek previews a video torrent without downloading it in full:
// frames spread across each video file, plus a technical summary.
//
// See REQUIREMENTS.md for what this is meant to do.
package main

import (
	"flag"
	"fmt"
	"os"

	"github.com/torpeek/torpeek/internal/version"
)

func main() {
	showVersion := flag.Bool("version", false, "print version and exit")
	flag.Parse()

	if *showVersion {
		fmt.Println(version.Version)
		return
	}

	fmt.Fprintf(os.Stderr, "torpeek %s: no commands wired up yet (TOR-1 is the skeleton only)\n", version.Version)
	os.Exit(2)
}
