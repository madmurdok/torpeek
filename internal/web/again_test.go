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
	if offer.OfferBytes >= stoppedCeiling {
		t.Errorf("offer_bytes is %d, not less than the %d ceiling this run stopped "+
			"at - four points in twenty must not be priced like twenty",
			offer.OfferBytes, stoppedCeiling)
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
func TestTopUpIsCappedByTheClientWideRoof(t *testing.T) {
	root := t.TempDir()
	hash := "ff11bb22cc33dd44ee55ff66aa77bb88cc99dd00"
	source := "magnet:?xt=urn:btih:" + hash
	writePartialSet(t, root, hash, "deadbeefdeadbeef", source,
		budgetCost(), takenPerFile, takenPerFile)

	const roof = 8 << 20
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
		// The figure, before it is spent.
		"more traffic",
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
