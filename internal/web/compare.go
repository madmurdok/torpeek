package web

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"

	"github.com/madmurdok/torpeek/internal/cache"
	"github.com/madmurdok/torpeek/internal/output"
)

// Comparing two runs by FLIPPING, not by tiling (TOR-109).
//
// The design survey's finding is the whole shape of this file: the same frame,
// in the same screen position, one keypress apart, makes a difference visible
// that a side-by-side grid hides - the eye compares against its own
// afterimage rather than across a gap. So the server's job here is not "two
// grids of frames"; it is a list of POSITIONS, each holding one frame from
// each arm, that a page can flip between without moving anything.
//
// Two pairings are supported and they are the same arithmetic:
//
//   - two result sets of ONE torrent (one infohash, two params directories -
//     n=6 against n=20, min-traffic against fastest). Their durations are
//     identical, so a fraction of duration and an absolute timecode order the
//     points the same way.
//   - two runs of DIFFERENT torrents (two encodes of one film, so two
//     infohashes and two durations). This is the slow.pics case, and it is
//     why the pairing is by fraction of duration rather than by index: the
//     twelfth point of a 100-minute encode and the twelfth of a 96-minute one
//     are not the same moment, and the manifests' own timecodes are what make
//     that answerable rather than guessed.
//
// Arbitrary directories outside the output tree are deliberately NOT
// addressable. An arm is (infohash, params, file index) - the same three
// things that already address a frame for a DELETE - so nothing here needs a
// naming scheme for a foreign results tree, and nothing here reads one.

// errNoSuchSet marks an arm that names no result set on disk.
var errNoSuchSet = errors.New("web: no such result set")

// setAddr addresses one result set's one video file: the arm of a comparison.
//
// It is spelled "<infohash>:<params>:<index>" in a query parameter rather
// than laid out in the path, because a comparison has TWO of them and a path
// can only be one address. Every component is checked before it reaches the
// filesystem - validInfoHash and validParams for the two directory names,
// Atoi for the index - which is the same gate the DELETE on a frame goes
// through (see listing.go's validInfoHash for why the infohash in particular
// has to be provably a digest).
type setAddr struct {
	InfoHash string
	Params   string
	Index    int
}

func (a setAddr) String() string {
	return a.InfoHash + ":" + a.Params + ":" + strconv.Itoa(a.Index)
}

// parseSetAddr reads one arm's address, refusing anything that is not exactly
// a digest, a params key and a non-negative index.
func parseSetAddr(s string) (setAddr, bool) {
	parts := strings.Split(strings.TrimSpace(s), ":")
	if len(parts) != 3 {
		return setAddr{}, false
	}
	if !validInfoHash(parts[0]) || !validParams(parts[1]) {
		return setAddr{}, false
	}
	index, err := strconv.Atoi(parts[2])
	if err != nil || index < 0 {
		return setAddr{}, false
	}
	return setAddr{InfoHash: parts[0], Params: parts[1], Index: index}, true
}

// resultSet is one video file's frames inside one params directory, plus the
// little of its run and its media that a comparison needs to reason about it.
//
// It is what both readers of a set on disk go through - fileDetail, which
// gathers every set of one torrent, and a comparison, which wants two named
// ones - so the rule for which frames exist is written once. That rule is
// fileDetail's own and its doc comment is where it is argued: a frame whose
// file has gone from disk is dropped silently (a person's own delete, never
// something the engine reported), and a point the engine itself recorded as
// failed is KEPT with no URL and its reason (TOR-118).
type resultSet struct {
	Params string
	// Path is the video file's path inside the torrent, and Name the
	// torrent's own name - both from run.json, which is what says where to
	// look rather than this package re-deriving a layout of its own.
	Path string
	Name string
	// Count is the plan's frame count: what was asked for.
	Count int
	// DurationMS is the video's own length, from the manifest. Zero when the
	// container never said - a sequential run (manifest.Cost.Sequential) is
	// the real case - which is what makes a fraction of it meaningless and
	// sends a comparison to its absolute-timecode fallback.
	DurationMS    int64
	Width, Height int
	Frames        []FrameRef
	// Reach is how much of this file the run ordered (TOR-111), nil when the
	// record has nothing to say. Computed here because this is where the run
	// record, the manifest's piece length and the file's own offset are all
	// already in hand.
	Reach *Reach
}

