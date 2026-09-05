package web

import (
	"encoding/json"
	"os"
	"path/filepath"
	"sort"
	"time"

	"github.com/madmurdok/torpeek/internal/cache"
	"github.com/madmurdok/torpeek/internal/swarm"
)

// RunSummary is one row of GET /runs: enough for a panel to show a run and
// open it, not everything the run produced - that stays a separate, per-run
// request, which is what keeps this listing cheap (see walkRuns).
type RunSummary struct {
	// ID is the registry id (TOR-53) this process minted when the run was
	// accepted. Present only for a run this process still holds live -
	// queued, running, or one of its last ten finished (keepFinishedRuns). A
	// row found only on disk has none: nothing ever minted one for it, and
	// nothing needs one - InfoHash plus Params already says where it lives.
	ID string `json:"id,omitempty"`

	// State is one of RunState's values, present only for a live entry.
	// cache.Run does not record how a run ended - completed, budget-stopped,
	// cancelled - only what it covered, so a disk-only row has no honest
	// state to report here. Files and Complete answer the question a panel
	// actually needs ("is the result whole") without one.
	State string `json:"state,omitempty"`

	Source string `json:"source,omitempty"`
	// Name is the torrent's own confirmed name - a disk record's, or a live
	// entry's own metadata pass (RunInfo.Name, TOR-117) - never a guess.
	// Empty for exactly the states that have not learned it yet: queued,
	// and running before its own metadata has arrived.
	Name string `json:"name,omitempty"`
	// ProvisionalName is a display name read off a magnet's own dn=
	// parameter (TOR-117, swarm.MagnetDisplayName), offered only when there
	// is nothing more trustworthy yet - set here exactly when Name is empty,
	// never alongside it, so a client need not choose between the two
	// itself. It exists for the row a person watches longest in the worst
	// case: a magnet whose metadata is slow, or a torrent with no peers,
	// sits with nothing confirmed for as long as
	// swarm.Config.MetadataTimeout allows, and the row said nothing at all
	// for that whole stretch before this.
	//
	// It is NOT confirmed. A magnet's dn= is whatever the person who made
	// the link typed, not anything the torrent's own metadata has agreed
	// to - the two can disagree, and Name is what wins once it arrives. A
	// client must keep this visibly distinct from Name (app.js's rowName
	// rendering marks it, and logs the correction if the confirmed name
	// turns out to differ) rather than let it silently pass for verified. A
	// .torrent source never has one - MagnetDisplayName only ever answers
	// for a magnet - so this stays empty for one exactly as it always has.
	ProvisionalName string `json:"provisional_name,omitempty"`
	InfoHash        string `json:"infohash,omitempty"`
	// Params is the run's parameters directory name (core.ParamsKey) - the
	// other half of where it lives on disk, alongside InfoHash. Present
	// whenever this row has a disk record behind it, whether disk-only or
	// merged with a live entry; empty for a live entry with no disk record
	// yet.
	Params string `json:"params,omitempty"`

	// Files is how many video files the torrent held, and Complete how many
	// of them came out with a whole frame set - both read straight from
	// run.json's own Videos and Complete. A live entry with no disk record
	// yet reports zero for both: the registry never counts files itself,
	// only the record a run leaves on disk does.
	Files    int `json:"files"`
	Complete int `json:"complete"`
	// Selected is how many files were ever asked for in this directory
	// (cache.Run.SelectedCount) - Partial's denominator, deliberately not
	// Files. A torrent with six files where three were chosen and all three
	// finished has Complete == Selected < Files: that is Done, and comparing
	// against Files instead would call it Partial for no reason a person
	// asked for. Zero alongside a zero Files means the same thing zero Files
	// already means: no disk record merged in yet.
	Selected int `json:"selected"`

	Err string `json:"error,omitempty"`

	// When orders the list: a live entry's most recent lifecycle timestamp
	// (ended, else started, else queued), or a disk row's created_at.
	When time.Time `json:"when"`
}

