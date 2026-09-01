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

	"github.com/torpeek/torpeek/internal/ffmpeg"
	"github.com/torpeek/torpeek/internal/torrenttest"
	"github.com/torpeek/torpeek/internal/version"
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

	var found int
	err := filepath.Walk(out, func(path string, info os.FileInfo, err error) error {
		if err == nil && !info.IsDir() && strings.HasSuffix(path, ".jpg") {
			found++
			if info.Size() == 0 {
				t.Errorf("frame %s is empty", path)
			}
		}
		return nil
	})
	if err != nil {
		t.Fatalf("walk output: %v", err)
	}
	if found != 4 {
		t.Errorf("found %d frames on disk, want 4", found)
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