// loadResultSet reads one set's frames for one file, or reports ok=false when
// there is no such set: no run.json, a run that does not hold that file, no
// manifest, or a manifest with nothing at all to say about any planned point.
func (s *Server) loadResultSet(infoHash, params string, index int) (resultSet, bool) {
	if s.cfg.OutputRoot == "" {
		return resultSet{}, false
	}
	layout := output.Layout{Root: s.cfg.OutputRoot, InfoHash: infoHash, Params: params}

	run, ok := cache.LoadRun(layout.RunDir())
	if !ok {
		return resultSet{}, false
	}
	path, ok := videoPath(run, index)
	if !ok {
		return resultSet{}, false
	}
	m, ok := cache.LoadManifest(layout.FileDir(index, path))
	if !ok {
		return resultSet{}, false
	}

	set := resultSet{
		Params: params, Path: path, Name: run.Name, Count: run.Plan.Count,
		DurationMS: m.File.DurationMS,
		Width:      m.Video.Width, Height: m.Video.Height,
	}
	if file, ok := videoEntry(run, index); ok {
		set.Reach = reachOf(run.Claimed, file, m.Torrent.PieceLength)
	}
	for _, f := range m.Frames {
		at := f.RequestedMS
		if f.ActualMS != nil {
			at = *f.ActualMS
		}

		if f.Path == "" {
			// The engine itself reported this point as failed -
			// manifest.Frame.Error says why (TOR-118). There is nothing to
			// stat and nothing to publish, but the reason is real data a
			// finished run's page should not lose, so it is kept rather than
			// dropped like the disk-only case below.
			set.Frames = append(set.Frames, FrameRef{
				TimeMS: at, Shift: string(f.Shift), Error: f.Error,
				Params: params, Index: f.Index,
			})
			continue
		}
		if info, err := os.Stat(f.Path); err != nil || info.Size() == 0 {
			// The manifest thought this point succeeded and the disk
			// disagrees - almost always a person's own delete
			// (TestFileDetailLeavesOutAFrameGoneFromDisk). That is not
			// something the engine ever reported failing, so unlike the
			// branch above it stays silently dropped: manifest.Frame.Error is
			// empty for it, and inventing a reason nobody recorded would be
			// worse than saying nothing.
			continue
		}
		set.Frames = append(set.Frames, FrameRef{
			TimeMS: at, Shift: string(f.Shift), URL: s.files.publish(f.Path),
			Params: params, Index: f.Index, Width: f.Width, Height: f.Height,
		})
	}

	if len(set.Frames) == 0 {
		return resultSet{}, false
	}
	return set, true
}

// CompareSet is one row of GET /compare/sets: a result set a comparison can
// be pointed at, with enough to label it in a picker.
//
// It is a separate request from GET /runs on purpose. That listing is fetched
// on every page load and deliberately opens nothing but run.json, one per
// directory (see walkRuns); this one opens each selected file's manifest as
// well, because an option offered in a picker has to be one that WORKS when
// it is picked - a set whose manifest is unreadable is not offered rather than
// offered and answering 404. It is fetched once, when a comparison is opened.
type CompareSet struct {
	InfoHash string `json:"infohash"`
	Params   string `json:"params"`
	Index    int    `json:"index"`
	// Name is the torrent's own name and Path the video file's path inside
	// it - together, what a person recognises a set by. Params is the other
	// half of its address and is shown too, because the same file of the
	// same torrent captured twice differs in nothing else.
	Name string `json:"name"`
	Path string `json:"path"`
	// Count is the plan's frame count, Points how many planned points this
	// set has anything to say about (a failed one included, TOR-118) and
	// Frames how many of those actually have a picture. All three, because a
	// picker that shows only Count would offer "20 frames" for a set holding
	// five.
	Count      int   `json:"count"`
	Points     int   `json:"points"`
	Frames     int   `json:"frames"`
	DurationMS int64 `json:"duration_ms"`
	// Addr is the arm string a compare request wants, built here rather than
	// assembled by the page: the format is this file's business, and a client
	// that spelled it itself would be a second place it is defined.
	Addr string `json:"addr"`
}

