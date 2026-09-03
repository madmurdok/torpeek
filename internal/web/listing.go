package web

import (
	"os"
	"path/filepath"
	"sort"
	"time"

	"github.com/madmurdok/torpeek/internal/cache"
	"github.com/madmurdok/torpeek/internal/output"
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

	Source   string `json:"source,omitempty"`
	Name     string `json:"name,omitempty"`
	InfoHash string `json:"infohash,omitempty"`
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

	Err string `json:"error,omitempty"`

	// When orders the list: a live entry's most recent lifecycle timestamp
	// (ended, else started, else queued), or a disk row's created_at.
	When time.Time `json:"when"`
}

// diskRun is one run.json found under OutputRoot, read with cache.LoadRun -
// the same parser a live run's own cache hit uses, so a listing and a cache
// hit never disagree about what counts as a usable record.
type diskRun struct {
	InfoHash, Params, Name, Source string
	Files, Complete                int
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
			InfoHash: info.InfoHash, Err: info.Err, When: liveWhen(info),
		}
		if info.State.final() && info.InfoHash != "" {
			if idxs := byHash[info.InfoHash]; len(idxs) == 1 {
				d := disk[idxs[0]]
				row.Name, row.Params = d.Name, d.Params
				row.Files, row.Complete = d.Files, d.Complete
				if row.Source == "" {
					row.Source = d.Source
				}
				consumed[idxs[0]] = true
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
			Files: d.Files, Complete: d.Complete, When: d.CreatedAt,
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
				CreatedAt: run.CreatedAt,
			})
		}
	}
	return out
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
	// Count is the plan's frame count from run.json - what was asked for,
	// which is not always what came out; Frames is what this set actually
	// has on disk and can serve.
	Count  int `json:"count"`
	Frames int `json:"frames"`
}

// FrameRef is one frame a page can show: when it is from, why it moved if it
// did, and where to get it.
type FrameRef struct {
	// TimeMS is where the frame actually came from (manifest ActualMS),
	// falling back to where it was asked for when the manifest records no
	// actual - which is also the sort key and the identity used to collapse
	// the sets' shared edges.
	TimeMS int64  `json:"time_ms"`
	Shift  string `json:"shift,omitempty"`
	// URL is a files/{id} handle minted here, the same way an event's frame
	// URL is (see fileSet.publish): the page can only ever ask for a path
	// this server named, and a sibling set's frames were never named by any
	// event in this process - that is precisely why this request exists.
	URL string `json:"url"`
	// Params says which set the frame came from. Nothing in the UI shows it;
	// it is here so a person reading the response can tell two coinciding
	// timecodes apart, and so a future per-frame action (TOR-70) has the
	// directory it would act on.
	Params string `json:"params"`
	Width  int    `json:"width"`
	Height int    `json:"height"`
}

// fileDetail gathers one file's frames across every set of one torrent.
//
// A frame is listed when its own file is still on disk and non-empty. That is
// deliberately more forgiving than cache.Usable, which refuses a whole
// manifest if a single frame is missing: this list only decides what to SHOW,
// never whether a run is servable, and cache.Usable stays the sole judge of
// the latter (weakening it is TOR-70's job, not this one's). A person who
// deleted one frame should still see the other nineteen.
//
// Reports ok=false when the torrent has no set holding this file at all -
// an unknown infohash, an index no set lists, or a file whose frames are all
// gone - which the handler answers as a 404 rather than an empty list: there
// is no such file to show, which is a different thing from a file with
// nothing left in it.
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
		params := paramDir.Name()
		layout := output.Layout{Root: s.cfg.OutputRoot, InfoHash: infoHash, Params: params}

		run, ok := cache.LoadRun(layout.RunDir())
		if !ok {
			continue
		}
		// The file's own path is what output.FileSlug turns into the
		// directory name, so the record is what says where to look rather
		// than this package re-deriving a layout of its own.
		path, ok := videoPath(run, index)
		if !ok {
			continue
		}
		m, ok := cache.LoadManifest(layout.FileDir(index, path))
		if !ok {
			continue
		}

		set := FrameSet{Params: params, Count: run.Plan.Count}
		for _, f := range m.Frames {
			if f.Path == "" {
				continue
			}
			if info, err := os.Stat(f.Path); err != nil || info.Size() == 0 {
				continue
			}
			at := f.RequestedMS
			if f.ActualMS != nil {
				at = *f.ActualMS
			}
			detail.Frames = append(detail.Frames, FrameRef{
				TimeMS: at, Shift: string(f.Shift), URL: s.files.publish(f.Path),
				Params: params, Width: f.Width, Height: f.Height,
			})
			set.Frames++
		}
		if set.Frames == 0 {
			continue
		}
		detail.Path = path
		detail.Sets = append(detail.Sets, set)
	}

	if len(detail.Frames) == 0 {
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
	for _, v := range run.Videos {
		if v.Index == index {
			return v.Path, true
		}
	}
	return "", false
}

// validInfoHash gates the one place a request's own string reaches the
// filesystem. Everywhere else this package serves only paths the event
// stream named, for exactly this reason (see files.go); here the infohash
// addresses a directory, so it has to be provably a hex digest and nothing
// else - "../.." must never become a path.
func validInfoHash(s string) bool {
	if len(s) != 40 {
		return false
	}
	for _, c := range s {
		if (c < '0' || c > '9') && (c < 'a' || c > 'f') {
			return false
		}
	}
	return true
}
