package web

import (
	"encoding/json"
	"errors"
	"net/http"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/madmurdok/torpeek/internal/cache"
	"github.com/madmurdok/torpeek/internal/core"
	"github.com/madmurdok/torpeek/internal/manifest"
)

// TOR-152: running a row again. The fixtures here are the ticket's own
// measured numbers, so nobody has to re-derive them: a run over TWO selected
// files, twenty frames asked of each, SIXTEEN taken of each, stopped on
// traffic with limit_hit "budget", 324583424 bytes received against a
// 314572800 ceiling, in 152416 ms of a 600000 ms limit.
const (
	stoppedSpent   = int64(324583424)
	stoppedCeiling = int64(314572800)
	stoppedElapsed = int64(152416)
	stoppedLimitMS = int64(600000)
	askedPerFile   = 20
	takenPerFile   = 16
)

// partialManifest is one file's manifest with taken frames of asked points -
// the rest recorded as the failures a stopped run leaves - carrying the cost
// the run had run up by the time this file was written.
//
// The frames are spread the way frames.Plan spreads them so the timecodes are
// distinct; nothing here re-implements that arithmetic, it only needs the
// points to differ.
func partialManifest(index int, path string, taken, asked int, cost manifest.Cost) manifest.Manifest {
	m := manifest.Manifest{
		Version: manifest.Version,
		File:    manifest.File{Index: index, Path: path, Bytes: 1 << 30, DurationMS: 600000, Container: "matroska"},
		Video:   manifest.Video{Codec: "h264", Width: 1920, Height: 1080},
		Cost:    cost,
	}
	for i := 0; i < asked; i++ {
		at := int64(i+1) * 20000
		if i < taken {
			ms := at
			m.Frames = append(m.Frames, manifest.Frame{
				Index: i, RequestedMS: at, ActualMS: &ms, Path: "placeholder",
				Width: 1920, Height: 1080,
			})
			continue
		}
		// A point the run never reached. Recorded, and deliberately not
		// counted as captured - see capturedFrames.
		m.Frames = append(m.Frames, manifest.Frame{
			Index: i, RequestedMS: at, Shift: manifest.ShiftFailed, Error: "budget",
		})
	}
	return m
}

// stoppedRun is the record a run that stopped at its ceiling leaves: both
// files asked for, neither of them complete.
func stoppedRun(infoHash, source string, files ...cache.File) cache.Run {
	run := cache.Run{
		Version:   cache.Version,
		Tool:      "test",
		CreatedAt: time.Now().UTC(),
		Source:    source,
		InfoHash:  infoHash,
		Name:      "Season 1",
		Plan: cache.Plan{
			Count: askedPerFile, Start: 0.05, End: 0.95,
			Profile: "min-traffic", Format: "jpeg",
		},
		Videos:   files,
		Selected: []int{},
		Complete: []int{},
	}
	for _, f := range files {
		run.Selected = append(run.Selected, f.Index)
	}
	return run
}

// writePartialSet lays down the whole fixture: the run record, and one
// manifest per file with real frame files under it.
func writePartialSet(t *testing.T, root, infoHash, params, source string,
	cost manifest.Cost, taken ...int) cache.Run {

	t.Helper()

	files := make([]cache.File, 0, len(taken))
	for i := range taken {
		files = append(files, cache.File{
			Index: i, Path: "s01e0" + strconv.Itoa(i+1) + ".mkv", Bytes: 1 << 30,
		})
	}
	run := stoppedRun(infoHash, source, files...)
	for i, n := range taken {
		if n >= askedPerFile {
			run.Complete = append(run.Complete, i)
		}
		buildCachedRun(t, root, infoHash, params, run,
			partialManifest(i, files[i].Path, n, askedPerFile, cost))
	}
	return run
}

// budgetCost is the cost a run stopped by its own TRAFFIC ceiling records.
func budgetCost() manifest.Cost {
	return manifest.Cost{
		DownloadedBytes: stoppedSpent, ElapsedMS: stoppedElapsed,
		LimitBytes: stoppedCeiling, LimitMS: stoppedLimitMS,
		LimitHit: string(core.StopBudget),
	}
}

