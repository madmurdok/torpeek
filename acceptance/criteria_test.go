//go:build acceptance

package acceptance

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/anacrolix/torrent/bencode"
	"github.com/anacrolix/torrent/metainfo"
	"github.com/madmurdok/torpeek/internal/cache"
	"github.com/madmurdok/torpeek/internal/core"
	"github.com/madmurdok/torpeek/internal/ffmpeg"
	"github.com/madmurdok/torpeek/internal/frames"
	"github.com/madmurdok/torpeek/internal/manifest"
	"github.com/madmurdok/torpeek/internal/swarm"
	"github.com/madmurdok/torpeek/internal/torrenttest"
)

const (
	maxMinTimeBytes   = 150 << 20
	maxMinTimeElapsed = 2 * time.Minute
	maxMinTrafficByte = 60 << 20
)

// Criterion1And2 share a run each, over one file of the live torrent.
//
// Each criterion is its own subtest so that one of them can be repeated on its
// own: `-run 'TestCriteria1And2MinTimeAndMinTraffic/criterion2'` costs one
// min-traffic run instead of both, which is what makes a spread of reps
// affordable (TOR-88). A plain `make acceptance` still runs both, in order,
// and measures each exactly as before - the subtest carries no state, and the
// data and output directories were already one fresh pair per criterion.
func TestCriteria1And2MinTimeAndMinTraffic(t *testing.T) {
	torrent := liveTorrent(t)
	report.Torrents = append(report.Torrents, "live: "+*torrentURL+" (file "+*fileSpec+")")

	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Minute)
	defer cancel()

	for _, c := range []struct {
		number  int
		profile swarm.Profile
		title   string
		maxByte int64
		maxTime time.Duration
	}{
		{1, swarm.MinTime, "min-time: 20 frames within 2 minutes and 150 MB", maxMinTimeBytes, maxMinTimeElapsed},
		{2, swarm.MinTraffic, "min-traffic: the same 20 frames within 60 MB", maxMinTrafficByte, 0},
	} {
		t.Run(fmt.Sprintf("criterion%d", c.number), func(t *testing.T) {
			cfg := baseConfig(t, torrent, t.TempDir(), t.TempDir())
			cfg.Profile = c.profile
			cfg.Files = []string{*fileSpec}

			got := run(ctx, t, cfg, 0)

			verdict := Met
			note := ""
			if got.frames != cfg.Plan.Count {
				verdict, note = Missed, "the frame set was not complete"
			} else if got.downloaded > c.maxByte {
				verdict, note = Missed, "over the traffic ceiling"
			} else if c.maxTime > 0 && got.elapsed > c.maxTime {
				verdict, note = Missed, "over the time ceiling"
			}
			if verdict == Met {
				note = "measured against one file of a public torrent; the criterion names a ~10 GB film, " +
					"and the largest file here is smaller than that"
			}

			// One machine-readable line per rep, so a spread can be read out
			// of `go test -v` without diffing eight report files.
			t.Logf("TOR88 criterion=%d profile=%s bytes=%d mib=%.1f elapsed_s=%.1f frames=%d/%d shifted=%d reason=%s",
				c.number, c.profile.Name, got.downloaded, float64(got.downloaded)/(1<<20),
				got.elapsed.Seconds(), got.frames, cfg.Plan.Count, got.shifted, got.reason)

			report.Add(Result{
				Number: c.number, Title: c.title, Verdict: verdict, Note: note,
				Measured: []Measurement{
					Measure("frames", "%d of %d", got.frames, cfg.Plan.Count),
					Measure("downloaded", "%s (ceiling %s)", mib(got.downloaded), mib(c.maxByte)),
					Measure("elapsed", "%s", secs(got.elapsed)),
					Measure("outcome", "%s", got.reason),
				},
			})

			if verdict == Missed {
				t.Errorf("criterion %d: %s (%s in %s)", c.number, note, mib(got.downloaded), secs(got.elapsed))
			}
		})
	}
}

