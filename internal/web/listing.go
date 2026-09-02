package web

import (
	"os"
	"path/filepath"
	"sort"
	"time"

	"github.com/madmurdok/torpeek/internal/cache"
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