// getTopUp reads the offer the way the page does.
func getTopUp(t *testing.T, base, infoHash, params string) (TopUp, int) {
	t.Helper()

	path := "/runs/" + infoHash + "/topup"
	if params != "" {
		path += "?params=" + params
	}
	resp := get(t, base, path)
	if resp.StatusCode != http.StatusOK {
		return TopUp{}, resp.StatusCode
	}

	var body struct {
		TopUp TopUp `json:"topup"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&body); err != nil {
		t.Fatalf("decode the top-up offer: %v", err)
	}
	return body.TopUp, resp.StatusCode
}

// serverOver is a server pointed at an output root, with a runner that
// records what it was handed.
func serverOver(t *testing.T, root string, roofBytes int64) (*fakeRuns, *Server, string) {
	t.Helper()

	cfg := DefaultConfig()
	cfg.OutputRoot = root
	cfg.RoofBytes = roofBytes
	fake := newFakeRuns()
	srv, ts := newTestServerWithConfig(t, cfg, fake.runner)
	return fake, srv, ts.URL
}

// TestTopUpStatesWhatItWillSpendBeforeItIsSpent is the acceptance criterion's
// first half, on the ticket's own numbers: the extra traffic a top-up will be
// allowed has to be a fact a person can read BEFORE pressing anything, and
// which ceiling stopped the run has to be visible with it.
func TestTopUpStatesWhatItWillSpendBeforeItIsSpent(t *testing.T) {
	root := t.TempDir()
	hash := "aa11bb22cc33dd44ee55ff66aa77bb88cc99dd00"
	writePartialSet(t, root, hash, "deadbeefdeadbeef", "magnet:?xt=urn:btih:"+hash,
		budgetCost(), takenPerFile, takenPerFile)

	_, _, base := serverOver(t, root, 0)

	offer, status := getTopUp(t, base, hash, "")
	if status != http.StatusOK {
		t.Fatalf("GET the top-up offer: status %d, want 200", status)
	}

	if offer.Refused != "" {
		t.Fatalf("a run with %d of %d frames on each of two files was refused: %s",
			takenPerFile, askedPerFile, offer.Refused)
	}
	// TOR-178: Complete is the flag the page now reads to tell "nothing
	// wrong, just whole" apart from every other refusal - it must not be set
	// on an offer that is not even refused, or a client trusting it alone
	// (without also checking Refused) would misread a genuine top-up as one.
	if offer.Complete {
		t.Error("a genuinely partial set (16 of 20 frames on each file) reports Complete - " +
			"that flag means there was nothing left to finish, and this set plainly has")
	}
	if offer.Count != askedPerFile || len(offer.Files) != 2 {
		t.Errorf("offer covers %d file(s) at %d frames, want 2 at %d",
			len(offer.Files), offer.Count, askedPerFile)
	}
	if want := 2 * takenPerFile; offer.Captured != want {
		t.Errorf("captured %d, want %d - the failed points a stopped run records "+
			"must not be counted as frames", offer.Captured, want)
	}
	if want := 2 * (askedPerFile - takenPerFile); offer.Remaining != want {
		t.Errorf("remaining %d, want %d", offer.Remaining, want)
	}

	// Which ceiling, and finer than core records it: the run stopped on
	// TRAFFIC, and it matters that this is not read as the clock (152s of a
	// 600s limit) or as the client-wide roof - a per-run raise is the wrong
	// lever for either.
	if offer.StoppedBy != string(core.StopBudget) || offer.Limit != LimitTraffic {
		t.Errorf("stopped_by %q / limit %q, want %q / %q",
			offer.StoppedBy, offer.Limit, string(core.StopBudget), LimitTraffic)
	}
	if offer.SpentBytes != stoppedSpent || offer.CeilingBytes != stoppedCeiling {
		t.Errorf("offer reports %d spent of %d, want %d of %d - the ceiling a person "+
			"actually met is not the number in the flag's help, and this is the "+
			"place that can say so", offer.SpentBytes, offer.CeilingBytes,
			stoppedSpent, stoppedCeiling)
	}

	// The figure itself: stated, more than nothing, and far less than
	// re-running the whole thing would have cost.
	if !offer.RaiseHelps {
		t.Error("raise_helps is false for a run its own traffic ceiling stopped - " +
			"raising that ceiling is exactly the lever here")
	}
	if offer.OfferBytes <= 0 {
		t.Fatalf("offer_bytes is %d, so the page has no figure to show before the "+
			"traffic is spent", offer.OfferBytes)
	}
	// TOR-166 replaced the assertion that used to sit here, which required
	// the figure to come in UNDER the 300 MB ceiling this run stopped at.
	// Both files are still short, so a plain re-run of them - MaxBytes unset,
	// core.budgetFor scaling to the file count - gets exactly that ceiling
	// again, and an offer under it would hand the run less than pressing
	// nothing. What is asserted instead is that the raise is neither below
	// what doing nothing gives nor above it, since this set's own receipt
	// prices the remaining eight points far under a two-file run's allowance.
	ordinary := core.DefaultBudget(2).MaxBytes
	if offer.OfferBytes < ordinary {
		t.Errorf("offer_bytes is %d, under the %d a plain re-run of the same two "+
			"still-short files is given - a raise that lowers the ceiling is not "+
			"a raise", offer.OfferBytes, ordinary)
	}
	if offer.OfferBytes > ordinary {
		t.Errorf("offer_bytes is %d, over the %d a plain re-run of those two files "+
			"would be allowed, though the set's own receipt prices the eight "+
			"remaining points at %d - a ceiling, not a blank cheque",
			offer.OfferBytes, ordinary,
			stoppedSpent*int64(2*(askedPerFile-takenPerFile))/int64(2*takenPerFile))
	}
	if offer.RoofCapped || offer.RoofBytes != 0 {
		t.Errorf("offer reports roof %d capped=%v on a server with no roof",
			offer.RoofBytes, offer.RoofCapped)
	}
}

// TestTopUpRunsWithTheRaisedCeilingItStated is the arms-apart half: a test
// that passed whether or not the ceiling was actually raised would prove
// nothing, so this reads the request the ENGINE was handed and checks the
// raise is on it - and that it is the figure the page was shown, not some
// other number.
func TestTopUpRunsWithTheRaisedCeilingItStated(t *testing.T) {
	root := t.TempDir()
	hash := "bb11bb22cc33dd44ee55ff66aa77bb88cc99dd00"
	source := "magnet:?xt=urn:btih:" + hash
	writePartialSet(t, root, hash, "deadbeefdeadbeef", source,
		budgetCost(), takenPerFile, takenPerFile)

	fake, _, base := serverOver(t, root, 0)

	offer, _ := getTopUp(t, base, hash, "")

	resp := post(t, base, "/runs/topup", `{"infohash":"`+hash+`"}`)
	if resp.StatusCode != http.StatusAccepted {
		t.Fatalf("POST /runs/topup: status %d, want 202: %s", resp.StatusCode, readAll(t, resp))
	}

	waitFor(t, func() bool { return fake.started(source) })
	req := fake.stream(t, source).req

	if req.MaxBytes != offer.OfferBytes {
		t.Errorf("the run was given a ceiling of %d, want the %d the page was shown. "+
			"A zero here is the ordinary scaled ceiling - the raise never happened",
			req.MaxBytes, offer.OfferBytes)
	}
	if req.MaxBytes == 0 {
		t.Error("the run was given no raised ceiling at all, so it will stop exactly " +
			"where the last one did")
	}
	if req.Source != source {
		t.Errorf("the run was given source %q, want the record's own %q - a top-up "+
			"must not need the link pasted back in", req.Source, source)
	}
	if req.Count != askedPerFile || req.Mode != "min-traffic" {
		t.Errorf("the run was given count %d mode %q, want %d %q - a different plan "+
			"writes a different result set and reuses none of its frames",
			req.Count, req.Mode, askedPerFile, "min-traffic")
	}
	if req.Window == nil {
		t.Fatal("the run carries no capture window, so it takes this server's current " +
			"-start/-end/-format instead of the set's own - which writes a sibling " +
			"result set and pays full price")
	}
	if req.Window.Start != 0.05 || req.Window.End != 0.95 || req.Window.Format != "jpeg" {
		t.Errorf("window is %+v, want the record's own 0.05-0.95 jpeg", *req.Window)
	}
}

// TestTopUpRunsOnlyTheFilesStillShort keeps a top-up from paying to
// re-inspect a file that came out whole: its container would be read again
// (pieces are discarded after every run) to reuse frames that were never in
// doubt.
func TestTopUpRunsOnlyTheFilesStillShort(t *testing.T) {
	root := t.TempDir()
	hash := "cc11bb22cc33dd44ee55ff66aa77bb88cc99dd00"
	source := "magnet:?xt=urn:btih:" + hash
	// File 0 came out whole; file 1 is four points short.
	writePartialSet(t, root, hash, "deadbeefdeadbeef", source,
		budgetCost(), askedPerFile, takenPerFile)

	fake, _, base := serverOver(t, root, 0)

	resp := post(t, base, "/runs/topup", `{"infohash":"`+hash+`"}`)
	if resp.StatusCode != http.StatusAccepted {
		t.Fatalf("POST /runs/topup: status %d, want 202: %s", resp.StatusCode, readAll(t, resp))
	}

	waitFor(t, func() bool { return fake.started(source) })
	req := fake.stream(t, source).req

	if len(req.Files) != 1 || req.Files[0] != "1" {
		t.Errorf("the top-up asked for files %v, want only [\"1\"] - file 0 already "+
			"has every frame it was asked for", req.Files)
	}
}

// TestTopUpOfARoofStoppedRunOffersNoRaise is the ticket's second question.
// core reports the client-wide roof as its own stop reason (StopRoof against
// StopBudget) precisely so a client can tell them apart, and this is what
// acting on that distinction looks like: the roof is not this run's ceiling,
// so raising this run's ceiling changes nothing, and offering to would be a
// cost consented to for no reason.
func TestTopUpOfARoofStoppedRunOffersNoRaise(t *testing.T) {
	root := t.TempDir()
	hash := "dd11bb22cc33dd44ee55ff66aa77bb88cc99dd00"
	source := "magnet:?xt=urn:btih:" + hash
	cost := budgetCost()
	cost.LimitHit = string(core.StopRoof)
	writePartialSet(t, root, hash, "deadbeefdeadbeef", source, cost, takenPerFile, takenPerFile)

	fake, _, base := serverOver(t, root, 512<<20)

	offer, _ := getTopUp(t, base, hash, "")
	if offer.Limit != LimitRoof {
		t.Errorf("limit is %q, want %q - a run the roof stopped must not be reported "+
			"as one its own budget stopped", offer.Limit, LimitRoof)
	}
	if offer.RaiseHelps {
		t.Error("raise_helps is true for a run the CLIENT-WIDE roof stopped - a bigger " +
			"per-run allowance is the wrong lever, and saying otherwise is a promise " +
			"a person only finds out about by spending")
	}
	if offer.OfferBytes != 0 {
		t.Errorf("offer_bytes is %d, want 0 - the ordinary ceiling - for a run no "+
			"raise would help", offer.OfferBytes)
	}

	resp := post(t, base, "/runs/topup", `{"infohash":"`+hash+`"}`)
	if resp.StatusCode != http.StatusAccepted {
		t.Fatalf("POST /runs/topup: status %d, want 202 - filling the gaps is still "+
			"worth trying, it just carries no raise: %s", resp.StatusCode, readAll(t, resp))
	}
	waitFor(t, func() bool { return fake.started(source) })
	if got := fake.stream(t, source).req.MaxBytes; got != 0 {
		t.Errorf("the run was given a raised ceiling of %d after the ROOF stopped it; "+
			"the roof is not a per-run ceiling and raising one does not move it", got)
	}
}

// TestTopUpOfATimeStoppedRunOffersNoRaise is the same argument for the other
// ceiling, now read directly (TOR-161: core reports the clock as its own
// reason, core.StopTime, instead of folding it into StopBudget) - and a run
// that ran out of TIME is not one more traffic helps either.
//
// ElapsedMS is deliberately left FAR under the time ceiling here (unlike
// before TOR-161, when this test had to set it equal to the limit to be
// classified as a time stop at all). That is the point of the fixture: if
// absorbCost were still inferring the reason from elapsed-against-limit
// instead of reading c.LimitHit directly, this record would be read as a
// TRAFFIC stop (elapsed is nowhere near the limit) rather than the time stop
// it actually records. Only a direct read of LimitHit gets this right.
func TestTopUpOfATimeStoppedRunOffersNoRaise(t *testing.T) {
	root := t.TempDir()
	hash := "ee11bb22cc33dd44ee55ff66aa77bb88cc99dd00"
	cost := budgetCost()
	cost.DownloadedBytes = 40 << 20      // nowhere near its byte ceiling
	cost.ElapsedMS = stoppedLimitMS / 10 // and nowhere near its time ceiling either
	cost.LimitHit = string(core.StopTime)
	writePartialSet(t, root, hash, "deadbeefdeadbeef", "magnet:?xt=urn:btih:"+hash,
		cost, takenPerFile, takenPerFile)

	_, _, base := serverOver(t, root, 0)

	offer, _ := getTopUp(t, base, hash, "")
	if offer.Limit != LimitTime {
		t.Errorf("limit is %q, want %q - this run spent 40 MB of a 300 MB ceiling and "+
			"only a tenth of its time ceiling, yet core recorded LimitHit as %q "+
			"directly; calling that a traffic stop would offer more traffic to a "+
			"run that never ran out of any", offer.Limit, LimitTime, core.StopTime)
	}
	if offer.RaiseHelps || offer.OfferBytes != 0 {
		t.Errorf("raise_helps=%v offer_bytes=%d for a run the clock stopped",
			offer.RaiseHelps, offer.OfferBytes)
	}
}

// TestTopUpFallsBackToTheElapsedComparisonForAPreTOR161Record covers the
// backward-compatibility half of TOR-161: a manifest.Cost written by a
// torpeek from before this change can only ever say LimitHit "budget" for a
// run its own clock stopped - core.StopTime did not exist yet - so absorbCost
// falls back to the same elapsed-against-limit comparison this package used
// exclusively before. This is a best-effort reading of an ambiguous old
// record, not a certainty; see LimitTime's own doc.
//
// The arm that makes this mean something: TestTopUpStatesWhatItWillSpendBeforeItIsSpent
// exercises the SAME "budget" LimitHit value with elapsed well under the
// limit and gets LimitTraffic - so the fallback comparison, not the mere
// presence of "budget", is what is under test here.
func TestTopUpFallsBackToTheElapsedComparisonForAPreTOR161Record(t *testing.T) {
	root := t.TempDir()
	hash := "ff11bb22cc33dd44ee55ff66aa77bb88cc99dd00"
	cost := budgetCost()            // LimitHit "budget" - the only value a pre-TOR-161 torpeek ever wrote
	cost.DownloadedBytes = 40 << 20 // nowhere near its byte ceiling
	cost.ElapsedMS = stoppedLimitMS // and out of time exactly
	writePartialSet(t, root, hash, "deadbeefdeadbeef", "magnet:?xt=urn:btih:"+hash,
		cost, takenPerFile, takenPerFile)

	_, _, base := serverOver(t, root, 0)

	offer, _ := getTopUp(t, base, hash, "")
	if offer.Limit != LimitTime {
		t.Errorf("limit is %q, want %q - a pre-TOR-161 record can only say \"budget\" "+
			"for a clock stop too, and this one has elapsed at exactly its time "+
			"ceiling with almost no traffic spent", offer.Limit, LimitTime)
	}
	if offer.StoppedBy != string(core.StopBudget) {
		t.Errorf("stopped_by = %q, want %q - StoppedBy stays what the record actually "+
			"says, ambiguous or not; only Limit is the refined reading",
			offer.StoppedBy, string(core.StopBudget))
	}
	if offer.RaiseHelps || offer.OfferBytes != 0 {
		t.Errorf("raise_helps=%v offer_bytes=%d for a record read as a clock stop",
			offer.RaiseHelps, offer.OfferBytes)
	}
}

// TestTopUpIsCappedByTheClientWideRoof is the acceptance criterion's last
// clause. core.Roof exists so a per-run decision cannot spend the whole
// allowance, and a top-up must not become a way around it: the run is held to
// the roof at runtime whatever this says (BudgetTracker.Exhausted asks the
// roof first), and what this test is about is that the FIGURE ON SCREEN never
// promises more than the roof would allow either.
//
// The roof here sits ABOVE what finishing costs at the very least (81145856,
// eight points at the set's own measured average) and below the offer. That
// is the band where clamping is honest: the run can still finish, it simply
// may not have the whole two-file allowance to do it in.
// TestTopUpSaysSoWhenTheRoofCannotCoverFinishing is the other side of that
// line, where clamping would be a promise nothing can keep (TOR-166).
func TestTopUpIsCappedByTheClientWideRoof(t *testing.T) {
	root := t.TempDir()
	hash := "ff11bb22cc33dd44ee55ff66aa77bb88cc99dd00"
	source := "magnet:?xt=urn:btih:" + hash
	writePartialSet(t, root, hash, "deadbeefdeadbeef", source,
		budgetCost(), takenPerFile, takenPerFile)

	const roof = 100 << 20
	fake, _, base := serverOver(t, root, roof)

	offer, _ := getTopUp(t, base, hash, "")
	if !offer.RoofCapped {
		t.Error("roof_capped is false though the roof is smaller than what finishing " +
			"needs - a page told otherwise would state a figure nothing could honour")
	}
	if offer.OfferBytes != roof {
		t.Errorf("offer_bytes is %d, want the roof's own %d", offer.OfferBytes, roof)
	}
	if offer.RoofBytes != roof {
		t.Errorf("roof_bytes is %d, want %d", offer.RoofBytes, roof)
	}

	resp := post(t, base, "/runs/topup", `{"infohash":"`+hash+`"}`)
	if resp.StatusCode != http.StatusAccepted {
		t.Fatalf("POST /runs/topup: status %d, want 202: %s", resp.StatusCode, readAll(t, resp))
	}
	waitFor(t, func() bool { return fake.started(source) })
	if got := fake.stream(t, source).req.MaxBytes; got > roof {
		t.Errorf("the run was given %d, over the client-wide roof of %d - topping up "+
			"must not be a way around the one ceiling that does not multiply", got, roof)
	}
}

// TestTopUpRefusesASetWithNothingMissing keeps the button honest about the
// one case where there is nothing to do.
func TestTopUpRefusesASetWithNothingMissing(t *testing.T) {
	root := t.TempDir()
	hash := "1111bb22cc33dd44ee55ff66aa77bb88cc99dd00"
	cost := budgetCost()
	cost.LimitHit = ""
	writePartialSet(t, root, hash, "deadbeefdeadbeef", "magnet:?xt=urn:btih:"+hash,
		cost, askedPerFile, askedPerFile)

	_, _, base := serverOver(t, root, 0)

	offer, status := getTopUp(t, base, hash, "")
	if status != http.StatusOK {
		t.Fatalf("GET the top-up offer: status %d, want 200 - the set is there, it "+
			"simply has nothing missing", status)
	}
	if offer.Refused == "" {
		t.Error("a complete set offered a top-up, which would re-inspect every file " +
			"to reuse every frame")
	}
	// TOR-178: this is the ONE refusal reason app.js no longer draws as a
	// line in the run-again block - the row's own DONE/PARTIAL badge already
	// says a whole set is whole. Complete is how the page tells this refusal
	// apart from the other five (a stale record, files missing from disk),
	// which still get drawn in full - so it has to be true here, on exactly
	// the set that has nothing missing.
	if !offer.Complete {
		t.Error("a set with nothing missing (askedPerFile of askedPerFile on both files) " +
			"does not report Complete - the page has no way left to tell this refusal apart " +
			"from one that explains an actual problem with the record")
	}

	resp := post(t, base, "/runs/topup", `{"infohash":"`+hash+`"}`)
	if resp.StatusCode != http.StatusConflict {
		t.Errorf("POST /runs/topup on a complete set: status %d, want 409", resp.StatusCode)
	}
}

// TestTopUpOfAnUnknownSetIsNotFound separates "there is no such run" from
// "that run cannot be finished" - the first is an address that names nothing
// and draws no control at all, the second is a sentence a person reads.
func TestTopUpOfAnUnknownSetIsNotFound(t *testing.T) {
	_, _, base := serverOver(t, t.TempDir(), 0)

	if _, status := getTopUp(t, base, "2222bb22cc33dd44ee55ff66aa77bb88cc99dd00", ""); status != http.StatusNotFound {
		t.Errorf("GET the top-up offer for a torrent with no record: status %d, want 404", status)
	}
	if _, status := getTopUp(t, base, "not-a-hash", ""); status != http.StatusNotFound {
		t.Errorf("GET the top-up offer for a malformed infohash: status %d, want 404", status)
	}
}

// TestTopUpReArmsTheRowItWasStartedFrom is TOR-140's rule under this
// ticket's pressure: a torrent is ONE row, and a top-up is not a new torrent.
// The finished entry the page is already showing is put back in the queue
// under its own id - so the registry never holds two entries for one result
// set, which is what would put two rows in GET /runs the moment the second
// one finished.
func TestTopUpReArmsTheRowItWasStartedFrom(t *testing.T) {
	root := t.TempDir()
	hash := "3333bb22cc33dd44ee55ff66aa77bb88cc99dd00"
	source := "magnet:?xt=urn:btih:" + hash
	writePartialSet(t, root, hash, "deadbeefdeadbeef", source,
		budgetCost(), takenPerFile, takenPerFile)

	fake, srv, base := serverOver(t, root, 0)

	// A run of this torrent that this process itself finished, so the page's
	// row has a live id rather than being a disk-only one.
	first := startRun(t, base, source)
	waitFor(t, func() bool { return fake.started(source) })
	fake.send(t, source, core.MetadataReady{Name: "Season 1", InfoHash: hash})
	fake.finish(t, source)
	waitFor(t, func() bool { return stateOf(t, srv, first.id) == RunDone })

	resp := post(t, base, "/runs/topup", `{"infohash":"`+hash+`","id":"`+first.id+`"}`)
	if resp.StatusCode != http.StatusAccepted {
		t.Fatalf("POST /runs/topup: status %d, want 202: %s", resp.StatusCode, readAll(t, resp))
	}
	body := decodeBody(t, resp)

	if got, _ := body["id"].(string); got != first.id {
		t.Errorf("the top-up answered with run %q, want the row's own %q - a second id "+
			"is a second registry entry, and two entries for one result set are two "+
			"rows in GET /runs as soon as both are final", got, first.id)
	}

	live := 0
	for _, info := range srv.snapshot() {
		if info.InfoHash == hash || info.Source == source {
			live++
		}
	}
	if live != 1 {
		t.Errorf("the registry holds %d entries for one torrent, want 1", live)
	}
}

// TestRetryRunsTheSameRequestAgainAtTheSameCeiling is the other half of the
// ticket, and the one the owner asked as a question: a torrent whose metadata
// never arrived had no control at all, and the only way back was to paste the
// magnet again.
//
// It raises NOTHING, and that is the point of it being a separate verb: a run
// that never reached a ceiling is not one a bigger ceiling helps.
func TestRetryRunsTheSameRequestAgainAtTheSameCeiling(t *testing.T) {
	fake := newFakeRuns()
	srv, ts := newTestServer(t, fake.runner)
	base := ts.URL

	source := "magnet:?xt=urn:btih:4444bb22cc33dd44ee55ff66aa77bb88cc99dd00"
	run := startRun(t, base, source)
	waitFor(t, func() bool { return fake.started(source) })
	fake.send(t, source, core.Failed{
		File: -1, Code: core.CodeNoMetadata,
		Err: errors.New("metadata not received after 1m0s"),
	})
	fake.finish(t, source)
	waitFor(t, func() bool { return stateOf(t, srv, run.id) == RunFailed })

	resp := post(t, base, "/runs/retry", `{"id":"`+run.id+`"}`)
	if resp.StatusCode != http.StatusAccepted {
		t.Fatalf("POST /runs/retry: status %d, want 202: %s", resp.StatusCode, readAll(t, resp))
	}
	if got, _ := decodeBody(t, resp)["id"].(string); got != run.id {
		t.Errorf("the retry answered with run %q, want the row's own %q - one torrent, "+
			"one row (TOR-140)", got, run.id)
	}

	waitFor(t, func() bool { return fake.count() == 2 })
	req := fake.stream(t, source).req
	if req.Source != source {
		t.Errorf("the retry ran %q, want %q", req.Source, source)
	}
	if req.MaxBytes != 0 {
		t.Errorf("the retry was given a raised ceiling of %d; a run that produced "+
			"nothing never met a ceiling, so raising one is a cost for nothing",
			req.MaxBytes)
	}
}

// TestRetryRefusesARunThatIsStillGoing: there is nothing to retry yet, and
// Cancel is the button for a run somebody wants stopped.
func TestRetryRefusesARunThatIsStillGoing(t *testing.T) {
	fake := newFakeRuns()
	_, ts := newTestServer(t, fake.runner)

	source := "magnet:?xt=urn:btih:5555bb22cc33dd44ee55ff66aa77bb88cc99dd00"
	run := startRun(t, ts.URL, source)
	waitFor(t, func() bool { return fake.started(source) })

	resp := post(t, ts.URL, "/runs/retry", `{"id":"`+run.id+`"}`)
	if resp.StatusCode != http.StatusConflict {
		t.Errorf("POST /runs/retry on a running run: status %d, want 409", resp.StatusCode)
	}
	if resp.StatusCode == http.StatusAccepted && fake.count() != 1 {
		t.Error("retrying a running run started it a second time")
	}
}

// TestRetryOfADroppedTorrentIsRefused: the staged copy of an uploaded
// .torrent is removed when its run ends (handleUploadTorrent), so its
// recorded source is a path that no longer exists. Refused with the reason,
// rather than started and left to fail minutes later on a path nobody typed -
// the same caveat Regenerate already documents.
func TestRetryOfADroppedTorrentIsRefused(t *testing.T) {
	fake := newFakeRuns()
	_, ts := newTestServer(t, fake.runner)

	resp := uploadTorrent(t, ts.URL, []byte("d4:infod6:lengthi1eee"), "")
	if resp.StatusCode != http.StatusAccepted {
		t.Fatalf("POST /runs/upload: status %d, want 202", resp.StatusCode)
	}
	id, _ := decodeBody(t, resp)["id"].(string)
	waitFor(t, func() bool { return fake.count() == 1 })

	retry := post(t, ts.URL, "/runs/retry", `{"id":"`+id+`"}`)
	if retry.StatusCode != http.StatusBadRequest {
		t.Errorf("POST /runs/retry for a dropped .torrent: status %d, want 400",
			retry.StatusCode)
	}
}

// TestRetryOfAnUnknownRunIsNotFound keeps a stale page's click from reading
// as a server error.
func TestRetryOfAnUnknownRunIsNotFound(t *testing.T) {
	_, ts := newTestServer(t, newFakeRuns().runner)

	if resp := post(t, ts.URL, "/runs/retry", `{"id":"nope"}`); resp.StatusCode != http.StatusNotFound {
		t.Errorf("POST /runs/retry for an unknown run: status %d, want 404", resp.StatusCode)
	}
}

// TestTheServedPageOffersToppingUpAndRetrying is as far as a Go test can
// reach into app.js: there is no JS runner in this project, so the page is
// guarded by asserting the text that actually ships (the same shape
// TestTheFrontendUsesNoAbsolutePaths uses).
//
// What it can prove: both endpoints are called, the block exists, and the two
// sentences this ticket insists on saying out loud are in the file - the one
// about pieces being discarded, and the one about the roof being the wrong
// lever. What it CANNOT prove is that any of it renders; that is the browser
// pass.
func TestTheServedPageOffersToppingUpAndRetrying(t *testing.T) {
	js, err := embedded.ReadFile("assets/app.js")
	if err != nil {
		t.Fatalf("reading the embedded page: %v", err)
	}
	page := string(js)

	for _, want := range []string{
		`post("runs/topup"`,
		`post("runs/retry"`,
		`"/topup"`,
		"run-again-go",
		"run-again-retry",
		"run-again-cost",
		// "function liveRowFor" was here: one torrent, one row (TOR-140),
		// kept true from the page while a top-up was going. TOR-162 moved
		// that rule into listing.go, where every consumer of GET /runs gets
		// it, and deleted this copy - so the assertion moved too, inverted,
		// to TestThePageKeepsNoMergeRuleOfItsOwn (listing_test.go), which
		// now guards that the page has no merge rule of its own at all.
		// The invariant itself is checked at the source by
		// TestATopUpInFlightIsStillOneRow.
		//
		// The figure, before it is spent - and since TOR-166, what kind of
		// figure it is. It is sized so one press finishes (core.TopUpBytes),
		// which makes it deliberately larger than the work: a page that
		// presented it as the cost would have a person refusing a top-up over
		// a number nothing is going to spend.
		"more traffic",
		"not an estimate of what it will cost",
		// Pieces are discarded after every run (REQUIREMENTS.md 2.9), so a
		// top-up re-fetches piece data for the points it still needs. Only
		// the frames are reused, and the page has to say so.
		"discarded after every run",
		// The roof is not a per-run ceiling, and the page must not offer to
		// raise one as though it were.
		"raising this run's allowance would change nothing",
	} {
		if !strings.Contains(page, want) {
			t.Errorf("the served app.js never mentions %q", want)
		}
	}
}

// ---- TOR-178: two lines dropped from the run detail, one of them a choice ----
//
// "every frame this run asked for is already on disk" and "N of M video
// file(s) selected" both used to render in the detail. The owner asked for
// both gone, on the grounds that the row's own DONE/PARTIAL badge already
// carries the first. The two are NOT equally redundant, though: the second
// names a fact - how many video files the torrent holds (M) - that appears
// nowhere else on the page, so removing the sentence without giving M
// another home would silently drop the answer to "did I take all of it, or
// a slice" (the same arithmetic TOR-50's trap is about). This test guards
// both halves: the sentence is gone from the served script, and the figure
// it used to carry still is a live spec, not a lost one.
func TestTheDetailDropsTheRedundantLineButKeepsTheFileCount(t *testing.T) {
	js := appJS(t)

	// The exact sentence metadata_ready used to draw on .torrent-summary. If
	// this comes back, either directly or via a differently-worded
	// reintroduction of "selected" against ev.videos.length, this guard is
	// the thing that should catch it - not a person noticing the page looks
	// busier again.
	if strings.Contains(js, "video file(s) selected") {
		t.Error("the served app.js still draws the removed \"N of M video file(s) selected\" " +
			"line - TOR-178 asked for it gone")
	}

	// M's new home: the per-file Metadata disclosure (onFileStarted's specs),
	// the only torrent-scoped-fact-adjacent disclosure that exists anywhere
	// in this detail. Dropping this spec would be the "half-way" outcome the
	// ticket named explicitly: the sentence gone and the figure gone with it,
	// with nothing recording that as a decision.
	if !strings.Contains(js, `"Torrent files"`) {
		t.Error(`the served app.js no longer offers a "Torrent files" spec - TOR-178 moved the ` +
			"torrent's own video-file count there when the summary line that used to carry it " +
			"was removed")
	}

	// The collapsing half: a refused top-up whose ONLY problem is
	// completeness (TopUp.Complete) must not draw entry.topup.refused as a
	// line of its own - that text is exactly the second redundant sentence,
	// now a server string instead of a page literal. Keying this off a
	// structured field rather than matching the refused text against a
	// literal keeps TopUp.Refused's own contract intact (its doc: "a
	// sentence rather than a code... nothing for a client to branch on",
	// Complete aside).
	// Keyed on the negated form, "!t.complete", specifically: that string
	// only occurs where the flag actually gates the refusal line (showRefusal
	// in renderAgain), not wherever the identifier merely gets mentioned in a
	// comment - a looser "t.complete" substring would still pass if the
	// wiring were renamed away and only prose kept saying it existed.
	if !strings.Contains(js, "!t.complete") {
		t.Error("the served app.js has no reference to topup.complete - the run-again block has " +
			"no way left to tell a genuinely-nothing-wrong refusal apart from every other one, " +
			"so it would either draw the redundant sentence again or hide a refusal that needed " +
			"reading")
	}
}

// stateOf reads one run's state out of the registry.
func stateOf(t *testing.T, srv *Server, id string) RunState {
	t.Helper()

	for _, info := range srv.snapshot() {
		if info.ID == id {
			return info.State
		}
	}
	return ""
}

// ---- TOR-166: one press, priced off the two manifests that recorded a miss. ----
//
// The set these numbers come from is the one in the ticket: two files, twenty
// frames asked of each, and two rounds of finishing it that left two
// receipts.
//
//	file 00: limit_bytes 8388608 (8 MiB), downloaded_bytes 20971520 (20 MiB), limit_hit "budget"
//	file 01: limit_bytes 81788928 (78 MiB), downloaded_bytes 79396864 (75.7 MiB), limit_hit ""
//
// core's own budget_test.go works the arithmetic on them. What is checked
// here is the whole path a person meets: the offer GET /runs/{ih}/topup
// states, and the ceiling POST /runs/topup then hands the engine.
const (
	// missedSpent is what this set's receipt said when the round that missed
	// was priced, and missedCeiling the 8 MiB it was handed for the single
	// point still owed.
	missedSpent   = int64(79396864)
	missedCeiling = int64(81788928)
	// missedCost is what that round then downloaded. It is 2.5x the ceiling
	// it was given, and it is the number any honest offer has to cover.
	missedCost = int64(20971520)
)

// nearlyDoneCost is the receipt this set carried when the miss was priced:
// stopped on its own traffic ceiling, nowhere near its clock.
func nearlyDoneCost() manifest.Cost {
	return manifest.Cost{
		DownloadedBytes: missedSpent, ElapsedMS: stoppedElapsed,
		LimitBytes: missedCeiling, LimitMS: stoppedLimitMS,
		LimitHit: string(core.StopBudget),
	}
}

// TestTopUpFinishesTheRecordedSetInOnePress is the ticket's acceptance
// criterion on the ticket's own set: one point of forty still missing, one
// file still short, and the round that followed cost 20971520 bytes.
//
// It is an arms-apart test twice over. The offer must COVER 20971520, which
// the 8388608 the manifest records does not; and it must not exceed what a
// plain run of the one still-short file would be allowed, which the cap
// (2 GiB) does. A pricing that changed nothing fails the first, and one that
// answered "as much as a run can ever have" fails the second.
func TestTopUpFinishesTheRecordedSetInOnePress(t *testing.T) {
	root := t.TempDir()
	hash := "6666bb22cc33dd44ee55ff66aa77bb88cc99dd00"
	source := "magnet:?xt=urn:btih:" + hash
	// File 0 is one point short; file 1 came out whole.
	writePartialSet(t, root, hash, "deadbeefdeadbeef", source,
		nearlyDoneCost(), askedPerFile-1, askedPerFile)

	fake, _, base := serverOver(t, root, 0)

	offer, status := getTopUp(t, base, hash, "")
	if status != http.StatusOK {
		t.Fatalf("GET the top-up offer: status %d, want 200", status)
	}
	if offer.Remaining != 1 || offer.Captured != 2*askedPerFile-1 {
		t.Fatalf("the set reads as %d captured / %d remaining, want %d / 1 - this is "+
			"not the state the recorded ceiling was priced from",
			offer.Captured, offer.Remaining, 2*askedPerFile-1)
	}

	if offer.OfferBytes < missedCost {
		t.Errorf("offer_bytes is %d for a round that went on to download %d. That is "+
			"the recorded miss: the ceiling was under the work, the round stopped on "+
			"it, and finishing took another press", offer.OfferBytes, missedCost)
	}
	// A raise below the ordinary ceiling is not a raise. Zero MaxBytes on the
	// request means core.budgetFor scales to the file count, so an offer under
	// DefaultBudget(1) hands the run LESS than never pressing the button.
	ordinary := core.DefaultBudget(1).MaxBytes
	if offer.OfferBytes < ordinary {
		t.Errorf("offer_bytes is %d, under the %d the same one-file run gets with no "+
			"raise at all - the button labelled more traffic gives it less",
			offer.OfferBytes, ordinary)
	}
	if offer.OfferBytes > ordinary {
		t.Errorf("offer_bytes is %d, over the %d a plain run of the one still-short "+
			"file would be allowed, though this set's own receipt prices the "+
			"remaining point far under that - a ceiling, not a blank cheque",
			offer.OfferBytes, ordinary)
	}

	// And the figure is what the engine is actually handed, not a number the
	// page was shown and the run then ignored.
	resp := post(t, base, "/runs/topup", `{"infohash":"`+hash+`"}`)
	if resp.StatusCode != http.StatusAccepted {
		t.Fatalf("POST /runs/topup: status %d, want 202: %s", resp.StatusCode, readAll(t, resp))
	}
	waitFor(t, func() bool { return fake.started(source) })
	req := fake.stream(t, source).req

	if req.MaxBytes != offer.OfferBytes {
		t.Errorf("the run was given %d, want the %d the page was shown",
			req.MaxBytes, offer.OfferBytes)
	}
	if req.MaxBytes < missedCost {
		t.Errorf("the run was given %d for work the manifests record at %d, so it "+
			"stops where the last one did and the owner presses again",
			req.MaxBytes, missedCost)
	}
	if len(req.Files) != 1 || req.Files[0] != "0" {
		t.Errorf("the top-up asked for files %v, want only [\"0\"]", req.Files)
	}
}

// TestTopUpSaysSoWhenTheRoofCannotCoverFinishing is the last clause of the
// criterion, and the one thing a top-up must never do quietly.
//
// Clamping the figure to the roof is right when the roof can still cover the
// work - the run finishes and the page never promised more than the roof
// allows. It is a LIE when the roof is under what finishing costs at the very
// least: the run is then guaranteed to stop short, and offering it is
// offering another instalment of an allowance that can never reach the end.
// So the least the work can cost is computed from the set's own receipt - the
// prorated average, which the recorded miss proves is an UNDER-estimate and
// therefore a sound floor - and a roof beneath it is said out loud instead.
func TestTopUpSaysSoWhenTheRoofCannotCoverFinishing(t *testing.T) {
	root := t.TempDir()
	hash := "7777bb22cc33dd44ee55ff66aa77bb88cc99dd00"
	source := "magnet:?xt=urn:btih:" + hash
	writePartialSet(t, root, hash, "deadbeefdeadbeef", source,
		budgetCost(), takenPerFile, takenPerFile)

	// Eight points still owed off a receipt of 324583424 over 32 captured:
	// finishing costs at least 81145856, and this roof is a tenth of that.
	const roof = 8 << 20
	fake, _, base := serverOver(t, root, roof)

	offer, status := getTopUp(t, base, hash, "")
	if status != http.StatusOK {
		t.Fatalf("GET the top-up offer: status %d, want 200", status)
	}
	if offer.Refused == "" {
		t.Errorf("a set whose remaining points cost at least %d was offered a top-up "+
			"under a client-wide roof of %d. Every press of it stops on the roof "+
			"having spent, and none of them ever finishes - that has to be said, "+
			"not sold as another instalment",
			stoppedSpent*int64(2*(askedPerFile-takenPerFile))/int64(2*takenPerFile), int64(roof))
	}
	if offer.OfferBytes != 0 {
		t.Errorf("offer_bytes is %d on a set that cannot be finished at all; a figure "+
			"here is a promise nothing can honour", offer.OfferBytes)
	}
	if offer.RoofBytes != roof {
		t.Errorf("roof_bytes is %d, want %d - the roof is still reported", offer.RoofBytes, roof)
	}
	// Plainly said means both figures, and rounded so neither flatters the
	// other: 8388608 bytes of roof is 8 MB and never 9, while 81145856 bytes
	// of work is 82 MB and never 81. A sentence that rounded the roof up
	// would hand the reader headroom nobody has.
	if !strings.Contains(offer.Refused, "8 MB") || !strings.Contains(offer.Refused, "82 MB") {
		t.Errorf("the refusal reads %q - it has to name the roof there actually is "+
			"(8 MB, rounded down) and the least finishing costs (82 MB, rounded "+
			"up), or a person cannot see why it cannot be done", offer.Refused)
	}

	resp := post(t, base, "/runs/topup", `{"infohash":"`+hash+`"}`)
	if resp.StatusCode != http.StatusConflict {
		t.Errorf("POST /runs/topup on a set the roof cannot finish: status %d, want "+
			"409 - starting it would spend the rest of the roof and stop short",
			resp.StatusCode)
	}
	if fake.count() != 0 {
		t.Errorf("%d run(s) were started for a set that cannot be finished", fake.count())
	}
}
