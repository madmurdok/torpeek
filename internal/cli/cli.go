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
	"github.com/madmurdok/torpeek/internal/web"
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
//
// scratch records a piece directory this program created and therefore owns.
// A directory the caller named is theirs: keeping their pieces or removing
// them is their decision, not ours.
type Options struct {
	Source   string
	Output   string
	DataDir  string
	Count    int
	Start    float64
	End      float64
	Profile  string
	Format   string
	MaxBytes int64
	MaxTime  time.Duration
	// MaxClientBytes is the client-wide traffic roof, not a per-run ceiling
	// (core.Roof). It bounds every run this process makes together, which is
	// the only bound that survives the web UI running several at once.
	MaxClientBytes int64
	Parallelism    int
	BridgePort     int
	WebHost        string
	WebPort        int
	BasePath       string
	Token          string
	WatchDir       string
	Headless       bool
	Peers          []string
	Upload         bool
	DHT            bool
	Sequential     bool
	JSON           bool
	Web            bool
	Version        bool
	List           bool
	Files          []string

	// TorrentPorts is -torrent-ports as typed, parsed by config() with
	// swarm.ParsePortSet rather than here, the same way CacheMaxSize is.
	//
	// It records whether the flag appeared at all, because "absent" and
	// "present but empty" are different answers here and must not collapse
	// into one. Absent means nobody configured ports, so the OS chooses -
	// the local default. Empty means somebody tried to configure them and
	// supplied nothing, which on a managed host is how a systemd unit with
	// an unset ${TORPEEK_TORRENT_PORTS} would otherwise slide silently onto
	// a random port outside the allocated range (REQUIREMENTS.md 4.1). That
	// is a usage error, and the old int-valued -torrent-port gave the same
	// answer for the same reason - "" was never a number either.
	TorrentPorts optionalString

	// CacheMaxSize is -cache-max-size as typed, parsed by config() with
	// parseSize rather than here: a bad value must be a usage error the same
	// way -mode and -format already are, and config() is where those live.
	CacheMaxSize  string
	CacheList     bool
	CacheClear    string
	CacheClearAll bool

	scratch string
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

	// The cache commands are the other mode that starts with nothing to work
	// on - a person inspecting or clearing what is already on disk names no
	// torrent at all, the same way -version does not - so they are handled
	// before the "give a magnet link" check below, and before opts.config()
	// does work (a temporary piece directory, most of all) that a cache-only
	// invocation has no use for.
	if opts.CacheList || opts.CacheClearAll || opts.CacheClear != "" {
		out, err := filepath.Abs(opts.Output)
		if err != nil {
			fmt.Fprintf(stderr, "torpeek: %v\n", err)
			return ExitFailed
		}
		return runCache(opts, out, stdout, stderr)
	}

	// The UI is the one mode that starts with nothing to work on: the source
	// is pasted into the page, not onto the command line.
	if opts.Source == "" && !opts.Web {
		fmt.Fprintln(stderr, "torpeek: give a magnet link or a .torrent file")
		return ExitUsage
	}

	cfg, err := opts.config()
	if err != nil {
		fmt.Fprintf(stderr, "torpeek: %v\n", err)
		return ExitUsage
	}

	// Results are the thing worth keeping; the pieces they were made from are
	// not (REQUIREMENTS.md section 2.9). Only a directory this run created is
	// removed - one the caller named belongs to them.
	//
	// This complements, rather than replaces, core.Engine's own per-run
	// cleanup (see swarm.Session.DiscardPieces): the engine drops each run's
	// <scratch>/<infohash>/ subtree as soon as that run ends, which is what
	// actually matters for -web - a session can hold this process open for
	// hours, so waiting for process exit would defeat the point. What this
	// defer still covers on its own: -list, which never opens a session and
	// so never reaches the engine's cleanup at all; a run that fails before
	// a session opens; and simply removing the now-empty scratch directory
	// itself, which the engine deliberately leaves standing (it only ever
	// drops its own subtree, on the theory that -data named a directory that
	// belongs to the caller - here there is no caller, so removing the whole
	// thing is fine, and tidier than leaving an empty temp directory behind).
	if opts.scratch != "" {
		defer os.RemoveAll(opts.scratch)
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

	if opts.Web {
		return serveWeb(ctx, opts, cfg, tools, stdout, stderr)
	}

	events, err := core.NewEngine(tools).Run(ctx, cfg)
	if err != nil {
		fmt.Fprintf(stderr, "torpeek: %v\n", err)
		return ExitFailed
	}

	if opts.JSON {
		return reportJSON(events, stdout, stderr)
	}
	return reportText(events, stdout, stderr, cfg.Plan.Count)
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
	fs.Int64Var(&opts.MaxClientBytes, "max-client-bytes", 0, "traffic ceiling for this whole process, across every run it makes together, counted on bytes actually received (default: no roof). A per-run -max-bytes multiplies by the number of runs going at once; this is the ceiling that does not (section 2.6). It does not cap upload, which nothing caps")
	fs.IntVar(&opts.Parallelism, "parallel", core.DefaultParallelism, "video files to work on at once")
	fs.Var(&opts.TorrentPorts, "torrent-ports", "BitTorrent listen ports, as one port, an inclusive range, a comma-separated list, or any mixture: 51413, 51000-51004, 51000-51002,51010. Each client binds one of them, and also pins its DHT and uTP to it (default: an OS-assigned port). Set it where a port range is allocated and going outside it is forbidden - and take the size seriously: every public torrent shares one client and one port, but a private torrent needs a client of its own, so this is what bounds how many private torrents can fetch at once (section 4.1)")
	fs.IntVar(&opts.BridgePort, "bridge-port", 0, "loopback port for the internal HTTP bridge (default: an OS-assigned port; required where a port range is allocated)")
	fs.StringVar(&opts.WebHost, "web-host", "", "bind address for the web UI (default: "+web.DefaultHost+"; a reverse proxy on the same host is the documented way to expose it, section 3.3)")
	fs.IntVar(&opts.WebPort, "web-port", 0, fmt.Sprintf("port for the web UI (default: %d)", web.DefaultPort))
	fs.StringVar(&opts.BasePath, "base-path", "", "path the UI is mounted under behind a reverse proxy, e.g. /torpeek (default: the site root, section 3.3)")
	fs.StringVar(&opts.Token, "web-token", "", "access token required to use the UI/API (default: none on localhost, auto-generated and required once reachable beyond it - a non-loopback -web-host or a -base-path; set this to pin one across restarts, e.g. under systemd, section 3.3)")
	fs.StringVar(&opts.WatchDir, "watch-dir", "", "directory a torrent client on this host watches for .torrent files; with -web, a run then offers a button that drops its .torrent there (default: none, and the button is absent)")
	fs.BoolVar(&opts.Headless, "headless", false, "do not try to open a browser; only serve (for a seedbox with no desktop, section 3.3)")
	fs.BoolVar(&opts.Upload, "upload", true, "serve pieces back to the swarm while running")
	fs.BoolVar(&opts.DHT, "dht", true, "use DHT and PEX (never for a private torrent)")
	fs.BoolVar(&opts.Sequential, "sequential", false, "when a container has no usable index, degrade to sequential capture from the start instead of failing")
	fs.BoolVar(&opts.JSON, "json", false, "emit NDJSON events instead of human output")
	fs.BoolVar(&opts.Web, "web", false, "serve the web UI and open it in a browser instead of running on the command line")
	fs.BoolVar(&opts.List, "list", false, "list the torrent's video files and exit, without taking frames")
	fs.BoolVar(&opts.Version, "version", false, "print version and exit")
	fs.StringVar(&opts.CacheMaxSize, "cache-max-size", "", "size ceiling for the whole -out tree; over it, whole cached result sets are removed oldest-first after each run (default: unset, no eviction at all, section 2.9); a number with an optional K/M/G/T suffix, e.g. 20G")
	fs.BoolVar(&opts.CacheList, "cache-list", false, "list cached result sets under -out with their size and date, and exit (no torrent argument needed)")
	fs.StringVar(&opts.CacheClear, "cache-clear", "", "remove one cached result set, named infohash/params as -cache-list prints it, and exit (no torrent argument needed)")
	fs.BoolVar(&opts.CacheClearAll, "cache-clear-all", false, "remove every cached result set under -out, and exit (no torrent argument needed)")

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

func (o *Options) config() (core.Config, error) {
	dataDir := o.DataDir
	if dataDir == "" {
		dir, err := os.MkdirTemp("", "torpeek-pieces-*")
		if err != nil {
			return core.Config{}, fmt.Errorf("create piece directory: %w", err)
		}
		dataDir, o.scratch = dir, dir
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

	cfg.Sequential = o.Sequential

	ports, err := o.portSet()
	if err != nil {
		return core.Config{}, err
	}

	cfg.Swarm.Upload = o.Upload
	cfg.Swarm.DHT = o.DHT
	// One pool for the whole process, built here rather than per run: a run
	// borrows a port from it and hands it back, and two runs must never be
	// handed the same one. cfg is copied per run (see cli/web.go's runner),
	// and a pointer is what survives that copy as one shared thing.
	cfg.Swarm.Ports = swarm.NewPortPool(ports)
	cfg.Swarm.Peers = o.Peers

	if o.BridgePort > 0 {
		cfg.Bridge.Addr = fmt.Sprintf("127.0.0.1:%d", o.BridgePort)
	}

	// Zero means "decide once the file count is known", which the engine does.
	cfg.Budget = core.Budget{MaxBytes: o.MaxBytes, MaxTime: o.MaxTime, WarnAt: 0.8}
	// Zero means no roof, which is the documented default and a decision -
	// core.DefaultRoof says why torpeek will not pick this number itself.
	// WarnAt is the same fraction the run's budget uses: there is no argument
	// for the two warning at different fullnesses, and a second flag for it
	// would be a knob nobody has asked for.
	cfg.Roof = core.Roof{MaxBytes: o.MaxClientBytes, WarnAt: 0.8}

	ceiling, err := parseSize(o.CacheMaxSize)
	if err != nil {
		return core.Config{}, fmt.Errorf("-cache-max-size: %w", err)
	}
	cfg.CacheCeiling = ceiling

	return cfg, nil
}

// portSet turns -torrent-ports into the set of ports this process may bind.
//
// The flag being absent is the unmanaged set: nothing was allocated, so every
// client asks the OS for a port and nothing bounds how many there can be.
// That is the local and test case, and it stays a distinct state rather than
// a set that happens to be empty - swarm.PortSet.Managed is what tells a
// managed host from a laptop.
func (o *Options) portSet() (swarm.PortSet, error) {
	if !o.TorrentPorts.given {
		return swarm.PortSet{}, nil
	}

	set, err := swarm.ParsePortSet(o.TorrentPorts.value)
	if err != nil {
		return swarm.PortSet{}, fmt.Errorf("-torrent-ports: %w", err)
	}
	return set, nil
}

// optionalString is a string flag that remembers whether it was given at all.
// flag.StringVar cannot: a default of "" and an explicit "" arrive as the
// same value, and for -torrent-ports those two mean opposite things (see
// Options.TorrentPorts).
type optionalString struct {
	value string
	given bool
}

func (s *optionalString) String() string {
	if s == nil {
		return ""
	}
	return s.value
}

func (s *optionalString) Set(v string) error {
	s.value, s.given = v, true
	return nil
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
       torpeek -web [flags] [magnet-uri | file.torrent]

flags:
`
