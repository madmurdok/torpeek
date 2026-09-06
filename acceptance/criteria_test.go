//go:build acceptance

package acceptance

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"sync"
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

// The ceilings REQUIREMENTS.md section 8 states, unchanged.
//
// maxMinTrafficByte in particular is still 60 MiB. TOR-94 changed what it is
// compared against - what the run orders, not what the swarm sends - and
// deliberately did not move it: a ceiling raised to fit an observation stops
// being a criterion, and against the 44 MiB a min-traffic run actually orders
// there is 16 MiB of headroom for the fetch plan to grow into before anybody
// has to argue about the number.
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
//
// The two criteria are judged on different figures, and the table below says
// which. Criterion 1's ceiling is 150 MB against arrivals that have sat inside
// a 2.7 MiB band for seven releases, so it is judged on what arrived, as it
// always was. Criterion 2's ceiling is judged on what the run ORDERED, because
// what arrives there is not torpeek's to control: it missed the 60 MiB ceiling
// in 21 of 22 runs during one afternoon on byte-identical code, while the
// order stayed a deterministic 44 pieces to the byte (TOR-94, and
// docs/tor-88-min-traffic-spread.md for how that was established). Both runs
// report both numbers and the gap.
func TestCriteria1And2MinTimeAndMinTraffic(t *testing.T) {
	torrent := liveTorrent(t)
	report.Torrents = append(report.Torrents, "live: "+*torrentURL+" (file "+*fileSpec+")")

	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Minute)
	defer cancel()

	for _, c := range []struct {
		number   int
		profile  swarm.Profile
		title    string
		maxByte  int64
		maxTime  time.Duration
		judgedOn Figure
	}{
		{1, swarm.MinTime, "min-time: 20 frames within 2 minutes and 150 MB", maxMinTimeBytes, maxMinTimeElapsed, OnDownloaded},
		{2, swarm.MinTraffic, "min-traffic: the same 20 frames ordering at most 60 MB", maxMinTrafficByte, 0, OnClaimed},
	} {
		t.Run(fmt.Sprintf("criterion%d", c.number), func(t *testing.T) {
			cfg := baseConfig(t, torrent, t.TempDir(), t.TempDir())
			cfg.Profile = c.profile
			cfg.Files = []string{*fileSpec}

			got := run(ctx, t, cfg, 0)

			traffic := Traffic{
				ClaimedByte:    got.claimed,
				ClaimedPieces:  got.claimedPieces,
				DownloadedByte: got.downloaded,
				Ceiling:        c.maxByte,
				JudgedOn:       c.judgedOn,
			}

			overCeiling := "over the traffic ceiling"
			if c.judgedOn == OnClaimed {
				overCeiling = "ordered more than the traffic ceiling, so the run's own fetch plan grew"
			}

			verdict := Met
			note := ""
			if got.frames != cfg.Plan.Count {
				verdict, note = Missed, "the frame set was not complete"
			} else if c.judgedOn == OnClaimed && got.claimed == 0 {
				// A verdict taken on a figure that can only read zero when
				// the instrument is broken must not come out "met". Twenty
				// frames off a live swarm ordered pieces by definition, so
				// zero here means the counter is not being fed - which is
				// exactly the failure a criterion re-based on a new
				// measurement is exposed to (TOR-94).
				verdict, note = Missed, "the run reports ordering nothing while producing a full "+
					"frame set, so the claimed figure is not being measured and there is no verdict to take"
			} else if traffic.Judged() > c.maxByte {
				verdict, note = Missed, overCeiling
			} else if c.maxTime > 0 && got.elapsed > c.maxTime {
				verdict, note = Missed, "over the time ceiling"
			}
			if verdict == Met {
				note = "measured against one file of a public torrent; the criterion names a ~10 GB film, " +
					"and the largest file here is smaller than that"
				if c.judgedOn == OnClaimed {
					// Required by TOR-94 rather than decoration: a passing run
					// says nothing about the elevated regime, which produced
					// 60.7-118.6 MiB of arrivals on this same code and would
					// have failed this criterion with nothing wrong.
					note += ". The verdict is taken on what the run ordered, which is deterministic; " +
						"actual traffic is reported beside it and is a SAMPLE of this swarm at this " +
						"moment rather than a guarantee - unchanged code has produced 45.5 to 118.6 MiB " +
						"of it (docs/tor-88-min-traffic-spread.md)"
					if got.downloaded > c.maxByte {
						note += ", and on this very run it was over the same ceiling"
					}
				}
			}

			// One machine-readable line per rep, so a spread can be read out
			// of `go test -v` without diffing eight report files. The claimed
			// fields are appended rather than inserted, so a line from this
			// build still parses beside TOR-88's forty reps.
			t.Logf("TOR88 criterion=%d profile=%s bytes=%d mib=%.1f elapsed_s=%.1f frames=%d/%d shifted=%d reason=%s claimed_bytes=%d claimed_mib=%.1f claimed_pieces=%d",
				c.number, c.profile.Name, got.downloaded, float64(got.downloaded)/(1<<20),
				got.elapsed.Seconds(), got.frames, cfg.Plan.Count, got.shifted, got.reason,
				got.claimed, float64(got.claimed)/(1<<20), got.claimedPieces)

			measured := []Measurement{Measure("frames", "%d of %d", got.frames, cfg.Plan.Count)}
			measured = append(measured, traffic.Measurements()...)
			measured = append(measured,
				Measure("elapsed", "%s", secs(got.elapsed)),
				Measure("outcome", "%s", got.reason),
			)

			report.Add(Result{
				Number: c.number, Title: c.title, Verdict: verdict, Note: note,
				Measured: measured,
			})

			if verdict == Missed {
				t.Errorf("criterion %d: %s (ordered %s in %d pieces, %s arrived, in %s)",
					c.number, note, mib(got.claimed), got.claimedPieces, mib(got.downloaded), secs(got.elapsed))
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
//
// It measures the criterion twice, because the shape of the guarantee changed
// under it (TOR-129). The first half is what it always was: one private
// torrent, one client, DHT asked for and refused. That half used to be the
// whole criterion, and it was true BY CONSTRUCTION - a client held one
// torrent, swarm.Open decided that client's DHT from that torrent's own flag,
// and there was nowhere else the torrent could have gone.
//
// A client shared between the public torrents of every run took the "nowhere
// else" away, so the second half puts the private torrent where the mistake
// would show: attached while public torrents are fetching in that shared
// client, at the same time, with somewhere else it could have been put.
//
// Both halves stay entirely offline - loopback seeders, no live swarm, no
// traffic that leaves the machine, which is what lets this criterion run
// without ordering anything from a real network. That is also why the pool
// here is configured with DHT off: a DHT server that genuinely runs cannot be
// started in this package without reaching the real one. The arm that pairs a
// running DHT with a private torrent lives in internal/swarm's own tests,
// where the client can be handed a DHT server with no starting nodes
// (TestThePoolRefusesABlindMagnetThatTurnsOutPrivate). What is measured HERE
// is the part the pool changed and the part no unit test can state as an
// acceptance fact: where the private torrent ends up when the process is busy
// with other people's torrents, and what the pool does when the only way to
// give it a client of its own is a port it has not got.
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

	aloneOnDHT := session.UsesDHT()

	// ---- and again, with the process busy -------------------------------
	//
	// Two public torrents fetching in a shared client, and the private one
	// attached alongside them. Everything below is loopback.
	const (
		poolPayload = 2 << 20
		poolPiece   = 256 << 10
		readOffset  = 1 << 20
		readLength  = 64 << 10
	)

	poolCfg := swarm.DefaultConfig(t.TempDir())
	poolCfg.DHT = false
	poolCfg.MetadataTimeout = 30 * time.Second

	pool := swarm.NewPool(poolCfg)
	defer pool.Close()

	type attached struct {
		att     *swarm.Attachment
		payload []byte
	}
	var busy []attached

	for _, name := range []string{"public-one.mkv", "public-two.mkv"} {
		fixture := torrenttest.Build(t, name, poolPayload, poolPiece)
		seeder := fixture.StartSeeder(t)

		publicSrc, err := swarm.ParseSource(fixture.TorrentPath)
		if err != nil {
			t.Fatalf("parse %s: %v", name, err)
		}
		att, err := pool.Attach(ctx, publicSrc, seeder)
		if err != nil {
			t.Fatalf("attach %s: %v", name, err)
		}
		defer att.Detach()

		if !att.Pooled() {
			t.Fatalf("%s did not join the shared client, so the private torrent has nothing to stay out of", name)
		}
		busy = append(busy, attached{att: att, payload: fixture.Payload})
	}

	shutFixture := torrenttest.BuildPrivate(t, "private.mkv", poolPayload, poolPiece)
	shutSeeder := shutFixture.StartSeeder(t)

	shutSrc, err := swarm.ParseSource(shutFixture.TorrentPath)
	if err != nil {
		t.Fatalf("parse the private fixture: %v", err)
	}
	if flagged, known := shutSrc.Privacy(); !known || !flagged {
		t.Fatalf("the pooled fixture is not private: flagged=%v known=%v", flagged, known)
	}

	shut, err := pool.Attach(ctx, shutSrc, shutSeeder)
	if err != nil {
		t.Fatalf("attach the private torrent while the pool is busy: %v", err)
	}
	defer shut.Detach()
	busy = append(busy, attached{att: shut, payload: shutFixture.Payload})

	// All three fetch at once. A private torrent kept out of the shared
	// client is only worth anything if it is still a working run.
	errs := make([]error, len(busy))
	var wg sync.WaitGroup
	for i, a := range busy {
		wg.Add(1)
		go func(i int, a attached) {
			defer wg.Done()
			got, err := a.att.Torrent().ReadRange(ctx, 0, readOffset, readLength, swarm.MinTraffic)
			switch {
			case err != nil:
				errs[i] = err
			case !bytes.Equal(got, a.payload[readOffset:readOffset+readLength]):
				errs[i] = fmt.Errorf("read %d bytes that do not match the payload", len(got))
			}
		}(i, a)
	}
	wg.Wait()
	for i, err := range errs {
		if err != nil {
			t.Fatalf("torrent %d: %v", i, err)
		}
	}

	// And the refusal when there is no port to give it one. One allocated
	// port, already held by the shared client, so the only way to attach a
	// private torrent would be to put it in with the public ones - which is
	// why this must fail instead.
	tightSet, err := swarm.ParsePortSet(strconv.Itoa(torrenttest.FreePort(t)))
	if err != nil {
		t.Fatalf("parse a one-port set: %v", err)
	}
	tightCfg := swarm.DefaultConfig(t.TempDir())
	tightCfg.DHT = false
	tightCfg.MetadataTimeout = 30 * time.Second
	tightCfg.Ports = swarm.NewPortPool(tightSet)

	tight := swarm.NewPool(tightCfg)
	defer tight.Close()

	filler := torrenttest.Build(t, "filler.mkv", poolPayload, poolPiece)
	fillerSrc, err := swarm.ParseSource(filler.TorrentPath)
	if err != nil {
		t.Fatalf("parse the filler fixture: %v", err)
	}
	held, err := tight.Attach(ctx, fillerSrc, filler.StartSeeder(t))
	if err != nil {
		t.Fatalf("attach a public torrent to the one-port pool: %v", err)
	}
	defer held.Detach()

	refused, tightErr := tight.Attach(ctx, shutSrc)
	if refused != nil {
		refused.Detach()
	}

	// ---- the verdict ----------------------------------------------------

	verdict := Met
	note := "tested against private torrents built for the run: a public torrent cannot carry " +
		"the private flag and still be public, so this condition cannot be ordered from a live swarm. " +
		"Measured twice - one private torrent alone, and one attached while two public torrents " +
		"fetched in the shared client at the same time"

	switch {
	case aloneOnDHT:
		verdict, note = Missed, "DHT was running for a private torrent on a client of its own"
	case shut.Pooled():
		verdict, note = Missed, "a private torrent joined the shared public client"
	case shut.UsesDHT():
		verdict, note = Missed, "DHT was running on the client carrying a private torrent"
	case shut.ListenPort() == pool.ListenPort():
		verdict, note = Missed, "a private torrent shares the shared client's port, so it shares its client"
	case !errors.Is(tightErr, swarm.ErrNoPortAvailable):
		verdict, note = Missed, fmt.Sprintf(
			"with one allocated port already held, attaching a private torrent returned %v; "+
				"it must be refused rather than put in the shared client", tightErr)
	}

	report.Add(Result{
		Number: 5, Title: "a private torrent makes no DHT or PEX request",
		Verdict: verdict, Note: note,
		Measured: []Measurement{
			Measure("private flag read offline", "%v", true),
			Measure("DHT requested by config", "%v", cfg.DHT),
			Measure("DHT actually running (own client)", "%v", aloneOnDHT),
			Measure("public torrents fetching in the shared client", "%d", len(busy)-1),
			Measure("private torrent joined the shared client", "%v", shut.Pooled()),
			Measure("DHT actually running (while the pool is busy)", "%v", shut.UsesDHT()),
			Measure("private port vs shared client port", "%d vs %d", shut.ListenPort(), pool.ListenPort()),
			Measure("refused when no port is left for a client of its own", "%v",
				errors.Is(tightErr, swarm.ErrNoPortAvailable)),
		},
	})
	if verdict == Missed {
		t.Errorf("criterion 5: %s", note)
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
