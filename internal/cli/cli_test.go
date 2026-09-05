package cli

import (
	"bytes"
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/madmurdok/torpeek/internal/ffmpeg"
	"github.com/madmurdok/torpeek/internal/torrenttest"
	"github.com/madmurdok/torpeek/internal/version"
)

func TestVersionFlag(t *testing.T) {
	var stdout, stderr bytes.Buffer

	if code := Run(context.Background(), []string{"-version"}, &stdout, &stderr); code != ExitOK {
		t.Errorf("exit code = %d, want %d", code, ExitOK)
	}
	if got := strings.TrimSpace(stdout.String()); got != version.Version {
		t.Errorf("printed %q, want %q", got, version.Version)
	}
}

func TestMissingSourceIsAUsageError(t *testing.T) {
	var stdout, stderr bytes.Buffer

	if code := Run(context.Background(), nil, &stdout, &stderr); code != ExitUsage {
		t.Errorf("exit code = %d, want %d", code, ExitUsage)
	}
	if !strings.Contains(stderr.String(), "magnet") {
		t.Errorf("stderr = %q, want it to say what is missing", stderr.String())
	}
}

func TestBadFlagValuesAreUsageErrors(t *testing.T) {
	cases := [][]string{
		{"-mode", "fastest", "magnet:?xt=urn:btih:abc"},
		{"-format", "webp", "magnet:?xt=urn:btih:abc"},
	}

	for _, args := range cases {
		t.Run(strings.Join(args, " "), func(t *testing.T) {
			var stdout, stderr bytes.Buffer
			if code := Run(context.Background(), args, &stdout, &stderr); code != ExitUsage {
				t.Errorf("exit code = %d, want %d", code, ExitUsage)
			}
		})
	}
}

func TestParseCollectsPeers(t *testing.T) {
	opts, err := parse([]string{"-peer", "1.2.3.4:5, 6.7.8.9:10 ,", "file.torrent"}, &bytes.Buffer{})
	if err != nil {
		t.Fatalf("parse: %v", err)
	}
	if len(opts.Peers) != 2 {
		t.Fatalf("parsed %d peers, want 2: %v", len(opts.Peers), opts.Peers)
	}
	if opts.Peers[0] != "1.2.3.4:5" || opts.Peers[1] != "6.7.8.9:10" {
		t.Errorf("peers = %v, want them trimmed", opts.Peers)
	}
	if opts.Source != "file.torrent" {
		t.Errorf("source = %q, want the positional argument", opts.Source)
	}
}

// TestPortFlagsFlowIntoConfig is the CLI half of TOR-28's plumbing, now that
// the BitTorrent side is a set rather than a port: -torrent-ports and
// -bridge-port must land, unaltered, in the config the engine actually runs
// with. One port is still expressible, because a single-client deployment is
// still a deployment.
func TestPortFlagsFlowIntoConfig(t *testing.T) {
	opts, err := parse([]string{
		"-data", t.TempDir(),
		"-torrent-ports", "51413",
		"-bridge-port", "51500",
		"magnet:?xt=urn:btih:abc",
	}, &bytes.Buffer{})
	if err != nil {
		t.Fatalf("parse: %v", err)
	}

	cfg, err := opts.config()
	if err != nil {
		t.Fatalf("config: %v", err)
	}

	if cfg.Swarm.Ports == nil {
		t.Fatal("Swarm.Ports is nil, so nothing hands the run a port at all")
	}
	if !cfg.Swarm.Ports.Managed() {
		t.Error("Swarm.Ports is not managed, so a configured allocation reads as the local case")
	}
	if got, want := cfg.Swarm.Ports.Set().String(), "51413"; got != want {
		t.Errorf("Swarm.Ports holds %q, want %q", got, want)
	}
	if want := "127.0.0.1:51500"; cfg.Bridge.Addr != want {
		t.Errorf("Bridge.Addr = %q, want %q", cfg.Bridge.Addr, want)
	}
}

// TestTorrentPortsAcceptsARange is the shape REQUIREMENTS.md 4.1 actually
// describes - a range allocated by the platform - and the size that comes out
// of it is the number of private torrents that can fetch at once, so it is
// checked rather than assumed.
func TestTorrentPortsAcceptsARange(t *testing.T) {
	opts, err := parse([]string{
		"-data", t.TempDir(),
		"-torrent-ports", "51000-51004",
		"magnet:?xt=urn:btih:abc",
	}, &bytes.Buffer{})
	if err != nil {
		t.Fatalf("parse: %v", err)
	}

	cfg, err := opts.config()
	if err != nil {
		t.Fatalf("config: %v", err)
	}

	if cfg.Swarm.Ports == nil {
		t.Fatal("Swarm.Ports is nil")
	}
	if got, want := cfg.Swarm.Ports.Size(), 5; got != want {
		t.Errorf("the run may use %d ports, want %d", got, want)
	}
	if got, want := cfg.Swarm.Ports.Set().String(), "51000-51004"; got != want {
		t.Errorf("Swarm.Ports holds %q, want %q", got, want)
	}
}