// comparableSets lists every result set under the output root, newest plan
// first is not promised: they come back ordered by torrent name, then file,
// then params, which is the order a picker wants rather than the order a
// directory walk happens to produce.
func (s *Server) comparableSets() []CompareSet {
	root := s.cfg.OutputRoot
	if root == "" {
		return nil
	}
	hashDirs, err := os.ReadDir(root)
	if err != nil {
		return nil
	}

	var out []CompareSet
	for _, hashDir := range hashDirs {
		if !hashDir.IsDir() || !validInfoHash(hashDir.Name()) {
			continue
		}
		infoHash := hashDir.Name()
		paramDirs, err := os.ReadDir(filepath.Join(root, infoHash))
		if err != nil {
			continue
		}
		for _, paramDir := range paramDirs {
			if !paramDir.IsDir() {
				continue
			}
			params := paramDir.Name()
			run, ok := cache.LoadRun(output.Layout{Root: root, InfoHash: infoHash, Params: params}.RunDir())
			if !ok {
				continue
			}
			for _, index := range selectedIndices(run) {
				set, ok := s.loadResultSet(infoHash, params, index)
				if !ok {
					continue
				}
				captured := 0
				for _, f := range set.Frames {
					if f.URL != "" {
						captured++
					}
				}
				addr := setAddr{InfoHash: infoHash, Params: params, Index: index}
				out = append(out, CompareSet{
					InfoHash: infoHash, Params: params, Index: index,
					Name: set.Name, Path: set.Path,
					Count: set.Count, Points: len(set.Frames), Frames: captured,
					DurationMS: set.DurationMS, Addr: addr.String(),
				})
			}
		}
	}

	sort.SliceStable(out, func(i, j int) bool {
		if out[i].Name != out[j].Name {
			return out[i].Name < out[j].Name
		}
		if out[i].Index != out[j].Index {
			return out[i].Index < out[j].Index
		}
		return out[i].Params < out[j].Params
	})
	return out
}

// selectedIndices is which of a run's files a directory has ever been asked
// for, which is what could have a manifest in it. It follows
// cache.Run.SelectedCount's own fallback: a nil Selected does not mean
// nothing was asked for, it means the record predates the field and cannot
// say, so every video file is a candidate then.
func selectedIndices(run cache.Run) []int {
	if run.Selected == nil {
		out := make([]int, 0, len(run.Videos))
		for _, v := range run.Videos {
			out = append(out, v.Index)
		}
		return out
	}
	out := append([]int(nil), run.Selected...)
	sort.Ints(out)
	return out
}

// ComparisonArm is one side of a comparison: which set it is, and what it
// brought.
type ComparisonArm struct {
	InfoHash string `json:"infohash"`
	Params   string `json:"params"`
	Index    int    `json:"index"`
	Name     string `json:"name"`
	Path     string `json:"path"`
	Addr     string `json:"addr"`
	// Count is the plan's frame count and Points how many planned points the
	// set actually recorded.
	Count      int   `json:"count"`
	Points     int   `json:"points"`
	DurationMS int64 `json:"duration_ms"`
	// Width and Height are the video's own, from the manifest - what lets a
	// page reserve the stage at the right shape before a single frame has
	// loaded, so the picture does not resize under the eye the first time an
	// arm is flipped to.
	Width  int `json:"width"`
	Height int `json:"height"`
	// Unpaired is how many of this arm's capture points found no partner on
	// the other arm and are therefore NOT positions. It is reported rather
	// than left to be derived from Points minus the position count, because
	// it is the number a person has to be told: a set of 8 flipped against a
	// set of 20 has twelve points that simply are not in the flipbook, and
	// silence about that would read as frames having been lost.
	Unpaired int `json:"unpaired"`
}

// ComparePosition is one moment both arms can be asked about: the unit a page
// flips between.
type ComparePosition struct {
	A FrameRef `json:"a"`
	B FrameRef `json:"b"`
	// Fraction is where in the film this position sits, the mean of the two
	// arms' own fractions - the position's place in the flipbook, and what it
	// is ordered by.
	Fraction float64 `json:"fraction"`
	// Drift is how far apart the two arms' points actually are, as a fraction
	// of duration. Zero for the shared window edges, which frames.Plan pins,
	// and small but non-zero everywhere else. It is carried rather than left
	// implicit because the tolerance below is deliberately generous, and a
	// person looking at two frames that do not quite match deserves to be
	// able to see whether the difference is the encode or the moment.
	Drift float64 `json:"drift"`
}