// Partial reports whether this row's selection is short of complete - a
// selected file with no frames, not merely a torrent with unselected files
// left over. Judged from counts alone, on purpose (TOR-72): cache.Run does
// not record how a run ended, so there is no run state to ask instead, and a
// disk-only row has no state to invent one for. Zero Selected reads as not
// partial for the same reason it reads as not done: a row with no merged
// disk record yet has nothing here to call either one.
//
// This is the one place that rule is written (TOR-80), by way of partial()
// below, which TOR-87 pulled out so the run_state message a page's socket
// gets when a run finishes (server.go's pump) can ask the identical
// question without a RunSummary to ask it of - carrying it as a second,
// hand-written comparison there would be exactly the two-copies problem
// TOR-80 closed for app.js.
func (r RunSummary) Partial() bool {
	return partial(r.Complete, r.Selected)
}

// partial is Partial()'s comparison, factored out to stay the only place it
// is written even though TOR-87 needs to ask it from outside a RunSummary
// (see that method's doc, and server.go's pump).
func partial(complete, selected int) bool {
	return selected > 0 && complete < selected
}

// MarshalJSON adds a "partial" field to RunSummary's JSON, computed from
// Partial() rather than left for a reader to derive from Selected and
// Complete. That is the whole point of TOR-80: the two counts alone do not
// settle anything, Partial()'s comparison does, and shipping only the counts
// is exactly what let app.js's isPartial() become a second, silently
// driftable copy of that comparison. Aliasing RunSummary rather than adding
// a stored Partial field is deliberate: `alias` has RunSummary's fields and
// struct tags but none of its methods, so encoding it falls through to the
// default struct encoding instead of recursing back into this method, and
// there is no second field for listRuns to remember to keep in sync with
// Selected/Complete - Partial() alone stays the only place the comparison is
// written.
//
// This does not make a live run's badge any more (or less) able to show
// partial than it already was. Selected and Complete are only ever non-zero
// once listRuns has merged a disk record in - never for a live entry that
// has not yet reached a final state (see this type's own field comments) -
// so Partial() answers false at exactly the moments it always did. TOR-80
// left one gap here, closed by TOR-87 in server.go's pump rather than in
// this method: the page also learns of a run's state over the WebSocket
// (run_state records), and until TOR-87 run_state carried neither count, so
// a run that went running -> done while a page was open kept whatever
// "partial" this response had last reported (false, since a live entry has
// nothing merged in) until that page's next GET /runs. Closing it here would
// have meant this method reaching into the registry and the filesystem for
// an entry it is never handed - the fix belongs where run_state itself is
// built, once that message's own entry has reached a final state and (per
// this package's normal merge rule) named exactly one disk record.
func (r RunSummary) MarshalJSON() ([]byte, error) {
	type alias RunSummary
	return json.Marshal(struct {
		alias
		Partial bool `json:"partial"`
	}{alias: alias(r), Partial: r.Partial()})
}

// diskRun is one run.json found under OutputRoot, read with cache.LoadRun -
// the same parser a live run's own cache hit uses, so a listing and a cache
// hit never disagree about what counts as a usable record.
type diskRun struct {
	InfoHash, Params, Name, Source string
	Files, Complete, Selected      int
	CreatedAt                      time.Time
}

