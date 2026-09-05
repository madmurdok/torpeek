package web

import (
	"encoding/json"
	"math"
	"net/http"
	"net/url"
	"testing"
	"time"

	"github.com/madmurdok/torpeek/internal/cache"
	"github.com/madmurdok/torpeek/internal/manifest"
)

// otherHash is a SECOND torrent, for the pairing the ticket actually exists
// for: two encodes of one film are two different files with two different
// durations, so they are two infohashes and there is no index a page could
// pair them by.
const otherHash = "4e1827ec34783a07358081c635a4e0beab1c11df"

// A params directory is core.ParamsKey's own shape - the first eight bytes of
// a sha256 in hex, so sixteen characters - and unlike GET
// /runs/{infohash}/files/{index}, which reads the directory listing, a
// comparison takes both of them from the request. So they go through
// validParams, and a test's params have to be the real length.
const (
	setSparse = "1111111111111111"
	setDense  = "2222222222222222"
	setOther  = "3333333333333333"
)

// compareManifest is one arm: a video file of a known duration whose capture
// points sit at the given timecodes, every one of them with a frame.
//
// The duration is the whole point - it is what a fraction is a fraction OF,
// and manifest.File.DurationMS is where it comes from. framesAt next door
// leaves it zero, which is exactly the "the container never said" case
// TestComparisonFallsBackToTimecodesWhenADurationIsUnknown covers.
func compareManifest(index int, path string, durationMS int64, times ...int64) manifest.Manifest {
	m := manifest.Manifest{
		Version: manifest.Version,
		File: manifest.File{
			Index: index, Path: path, Bytes: 1 << 20,
			DurationMS: durationMS, Container: "matroska",
		},
		Video: manifest.Video{Codec: "h264", Width: 1920, Height: 1080},
	}
	for i, at := range times {
		ms := at
		m.Frames = append(m.Frames, manifest.Frame{
			Index: i, RequestedMS: ms, ActualMS: &ms, Path: "placeholder",
			Width: 1920, Height: 1080,
		})
	}
	return m
}

// writeCompareSet writes one arm to disk: run.json plus the file's manifest
// and real frame files, through the same writer a live run uses.
func writeCompareSet(t *testing.T, root, infoHash, params, name string, index int, path string, durationMS int64, times ...int64) {
	t.Helper()
	writeCompareSetFrom(t, root, infoHash, params, name, index, path,
		compareManifest(index, path, durationMS, times...))
}

// writeCompareSetFrom is writeCompareSet for a manifest a test built itself -
// one holding a failed point, most often, which compareManifest cannot make.
func writeCompareSetFrom(t *testing.T, root, infoHash, params, name string, index int, path string, m manifest.Manifest) {
	t.Helper()

	run := cache.Run{
		Version: cache.Version, Tool: "test", CreatedAt: time.Now(),
		InfoHash: infoHash, Name: name,
		Plan:     cache.Plan{Count: len(m.Frames), Start: 0.05, End: 0.95, Profile: "min-traffic", Format: "jpeg"},
		Videos:   []cache.File{{Index: index, Path: path, Bytes: 1 << 20}},
		Selected: []int{index}, Complete: []int{index},
	}
	buildHoledRun(t, root, infoHash, params, run, m)
}