// TestCriterion3RerunCostsNothing.
func TestCriterion3RerunCostsNothing(t *testing.T) {
	torrent := liveTorrent(t)

	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Minute)
	defer cancel()

	out, data := t.TempDir(), t.TempDir()
	cfg := baseConfig(t, torrent, out, data)
	cfg.Profile = swarm.MinTraffic
	cfg.Files = []string{*fileSpec}
	cfg.Plan = frames.Plan{Count: 4, Start: 0.1, End: 0.9}

	first := run(ctx, t, cfg, 0)
	if first.frames == 0 {
		t.Fatal("the first run produced nothing, so the rerun proves nothing")
	}

	// Nothing to talk to and no pieces to re-read: a rerun that went to the
	// swarm would fail rather than merely cost something.
	if err := os.RemoveAll(data); err != nil {
		t.Fatalf("remove pieces: %v", err)
	}
	second := cfg
	second.Swarm.DataDir = t.TempDir()
	second.Swarm.DHT = false
	second.Budget = core.Budget{MaxBytes: 8 << 20, MaxTime: 15 * time.Second, WarnAt: 0.8}

	again := run(ctx, t, second, 0)

	verdict := Met
	note := ""
	if again.downloaded != 0 || again.frames != first.frames {
		verdict, note = Missed, "the rerun went to the swarm"
	}

	report.Add(Result{
		Number: 3, Title: "a repeat run with the same parameters makes no network request",
		Verdict: verdict, Note: note,
		Measured: []Measurement{
			Measure("first run", "%d frames, %s", first.frames, mib(first.downloaded)),
			Measure("rerun", "%d frames, %s", again.frames, mib(again.downloaded)),
			Measure("rerun elapsed", "%s", secs(again.elapsed)),
		},
	})
	if verdict == Missed {
		t.Errorf("criterion 3: rerun downloaded %s", mib(again.downloaded))
	}
}

// TestCriterion4CancelAndResume.
func TestCriterion4CancelAndResume(t *testing.T) {
	torrent := liveTorrent(t)

	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Minute)
	defer cancel()

	out, data := t.TempDir(), t.TempDir()
	cfg := baseConfig(t, torrent, out, data)
	cfg.Profile = swarm.MinTraffic
	cfg.Files = []string{*fileSpec}
	cfg.Plan = frames.Plan{Count: 6, Start: 0.1, End: 0.9}

	const stopAfter = 2
	stopped := run(ctx, t, cfg, stopAfter)

	kept := 0
	stamps := map[string]time.Time{}
	for _, path := range stopped.paths {
		if info, err := os.Stat(path); err == nil && info.Size() > 0 {
			kept++
			stamps[path] = info.ModTime()
		}
	}

	time.Sleep(1100 * time.Millisecond)
	finished := run(ctx, t, cfg, 0)

	reused := 0
	for path, when := range stamps {
		if info, err := os.Stat(path); err == nil && info.ModTime().Equal(when) {
			reused++
		}
	}

	verdict := Met
	note := ""
	if kept == 0 {
		verdict, note = Missed, "a cancelled run kept nothing"
	} else if finished.frames != cfg.Plan.Count {
		verdict, note = Missed, "the second run did not finish the set"
	} else if reused != kept {
		verdict, note = Missed, "the second run rewrote frames it already had"
	}

	report.Add(Result{
		Number: 4, Title: "cancelling keeps frames; the next run fills the gaps",
		Verdict: verdict, Note: note,
		Measured: []Measurement{
			Measure("cancelled after", "%d frames, outcome %s", stopAfter, stopped.reason),
			Measure("kept on disk", "%d", kept),
			Measure("second run", "%d of %d frames, %s", finished.frames, cfg.Plan.Count, mib(finished.downloaded)),
			Measure("reused untouched", "%d of %d", reused, kept),
		},
	})
	if verdict == Missed {
		t.Errorf("criterion 4: %s", note)
	}
}

// TestCriterion5PrivateTorrentStaysOffDHT runs against a private torrent built
// here, because a public one cannot be private by definition.
func TestCriterion5PrivateTorrentStaysOffDHT(t *testing.T) {
	tools, err := ffmpeg.Locate()
	if err != nil {
		t.Skipf("no ffmpeg: %v", err)
	}
	_ = tools

	dir := t.TempDir()
	payload := filepath.Join(dir, "movie.mkv")
	if err := os.WriteFile(payload, make([]byte, 1<<20), 0o600); err != nil {
		t.Fatalf("write payload: %v", err)
	}

	info := metainfo.Info{PieceLength: 256 << 10}
	if err := info.BuildFromFilePath(dir); err != nil {
		t.Fatalf("build info: %v", err)
	}
	private := true
	info.Private = &private

	infoBytes, err := bencode.Marshal(info)
	if err != nil {
		t.Fatalf("marshal info: %v", err)
	}
	path := filepath.Join(t.TempDir(), "private.torrent")
	f, err := os.Create(path)
	if err != nil {
		t.Fatalf("create torrent: %v", err)
	}
	if err := (&metainfo.MetaInfo{InfoBytes: infoBytes}).Write(f); err != nil {
		t.Fatalf("write torrent: %v", err)
	}
	f.Close()

	src, err := swarm.ParseSource(path)
	if err != nil {
		t.Fatalf("parse: %v", err)
	}
	if flagged, known := src.Privacy(); !known || !flagged {
		t.Fatalf("the fixture is not private: flagged=%v known=%v", flagged, known)
	}

	cfg := swarm.DefaultConfig(t.TempDir())
	cfg.DHT = true // asked for, and must still be refused

	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()

	session, _, err := swarm.Open(ctx, cfg, src)
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	defer session.Close()

	verdict := Met
	note := "tested against a private torrent built for the run: a public torrent cannot carry " +
		"the private flag and still be public, so this condition cannot be ordered from a live swarm"
	if session.UsesDHT() {
		verdict, note = Missed, "DHT was running for a private torrent"
	}

	report.Add(Result{
		Number: 5, Title: "a private torrent makes no DHT or PEX request",
		Verdict: verdict, Note: note,
		Measured: []Measurement{
			Measure("private flag read offline", "%v", true),
			Measure("DHT requested by config", "%v", cfg.DHT),
			Measure("DHT actually running", "%v", session.UsesDHT()),
		},
	})
	if verdict == Missed {
		t.Error("criterion 5: DHT ran for a private torrent")
	}
}