// TestPortFlagsDefaultToZero: with nothing configured, the run gets a pool
// that lets the OS choose every client's port and bounds nothing - the local
// case - and it stays distinguishable from a managed host, which
// REQUIREMENTS.md 4.1 requires to configure a range instead.
func TestPortFlagsDefaultToZero(t *testing.T) {
	opts, err := parse([]string{
		"-data", t.TempDir(),
		"magnet:?xt=urn:btih:abc",
	}, &bytes.Buffer{})
	if err != nil {
		t.Fatalf("parse: %v", err)
	}

	cfg, err := opts.config()
	if err != nil {
		t.Fatalf("config: %v", err)
	}

	if cfg.Swarm.Ports == nil {
		t.Fatal("Swarm.Ports is nil, so a run has nowhere to get a port from")
	}
	if cfg.Swarm.Ports.Managed() {
		t.Errorf("Swarm.Ports reports a managed allocation of %s with nothing configured", cfg.Swarm.Ports.Set())
	}
	if cfg.Swarm.ListenPort != 0 {
		t.Errorf("Swarm.ListenPort = %d, want 0 (OS-assigned)", cfg.Swarm.ListenPort)
	}
	if want := "127.0.0.1:0"; cfg.Bridge.Addr != want {
		t.Errorf("Bridge.Addr = %q, want %q (bridge.DefaultConfig: an OS-assigned loopback port)", cfg.Bridge.Addr, want)
	}
}

// TestEmptyTorrentPortsIsAUsageError: "the flag is absent" and "the flag is
// present and empty" must not be the same answer. The systemd unit writes
// -torrent-ports ${TORPEEK_TORRENT_PORTS}, and an unset variable there
// expands to one empty argument - if that meant "let the OS choose", a
// managed host would silently bind a port nobody allocated, which is the one
// thing REQUIREMENTS.md 4.1 forbids outright. The old int-valued flag
// refused "" for its own reasons; this keeps the same answer on purpose.
func TestEmptyTorrentPortsIsAUsageError(t *testing.T) {
	opts, err := parse([]string{
		"-data", t.TempDir(),
		"-torrent-ports", "",
		"magnet:?xt=urn:btih:abc",
	}, &bytes.Buffer{})
	if err != nil {
		t.Fatalf("parse: %v", err)
	}

	cfg, err := opts.config()
	if err == nil {
		t.Fatalf("an empty -torrent-ports produced a usable config (managed=%v), want a usage error", cfg.Swarm.Ports.Managed())
	}
	if !strings.Contains(err.Error(), "-torrent-ports") {
		t.Errorf("the error does not name the flag, so nobody can tell which one to fix: %v", err)
	}
}

// TestBadTorrentPortsIsAUsageError: a mistyped range is a command line to
// fix, reported the same way -mode and -cache-max-size already are, rather
// than a run that starts on ports nobody allocated.
func TestBadTorrentPortsIsAUsageError(t *testing.T) {
	for _, spec := range []string{"51004-51000", "0", "abc", "51000,51000"} {
		opts, err := parse([]string{
			"-data", t.TempDir(),
			"-torrent-ports", spec,
			"magnet:?xt=urn:btih:abc",
		}, &bytes.Buffer{})
		if err != nil {
			t.Fatalf("parse %q: %v", spec, err)
		}

		if _, err := opts.config(); err == nil {
			t.Errorf("-torrent-ports %q was accepted, want a usage error", spec)
		}
	}
}

// TestWebAddr covers the web bind-address reconciliation: -web-port alone
// keeps the old behaviour, -web-host alone still does something useful with
// the documented default port, and with neither the caller keeps
// web.DefaultConfig()'s address rather than being handed an empty one.
func TestWebAddr(t *testing.T) {
	cases := []struct {
		name string
		host string
		port int
		want string
	}{
		{name: "neither set", host: "", port: 0, want: ""},
		{name: "port only", host: "", port: 9000, want: "127.0.0.1:9000"},
		{name: "host only", host: "0.0.0.0", port: 0, want: "0.0.0.0:8765"},
		{name: "both set", host: "0.0.0.0", port: 9000, want: "0.0.0.0:9000"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := webAddr(tc.host, tc.port); got != tc.want {
				t.Errorf("webAddr(%q, %d) = %q, want %q", tc.host, tc.port, got, tc.want)
			}
		})
	}
}