// listRuns answers GET /runs: the registry's live and recently finished runs
// (Server.snapshot), plus every run the walk finds under OutputRoot that the
// registry does not already account for - merged so a run that is both live
// and already on disk appears once.
//
// A live entry only merges with a disk row when it has reached a final state
// with a known infohash (see runs.go's RunState.final and RunInfo.InfoHash)
// and that infohash names exactly one directory on disk. A queued or running
// entry can never merge - it has not written a record yet, whatever its
// infohash - so it is never at risk of being hidden behind a stale disk row.
// When an infohash names more than one directory - different capture plans
// of the same torrent, which core.ParamsKey distinguishes but this package
// deliberately does not compute (it is a client of the event stream, not a
// second place run parameters are decided, per ARCHITECTURE.md) - merging is
// skipped rather than guessed: the live entry is listed without Files or
// Complete, and every one of those disk directories is listed too. That is
// the one case a run can still show up twice; it is rare (the same torrent
// captured under two different plans, one of them still in memory) and
// listing both is safer than silently merging with the wrong one.
func (s *Server) listRuns() []RunSummary {
	live := s.snapshot()
	disk := walkRuns(s.cfg.OutputRoot)

	byHash := make(map[string][]int, len(disk))
	for i, d := range disk {
		byHash[d.InfoHash] = append(byHash[d.InfoHash], i)
	}
	consumed := make([]bool, len(disk))

	out := make([]RunSummary, 0, len(live)+len(disk))
	for _, info := range live {
		row := RunSummary{
			ID: info.ID, State: string(info.State), Source: info.Source,
			Name: info.Name, InfoHash: info.InfoHash, Err: info.Err, When: liveWhen(info),
		}
		if info.State.final() && info.InfoHash != "" {
			if idxs := byHash[info.InfoHash]; len(idxs) == 1 {
				d := disk[idxs[0]]
				row.Name, row.Params = d.Name, d.Params
				row.Files, row.Complete, row.Selected = d.Files, d.Complete, d.Selected
				if row.Source == "" {
					row.Source = d.Source
				}
				consumed[idxs[0]] = true
			}
		}
		// TOR-117: whatever is left with nothing confirmed - queued, or
		// running before its own metadata has arrived - gets a magnet's own
		// dn= instead, visibly marked provisional rather than passed off as
		// the row's real Name (see RunSummary.ProvisionalName).
		if row.Name == "" {
			if dn, ok := swarm.MagnetDisplayName(row.Source); ok {
				row.ProvisionalName = dn
			}
		}
		out = append(out, row)
	}

	for i, d := range disk {
		if consumed[i] {
			continue
		}
		out = append(out, RunSummary{
			Source: d.Source, Name: d.Name, InfoHash: d.InfoHash, Params: d.Params,
			Files: d.Files, Complete: d.Complete, Selected: d.Selected, When: d.CreatedAt,
		})
	}

	sort.SliceStable(out, func(i, j int) bool { return out[i].When.After(out[j].When) })
	return out
}

// liveWhen is the one timestamp a panel shows for a live entry: the most
// recent thing that has happened to it.
func liveWhen(info RunInfo) time.Time {
	switch {
	case !info.EndedAt.IsZero():
		return info.EndedAt
	case !info.StartedAt.IsZero():
		return info.StartedAt
	default:
		return info.QueuedAt
	}
}

// walkRuns finds every run.json under root and reads it with cache.LoadRun.
//
// It only ever opens run.json, one per run directory - never a file's
// manifest.json underneath it. run.json alone already answers name,
// infohash, when, file count and completeness (Videos and Complete); opening
// every file's manifest as well to double check would turn a listing of
// fifty runs into fifty times as many file reads for no answer this listing
// needs - a manifest's frame-by-frame detail is what a future per-run detail
// request is for, not this one.
//
// root is a user's directory, not something only this process writes into:
// a directory that is not a run - malformed JSON, a half-written record
// (LoadRun already treats a version mismatch as a miss), an unrelated folder
// someone parked there - is skipped rather than failing the whole request.
func walkRuns(root string) []diskRun {
	if root == "" {
		return nil
	}

	hashDirs, err := os.ReadDir(root)
	if err != nil {
		return nil
	}

	var out []diskRun
	for _, hashDir := range hashDirs {
		if !hashDir.IsDir() {
			continue
		}
		paramDirs, err := os.ReadDir(filepath.Join(root, hashDir.Name()))
		if err != nil {
			continue
		}
		for _, paramDir := range paramDirs {
			if !paramDir.IsDir() {
				continue
			}
			dir := filepath.Join(root, hashDir.Name(), paramDir.Name())
			run, ok := cache.LoadRun(dir)
			if !ok {
				continue
			}
			out = append(out, diskRun{
				InfoHash: run.InfoHash, Params: paramDir.Name(),
				Name: run.Name, Source: run.Source,
				Files: len(run.Videos), Complete: len(run.Complete),
				Selected:  run.SelectedCount(),
				CreatedAt: run.CreatedAt,
			})
		}
	}
	return out
}

