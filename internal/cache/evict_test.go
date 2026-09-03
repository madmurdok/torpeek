package cache

import (
	"os"
	"path/filepath"
	"testing"
	"time"
)

// makeSet writes a minimal, readable set at root/infoHash/params: a run.json
// with the given created_at, plus a payload file of the given size so the
// set has a real footprint on disk for Scan to total up.
func makeSet(t *testing.T, root, infoHash, params string, createdAt time.Time, size int) string {
	t.Helper()

	dir := filepath.Join(root, infoHash, params)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatalf("create set dir: %v", err)
	}
	if err := SaveRun(dir, Run{Version: Version, CreatedAt: createdAt, InfoHash: infoHash}); err != nil {
		t.Fatalf("save run.json: %v", err)
	}
	if size > 0 {
		if err := os.WriteFile(filepath.Join(dir, "payload.bin"), make([]byte, size), 0o600); err != nil {
			t.Fatalf("write payload: %v", err)
		}
	}
	return dir
}

// mustExist and mustNotExist read the filesystem back after a write, rather
// than trusting a nil error from the call that supposedly removed or spared
// something - the discipline this ticket's own review calls for.
func mustExist(t *testing.T, dir string) {
	t.Helper()
	if _, err := os.Stat(dir); err != nil {
		t.Errorf("%s should still be on disk: %v", dir, err)
	}
}

func mustNotExist(t *testing.T, dir string) {
	t.Helper()
	if _, err := os.Stat(dir); !os.IsNotExist(err) {
		t.Errorf("%s should have been removed, stat returned err=%v", dir, err)
	}
}

var day = 24 * time.Hour

// TestScanReportsEverySetWithSizeAndAge is Scan's own acceptance test: three
// independent sets, each with a distinct size and created_at, must all come
// back, undisturbed, with Aged true.
func TestScanReportsEverySetWithSizeAndAge(t *testing.T) {
	root := t.TempDir()
	now := time.Now().UTC().Truncate(time.Second)

	makeSet(t, root, "aaaa000000000000000000000000000000000a", "1111111111111111", now.Add(-2*day), 1000)
	makeSet(t, root, "bbbb000000000000000000000000000000000b", "2222222222222222", now.Add(-1*day), 2000)
	makeSet(t, root, "cccc000000000000000000000000000000000c", "3333333333333333", now, 3000)

	sets, err := Scan(root)
	if err != nil {
		t.Fatalf("scan: %v", err)
	}
	if len(sets) != 3 {
		t.Fatalf("scan found %d sets, want 3: %+v", len(sets), sets)
	}
	for _, s := range sets {
		if !s.Aged {
			t.Errorf("set %s/%s: Aged = false, want true - its run.json is perfectly readable", s.InfoHash, s.Params)
		}
		if s.Bytes == 0 {
			t.Errorf("set %s/%s: Bytes = 0, want its payload's size", s.InfoHash, s.Params)
		}
	}
}

// TestScanReportsAnUnreadableRunAsUnaged is the decision this ticket asks to
// be made explicit and tested: a set whose run.json cannot be parsed (here,
// a newer-than-known version - LoadRun's own miss case) is still reported by
// Scan, with its real size, but Aged is false because there is no honest
// CreatedAt behind it.
func TestScanReportsAnUnreadableRunAsUnaged(t *testing.T) {
	root := t.TempDir()
	dir := filepath.Join(root, "dddd000000000000000000000000000000000d", "4444444444444444")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatalf("create set dir: %v", err)
	}
	// A record from a version this build does not know - LoadRun's documented
	// miss case - rather than hand-corrupted JSON, so this exercises exactly
	// the path a future format bump would hit.
	if err := SaveRun(dir, Run{Version: Version + 1, CreatedAt: time.Now()}); err != nil {
		t.Fatalf("save run.json: %v", err)
	}
	if err := os.WriteFile(filepath.Join(dir, "payload.bin"), make([]byte, 500), 0o600); err != nil {
		t.Fatalf("write payload: %v", err)
	}

	sets, err := Scan(root)
	if err != nil {
		t.Fatalf("scan: %v", err)
	}
	if len(sets) != 1 {
		t.Fatalf("scan found %d sets, want 1: %+v", len(sets), sets)
	}
	if sets[0].Aged {
		t.Error("Aged = true for a set whose run.json this build cannot read")
	}
	if sets[0].Bytes == 0 {
		t.Error("Bytes = 0 - the payload is on disk and real, whatever run.json says")
	}
	if !sets[0].CreatedAt.IsZero() {
		t.Errorf("CreatedAt = %v, want the zero value for an unaged set", sets[0].CreatedAt)
	}
}