// TestBasePathAndHeadlessFlagsParse is the CLI half of TOR-29's plumbing:
// -base-path and -headless must land unaltered in Options for serveWeb to
// use. What they do once there is the web package's own tests
// (TestConfiguredBasePathIsServedEndToEnd) and the headless behaviour proven
// against the real binary in this task's manual verification.
func TestBasePathAndHeadlessFlagsParse(t *testing.T) {
	opts, err := parse([]string{"-web", "-base-path", "/torpeek", "-headless"}, &bytes.Buffer{})
	if err != nil {
		t.Fatalf("parse: %v", err)
	}
	if opts.BasePath != "/torpeek" {
		t.Errorf("BasePath = %q, want /torpeek", opts.BasePath)
	}
	if !opts.Headless {
		t.Error("Headless = false, want true")
	}
}

// TestBasePathAndHeadlessDefaultOff: neither flag given must leave the site
// root and a browser opened, the behaviour before this task existed.
func TestBasePathAndHeadlessDefaultOff(t *testing.T) {
	opts, err := parse([]string{"-web"}, &bytes.Buffer{})
	if err != nil {
		t.Fatalf("parse: %v", err)
	}
	if opts.BasePath != "" {
		t.Errorf("BasePath = %q, want empty by default", opts.BasePath)
	}
	if opts.Headless {
		t.Error("Headless = true, want false by default")
	}
}

// sampleTorrent renders a clip, makes a torrent of it and starts a seeder.
func sampleTorrent(t *testing.T) (torrentPath, seeder string) {
	t.Helper()

	tools, err := ffmpeg.LocateIn()
	if err != nil {
		t.Skipf("no ffmpeg available: %v", err)
	}

	dir := t.TempDir()
	path := filepath.Join(dir, "movie.mkv")

	ctx, cancel := context.WithTimeout(context.Background(), 180*time.Second)
	defer cancel()

	if _, err := tools.Run(ctx, "ffmpeg",
		"-hide_banner", "-loglevel", "error", "-y",
		"-f", "lavfi", "-i", "testsrc=size=640x360:rate=25:duration=60",
		"-f", "lavfi", "-i", "sine=frequency=440:duration=60",
		"-map", "0:v", "-map", "1:a",
		"-c:v", "libx264", "-g", "50", "-pix_fmt", "yuv420p", "-b:v", "800k",
		"-c:a", "aac", "-metadata:s:a:0", "language=eng",
		path,
	); err != nil {
		t.Fatalf("render clip: %v", err)
	}

	payload, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read clip: %v", err)
	}

	fixture := torrenttest.BuildFromBytes(t, "movie.mkv", payload, 256<<10)
	return fixture.TorrentPath, fixture.StartSeeder(t)
}

func runArgs(t *testing.T, torrentPath, seeder string, extra ...string) []string {
	t.Helper()

	args := []string{
		"-out", t.TempDir(),
		"-data", t.TempDir(),
		"-n", "4",
		"-mode", "min-traffic",
		"-dht=false",
		"-peer", seeder,
	}
	args = append(args, extra...)
	return append(args, torrentPath)
}

// TestEndToEndTextOutput is the whole point of the release: one command turns
// a torrent into frames on disk.
func TestEndToEndTextOutput(t *testing.T) {
	torrentPath, seeder := sampleTorrent(t)

	out := t.TempDir()
	args := []string{
		"-out", out, "-data", t.TempDir(), "-n", "4",
		"-mode", "min-traffic", "-dht=false", "-peer", seeder, torrentPath,
	}

	var stdout, stderr bytes.Buffer
	ctx, cancel := context.WithTimeout(context.Background(), 300*time.Second)
	defer cancel()

	code := Run(ctx, args, &stdout, &stderr)
	text := stdout.String()
	t.Logf("stdout:\n%s", text)
	if stderr.Len() > 0 {
		t.Logf("stderr:\n%s", stderr.String())
	}

	if code != ExitOK {
		t.Fatalf("exit code = %d, want %d", code, ExitOK)
	}

	for _, want := range []string{"movie.mkv", "640x360", "audio: eng", "frame 00", "4 frames"} {
		if !strings.Contains(text, want) {
			t.Errorf("output does not mention %q", want)
		}
	}

	var found, sheets int
	err := filepath.Walk(out, func(path string, info os.FileInfo, err error) error {
		if err != nil || info.IsDir() || !strings.HasSuffix(path, ".jpg") {
			return nil
		}
		if info.Size() == 0 {
			t.Errorf("%s is empty", path)
		}
		// The contact sheet lands beside the frames, named sheet.jpg (TOR-16)
		// - a real .jpg, but not one of the four capture points.
		if filepath.Base(path) == "sheet.jpg" {
			sheets++
			return nil
		}
		found++
		return nil
	})
	if err != nil {
		t.Fatalf("walk output: %v", err)
	}
	if found != 4 {
		t.Errorf("found %d frames on disk, want 4", found)
	}
	if sheets != 1 {
		t.Errorf("found %d contact sheet(s) on disk, want 1", sheets)
	}
}