// Comparison is what GET /compare answers: two arms and the positions they
// share.
type Comparison struct {
	A ComparisonArm `json:"a"`
	B ComparisonArm `json:"b"`
	// Basis is "fraction" when both arms know their own duration and the
	// pairing is by fraction of it - the normal case, and the only one that
	// lines two encodes of different length up. It is "time" when one of them
	// does not (a sequential run, whose container never said how long the
	// file is) and absolute timecodes had to stand in. Reported rather than
	// silently substituted: the fallback is right for two captures of one
	// file and wrong for two encodes of different lengths, and only the
	// person looking knows which they asked for.
	Basis     string            `json:"basis"`
	Positions []ComparePosition `json:"positions"`
}

// Basis values.
const (
	basisFraction = "fraction"
	basisTime     = "time"
)

// comparison pairs two result sets into positions a page can flip between.
//
// THE PAIRING RULE, and why it is this one. Each arm's capture points are
// normalised to a fraction of that arm's own duration, and two points become
// a position when each is the other's NEAREST - mutual nearest neighbour -
// and they are no further apart than the tolerance below.
//
// Mutual, rather than "for each point of the first arm take the nearest point
// of the second", because that is not symmetric: with 8 points against 20 it
// would give 8 positions read one way round and 20 the other, the same
// twenty-frame set pairing two different positions to one eight-frame point.
// Mutual nearest gives the same flipbook whichever arm is named first, never
// shows a frame twice, and on a line its pairs cannot cross - so ordering the
// positions by either arm's own time gives the same order.
//
// WHAT HAPPENS WHEN ONE ARM HAS A POINT THE OTHER LACKS, decided rather than
// left to break, and it is two different questions:
//
//   - A point with NO PARTNER is not a position. Twelve of a twenty-point
//     set's points have no counterpart in an eight-point set, and pairing
//     them anyway - to whichever eight-point frame happens to be nearest,
//     two minutes of film away - would put a difference on screen that the
//     film caused rather than the encode, which is precisely the illusion
//     this whole feature exists to avoid. They are counted in
//     ComparisonArm.Unpaired instead, so a page can say so out loud.
//   - A position where one arm's point FAILED is kept. Since TOR-118 a run
//     records the points that produced nothing and why, and since TOR-110 the
//     grid draws them; a comparison does the same. The moment is comparable -
//     both arms planned it - and the honest answer for the arm that has no
//     picture is the reason it has none, at the same size and in the same
//     place as the picture would have been. Dropping those positions would
//     hide the one thing a person comparing two runs most wants to know about
//     a holed one.
func (s *Server) comparison(a, b setAddr) (Comparison, error) {
	left, ok := s.loadResultSet(a.InfoHash, a.Params, a.Index)
	if !ok {
		return Comparison{}, fmt.Errorf("%w: %s", errNoSuchSet, a)
	}
	right, ok := s.loadResultSet(b.InfoHash, b.Params, b.Index)
	if !ok {
		return Comparison{}, fmt.Errorf("%w: %s", errNoSuchSet, b)
	}

	basis := basisFraction
	floor := fractionFloor
	if left.DurationMS <= 0 || right.DurationMS <= 0 {
		basis = basisTime
		floor = timeFloorMS
	}

	// Both arms in ascending order of their own points, so the arithmetic
	// below - and the order the positions come out in - does not depend on
	// what order a manifest happened to list its frames in.
	leftFrames := ascendingByTime(left.Frames)
	rightFrames := ascendingByTime(right.Frames)
	leftPos := positions(leftFrames, left.DurationMS, basis)
	rightPos := positions(rightFrames, right.DurationMS, basis)

	pairs := pairNearest(leftPos, rightPos, pairingTolerance(leftPos, rightPos, floor))

	out := Comparison{
		A:     arm(a, left, len(leftFrames)-len(pairs)),
		B:     arm(b, right, len(rightFrames)-len(pairs)),
		Basis: basis,
	}
	// The mean of the two fractions, so a position sits where it really is
	// rather than where one of the arms says it is. In the "time" basis there
	// is no fraction to report, and a made-up one would be worse than none.
	scale := 1.0
	if basis == basisTime {
		scale = 0
	}
	for _, p := range pairs {
		out.Positions = append(out.Positions, ComparePosition{
			A: leftFrames[p[0]], B: rightFrames[p[1]],
			Fraction: scale * (leftPos[p[0]] + rightPos[p[1]]) / 2,
			Drift:    scale * abs(leftPos[p[0]]-rightPos[p[1]]),
		})
	}
	return out, nil
}