// runRecordCounts reads the disk record(s) filed under one infohash and
// answers with the counts a finished run just wrote there, or ok=false if
// there is nothing (yet) to answer with. It exists for TOR-87: a run_state
// message, unlike a GET /runs response, is built for one entry at a time as
// the moment it names happens, so attaching that entry's counts calls for a
// lookup keyed by its own infohash rather than a pass over every run under
// OutputRoot (walkRuns) followed by matching one row back out of it.
//
// The ambiguity rule is listRuns' own, applied here to the same effect: an
// infohash naming more than one params directory - two capture plans of the
// same torrent - has no single record to answer from, so this reports
// ok=false exactly as listRuns leaves such an entry unmerged rather than
// guessing which directory a reload would have picked. An infohash naming
// none (nothing has been written yet, or root does not exist) answers the
// same way, for the same reason RunSummary's own zero counts do: there is
// nothing here to call done or partial either one.
func runRecordCounts(root, infoHash string) (files, complete, selected int, ok bool) {
	if root == "" || infoHash == "" {
		return 0, 0, 0, false
	}

	paramDirs, err := os.ReadDir(filepath.Join(root, infoHash))
	if err != nil {
		return 0, 0, 0, false
	}

	found := false
	for _, paramDir := range paramDirs {
		if !paramDir.IsDir() {
			continue
		}
		run, loaded := cache.LoadRun(filepath.Join(root, infoHash, paramDir.Name()))
		if !loaded {
			continue
		}
		if found {
			return 0, 0, 0, false
		}
		files, complete, selected = len(run.Videos), len(run.Complete), run.SelectedCount()
		found = true
	}
	return files, complete, selected, found
}

// FileDetail is what GET /runs/{infohash}/files/{index} answers: one video
// file's frames, gathered from every result set that torrent has on disk.
//
// Sets exist because core.ParamsKey hashes the frame count, so a
// regeneration at a different count writes a sibling params directory
// rather than growing the first one (TOR-68). What the person asked for,
// though, is more frames of that file - so the frames arrive here as one
// list ordered by time, with the sets they came from named separately for
// anyone who wants to know rather than woven into the frames.
type FileDetail struct {
	Index int    `json:"index"`
	Path  string `json:"path"`
	// Sets is every params directory that holds frames for this file, newest
	// plan first is deliberately NOT promised: they are listed in the order
	// the directory walk found them, since nothing about a set makes one of
	// them the current one.
	Sets []FrameSet `json:"sets"`
	// Frames is the union, ordered by timecode. Two sets always share the
	// window's edges - frames.Plan pins the first and last point to the
	// window - so a timecode carrying more than one frame on disk appears
	// once here. Which one survives is decided by params order, so the same
	// disk always answers the same way.
	Frames []FrameRef `json:"frames"`
}