// getComparison calls GET /compare and decodes it.
func getComparison(t *testing.T, base, a, b string) (Comparison, int) {
	t.Helper()

	q := url.Values{"a": {a}, "b": {b}}
	resp := get(t, base, "/compare?"+q.Encode())
	if resp.StatusCode != http.StatusOK {
		return Comparison{}, resp.StatusCode
	}

	var body struct {
		Comparison Comparison `json:"comparison"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&body); err != nil {
		t.Fatalf("decode comparison: %v", err)
	}
	return body.Comparison, resp.StatusCode
}

func compareServer(t *testing.T, root string) string {
	t.Helper()

	cfg := DefaultConfig()
	cfg.OutputRoot = root
	fake := &fakeRun{}
	_, ts := newTestServerWithConfig(t, cfg, fake.runner)
	return ts.URL
}

func addr(infoHash, params string, index int) string {
	return setAddr{InfoHash: infoHash, Params: params, Index: index}.String()
}

// TestComparisonPairsByFractionOfDurationNotByIndexOrTimecode is TOR-109's
// second acceptance criterion, and the only test here that covers the pairing
// the ticket actually exists for: two encodes of ONE film, so two different
// files of two different lengths.
//
// The two arms are built so that all three plausible rules give DIFFERENT
// answers, which is what makes this a test rather than a demonstration:
//
//		arm A  100s, 5 points at 5%, 27.5%, 50%, 72.5%, 95%
//		arm B  200s, 3 points at 5%,        50%,        95%
//
//	  - BY FRACTION (what this must do): three positions, A's points 0, 2 and 4
//	    against B's 0, 1 and 2, each pair landing on the same fraction of its
//	    own film, so every drift is zero.
//	  - BY INDEX: A0-B0, A1-B1, A2-B2 - pairing 27.5% of one film with the
//	    midpoint of the other, which is the whole error the ticket names.
//	  - BY ABSOLUTE TIMECODE: A's 5s finds B's 10s and A's 95s finds B's 100s,
//	    giving two positions and calling the midpoint of the longer film the
//	    end of the shorter one.
//
// There is no fixture of two real encodes of one film at different durations,
// so this is a Go test over hand-built manifests - it proves the arithmetic,
// and deliberately nothing about the interface.
func TestComparisonPairsByFractionOfDurationNotByIndexOrTimecode(t *testing.T) {
	root := t.TempDir()
	writeCompareSet(t, root, detailHash, setDense, "Film (1080p)", 0, "film-1080p.mkv",
		100_000, 5_000, 27_500, 50_000, 72_500, 95_000)
	writeCompareSet(t, root, otherHash, setOther, "Film (720p)", 0, "film-720p.mkv",
		200_000, 10_000, 100_000, 190_000)

	base := compareServer(t, root)
	got, status := getComparison(t, base,
		addr(detailHash, setDense, 0), addr(otherHash, setOther, 0))
	if status != http.StatusOK {
		t.Fatalf("status %d, want 200", status)
	}

	if got.Basis != basisFraction {
		t.Fatalf("basis = %q, want %q - both arms know their duration", got.Basis, basisFraction)
	}
	if len(got.Positions) != 3 {
		t.Fatalf("positions = %d, want 3: %+v", len(got.Positions), got.Positions)
	}

	want := []struct {
		aIndex, bIndex   int
		aTimeMS, bTimeMS int64
		fraction         float64
	}{
		{0, 0, 5_000, 10_000, 0.05},
		{2, 1, 50_000, 100_000, 0.50},
		{4, 2, 95_000, 190_000, 0.95},
	}
	for i, w := range want {
		p := got.Positions[i]
		if p.A.Index != w.aIndex || p.B.Index != w.bIndex {
			t.Errorf("position %d pairs frame %d with frame %d, want %d with %d",
				i, p.A.Index, p.B.Index, w.aIndex, w.bIndex)
		}
		if p.A.TimeMS != w.aTimeMS || p.B.TimeMS != w.bTimeMS {
			t.Errorf("position %d is at %dms / %dms, want %dms / %dms",
				i, p.A.TimeMS, p.B.TimeMS, w.aTimeMS, w.bTimeMS)
		}
		if math.Abs(p.Fraction-w.fraction) > 1e-9 {
			t.Errorf("position %d fraction = %v, want %v", i, p.Fraction, w.fraction)
		}
		if p.Drift > 1e-9 {
			t.Errorf("position %d drift = %v, want 0 - the two points are the same "+
				"fraction of their own films", i, p.Drift)
		}
	}

	// The two points of A that sit between B's - 27.5% and 72.5% of the film,
	// where the shorter set simply has nothing - are counted rather than
	// paired with whatever happened to be nearest.
	if got.A.Unpaired != 2 || got.B.Unpaired != 0 {
		t.Errorf("unpaired = %d on A and %d on B, want 2 and 0", got.A.Unpaired, got.B.Unpaired)
	}
	if got.A.DurationMS != 100_000 || got.B.DurationMS != 200_000 {
		t.Errorf("durations = %d / %d, want 100000 / 200000 - the arms are two "+
			"different-length encodes", got.A.DurationMS, got.B.DurationMS)
	}
}

// TestComparisonOfTwoSetsOfOneTorrentPairsEveryPointOfTheSparserSet is the
// other pairing the user asked for, and the one verifiable on real data: one
// infohash, one file, two params directories - a run of 8 against a run of 20.
//
// The timecodes are the real ones off the fixture tree's own manifests
// (e4d37e62's 351e62900fd105c9 and 4b0e778dfe6ad5e2, file 6, Sintel's
// 2048-stereo mp4), so what this pins is what the browser is looking at. Both
// sets pin the window's edges, which is frames.Plan's own property (TOR-69):
// the first and last point coincide EXACTLY and every point in between does
// not, which is why even one torrent's own two sets need the pairing rule
// rather than a lookup by timecode.
func TestComparisonOfTwoSetsOfOneTorrentPairsEveryPointOfTheSparserSet(t *testing.T) {
	root := t.TempDir()
	const (
		path = "Sintel/sintel-2048-stereo.mp4"
		dur  = int64(888_064)
	)
	writeCompareSet(t, root, detailHash, setSparse, "Sintel", 6, path, dur,
		43666, 155833, 271625, 382916, 490958, 613791, 720458, 833625)
	writeCompareSet(t, root, detailHash, setDense, "Sintel", 6, path, dur,
		43666, 85833, 126333, 162083, 211750, 254666, 289333, 337458, 380541, 419958,
		462208, 505791, 549125, 590708, 632125, 673708, 710041, 757625, 791958, 833625)

	base := compareServer(t, root)
	got, status := getComparison(t, base,
		addr(detailHash, setSparse, 6), addr(detailHash, setDense, 6))
	if status != http.StatusOK {
		t.Fatalf("status %d, want 200", status)
	}

	if len(got.Positions) != 8 {
		t.Fatalf("positions = %d, want 8 - one per point of the sparser set: %+v",
			len(got.Positions), got.Positions)
	}
	if got.A.Unpaired != 0 {
		t.Errorf("A.unpaired = %d, want 0 - every one of the eight points found a partner", got.A.Unpaired)
	}
	if got.B.Unpaired != 12 {
		t.Errorf("B.unpaired = %d, want 12 - the twenty-point set has twelve points "+
			"the eight-point set cannot answer for", got.B.Unpaired)
	}

	// Ordered by where in the film the position sits, and no point of either
	// arm is shown twice: mutual nearest cannot cross on a line, so the two
	// arms' own orders agree with the flipbook's.
	for i, p := range got.Positions {
		if i > 0 {
			prev := got.Positions[i-1]
			if p.A.TimeMS <= prev.A.TimeMS || p.B.TimeMS <= prev.B.TimeMS {
				t.Fatalf("position %d (%dms / %dms) does not follow %d (%dms / %dms)",
					i, p.A.TimeMS, p.B.TimeMS, i-1, prev.A.TimeMS, prev.B.TimeMS)
			}
		}
		if p.A.URL == "" || p.B.URL == "" {
			t.Errorf("position %d has an arm with no frame: %+v", i, p)
		}
	}

	// The window's own edges, which every regeneration lands on exactly.
	first, last := got.Positions[0], got.Positions[7]
	if first.A.TimeMS != 43666 || first.B.TimeMS != 43666 || first.Drift != 0 {
		t.Errorf("the first position is %dms / %dms drift %v, want 43666 / 43666 / 0",
			first.A.TimeMS, first.B.TimeMS, first.Drift)
	}
	if last.A.TimeMS != 833625 || last.B.TimeMS != 833625 || last.Drift != 0 {
		t.Errorf("the last position is %dms / %dms drift %v, want 833625 / 833625 / 0",
			last.A.TimeMS, last.B.TimeMS, last.Drift)
	}

	// And the middle ones do not coincide, which is the half of TOR-69's
	// property that makes the pairing rule necessary at all rather than a
	// lookup by timecode.
	if got.Positions[3].A.TimeMS == got.Positions[3].B.TimeMS {
		t.Error("an interior position's two points coincide, so this fixture no longer " +
			"exercises the pairing at all")
	}
}

// TestComparisonKeepsAPositionWhoseArmCapturedNothing is the third acceptance
// criterion's other half: a point that FAILED still makes a position.
//
// Since TOR-118 a run records the points that produced nothing and why, and
// since TOR-110 the grid draws them. A comparison does the same, and it must:
// the moment is one both runs planned, so it is comparable, and "this encode's
// run could not get this frame" is the single most useful thing a person
// comparing a holed run against a whole one can be told. Dropping the position
// would hide it.
//
// This is deliberately the opposite decision from the one above it, where a
// point with no PARTNER is not a position at all - the two cases look alike
// and are not, and both are decided rather than left to break.
func TestComparisonKeepsAPositionWhoseArmCapturedNothing(t *testing.T) {
	root := t.TempDir()
	const (
		path = "001/movie.mkv"
		dur  = int64(180_000)
	)
	writeCompareSet(t, root, detailHash, setSparse, "001 (whole)", 0, path, dur,
		9_000, 90_000, 171_000)

	holed := compareManifest(0, path, dur, 9_000, 90_000, 171_000)
	holed.Frames[1] = manifest.Frame{
		Index: 1, RequestedMS: 90_000, Shift: manifest.ShiftFailed, Error: "read_stalled",
	}
	writeCompareSetFrom(t, root, otherHash, setOther, "001 (holed)", 0, path, holed)

	base := compareServer(t, root)
	got, status := getComparison(t, base,
		addr(detailHash, setSparse, 0), addr(otherHash, setOther, 0))
	if status != http.StatusOK {
		t.Fatalf("status %d, want 200", status)
	}

	if len(got.Positions) != 3 {
		t.Fatalf("positions = %d, want 3 - the failed point is still a position: %+v",
			len(got.Positions), got.Positions)
	}
	if got.A.Unpaired != 0 || got.B.Unpaired != 0 {
		t.Errorf("unpaired = %d / %d, want 0 / 0 - a failed point has a partner like any other",
			got.A.Unpaired, got.B.Unpaired)
	}

	middle := got.Positions[1]
	if middle.A.URL == "" {
		t.Errorf("the whole run's arm lost its frame at the middle position: %+v", middle.A)
	}
	if middle.B.URL != "" {
		t.Errorf("the holed run's arm has a URL at a point that captured nothing: %+v", middle.B)
	}
	if middle.B.Error != "read_stalled" {
		t.Errorf("the holed arm's reason = %q, want \"read_stalled\" - the position is "+
			"kept precisely so the reason reaches the page", middle.B.Error)
	}
	if middle.B.TimeMS != 90_000 {
		t.Errorf("the holed arm's point is at %dms, want 90000 - where it was planned, "+
			"since nowhere else is true for it", middle.B.TimeMS)
	}
}

// TestComparisonFallsBackToTimecodesWhenADurationIsUnknown covers the one
// input the fraction rule cannot be applied to: a file whose container never
// said how long it is, which is a real case (a sequential run, see
// manifest.Cost.Sequential).
//
// The fallback is announced rather than silent, because it is right for two
// captures of one file and wrong for two encodes of different lengths, and
// only the person looking knows which they asked for.
func TestComparisonFallsBackToTimecodesWhenADurationIsUnknown(t *testing.T) {
	root := t.TempDir()
	const path = "clip.mkv"
	writeCompareSet(t, root, detailHash, setSparse, "Clip", 0, path, 60_000, 3_000, 30_000, 57_000)
	// Duration zero: the container carried none.
	writeCompareSet(t, root, otherHash, setOther, "Clip again", 0, path, 0, 3_100, 29_900, 57_200)

	base := compareServer(t, root)
	got, status := getComparison(t, base,
		addr(detailHash, setSparse, 0), addr(otherHash, setOther, 0))
	if status != http.StatusOK {
		t.Fatalf("status %d, want 200", status)
	}

	if got.Basis != basisTime {
		t.Fatalf("basis = %q, want %q - one arm has no duration to take a fraction of",
			got.Basis, basisTime)
	}
	if len(got.Positions) != 3 {
		t.Fatalf("positions = %d, want 3: %+v", len(got.Positions), got.Positions)
	}
	for i, p := range got.Positions {
		if p.Fraction != 0 || p.Drift != 0 {
			t.Errorf("position %d reports fraction %v drift %v; with no duration there is "+
				"no fraction, and a made-up one is worse than none", i, p.Fraction, p.Drift)
		}
	}
	if got.Positions[1].A.TimeMS != 30_000 || got.Positions[1].B.TimeMS != 29_900 {
		t.Errorf("the middle position is %dms / %dms, want 30000 / 29900",
			got.Positions[1].A.TimeMS, got.Positions[1].B.TimeMS)
	}
}

// TestCompareRefusesWhatCannotNameTwoResultSets covers the three ways a
// request is turned away, and the split between them: an arm that cannot be
// read is a 400 about the request, an arm that reads fine and names nothing on
// disk is a 404, and two arms that are the same set is a 400 because there is
// nothing to flip between.
//
// The traversal cases matter beyond tidiness: the arms are the only strings a
// request puts into a path here, exactly as the infohash is for GET
// /runs/{infohash}/files/{index} (see files.go).
func TestCompareRefusesWhatCannotNameTwoResultSets(t *testing.T) {
	root := t.TempDir()
	writeCompareSet(t, root, detailHash, setSparse, "Sintel", 6, "Sintel/sintel.mp4", 888_064, 43666, 833625)
	writeCompareSet(t, root, detailHash, setDense, "Sintel", 6, "Sintel/sintel.mp4", 888_064, 43666, 400000, 833625)
	base := compareServer(t, root)

	good := addr(detailHash, setSparse, 6)
	cases := []struct {
		name, a, b string
		want       int
	}{
		{"a missing arm", good, "", http.StatusBadRequest},
		{"an arm with too few parts", good, detailHash + ":" + setDense, http.StatusBadRequest},
		{"an arm whose infohash is not a digest", good, "../../etc:" + setDense + ":6", http.StatusBadRequest},
		{"an arm whose params is not a key", good, detailHash + ":../../etc:6", http.StatusBadRequest},
		{"an arm whose params is the wrong length", good, detailHash + ":aaaa1111:6", http.StatusBadRequest},
		{"an arm whose index is not a number", good, detailHash + ":" + setDense + ":six", http.StatusBadRequest},
		{"an arm whose index is negative", good, detailHash + ":" + setDense + ":-1", http.StatusBadRequest},
		{"both arms the same set", good, good, http.StatusBadRequest},
		{"an arm naming a set that was never written", good, detailHash + ":ffffffffffffffff:6", http.StatusNotFound},
		{"an arm naming a file the set does not hold", good, detailHash + ":" + setDense + ":3", http.StatusNotFound},
		{"an arm naming an unknown torrent", good, "0000000000000000000000000000000000000000:" + setDense + ":6", http.StatusNotFound},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			if _, status := getComparison(t, base, c.a, c.b); status != c.want {
				t.Errorf("status %d, want %d", status, c.want)
			}
		})
	}

	// And the two real sets do compare, so the cases above are refusals
	// rather than a comparison that never works.
	if _, status := getComparison(t, base, good, addr(detailHash, setDense, 6)); status != http.StatusOK {
		t.Errorf("two real sets: status %d, want 200", status)
	}
}

// TestCompareSetsOffersOnlyWhatCanBeCompared is the picker's own contract: an
// option a person can choose has to be one that works when it is chosen, so a
// params directory whose manifest is unreadable is not offered rather than
// offered and answering 404.
func TestCompareSetsOffersOnlyWhatCanBeCompared(t *testing.T) {
	root := t.TempDir()
	writeCompareSet(t, root, detailHash, setSparse, "Sintel", 6, "Sintel/sintel.mp4", 888_064, 43666, 833625)
	writeCompareSet(t, root, otherHash, setOther, "001", 0, "001/movie.mkv", 180_023, 9000, 171000)

	// A run.json with no manifest under it: a run that was accepted and got
	// nowhere. Real, and not comparable.
	empty := cache.Run{
		Version: cache.Version, Tool: "test", CreatedAt: time.Now(),
		InfoHash: detailHash, Name: "Sintel",
		Plan:     cache.Plan{Count: 20},
		Videos:   []cache.File{{Index: 6, Path: "Sintel/sintel.mp4", Bytes: 1 << 20}},
		Selected: []int{6},
	}
	if err := cache.SaveRun(runDirFor(t, root, detailHash, setDense), empty); err != nil {
		t.Fatalf("save the empty run: %v", err)
	}

	base := compareServer(t, root)
	resp := get(t, base, "/compare/sets")
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("status %d, want 200", resp.StatusCode)
	}
	var body struct {
		Sets []CompareSet `json:"sets"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&body); err != nil {
		t.Fatalf("decode sets: %v", err)
	}

	if len(body.Sets) != 2 {
		t.Fatalf("sets = %d, want 2 - the run with no manifest is not offered: %+v",
			len(body.Sets), body.Sets)
	}
	// Ordered by torrent name, so "001" comes before "Sintel".
	if body.Sets[0].Name != "001" || body.Sets[1].Name != "Sintel" {
		t.Errorf("sets are named %q, %q; want them ordered by name",
			body.Sets[0].Name, body.Sets[1].Name)
	}

	sintel := body.Sets[1]
	if sintel.Addr != addr(detailHash, setSparse, 6) {
		t.Errorf("addr = %q, want %q - the arm string is built by the server, not the page",
			sintel.Addr, addr(detailHash, setSparse, 6))
	}
	if sintel.Points != 2 || sintel.Frames != 2 || sintel.DurationMS != 888_064 {
		t.Errorf("set = %+v, want 2 points, 2 frames, duration 888064", sintel)
	}

	// Every offered option really does compare.
	if _, status := getComparison(t, base, body.Sets[0].Addr, body.Sets[1].Addr); status != http.StatusOK {
		t.Errorf("comparing the two offered sets: status %d, want 200", status)
	}
}