func arm(addr setAddr, set resultSet, unpaired int) ComparisonArm {
	return ComparisonArm{
		InfoHash: addr.InfoHash, Params: addr.Params, Index: addr.Index,
		Name: set.Name, Path: set.Path, Addr: addr.String(),
		Count: set.Count, Points: len(set.Frames), DurationMS: set.DurationMS,
		Width: set.Width, Height: set.Height, Unpaired: unpaired,
	}
}

// ascendingByTime is the arm's own points in time order, on a copy - a
// manifest's frame order is the plan's, and a shifted frame can land before
// the one planned ahead of it.
func ascendingByTime(frames []FrameRef) []FrameRef {
	out := append([]FrameRef(nil), frames...)
	sort.SliceStable(out, func(i, j int) bool { return out[i].TimeMS < out[j].TimeMS })
	return out
}

// positions normalises one arm's capture points into the space the pairing
// happens in: a fraction of that arm's own duration, or - when the container
// never said how long the file is - the raw milliseconds, which is the only
// honest thing left (see Comparison.Basis).
func positions(frames []FrameRef, durationMS int64, basis string) []float64 {
	out := make([]float64, 0, len(frames))
	for _, f := range frames {
		if basis == basisTime || durationMS <= 0 {
			out = append(out, float64(f.TimeMS))
			continue
		}
		out = append(out, float64(f.TimeMS)/float64(durationMS))
	}
	return out
}

// The two floors under the tolerance, one per basis. Below a hundredth of a
// film's length two capture points are a few seconds apart on anything
// feature-length, which is well inside one shot; a second is the same
// judgement in the units the fallback basis works in.
const (
	fractionFloor = 0.01
	timeFloorMS   = 1000.0
)

// pairingTolerance is how far apart two points may be and still be one
// position: half the step of the COARSER arm, never less than floor.
//
// Half a step is the bound that comes out of what a capture plan is. An arm
// samples the film every step; the nearest point it has to some moment is
// therefore up to half a step away, and refusing a pair inside that would
// reject partners that are as close as the arm is able to get. The coarser of
// the two arms is the one that sets it, because it is the one whose resolution
// the comparison is actually limited by - taking the finer arm's step instead
// would reject correct pairs whenever the two plans differ much in density.
//
// It is deliberately generous, and ComparePosition.Drift is the other half of
// that decision: a position whose two points are far apart is shown, with how
// far apart they are, rather than hidden on a threshold nobody can see.
func pairingTolerance(a, b []float64, floor float64) float64 {
	return max(halfStep(a), halfStep(b), floor)
}

// halfStep is half an arm's mean gap - its own sampling resolution. An arm
// with fewer than two points has no gap to measure and contributes nothing,
// leaving the other arm (or the floor) to decide.
func halfStep(pos []float64) float64 {
	if len(pos) < 2 {
		return 0
	}
	return (pos[len(pos)-1] - pos[0]) / float64(2*(len(pos)-1))
}

// pairNearest pairs two ascending sequences of normalised positions by mutual
// nearest neighbour within tolerance, returning index pairs in ascending
// order. See Server.comparison for why mutual, and what the points it leaves
// unpaired mean.
func pairNearest(a, b []float64, tolerance float64) [][2]int {
	var out [][2]int
	if len(a) == 0 || len(b) == 0 {
		return out
	}

	nearestInB := make([]int, len(a))
	for i, v := range a {
		nearestInB[i] = nearestIndex(b, v)
	}
	nearestInA := make([]int, len(b))
	for j, v := range b {
		nearestInA[j] = nearestIndex(a, v)
	}

	for i := range a {
		j := nearestInB[i]
		if nearestInA[j] != i {
			continue
		}
		if abs(a[i]-b[j]) > tolerance {
			continue
		}
		out = append(out, [2]int{i, j})
	}
	return out
}

// nearestIndex is the index of the value in sorted nearest to v, with a tie
// going to the lower index so that the answer never depends on which
// direction the question was asked from.
func nearestIndex(sorted []float64, v float64) int {
	i := sort.SearchFloat64s(sorted, v)
	switch {
	case i == 0:
		return 0
	case i == len(sorted):
		return len(sorted) - 1
	case v-sorted[i-1] <= sorted[i]-v:
		return i - 1
	default:
		return i
	}
}

func abs(v float64) float64 {
	if v < 0 {
		return -v
	}
	return v
}