// TestEndToEndJSONOutput checks the machine-readable mode: every line parses,
// and the stream is bracketed by metadata and a terminal event.
func TestEndToEndJSONOutput(t *testing.T) {
	torrentPath, seeder := sampleTorrent(t)

	var stdout, stderr bytes.Buffer
	ctx, cancel := context.WithTimeout(context.Background(), 300*time.Second)
	defer cancel()

	code := Run(ctx, runArgs(t, torrentPath, seeder, "-json"), &stdout, &stderr)
	if code != ExitOK {
		t.Fatalf("exit code = %d, want %d (stderr: %s)", code, ExitOK, stderr.String())
	}

	types := map[string]int{}
	for i, line := range strings.Split(strings.TrimSpace(stdout.String()), "\n") {
		var event map[string]any
		if err := json.Unmarshal([]byte(line), &event); err != nil {
			t.Fatalf("line %d is not JSON: %v\n%s", i, err, line)
		}
		kind, ok := event["type"].(string)
		if !ok || kind == "" {
			t.Fatalf("line %d has no type: %s", i, line)
		}
		types[kind]++
	}

	for _, want := range []string{"metadata_ready", "file_started", "frame_ready", "file_done", "done"} {
		if types[want] == 0 {
			t.Errorf("no %s events in the stream (saw %v)", want, types)
		}
	}
	if types["frame_ready"] != 4 {
		t.Errorf("%d frame_ready events, want 4", types["frame_ready"])
	}
	if types["unknown"] != 0 {
		t.Errorf("%d events serialised as unknown - an event type is missing from the wire format", types["unknown"])
	}
}

// TestBudgetStopExitsPartial: a script must be able to tell "stopped early but
// kept results" from both success and failure.
func TestBudgetStopExitsPartial(t *testing.T) {
	torrentPath, seeder := sampleTorrent(t)

	var stdout, stderr bytes.Buffer
	ctx, cancel := context.WithTimeout(context.Background(), 300*time.Second)
	defer cancel()

	args := runArgs(t, torrentPath, seeder, "-n", "12", "-max-bytes", strconv.Itoa(2<<20))
	code := Run(ctx, args, &stdout, &stderr)

	if code != ExitPartial {
		t.Errorf("exit code = %d, want %d for a run stopped by its budget", code, ExitPartial)
	}
	if !strings.Contains(stderr.String(), "kept") {
		t.Errorf("stderr = %q, want it to say results were kept", stderr.String())
	}
}

// TestListShowsFilesWithoutTakingFrames: choosing which file to look at must
// not cost what looking at it costs.
func TestListShowsFilesWithoutTakingFrames(t *testing.T) {
	torrentPath, seeder := sampleTorrent(t)

	out := t.TempDir()
	var stdout, stderr bytes.Buffer
	ctx, cancel := context.WithTimeout(context.Background(), 120*time.Second)
	defer cancel()

	code := Run(ctx, []string{
		"-list", "-out", out, "-data", t.TempDir(), "-dht=false", "-peer", seeder, torrentPath,
	}, &stdout, &stderr)

	if code != ExitOK {
		t.Fatalf("exit code = %d, want %d (stderr: %s)", code, ExitOK, stderr.String())
	}
	text := stdout.String()
	t.Logf("stdout:\n%s", text)
	if !strings.Contains(text, "movie.mkv") {
		t.Errorf("listing does not name the file: %q", text)
	}
	if !strings.Contains(text, "-file") {
		t.Errorf("listing does not say how to use what it printed: %q", text)
	}

	var frames int
	_ = filepath.Walk(out, func(path string, info os.FileInfo, err error) error {
		if err == nil && !info.IsDir() {
			frames++
		}
		return nil
	})
	if frames != 0 {
		t.Errorf("listing wrote %d files into the output directory", frames)
	}
}

