// Package cli turns command line arguments into a run and its events into
// output. It is one client of the core, not a layer the core knows about.
//
// Requirements: section 3.2.
package cli

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/madmurdok/torpeek/internal/core"
	"github.com/madmurdok/torpeek/internal/ffmpeg"
	"github.com/madmurdok/torpeek/internal/frames"
	"github.com/madmurdok/torpeek/internal/swarm"
	"github.com/madmurdok/torpeek/internal/version"
)

// Exit codes. These are an interface: a script calling torpeek reacts to them,
// so they are enumerated here rather than scattered as literals.
const (
	ExitOK = 0
	// ExitUsage is a bad command line - nothing was attempted.
	ExitUsage = 2
	// ExitPartial means the run stopped at a limit or was cancelled, and
	// whatever it produced is on disk.
	ExitPartial = 3
	// ExitFailed means the run could not produce results at all.
	ExitFailed = 1
)

// Options are the parsed command line.
type Options struct {
	Source      string
	Output      string
	DataDir     string
	Count       int
	Start       float64
	End         float64
	Profile     string
	Format      string
	MaxBytes    int64
	MaxTime     time.Duration
	Parallelism int
	Port        int
	BridgePort  int
	Peers       []string
	Upload      bool
	DHT         bool
	JSON        bool
	Version     bool
	List        bool
	Files       []string
}

// Run parses arguments, executes the job and reports it. It returns an exit
// code rather than calling os.Exit, so it can be tested.
func Run(ctx context.Context, args []string, stdout, stderr io.Writer) int {
	opts, err := parse(args, stderr)
	if err != nil {
		if errors.Is(err, flag.ErrHelp) {
			return ExitOK
		}
		fmt.Fprintf(stderr, "torpeek: %v\n", err)
		return ExitUsage
	}

	if opts.Version {
		fmt.Fprintln(stdout, version.Version)
		return ExitOK
	}

	if opts.Source == "" {
		fmt.Fprintln(stderr, "torpeek: give a magnet link or a .torrent file")
		return ExitUsage
	}

	cfg, err := opts.config()
	if err != nil {
		fmt.Fprintf(stderr, "torpeek: %v\n", err)
		return ExitUsage
	}

	// Listing needs no decoder, so it must not require one to be installed:
	// choosing which file to look at is exactly what someone does before
	// setting the rest up.
	if opts.List {
		return listFiles(ctx, cfg, opts.JSON, stdout, stderr)
	}

	tools, err := ffmpeg.Locate()
	if err != nil {
		fmt.Fprintf(stderr, "torpeek: %v\n", err)
		fmt.Fprintln(stderr, "put ffmpeg and ffprobe next to the torpeek binary, or install them")
		return ExitFailed
	}

	events, err := core.NewEngine(tools).Run(ctx, cfg)
	if err != nil {
		fmt.Fprintf(stderr, "torpeek: %v\n", err)
		return ExitFailed
	}

	if opts.JSON {
		return reportJSON(events, stdout, stderr)
	}
	return reportText(events, stdout, stderr)
}