// runDirFor is where one result set's run.json lives, for the one test that
// writes a run record with nothing under it.
func runDirFor(t *testing.T, root, infoHash, params string) string {
	t.Helper()

	dir := fileDirOf(t, root, infoHash, params, 0, "placeholder/placeholder.mkv")
	// FileDir is <run dir>/<slug>; the run directory is its parent.
	return dir[:len(dir)-len("/00-placeholder")]
}

// TestPairNearestIsSymmetric pins the property the mutual rule was chosen for,
// on the arithmetic itself rather than through HTTP: naming the arms the other
// way round gives the same flipbook, with the pairs simply mirrored.
//
// A "for each point of A take the nearest point of B" rule fails this - with
// 8 points against 20 it gives 8 pairs read one way and 20 the other - and
// that is not a detail: a page whose two selects are freely swappable would
// otherwise show a different number of positions depending on which set a
// person happened to pick first.
func TestPairNearestIsSymmetric(t *testing.T) {
	sparse := []float64{0.05, 0.35, 0.65, 0.95}
	dense := []float64{0.05, 0.16, 0.28, 0.39, 0.50, 0.61, 0.73, 0.84, 0.95}
	tolerance := pairingTolerance(sparse, dense, fractionFloor)

	forward := pairNearest(sparse, dense, tolerance)
	backward := pairNearest(dense, sparse, tolerance)

	if len(forward) != len(backward) {
		t.Fatalf("%d pairs one way round and %d the other: %v vs %v",
			len(forward), len(backward), forward, backward)
	}
	for i := range forward {
		if forward[i][0] != backward[i][1] || forward[i][1] != backward[i][0] {
			t.Errorf("pair %d is %v forward and %v backward", i, forward[i], backward[i])
		}
	}
	if len(forward) != len(sparse) {
		t.Errorf("%d pairs, want %d - every point of the sparser arm has a partner: %v",
			len(forward), len(sparse), forward)
	}
}

// TestPairNearestRefusesAPartnerBeyondTheCoarserArmsOwnStep is the tolerance,
// which is what stops mutual nearest from pairing two points that are simply
// the only ones left. Without it a one-point set at the very start of a film
// and a one-point set at the very end are each other's nearest, and would be
// shown as the same moment.
func TestPairNearestRefusesAPartnerBeyondTheCoarserArmsOwnStep(t *testing.T) {
	start := []float64{0.05}
	end := []float64{0.95}
	if got := pairNearest(start, end, pairingTolerance(start, end, fractionFloor)); len(got) != 0 {
		t.Errorf("pairs = %v, want none: the two points are nine tenths of a film apart", got)
	}

	// The same two arms with a point each near the same moment do pair, so
	// the refusal above is about the distance rather than about single-point
	// arms.
	near := []float64{0.052}
	if got := pairNearest(start, near, pairingTolerance(start, near, fractionFloor)); len(got) != 1 {
		t.Errorf("pairs = %v, want one: the two points are two thousandths apart", got)
	}
}