// TestListInJSONIsOneObject, not an event stream: it answers a question rather
// than reporting a run.
func TestListInJSONIsOneObject(t *testing.T) {
	torrentPath, seeder := sampleTorrent(t)

	var stdout, stderr bytes.Buffer
	ctx, cancel := context.WithTimeout(context.Background(), 120*time.Second)
	defer cancel()

	code := Run(ctx, []string{
		"-list", "-json", "-out", t.TempDir(), "-data", t.TempDir(),
		"-dht=false", "-peer", seeder, torrentPath,
	}, &stdout, &stderr)
	if code != ExitOK {
		t.Fatalf("exit code = %d, want %d (stderr: %s)", code, ExitOK, stderr.String())
	}

	lines := strings.Split(strings.TrimSpace(stdout.String()), "\n")
	if len(lines) != 1 {
		t.Fatalf("got %d lines, want one object", len(lines))
	}

	var got struct {
		Type   string `json:"type"`
		Videos []struct {
			Index int    `json:"index"`
			Path  string `json:"path"`
			Bytes int64  `json:"bytes"`
		} `json:"videos"`
	}
	if err := json.Unmarshal([]byte(lines[0]), &got); err != nil {
		t.Fatalf("not JSON: %v\n%s", err, lines[0])
	}
	if got.Type != "contents" {
		t.Errorf("type = %q, want %q", got.Type, "contents")
	}
	if len(got.Videos) != 1 || got.Videos[0].Bytes <= 0 {
		t.Errorf("videos = %+v, want the one file with a real size", got.Videos)
	}
}

// TestPieceDirectoryIsDiscardedWhenWeMadeIt: results are worth keeping, the
// pieces they were made from are not (REQUIREMENTS.md section 2.9). A
// directory the caller named is a different matter - it is theirs.
func TestPieceDirectoryIsDiscardedWhenWeMadeIt(t *testing.T) {
	torrentPath, seeder := sampleTorrent(t)

	// Before the first run, and before anything is globbed: what this test
	// asserts is that a run removes the directory IT made, and the only way
	// to know which one that is, is for nothing else to be able to put one
	// where the test is looking.
	privateTempDir(t)

	before := scratchDirs(t)

	out := t.TempDir()
	var stdout, stderr bytes.Buffer
	ctx, cancel := context.WithTimeout(context.Background(), 300*time.Second)
	defer cancel()

	// No -data, so the run makes its own piece directory and owns it.
	code := Run(ctx, []string{
		"-out", out, "-n", "2", "-mode", "min-traffic",
		"-dht=false", "-peer", seeder, torrentPath,
	}, &stdout, &stderr)
	if code != ExitOK {
		t.Fatalf("exit code = %d (stderr: %s)", code, stderr.String())
	}

	for dir := range scratchDirs(t) {
		if !before[dir] {
			t.Errorf("%s was left behind; the pieces should go with the run", dir)
		}
	}

	// A named directory survives, pieces and all.
	data := t.TempDir()
	stdout.Reset()
	stderr.Reset()
	code = Run(ctx, []string{
		"-out", t.TempDir(), "-data", data, "-n", "2", "-mode", "min-traffic",
		"-dht=false", "-peer", seeder, torrentPath,
	}, &stdout, &stderr)
	if code != ExitOK {
		t.Fatalf("exit code = %d (stderr: %s)", code, stderr.String())
	}
	if _, err := os.Stat(data); err != nil {
		t.Errorf("a directory the caller named was removed: %v", err)
	}
}

// scratchDirs lists the piece directories torpeek would have made.
// privateTempDir points os.MkdirTemp at a directory belonging to this test
// alone, so a scratchDirs glob can only ever see piece directories this
// test's own runs made.
//
// The alternative - globbing the machine's shared temp directory - reads
// whatever else is on the machine at that moment as something the run left
// behind. That is not hypothetical: a second checkout running its own suite
// at the same time trips it, which is now a normal way to work here (three
// agents ran this repo in parallel while 0.7.0 was built). os.TempDir
// answers $TMPDIR on unix and Windows' own variables elsewhere, so setting
// it is all that is needed; t.Setenv restores it and refuses to run under
// t.Parallel, which is the guard against one test's temp dir leaking into
// another's.
func privateTempDir(t *testing.T) {
	t.Helper()

	t.Setenv("TMPDIR", t.TempDir())
}

func scratchDirs(t *testing.T) map[string]bool {
	t.Helper()

	matches, err := filepath.Glob(filepath.Join(os.TempDir(), "torpeek-pieces-*"))
	if err != nil {
		t.Fatalf("glob: %v", err)
	}
	out := make(map[string]bool, len(matches))
	for _, m := range matches {
		out[m] = true
	}
	return out
}