func parse(args []string, stderr io.Writer) (Options, error) {
	var opts Options

	fs := flag.NewFlagSet("torpeek", flag.ContinueOnError)
	fs.SetOutput(stderr)
	fs.Usage = func() {
		fmt.Fprint(stderr, usage)
		fs.PrintDefaults()
	}

	fs.StringVar(&opts.Output, "out", "torpeek-out", "directory for frames")
	fs.StringVar(&opts.DataDir, "data", "", "directory for fetched pieces (default: a temporary one)")
	fs.IntVar(&opts.Count, "n", frames.DefaultCount, "frames per video file")
	fs.Float64Var(&opts.Start, "start", frames.DefaultStart, "start of the capture window, as a fraction of the duration")
	fs.Float64Var(&opts.End, "end", frames.DefaultEnd, "end of the capture window")
	fs.StringVar(&opts.Profile, "mode", swarm.MinTime.Name, "min-time or min-traffic")
	fs.StringVar(&opts.Format, "format", string(frames.JPEG), "jpeg or png")
	fs.Int64Var(&opts.MaxBytes, "max-bytes", 0, "traffic ceiling for the run (default: scaled to the file count)")
	fs.DurationVar(&opts.MaxTime, "max-time", 0, "time ceiling for the run (default: 10m)")
	fs.IntVar(&opts.Parallelism, "parallel", core.DefaultParallelism, "video files to work on at once")
	fs.IntVar(&opts.Port, "torrent-port", 0, "BitTorrent listen port (required where a port range is allocated)")
	fs.IntVar(&opts.BridgePort, "bridge-port", 0, "loopback port for the internal HTTP bridge")
	fs.BoolVar(&opts.Upload, "upload", true, "serve pieces back to the swarm while running")
	fs.BoolVar(&opts.DHT, "dht", true, "use DHT and PEX (never for a private torrent)")
	fs.BoolVar(&opts.JSON, "json", false, "emit NDJSON events instead of human output")
	fs.BoolVar(&opts.List, "list", false, "list the torrent's video files and exit, without taking frames")
	fs.BoolVar(&opts.Version, "version", false, "print version and exit")

	var peers string
	fs.StringVar(&peers, "peer", "", "comma-separated peer addresses to contact directly")

	var files string
	fs.StringVar(&files, "file", "", "which video files to process: torrent index or path pattern, comma-separated (default: all of them)")

	if err := fs.Parse(args); err != nil {
		return opts, err
	}

	opts.Peers = splitList(peers)
	opts.Files = splitList(files)

	opts.Source = fs.Arg(0)
	return opts, nil
}

func (o Options) config() (core.Config, error) {
	dataDir := o.DataDir
	if dataDir == "" {
		dir, err := os.MkdirTemp("", "torpeek-pieces-*")
		if err != nil {
			return core.Config{}, fmt.Errorf("create piece directory: %w", err)
		}
		dataDir = dir
	}

	profile, err := swarm.ProfileByName(o.Profile)
	if err != nil {
		return core.Config{}, err
	}

	format := frames.Format(strings.ToLower(o.Format))
	switch format {
	case frames.JPEG, frames.PNG:
	case "jpg":
		format = frames.JPEG
	default:
		return core.Config{}, fmt.Errorf("unknown format %q, want jpeg or png", o.Format)
	}

	out, err := filepath.Abs(o.Output)
	if err != nil {
		return core.Config{}, fmt.Errorf("resolve output directory: %w", err)
	}

	cfg := core.DefaultConfig(o.Source, out, dataDir)
	cfg.Plan = frames.Plan{Count: o.Count, Start: o.Start, End: o.End}
	cfg.Profile = profile
	cfg.Format = format
	cfg.Parallelism = o.Parallelism

	cfg.Files = o.Files

	cfg.Swarm.Upload = o.Upload
	cfg.Swarm.DHT = o.DHT
	cfg.Swarm.ListenPort = o.Port
	cfg.Swarm.Peers = o.Peers

	if o.BridgePort > 0 {
		cfg.Bridge.Addr = fmt.Sprintf("127.0.0.1:%d", o.BridgePort)
	}

	// Zero means "decide once the file count is known", which the engine does.
	cfg.Budget = core.Budget{MaxBytes: o.MaxBytes, MaxTime: o.MaxTime, WarnAt: 0.8}

	return cfg, nil
}

// splitList reads a comma-separated flag value, dropping blanks so a trailing
// comma or a stray space is not taken for an entry.
func splitList(raw string) []string {
	var out []string
	for _, part := range strings.Split(raw, ",") {
		if part = strings.TrimSpace(part); part != "" {
			out = append(out, part)
		}
	}
	return out
}

const usage = `torpeek - preview a video torrent without downloading it

usage: torpeek [flags] <magnet-uri | file.torrent>

flags:
`