// TestEvictRemovesOldestFirstUntilUnderCeiling is Evict's central claim: given
// three sets of one size each, evicting to a ceiling that only leaves room
// for one must remove the two oldest and keep the newest.
func TestEvictRemovesOldestFirstUntilUnderCeiling(t *testing.T) {
	root := t.TempDir()
	now := time.Now().UTC()

	oldest := makeSet(t, root, "aaaa000000000000000000000000000000000a", "1111111111111111", now.Add(-3*day), 1000)
	middle := makeSet(t, root, "bbbb000000000000000000000000000000000b", "2222222222222222", now.Add(-2*day), 1000)
	newest := makeSet(t, root, "cccc000000000000000000000000000000000c", "3333333333333333", now.Add(-1*day), 1000)

	// Three sets of ~1000 bytes each; a ceiling of 1500 leaves room for only
	// one of them once run.json's own bytes are counted too.
	result, err := Evict(root, 1500, "")
	if err != nil {
		t.Fatalf("evict: %v", err)
	}

	mustNotExist(t, oldest)
	mustNotExist(t, middle)
	mustExist(t, newest)

	if len(result.Removed) != 2 {
		t.Fatalf("removed %d sets, want 2: %+v", len(result.Removed), result.Removed)
	}
	if result.Removed[0].Dir != oldest || result.Removed[1].Dir != middle {
		t.Errorf("removed %v in that order, want oldest then middle", result.Removed)
	}
}

// TestEvictDoesNothingWithNoCeiling is REQUIREMENTS.md 2.9's default: a
// ceiling of zero (never set) must not evict anything, however large the
// tree.
func TestEvictDoesNothingWithNoCeiling(t *testing.T) {
	root := t.TempDir()
	now := time.Now().UTC()
	old := makeSet(t, root, "aaaa000000000000000000000000000000000a", "1111111111111111", now.Add(-10*day), 10_000)

	result, err := Evict(root, 0, "")
	if err != nil {
		t.Fatalf("evict: %v", err)
	}
	if len(result.Removed) != 0 {
		t.Fatalf("removed %v with no ceiling set, want nothing touched", result.Removed)
	}
	mustExist(t, old)
}

// TestEvictNeverRemovesTheProtectedSet is the dangerous half of the
// criterion: the set named as protected must survive even when it is the
// oldest set in a tree over ceiling - proving the exclusion is an explicit
// check, not merely an accident of a live run's own recency.
func TestEvictNeverRemovesTheProtectedSet(t *testing.T) {
	root := t.TempDir()
	now := time.Now().UTC()

	// Backdated on purpose: a real live run always has the newest CreatedAt,
	// but the guarantee under test must not depend on that being true.
	live := makeSet(t, root, "aaaa000000000000000000000000000000000a", "1111111111111111", now.Add(-30*day), 1000)
	other := makeSet(t, root, "bbbb000000000000000000000000000000000b", "2222222222222222", now, 1000)

	// A ceiling low enough that, unprotected, both sets would need to go.
	result, err := Evict(root, 100, live)
	if err != nil {
		t.Fatalf("evict: %v", err)
	}

	mustExist(t, live)
	mustNotExist(t, other)

	for _, s := range result.Removed {
		if s.Dir == live {
			t.Fatalf("the protected set %s was removed: %+v", live, result.Removed)
		}
	}
}

// TestEvictLeavesAnUnagedSetInPlace proves the decision recorded on Evict and
// Scan: a set with no honest CreatedAt is never chosen for removal, even
// when removing it would be the only way under ceiling - Evict reports what
// it could free rather than guessing an age to justify deleting it.
func TestEvictLeavesAnUnagedSetInPlace(t *testing.T) {
	root := t.TempDir()
	dir := filepath.Join(root, "dddd000000000000000000000000000000000d", "4444444444444444")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatalf("create set dir: %v", err)
	}
	if err := SaveRun(dir, Run{Version: Version + 1}); err != nil {
		t.Fatalf("save unreadable run.json: %v", err)
	}
	if err := os.WriteFile(filepath.Join(dir, "payload.bin"), make([]byte, 10_000), 0o600); err != nil {
		t.Fatalf("write payload: %v", err)
	}

	result, err := Evict(root, 1, "")
	if err != nil {
		t.Fatalf("evict: %v", err)
	}
	mustExist(t, dir)
	if len(result.Removed) != 0 {
		t.Fatalf("removed %v; the only set on disk has no honest age to justify that", result.Removed)
	}
}

// TestClearRemovesOneNamedSet is the manual half of REQUIREMENTS.md 2.9: a
// person names one set and it goes, regardless of ceiling or age.
func TestClearRemovesOneNamedSet(t *testing.T) {
	root := t.TempDir()
	now := time.Now().UTC()
	kept := makeSet(t, root, "aaaa000000000000000000000000000000000a", "1111111111111111", now, 100)
	gone := makeSet(t, root, "bbbb000000000000000000000000000000000b", "2222222222222222", now, 100)

	if err := Clear(root, "bbbb000000000000000000000000000000000b", "2222222222222222"); err != nil {
		t.Fatalf("clear: %v", err)
	}
	mustNotExist(t, gone)
	mustExist(t, kept)
}

// TestClearAllRemovesEverySet is the other manual half: with no ceiling set,
// this is the only way to reclaim the space at all.
func TestClearAllRemovesEverySet(t *testing.T) {
	root := t.TempDir()
	now := time.Now().UTC()
	a := makeSet(t, root, "aaaa000000000000000000000000000000000a", "1111111111111111", now, 100)
	b := makeSet(t, root, "bbbb000000000000000000000000000000000b", "2222222222222222", now, 100)

	result, err := ClearAll(root)
	if err != nil {
		t.Fatalf("clear all: %v", err)
	}
	mustNotExist(t, a)
	mustNotExist(t, b)
	if len(result.Removed) != 2 {
		t.Errorf("removed %d sets, want 2", len(result.Removed))
	}
}
