package core

import (
	"context"
	"os"
	"path/filepath"
	"strconv"
	"testing"
	"time"

	"github.com/anacrolix/torrent/metainfo"

	"github.com/madmurdok/torpeek/internal/bridge"
	"github.com/madmurdok/torpeek/internal/cache"
	"github.com/madmurdok/torpeek/internal/ffmpeg"
	"github.com/madmurdok/torpeek/internal/frames"
	"github.com/madmurdok/torpeek/internal/output"
	"github.com/madmurdok/torpeek/internal/swarm"
	"github.com/madmurdok/torpeek/internal/torrenttest"
)

// oneClipTorrent is multiFileTorrent's single-file sibling, and it hands back
// the fixture itself rather than only its paths: the magnet arm below needs
// one, and a magnet is the only source a .torrent fixture cannot stand in for
// (its metadata has to arrive from the swarm).
func oneClipTorrent(t *testing.T, tools ffmpeg.Tools, seconds int) torrenttest.Fixture {
	t.Helper()

	dir := t.TempDir()
	ctx, cancel := context.WithTimeout(context.Background(), 300*time.Second)
	defer cancel()

	if _, err := tools.Run(ctx, "ffmpeg",
		"-hide_banner", "-loglevel", "error", "-y",
		"-f", "lavfi", "-i", "testsrc=size=320x180:rate=25:duration="+strconv.Itoa(seconds),
		"-c:v", "libx264", "-g", "25", "-pix_fmt", "yuv420p", "-b:v", "150k",
		filepath.Join(dir, "episode.mkv"),
	); err != nil {
		t.Fatalf("render the clip: %v", err)
	}

	return torrenttest.BuildDir(t, dir, 256<<10)
}

// TestEveryRunLeavesALoadableTorrentBehind is TOR-73's acceptance criterion
// end to end, for both source kinds in one fixture: the run writes a
// .torrent into its own directory, that file loads back through the metainfo
// package, and its infohash is the torrent's.
//
// Both arms are here rather than only one because the two sources reach the
// same code by different routes and used to fail differently. A magnet has no
// .torrent at all, so the file has to be rebuilt from the info dictionary the
// swarm sent; a .torrent source had one, and what it did NOT have was an
// honest run.json - the web UI stages an upload in a temp directory it
// deletes when the run ends, so the recorded source named a file that was
// already gone. The Source assertions below are that lie's regression test.
//
// The two runs deliberately ask for different frame counts: ParamsKey hashes
// the count, so they write sibling result directories instead of the second
// run being served from the first one's cache and never touching a session.
func TestEveryRunLeavesALoadableTorrentBehind(t *testing.T) {
	tools := locateTools(t)
	fixture := oneClipTorrent(t, tools, 12)
	seeder := fixture.StartSeeder(t)

	root := t.TempDir()

	base := func(source string, count int) Config {
		cfg := DefaultConfig(source, root, t.TempDir())
		cfg.Swarm.DHT = false
		cfg.Swarm.MetadataTimeout = 30 * time.Second
		cfg.Swarm.Peers = []string{seeder}
		cfg.Profile = swarm.MinTraffic
		cfg.Plan = frames.Plan{Count: count, Start: 0.1, End: 0.9}
		cfg.Budget = Budget{MaxBytes: 64 << 20, MaxTime: 3 * time.Minute, WarnAt: 0.8}
		cfg.Parallelism = 1
		cfg.Bridge = bridge.DefaultConfig()
		return cfg
	}

	for _, arm := range []struct {
		name       string
		cfg        Config
		wantSource func(saved string) string
	}{
		{
			name: "from a .torrent file",
			cfg:  base(fixture.TorrentPath, 2),
			// The saved copy, not the path the file was read from: that path
			// is a temp file for an uploaded .torrent and is deleted as the
			// run ends.
			wantSource: func(saved string) string { return saved },
		},
		{
			name: "from a magnet",
			cfg:  base(fixture.Magnet(t), 3),
			// A magnet is recorded as typed - it is the whole torrent in one
			// line and pasting it back is how the run is repeated.
			wantSource: func(string) string { return fixture.Magnet(t) },
		},
	} {
		t.Run(arm.name, func(t *testing.T) {
			ctx, cancel := context.WithTimeout(context.Background(), 300*time.Second)
			defer cancel()

			events, err := NewEngine(tools).Run(ctx, arm.cfg)
			if err != nil {
				t.Fatalf("Run: %v", err)
			}

			var (
				metadata *MetadataReady
				done     *Done
				failures []Failed
			)
			for _, ev := range collect(t, events) {
				switch e := ev.(type) {
				case MetadataReady:
					metadata = &e
				case Done:
					done = &e
				case Failed:
					failures = append(failures, e)
				}
			}
			for _, f := range failures {
				t.Errorf("run reported failure: file %d, code %s: %v", f.File, f.Code, f.Err)
			}
			if metadata == nil || done == nil {
				t.Fatalf("run produced metadata=%v done=%v", metadata != nil, done != nil)
			}

			layout := output.Layout{Root: root, InfoHash: metadata.InfoHash, Params: ParamsKey(arm.cfg)}
			saved := layout.TorrentPath()

			// Announced, so a client can offer it without knowing anything
			// about the layout - and announced as the same file the layout
			// names, or the UI and the disk would disagree.
			if done.TorrentPath != saved {
				t.Errorf("Done.TorrentPath = %q, want %q", done.TorrentPath, saved)
			}

			// The criterion, and the reason it is worded as it is: the write
			// returning no error proves nothing. This loads the file back.
			mi, err := metainfo.LoadFromFile(saved)
			if err != nil {
				t.Fatalf("load the saved .torrent back: %v", err)
			}
			got := mi.HashInfoBytes().HexString()
			t.Logf("saved %s: infohash reloaded from disk = %s, run's torrent = %s",
				filepath.Base(saved), got, metadata.InfoHash)
			if got != metadata.InfoHash {
				t.Errorf("the saved file's infohash is %s, the run's torrent is %s", got, metadata.InfoHash)
			}

			// Loadable as a source, which is a stronger claim than parseable:
			// this is the same call a person handing the file back to torpeek
			// would go through.
			src, err := swarm.ParseSource(saved)
			if err != nil {
				t.Fatalf("the saved .torrent does not parse as a source: %v", err)
			}
			if hash, err := src.InfoHash(); err != nil || hash.HexString() != metadata.InfoHash {
				t.Errorf("the saved .torrent parses to infohash %v (err %v), want %s", hash, err, metadata.InfoHash)
			}

			record, ok := cache.LoadRun(layout.RunDir())
			if !ok {
				t.Fatal("no run record was written")
			}
			if want := arm.wantSource(saved); record.Source != want {
				t.Errorf("run.json records source %q, want %q", record.Source, want)
			}
			// Whatever it records has to still be there, which is the whole
			// point of the field: a source naming a deleted temp file is the
			// lie this arm exists to catch.
			if _, err := os.Stat(record.Source); err != nil && !isMagnet(record.Source) {
				t.Errorf("run.json records source %q, which is not on disk: %v", record.Source, err)
			}
		})
	}
}

// isMagnet is only the test's own way of telling which of the two shapes
// run.json holds; swarm.ParseSource is the real judge and is used above.
func isMagnet(source string) bool { return len(source) > 7 && source[:7] == "magnet:" }