// TestCriterion6MultiFile.
func TestCriterion6MultiFile(t *testing.T) {
	torrent := liveTorrent(t)

	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Minute)
	defer cancel()

	cfg := baseConfig(t, torrent, t.TempDir(), t.TempDir())
	cfg.Profile = swarm.MinTraffic
	cfg.Plan = frames.Plan{Count: 3, Start: 0.1, End: 0.9}
	cfg.Parallelism = 2

	got := run(ctx, t, cfg, 0)

	withManifest := 0
	for _, m := range got.manifests {
		if len(m.Frames) > 0 {
			withManifest++
		}
	}

	verdict := Met
	note := "three frames per file rather than twenty: the criterion is a set per file, " +
		"and twenty of each would cost a gigabyte to prove the same thing"
	if got.files < 2 || withManifest < got.files {
		verdict, note = Missed, "not every file got its own frames and manifest"
	}

	report.Add(Result{
		Number: 6, Title: "a multi-file torrent yields a frame set and manifest per video file",
		Verdict: verdict, Note: note,
		Measured: []Measurement{
			Measure("files processed", "%d", got.files),
			Measure("manifests written", "%d", withManifest),
			Measure("frames", "%d", got.frames),
			Measure("downloaded", "%s", mib(got.downloaded)),
		},
	})
	if verdict == Missed {
		t.Errorf("criterion 6: %s", note)
	}
}

// TestCriterion7ShiftedPointsAreMarked runs against a fixture with a hole,
// because a healthy public swarm has none to order.
func TestCriterion7ShiftedPointsAreMarked(t *testing.T) {
	tools, err := ffmpeg.Locate()
	if err != nil {
		t.Skipf("no ffmpeg: %v", err)
	}

	dir := t.TempDir()
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Minute)
	defer cancel()
	if _, err := tools.Run(ctx, "ffmpeg",
		"-hide_banner", "-loglevel", "error", "-y",
		"-f", "lavfi", "-i", "testsrc=size=640x360:rate=25:duration=60",
		"-c:v", "libx264", "-g", "50", "-pix_fmt", "yuv420p", "-b:v", "1500k",
		filepath.Join(dir, "movie.mkv"),
	); err != nil {
		t.Fatalf("render: %v", err)
	}

	fixture := torrenttest.BuildDir(t, dir, 256<<10)
	seeder := fixture.StartSeederWithHole(t, 0.42, 0.58)

	cfg := core.DefaultConfig(fixture.TorrentPath, t.TempDir(), t.TempDir())
	cfg.Swarm.DHT = false
	cfg.Swarm.MetadataTimeout = 10 * time.Second
	cfg.Swarm.Peers = []string{seeder}
	cfg.Profile = swarm.MinTraffic
	cfg.Plan = frames.Plan{Count: 3, Start: 0.1, End: 0.9}
	cfg.Parallelism = 1
	cfg.Budget = core.Budget{MaxBytes: 64 << 20, MaxTime: 3 * time.Minute, WarnAt: 0.8}

	got := run(ctx, t, cfg, 0)

	marked := 0
	for _, m := range got.manifests {
		for _, f := range m.Frames {
			if f.Shift == manifest.ShiftUnavailable {
				marked++
			}
		}
	}

	verdict := Met
	note := "tested against a seeder whose copy is deliberately wrong across the middle of the " +
		"file, so those pieces fail its own verification and it never offers them; a healthy " +
		"public swarm has no hole to order"
	if got.frames != cfg.Plan.Count {
		verdict, note = Missed, "the frame set was not complete despite the hole"
	} else if marked == 0 {
		verdict, note = Missed, "a point was moved but the manifest does not say so"
	}

	report.Add(Result{
		Number: 7, Title: "with pieces unavailable the set is still complete and shifts are marked",
		Verdict: verdict, Note: note,
		Measured: []Measurement{
			Measure("frames", "%d of %d", got.frames, cfg.Plan.Count),
			Measure("marked shifted in the manifest", "%d", marked),
			Measure("downloaded", "%s", mib(got.downloaded)),
		},
	})
	if verdict == Missed {
		t.Errorf("criterion 7: %s", note)
	}
	_ = cache.Version
}