// FrameSet names one result set holding frames for the file.
type FrameSet struct {
	Params string `json:"params"`
	// Count is the plan's frame count from run.json - what was asked for.
	// Frames is how many planned points this set has anything to say about:
	// a servable frame or, since TOR-118, a failed point recording why there
	// is none - so the two need not match even on a set nothing went wrong
	// for, and a set that is not fully captured no longer has to mean a
	// smaller Frames than Count, only a Frames with fewer URLs in it.
	Count  int `json:"count"`
	Frames int `json:"frames"`
	// Reach is how much of this file's own stretch of the torrent the run
	// that wrote this set actually ordered (TOR-111). It hangs off the SET
	// rather than off the file because a claim belongs to a run: two sets of
	// one file are two runs that reached differently.
	//
	// Absent, not empty, for a run recorded before the claims were kept
	// (TOR-119 added them without bumping cache.Version, so older records
	// stay readable and simply have nothing to say here). A strip drawn from
	// an absent reach would assert that a run touched nothing.
	Reach *Reach `json:"reach,omitempty"`
}

// Reach is one file's slice of what a run ordered from the swarm: where the
// file starts in the torrent's pieces, how many pieces it spans, and which of
// those the run claimed.
//
// 44 of 270 pieces is the argument of the whole product and we could only say
// it as a sentence. This is the shape a picture of it needs, and every field
// comes off disk rather than from a new measurement: the claims from
// run.json's own record, the piece length from the manifest, the file's offset
// and length from the run record.
//
// Claimed is in pieces counted FROM FirstPiece, half-open, ascending and
// non-touching - the same shape swarm.ClaimedRanges produces, clipped to this
// file. Offsets rather than absolute indices so a drawing does not have to
// subtract, and because the interesting axis is the file, not the torrent: a
// run over one file of a six-file torrent claims pieces in the thousands and
// none of that is where the picture wants its origin.
type Reach struct {
	FirstPiece int   `json:"first_piece"`
	Pieces     int   `json:"pieces"`
	PieceBytes int64 `json:"piece_bytes"`
	// ClaimedPieces is the total, so a reader has the number without summing
	// the ranges - and so the two can be checked against each other.
	ClaimedPieces int      `json:"claimed_pieces"`
	Claimed       [][2]int `json:"claimed"`
}

// reachOf turns a run's torrent-wide piece claims into one file's stretch.
//
// Reports nil rather than a zeroed Reach whenever it cannot answer: no claims
// recorded (a pre-TOR-119 record), no piece length (a manifest old enough not
// to carry one), or a file whose length is zero. Each of those is "nothing to
// say", which a drawing must render as nothing rather than as a run that
// touched none of the file.
func reachOf(claimed [][2]int, file cache.File, pieceBytes int64) *Reach {
	if len(claimed) == 0 || pieceBytes <= 0 || file.Bytes <= 0 {
		return nil
	}

	first := int(file.Offset / pieceBytes)
	last := int((file.Offset + file.Bytes - 1) / pieceBytes)
	out := &Reach{FirstPiece: first, Pieces: last - first + 1, PieceBytes: pieceBytes}

	for _, r := range claimed {
		begin, end := r[0], r[1]
		if begin < first {
			begin = first
		}
		if end > last+1 {
			end = last + 1
		}
		if end <= begin {
			// A claim entirely outside this file - another file of the same
			// torrent, or the metadata pieces at the front.
			continue
		}
		out.Claimed = append(out.Claimed, [2]int{begin - first, end - first})
		out.ClaimedPieces += end - begin
	}

	// A run whose claims all fall outside this file reached none of it, which
	// is an answer rather than a silence: it is returned with no ranges, and a
	// strip of untouched blocks is the truthful drawing of it. Only the cases
	// above, where nothing was recorded to reason from, are nil.
	return out
}

