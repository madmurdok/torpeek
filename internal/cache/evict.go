package cache

import (
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"sort"
	"time"
)

// Set is one result set on disk: everything under a single
// output.Layout.RunDir - one infohash, one parameter hash. It is the unit
// eviction and manual clearing both act on, never a single video file within
// it, because a set's run.json and its files' manifests only make sense
// together (cache.Usable judges the whole set, and DeleteFrame already
// explains why a file's own directory cannot be removed on its own without
// breaking that).
type Set struct {
	InfoHash string
	Params   string
	Dir      string
	Bytes    int64

	// CreatedAt is run.json's own timestamp, honest only when Aged is true -
	// see Scan for why an unreadable record cannot simply report the zero
	// time and be treated as "oldest".
	CreatedAt time.Time
	// Aged is false when this set's run.json could not be read: missing,
	// truncated by a crash mid-write, or written by a version this build
	// does not know (cache.LoadRun's own miss cases). See Scan and Evict for
	// what that means for ordering.
	Aged bool
}

// Scan finds every result set under root: one per <infohash>/<params>
// directory, mirroring the exact layout output.Layout.RunDir writes to and
// web.walkRuns already reads for the runs listing.
//
// root is a user's directory, not something only this process ever writes
// into. A read that fails - root does not exist yet, an entry that is not a
// directory, a directory that vanished between the readdir and the size walk
// - is not this function's problem to fail over: skip it and describe
// whatever is actually there, the same tolerance web.walkRuns already
// applies to a single unreadable run.json.
//
// A set whose run.json cannot be read is still reported - Aged is what says
// so - rather than silently dropped. A person asking to see the cache
// (REQUIREMENTS.md 2.9's last bullet) should see every directory occupying
// their disk, orphaned results included, not only the ones this build can
// date.
func Scan(root string) ([]Set, error) {
	hashDirs, err := os.ReadDir(root)
	if err != nil {
		// Nothing on disk yet reads the same as nothing found - not an error
		// a caller running eviction after every run should ever have to
		// handle specially.
		return nil, nil
	}

	var out []Set
	for _, hashDir := range hashDirs {
		if !hashDir.IsDir() {
			continue
		}
		hashPath := filepath.Join(root, hashDir.Name())
		paramDirs, err := os.ReadDir(hashPath)
		if err != nil {
			continue
		}
		for _, paramDir := range paramDirs {
			if !paramDir.IsDir() {
				continue
			}
			dir := filepath.Join(hashPath, paramDir.Name())
			size, err := dirSize(dir)
			if err != nil {
				continue
			}
			run, ok := LoadRun(dir)
			out = append(out, Set{
				InfoHash:  hashDir.Name(),
				Params:    paramDir.Name(),
				Dir:       dir,
				Bytes:     size,
				CreatedAt: run.CreatedAt,
				Aged:      ok,
			})
		}
	}
	return out, nil
}

// dirSize totals the size of every regular file under dir, the actual
// footprint a set has on disk - not an approximation from a single stat,
// since a set is a tree of frames, manifests, a sheet and a .torrent.
func dirSize(dir string) (int64, error) {
	var total int64
	err := filepath.WalkDir(dir, func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if d.IsDir() {
			return nil
		}
		info, err := d.Info()
		if err != nil {
			return err
		}
		total += info.Size()
		return nil
	})
	return total, err
}

// EvictResult is what Evict or ClearAll actually removed, so a caller can
// report it (core.Done's Warnings, on failure; a person running -cache-clear
// on success) without re-scanning the tree to find out.
type EvictResult struct {
	Removed []Set
	Freed   int64
}

// Evict keeps the tree under root at or under ceiling bytes by removing whole
// sets, oldest first by CreatedAt, until it fits or nothing more can be
// removed (REQUIREMENTS.md 2.9).
//
// ceiling <= 0 means no ceiling was ever set - the documented default, no
// eviction at all - and Evict does nothing. This is the one function that
// has to know that convention, since it is the one deciding whether to
// delete anything; a caller (core.Engine) may also skip calling it in that
// case purely to avoid the scan, but correctness does not depend on that.
//
// protect names one set's directory - the run this process is writing right
// now - that is never removed, however old it looks. It is checked by path,
// explicitly, rather than left to fall out of the ordering on its own (a
// live run's CreatedAt would normally be the newest thing in the tree
// anyway): the single-slot queue (REQUIREMENTS.md 3.3) guarantees there is
// at most one such run, but that is a guarantee about the queue, not about
// the clock, and the one case this must never get wrong - deleting a result
// a running job is producing - is worth an explicit check rather than an
// inference.
//
// A set with an unreadable run.json (Set.Aged == false) is likewise never
// picked for removal: there is no honest CreatedAt to place it in the
// oldest-first order, and inventing one - the newest timestamp available, or
// the zero time, or the directory's mtime - would be exactly the guess
// created_at was chosen over atime to avoid (REQUIREMENTS.md 2.9: atime does
// not survive a copy, so an invented age would not even be a stable one).
// Its bytes still count toward the tree's total, since the ceiling is about
// disk space actually occupied and the space is real either way - so a tree
// made mostly of unaged sets can legitimately fail to shrink under ceiling.
// That is reported, not hidden or forced: the manual clear
// (REQUIREMENTS.md 2.9's last bullet) is the deliberate way to remove
// something automatic eviction refuses to guess about.
func Evict(root string, ceiling int64, protect string) (EvictResult, error) {
	if ceiling <= 0 {
		return EvictResult{}, nil
	}

	sets, err := Scan(root)
	if err != nil {
		return EvictResult{}, err
	}

	protect = filepath.Clean(protect)

	var total int64
	candidates := make([]Set, 0, len(sets))
	for _, s := range sets {
		total += s.Bytes
		if !s.Aged || filepath.Clean(s.Dir) == protect {
			continue
		}
		candidates = append(candidates, s)
	}

	// Oldest first is the whole policy: age is the only thing that decides
	// which whole set goes, never size or which file within it changed most
	// recently.
	sort.Slice(candidates, func(i, j int) bool {
		return candidates[i].CreatedAt.Before(candidates[j].CreatedAt)
	})

	var result EvictResult
	for _, s := range candidates {
		if total <= ceiling {
			break
		}
		if err := os.RemoveAll(s.Dir); err != nil {
			return result, fmt.Errorf("evict %s: %w", s.Dir, err)
		}
		total -= s.Bytes
		result.Removed = append(result.Removed, s)
		result.Freed += s.Bytes
	}
	return result, nil
}

// Clear removes one named set outright, regardless of size or age -
// REQUIREMENTS.md 2.9's manual half: a person decided, not the ceiling.
func Clear(root, infoHash, params string) error {
	return os.RemoveAll(filepath.Join(root, infoHash, params))
}

// ClearAll removes every result set Scan finds under root, the other half of
// REQUIREMENTS.md 2.9's manual clearing: with no ceiling set (the default),
// asking is the only way to free the space.
func ClearAll(root string) (EvictResult, error) {
	sets, err := Scan(root)
	if err != nil {
		return EvictResult{}, err
	}

	var result EvictResult
	for _, s := range sets {
		if err := os.RemoveAll(s.Dir); err != nil {
			return result, fmt.Errorf("clear %s: %w", s.Dir, err)
		}
		result.Removed = append(result.Removed, s)
		result.Freed += s.Bytes
	}
	return result, nil
}
