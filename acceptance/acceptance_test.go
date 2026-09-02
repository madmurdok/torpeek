//go:build acceptance

// Run with: make acceptance
//
// These go to a live public swarm, take minutes and cost real traffic, which
// is why they sit behind a tag instead of in `go test ./...`.
package acceptance

import (
	"context"
	"flag"
	"fmt"
	"io"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/madmurdok/torpeek/internal/cache"
	"github.com/madmurdok/torpeek/internal/core"
	"github.com/madmurdok/torpeek/internal/ffmpeg"
	"github.com/madmurdok/torpeek/internal/frames"
	"github.com/madmurdok/torpeek/internal/manifest"
	"github.com/madmurdok/torpeek/internal/swarm"
	"github.com/madmurdok/torpeek/internal/version"
)

var (
	torrentURL = flag.String("torrent-url",
		"https://archive.org/download/Sintel/Sintel_archive.torrent",
		"where to fetch the torrent under test, if -torrent is not given")
	torrentPath = flag.String("torrent", "", "a .torrent file to use instead of fetching one")
	fileSpec    = flag.String("file", "sintel-2048-stereo.mp4",
		"which file of the torrent the single-file criteria run against")
	reportPath = flag.String("report", "", "where to write the report (default docs/results/<version>-acceptance.md)")

	report Report
)

func TestMain(m *testing.M) {
	flag.Parse()

	report = Report{
		Tool:    version.Version,
		Started: time.Now(),
		Machine: machineState(),
	}

	code := m.Run()

	path := *reportPath
	if path == "" {
		path = filepath.Join("..", "docs", "results", version.Version+"-acceptance.md")
	}
	if err := report.Write(path); err != nil {
		fmt.Fprintf(os.Stderr, "writing report: %v\n", err)
		if code == 0 {
			code = 1
		}
	} else {
		fmt.Fprintf(os.Stderr, "report written to %s\n", path)
	}
	os.Exit(code)
}

// machineState records what the timings were taken on. Without it an elapsed
// number is unreadable: the 0.1.0 baseline was measured across load averages
// from 4 to 29, which changes wall clock and changes nothing about traffic.
func machineState() string {
	out, err := exec.Command("uptime").Output()
	if err != nil {
		return ""
	}
	return strings.TrimSpace(string(out))
}

// ---- harness -------------------------------------------------------------

type outcome struct {
	frames     int
	files      int
	downloaded int64
	elapsed    time.Duration
	reason     core.StopReason
	failures   []core.Failed
	// paths are the frames written, in the order they were reported.
	paths []string
	// byFile remembers one frame path per file, which is how a manifest is
	// found: FileDone carries the path inside the torrent, not on disk.
	byFile    map[int]string
	manifests []manifest.Manifest
	shifted   int
}

func baseConfig(t *testing.T, source, out, data string) core.Config {
	t.Helper()

	cfg := core.DefaultConfig(source, out, data)
	cfg.Plan = frames.Plan{Count: 20, Start: 0.05, End: 0.95}
	cfg.Profile = swarm.MinTime
	cfg.Parallelism = 1
	cfg.Budget = core.Budget{MaxBytes: 2 << 30, MaxTime: 10 * time.Minute, WarnAt: 0.8}
	return cfg
}

// run drives one job to its terminal event, optionally stopping it early.
func run(ctx context.Context, t *testing.T, cfg core.Config, stopAfter int) outcome {
	t.Helper()

	tools, err := ffmpeg.Locate()
	if err != nil {
		t.Skipf("no ffmpeg: %v", err)
	}

	runCtx, cancel := context.WithCancel(ctx)
	defer cancel()

	events, err := core.NewEngine(tools).Run(runCtx, cfg)
	if err != nil {
		t.Fatalf("run: %v", err)
	}

	got := outcome{byFile: map[int]string{}}
	for ev := range events {
		switch e := ev.(type) {
		case core.FrameReady:
			got.paths = append(got.paths, e.Path)
			got.byFile[e.File] = e.Path
			got.frames++
			if e.Shift == core.ShiftUnavailable {
				got.shifted++
			}
			if stopAfter > 0 && got.frames == stopAfter {
				cancel()
			}
		case core.FileDone:
			// <run>/<NN-file>/frames/000.jpg -> <run>/<NN-file>, where the
			// manifest sits. Derived from a frame because FileDone's path is
			// the one inside the torrent.
			if frame, ok := got.byFile[e.File]; ok {
				if m, ok := cache.LoadManifest(filepath.Dir(filepath.Dir(frame))); ok {
					got.manifests = append(got.manifests, m)
				}
			}
		case core.Failed:
			got.failures = append(got.failures, e)
		case core.Done:
			got.files, got.downloaded = e.Files, e.DownloadedByte
			got.elapsed, got.reason = e.Elapsed, e.Reason
		}
	}
	return got
}

func mib(n int64) string          { return fmt.Sprintf("%.1f MiB", float64(n)/(1<<20)) }
func secs(d time.Duration) string { return fmt.Sprintf("%.1fs", d.Seconds()) }

// liveTorrent gets the torrent under test, fetching it if no path was given.
func liveTorrent(t *testing.T) string {
	t.Helper()

	if *torrentPath != "" {
		return *torrentPath
	}

	dir := t.TempDir()
	path := filepath.Join(dir, "under-test.torrent")

	resp, err := http.Get(*torrentURL)
	if err != nil {
		t.Skipf("cannot fetch %s: %v", *torrentURL, err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Skipf("fetching %s: HTTP %d", *torrentURL, resp.StatusCode)
	}

	f, err := os.Create(path)
	if err != nil {
		t.Fatalf("create torrent file: %v", err)
	}
	defer f.Close()
	if _, err := io.Copy(f, resp.Body); err != nil {
		t.Fatalf("save torrent file: %v", err)
	}
	return path
}