// FrameRef is one frame a page can show: when it is from, why it moved if it
// did, and where to get it - or, for a point that produced nothing, why not.
type FrameRef struct {
	// TimeMS is where the frame actually came from (manifest ActualMS),
	// falling back to where it was asked for when the manifest records no
	// actual - which is also the sort key and the identity used to collapse
	// the sets' shared edges. A failed point has no ActualMS, so this is
	// always RequestedMS for one - where it was planned, since nowhere else
	// is true.
	TimeMS int64  `json:"time_ms"`
	Shift  string `json:"shift,omitempty"`
	// Error is manifest.Frame.Error, carried through for a point that has no
	// URL below: the typed reason nothing was captured there.
	//
	// It is deliberately not folded into Shift, and that is the whole point
	// of TOR-118. Shift answers "where did this frame come from" - for a
	// failed point it is manifest.ShiftFailed, whose serialised value is the
	// word "unavailable", and it says only THAT the point was lost. Error
	// says what lost it, and it is the field that separates the two causes
	// the engine actually reports: "unavailable", no peer offered those
	// pieces, so a later run will not get them either unless the swarm
	// changes; and "read_stalled", the pieces were being fetched and the read
	// timed out, which a later run may well get through. One says do not
	// bother, the other says try again.
	//
	// The trap here is real, because it produced this ticket's own wrong
	// premise: ShiftFailed's value is the same word as one of Error's codes.
	// A reader who looks at shift alone sees "unavailable" on every failed
	// point and concludes the two causes were collapsed into one - they were
	// not, they are in the field beside it. On a real holed run's manifest
	// the seven failed points all read shift "unavailable" while Error
	// separates them five read_stalled to two unavailable, matching the live
	// event stream exactly. The manifest always kept them apart; this field
	// is what stops the REST payload from re-merging them on the way out.
	Error string `json:"error,omitempty"`
	// URL is a files/{id} handle minted here, the same way an event's frame
	// URL is (see fileSet.publish): the page can only ever ask for a path
	// this server named, and a sibling set's frames were never named by any
	// event in this process - that is precisely why this request exists.
	// Empty for a failed point: there is nothing to publish.
	URL string `json:"url"`
	// Params says which set the frame came from, and Index is its own number
	// in that set's manifest. Nothing in the UI shows either; together they
	// are the address a DELETE needs (with the infohash and file index
	// already in its path), which is why the page keeps them on every frame
	// it draws a cross on. They also let a person reading the response tell
	// two coinciding timecodes apart.
	//
	// Index is the manifest's own number and never a position in Frames:
	// deleting a frame leaves a hole in the sequence on purpose, since
	// core's reuse of an earlier run's frames keys on exactly this number
	// (core.DeleteFrame explains why renumbering would be destructive).
	Params string `json:"params"`
	Index  int    `json:"index"`
	Width  int    `json:"width"`
	Height int    `json:"height"`
}

// fileDetail gathers one file's frames across every set of one torrent.
//
// A frame with a path is listed when its own file is still on disk and
// non-empty. That is deliberately more forgiving than cache.Usable, which
// refuses a whole manifest if a single frame is missing: this list only
// decides what to SHOW, never whether a run is servable, and cache.Usable
// stays the sole judge of the latter. A person who deleted one frame should
// still see the other nineteen. TOR-70 did not weaken cache.Usable in the
// end and did not need to: core.DeleteFrame removes the record along with
// the file, and a manifest that no longer mentions a frame has nothing left
// to fail on.
//
// A frame with NO path - the engine never produced one - is listed too
// (TOR-118), with no URL and manifest.Frame.Error carried onto FrameRef.Error
// so a person reading a finished run learns what a person watching it live
// already could (wire.go's frame_skipped). That is a different question from
// the paragraph above: there, the manifest thought the point succeeded and
// the disk disagrees; here, the manifest itself says the point failed. Only
// the latter is something the engine reported - the former is an ordinary
// fact about the local disk (most often a person's own delete) that never
// was a captured point, so it stays dropped rather than shown as a failure
// nobody claimed.
//
// Reports ok=false when the torrent has no set holding this file at all -
// an unknown infohash, an index no set lists, or a file with nothing to say
// about any of its planned points - which the handler answers as a 404
// rather than an empty list: there is no such file to show, which is a
// different thing from a file whose every point failed but is still
// accounted for.
func (s *Server) fileDetail(infoHash string, index int) (FileDetail, bool) {
	if s.cfg.OutputRoot == "" || !validInfoHash(infoHash) || index < 0 {
		return FileDetail{}, false
	}

	paramDirs, err := os.ReadDir(filepath.Join(s.cfg.OutputRoot, infoHash))
	if err != nil {
		return FileDetail{}, false
	}

	detail := FileDetail{Index: index}
	for _, paramDir := range paramDirs {
		if !paramDir.IsDir() {
			continue
		}
		// loadResultSet (compare.go) is where the per-frame rule argued in
		// this function's own doc comment actually lives, since TOR-109 gave
		// it a second reader: a comparison wants two NAMED sets where this
		// wants every set of one torrent, and the question "which frames does
		// this set have" must have one answer for both. A set it reports
		// ok=false for is one with nothing to say about any planned point -
		// not even a failure - so there is nothing here worth naming as a
		// result set either.
		set, ok := s.loadResultSet(infoHash, paramDir.Name(), index)
		if !ok {
			continue
		}
		detail.Frames = append(detail.Frames, set.Frames...)
		detail.Path = set.Path
		detail.Sets = append(detail.Sets, FrameSet{
			Params: set.Params, Count: set.Count, Frames: len(set.Frames),
			Reach: set.Reach,
		})
	}

	if len(detail.Frames) == 0 {
		// Same question one level up: nothing from any set, real or failed,
		// landed in detail.Frames, so there is no such file to show - not
		// merely a file with nothing left in it (that case now has failed
		// points to show instead, and returns true).
		return FileDetail{}, false
	}

	sort.SliceStable(detail.Frames, func(i, j int) bool {
		if detail.Frames[i].TimeMS != detail.Frames[j].TimeMS {
			return detail.Frames[i].TimeMS < detail.Frames[j].TimeMS
		}
		return detail.Frames[i].Params < detail.Frames[j].Params
	})

	merged := detail.Frames[:0:0]
	for _, f := range detail.Frames {
		if len(merged) > 0 && merged[len(merged)-1].TimeMS == f.TimeMS {
			continue
		}
		merged = append(merged, f)
	}
	detail.Frames = merged

	return detail, true
}

// videoPath finds one video file's path in a run record.
func videoPath(run cache.Run, index int) (string, bool) {
	v, ok := videoEntry(run, index)
	if !ok {
		return "", false
	}
	return v.Path, true
}

// videoEntry is videoPath when the caller needs more than the path - the
// file's own offset and length, which is what turns a run's piece claims into
// a stretch of THIS file (TOR-111).
func videoEntry(run cache.Run, index int) (cache.File, bool) {
	for _, v := range run.Videos {
		if v.Index == index {
			return v, true
		}
	}
	return cache.File{}, false
}

// validInfoHash gates one of the two places a request's own string reaches
// the filesystem. Everywhere else this package serves only paths the event
// stream named, for exactly this reason (see files.go); here the infohash
// addresses a directory, so it has to be provably a hex digest and nothing
// else - "../.." must never become a path.
func validInfoHash(s string) bool { return hexOfLength(s, 40) }

// validParams gates the other one: the result set a DELETE names
// (Server.DeleteFrame), which addresses a directory the same way. A
// core.ParamsKey is the first eight bytes of a sha256 in hex, so sixteen
// characters exactly - anything else was not written by a run.
func validParams(s string) bool { return hexOfLength(s, 16) }

// hexOfLength reports whether s is exactly n lower-case hex digits. Lower
// case only: that is how the directories are named, and accepting the other
// case would mean two spellings of one run on a case-sensitive filesystem.
func hexOfLength(s string, n int) bool {
	if len(s) != n {
		return false
	}
	for _, c := range s {
		if (c < '0' || c > '9') && (c < 'a' || c > 'f') {
			return false
		}
	}
	return true
}
