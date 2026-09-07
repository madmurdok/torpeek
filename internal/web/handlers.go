package web

import (
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"
	"unicode"
	"unicode/utf8"

	"github.com/gorilla/websocket"

	"github.com/madmurdok/torpeek/internal/cache"
	"github.com/madmurdok/torpeek/internal/core"
)

// maxTorrentUpload bounds a dropped .torrent, not the download that follows:
// a real .torrent file is rarely more than a few hundred KB even for a large
// multi-file release, so this is generous headroom against a mistaken drop,
// not an expected size.
const maxTorrentUpload = 32 << 20

// Bounds on the event socket. A browser that stops reading must not pin a
// run's events in memory forever, and a connection whose peer vanished
// without a close frame has to be noticed.
const (
	writeWait  = 10 * time.Second
	pongWait   = 60 * time.Second
	pingPeriod = pongWait / 2
	// readLimit is generous for the control messages a client may send and
	// small enough that nothing can be pushed at the server.
	readLimit = 4 << 10
)

// upgrader's origin check is checkOrigin rather than gorilla's stock
// same-origin comparison - see its doc comment for why X-Forwarded-Host has
// to be part of it once nginx is in front (section 3.3).
var upgrader = websocket.Upgrader{
	ReadBufferSize:  1 << 10,
	WriteBufferSize: 8 << 10,
	CheckOrigin:     checkOrigin,
}

// checkOrigin defends the socket the same way gorilla's default does -
// reject a cross-site page in the same browser from opening it - but adds
// one fallback: accept an Origin that matches X-Forwarded-Host instead of
// r.Host.
//
// Behind a reverse proxy, r.Host is whatever Host header the proxy forwards,
// and the boilerplate WebSocket-upgrade config people copy sets
// X-Forwarded-Host without always remembering to also rewrite Host itself
// (nginx's own default, absent an explicit proxy_set_header Host, sends the
// upstream address). Without this fallback that already-common config would
// 403 every browser tab, since the Origin a real browser sends is the public
// host, never the upstream one.
//
// This does not weaken the check: X-Forwarded-Host is attacker-controlled
// input whenever the server is reachable directly, but so is Origin, and a
// raw client able to set one can set the other to match r.Host and pass the
// stock check anyway. The header is trusted for exactly what it is - the
// public name this request is asking for - never used to build a URL.
func checkOrigin(r *http.Request) bool {
	origin := r.Header.Get("Origin")
	if origin == "" {
		return true
	}
	u, err := url.Parse(origin)
	if err != nil {
		return false
	}
	if strings.EqualFold(u.Host, r.Host) {
		return true
	}

	// A chain of proxies appends to X-Forwarded-Host; only the first entry
	// names what the client actually asked for.
	forwarded, _, _ := strings.Cut(r.Header.Get("X-Forwarded-Host"), ",")
	forwarded = strings.TrimSpace(forwarded)
	return forwarded != "" && strings.EqualFold(u.Host, forwarded)
}

// handleEvents upgrades to a WebSocket, replays the run so far and then
// streams it live.
func (s *Server) handleEvents(w http.ResponseWriter, r *http.Request) {
	conn, err := upgrader.Upgrade(w, r, nil)
	if err != nil {
		// Upgrade has already written a response.
		return
	}

	client, backlog := s.hub.subscribe()

	// Reading is only how a closed connection and a missing pong are noticed;
	// the UI does not steer the run over this socket. Keeping control on
	// ordinary requests means a client that cannot hold a socket open can
	// still start and cancel a run.
	go func() {
		defer s.hub.remove(client)
		conn.SetReadLimit(readLimit)
		_ = conn.SetReadDeadline(time.Now().Add(pongWait))
		conn.SetPongHandler(func(string) error {
			return conn.SetReadDeadline(time.Now().Add(pongWait))
		})
		for {
			if _, _, err := conn.ReadMessage(); err != nil {
				return
			}
		}
	}()

	defer conn.Close()

	ping := time.NewTicker(pingPeriod)
	defer ping.Stop()

	for _, data := range backlog {
		if err := writeMessage(conn, data); err != nil {
			return
		}
	}

	for {
		select {
		case data, ok := <-client.out:
			if !ok {
				// The hub dropped this client: either the server is closing,
				// or its queue overflowed. Closing the socket makes the page
				// reconnect and replay, rather than leaving it with a gap.
				_ = conn.WriteControl(websocket.CloseMessage,
					websocket.FormatCloseMessage(websocket.CloseGoingAway, "reconnect"),
					time.Now().Add(writeWait))
				return
			}
			if err := writeMessage(conn, data); err != nil {
				return
			}
		case <-ping.C:
			if err := conn.WriteControl(websocket.PingMessage, nil, time.Now().Add(writeWait)); err != nil {
				return
			}
		}
	}
}

func writeMessage(conn *websocket.Conn, data []byte) error {
	if err := conn.SetWriteDeadline(time.Now().Add(writeWait)); err != nil {
		return err
	}
	return conn.WriteMessage(websocket.TextMessage, data)
}

// handleStartRun begins a run for the source the page submitted.
func (s *Server) handleStartRun(w http.ResponseWriter, r *http.Request) {
	var req RunRequest
	if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, 1<<20)).Decode(&req); err != nil {
		writeError(w, http.StatusBadRequest, "read the request: "+err.Error())
		return
	}

	info, err := s.StartRun(req)
	if err != nil {
		writeError(w, startStatus(err), err.Error())
		return
	}

	// The id is the answer: the result arrives as events on that run's
	// stream, not in this response's body. state says whether the run took
	// the slot or is waiting for it - never a refusal, which is the whole
	// point of the queue.
	writeJSON(w, http.StatusAccepted, map[string]any{"id": info.ID, "state": string(info.State)})
}

// startStatus maps the two ways a start can be turned away. Neither is "a run
// is already going": that answer no longer exists.
func startStatus(err error) int {
	if errors.Is(err, errClosed) || errors.Is(err, errReplayUnavailable) {
		return http.StatusServiceUnavailable
	}
	return http.StatusBadRequest
}

// handleUploadTorrent begins a run for a .torrent dropped onto the page.
//
// The bytes cannot be a Source themselves - swarm.ParseSource reads a magnet
// string or a filesystem path, not a byte stream - so this stages the upload
// as a temp file and hands its path to the exact same StartRun a pasted
// magnet goes through (see the RunRequest doc). That path has to survive
// past this handler's return: swarm.Open re-reads a file Source from disk
// (AddTorrentFromFile) from inside the run's own goroutine, well after
// StartRun - and therefore this handler - has returned, so the temp file is
// only removed once startRun's cleanup says the run that might still be
// reading it is over.
func (s *Server) handleUploadTorrent(w http.ResponseWriter, r *http.Request) {
	r.Body = http.MaxBytesReader(w, r.Body, maxTorrentUpload)
	if err := r.ParseMultipartForm(1 << 20); err != nil {
		writeError(w, http.StatusBadRequest, "read the upload: "+err.Error())
		return
	}
	defer r.MultipartForm.RemoveAll()

	file, _, err := r.FormFile("torrent")
	if err != nil {
		writeError(w, http.StatusBadRequest, "no .torrent file in the upload: "+err.Error())
		return
	}
	defer file.Close()

	dir, err := os.MkdirTemp("", "torpeek-upload-*")
	if err != nil {
		writeError(w, http.StatusInternalServerError, "stage the upload: "+err.Error())
		return
	}
	cleanup := func() { os.RemoveAll(dir) }

	// A fixed name rather than the browser-supplied filename: that value is
	// attacker-controlled input and buys nothing here, since only this
	// request ever reads the path back.
	path := filepath.Join(dir, "upload.torrent")
	dst, err := os.OpenFile(path, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o600)
	if err != nil {
		cleanup()
		writeError(w, http.StatusInternalServerError, "stage the upload: "+err.Error())
		return
	}
	_, copyErr := io.Copy(dst, file)
	closeErr := dst.Close()
	if copyErr != nil {
		cleanup()
		writeError(w, http.StatusInternalServerError, "stage the upload: "+copyErr.Error())
		return
	}
	if closeErr != nil {
		cleanup()
		writeError(w, http.StatusInternalServerError, "stage the upload: "+closeErr.Error())
		return
	}

	// The count comes off the same intake line a pasted magnet uses, so a
	// dropped .torrent has to read it here: this handler builds its
	// RunRequest by hand and would otherwise silently ignore the number the
	// person just typed. An unparseable or absent field is no count at all,
	// which is exactly what the server's default means - a drop is not the
	// place to argue about a form value.
	count, err := strconv.Atoi(strings.TrimSpace(r.FormValue("count")))
	if err != nil {
		count = 0
	}

	req := RunRequest{
		Source: path, Mode: r.FormValue("mode"), Count: count,
		Label: "dropped .torrent",
	}
	// startRun runs cleanup itself on every path that does not end up owning
	// the file: a request it refuses, a run cancelled while it waits, a
	// server that closes under it. A run that reaches the slot defers cleanup
	// to pump, after its event stream ends.
	info, err := s.startRun(req, cleanup)
	if err != nil {
		writeError(w, startStatus(err), err.Error())
		return
	}

	writeJSON(w, http.StatusAccepted, map[string]any{"id": info.ID, "state": string(info.State)})
}

// reopenRequest addresses a run already on disk. It carries no id on
// purpose: infohash and params are exactly what a GET /runs disk-only row
// gives a panel for a run this process may never have minted an id for (a
// previous process's run, or one this process itself trimmed from memory -
// see keepFinishedRuns).
type reopenRequest struct {
	InfoHash string `json:"infohash"`
	Params   string `json:"params"`
}

// handleReopenRun replays a finished run from disk under a fresh registry
// entry - see Server.ReopenRun for why it never waits for the queue slot,
// and for why the response already carries the run's real outcome rather
// than "queued" or "running".
func (s *Server) handleReopenRun(w http.ResponseWriter, r *http.Request) {
	var req reopenRequest
	if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, 1<<20)).Decode(&req); err != nil {
		writeError(w, http.StatusBadRequest, "read the request: "+err.Error())
		return
	}

	info, err := s.ReopenRun(req.InfoHash, req.Params)
	if err != nil {
		writeError(w, startStatus(err), err.Error())
		return
	}

	writeJSON(w, http.StatusAccepted, map[string]any{"id": info.ID, "state": string(info.State)})
}

// handleTopUp answers what finishing one result set would cost, and which
// ceiling stopped it last time (TOR-152). A GET, because it changes nothing
// and is the sentence a page shows before the button that does.
//
// 404 for an address that names no set on disk. A set that exists but cannot
// be finished is a 200 carrying TopUp.Refused: the request was answerable,
// and the answer is a sentence a person has to read.
func (s *Server) handleTopUp(w http.ResponseWriter, r *http.Request) {
	// params is a query parameter rather than a path segment, the same shape
	// the frame delete on the neighbouring path uses, and for a stronger
	// reason here: it is OPTIONAL. A page watching a run that has just
	// finished knows the torrent's infohash and nothing about which
	// directory it wrote (run_state carries no params), so the commonest
	// call omits it entirely and lets the server resolve it - see
	// Server.TopUp.
	plan, ok := s.TopUp(r.PathValue("infohash"),
		strings.TrimSpace(r.URL.Query().Get("params")))
	if !ok {
		writeError(w, http.StatusNotFound, "no such run on disk")
		return
	}

	// Wrapped under a key like every other response here, so a field can be
	// added beside it later without the body changing shape.
	writeJSON(w, http.StatusOK, map[string]any{"topup": plan})
}

// topUpRequest addresses the result set to finish, and - when the page has
// one - the live row it is already showing for that torrent.
//
// IT CARRIES NO CEILING, and that absence is the design. The extra traffic a
// top-up may spend is computed by the server from the record on disk
// (TopUp.price) and stated to the page beforehand by the GET above; a number
// a client could put here would be a way to raise somebody's traffic
// allowance by asking for it. The page consents to a figure it was shown; it
// does not choose one.
type topUpRequest struct {
	InfoHash string `json:"infohash"`
	Params   string `json:"params"`
	// ID is optional: it names the registry entry this row already has, so a
	// finished one can be re-armed instead of a second row appearing for one
	// torrent (Server.TopUpRun). A disk-only row has none, which is exactly
	// the case reopenRequest above also has to answer for.
	ID string `json:"id,omitempty"`
}

// handleTopUpRun starts the run that fills a partial set's gaps. Same
// {id, state} answer POST /runs gives, because that is what this is.
func (s *Server) handleTopUpRun(w http.ResponseWriter, r *http.Request) {
	var req topUpRequest
	if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, 1<<20)).Decode(&req); err != nil {
		writeError(w, http.StatusBadRequest, "read the request: "+err.Error())
		return
	}

	info, err := s.TopUpRun(req.InfoHash, req.Params, req.ID)
	if err != nil {
		writeError(w, decideStatus(err), err.Error())
		return
	}

	writeJSON(w, http.StatusAccepted, map[string]any{"id": info.ID, "state": string(info.State)})
}

// retryRequest names the run to run again. By id and only by id: a run worth
// retrying is one that produced nothing, so it left no record on disk for an
// infohash and params to address (see Server.RetryRun).
type retryRequest struct {
	ID string `json:"id"`
}

// handleRetryRun runs a finished run's own request again, unchanged - the
// control that did not exist for the torrent whose metadata never arrived,
// where the only way back was to paste the magnet a second time.
func (s *Server) handleRetryRun(w http.ResponseWriter, r *http.Request) {
	var req retryRequest
	if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, 1<<20)).Decode(&req); err != nil {
		writeError(w, http.StatusBadRequest, "read the request: "+err.Error())
		return
	}

	info, err := s.RetryRun(strings.TrimSpace(req.ID))
	if err != nil {
		writeError(w, decideStatus(err), err.Error())
		return
	}

	writeJSON(w, http.StatusAccepted, map[string]any{"id": info.ID, "state": string(info.State)})
}

// cancelRequest names the run to stop. An absent id means the run in the
// slot, which is what a page showing a single run asks for.
type cancelRequest struct {
	ID string `json:"id"`
}

// handleCancelRun stops one run, keeping what it produced. It reaches a
// queued run as well as a running one - a queued run has no context to
// cancel, so leaving it to the run to notice would never stop it.
func (s *Server) handleCancelRun(w http.ResponseWriter, r *http.Request) {
	var req cancelRequest
	if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, 1<<20)).Decode(&req); err != nil && !errors.Is(err, io.EOF) {
		writeError(w, http.StatusBadRequest, "read the request: "+err.Error())
		return
	}

	info, err := s.CancelRun(strings.TrimSpace(req.ID))
	if err != nil {
		status := http.StatusConflict
		if errors.Is(err, ErrNoSuchRun) {
			status = http.StatusNotFound
		}
		writeError(w, status, err.Error())
		return
	}

	writeJSON(w, http.StatusAccepted, map[string]any{
		"cancelling": true, "id": info.ID, "state": string(info.State),
	})
}

// decideRequest is one tick: this run, these files, this many frames each.
//
// It was a picker's whole ANSWER before TOR-181 - everything somebody had
// staged, sent once by a button under the list - and the route is unchanged
// because the shape is: the page still sends a set rather than a single
// index, so a tick that raced a lost message, or a "Select all", is one
// call. What changed is that the server ADDS the set to what the row is
// already fetching instead of replacing a selection nobody had committed
// yet (Server.DecideRun).
//
// It names the run by id rather than by infohash, unlike the reopen request
// next to it: this is about one entry in this process's registry - the row
// whose run is being grown - not about a torrent's results on disk, and the
// same torrent may well have been added twice.
type decideRequest struct {
	ID    string   `json:"id"`
	Files []string `json:"files"`
	// Count is the intake's frames-per-file at the moment the box was
	// ticked, so the number a person was looking at beside that file is the
	// number the run uses. Absent (or zero) leaves the run with whatever the
	// original request carried, and a count on a tick that joins a pass
	// already forming is ignored - see DecideRun on why the figure locks to
	// the tick that opened it.
	Count int `json:"count,omitempty"`
}

// handleDecideRun starts a ticked file's frames on the row that holds it
// (TOR-181), which for a parked torrent is the moment it leaves needs-action
// and rejoins the queue (TOR-67). The answer is the same {id, state} shape
// POST /runs gives, because that is what this is: the moment the run someone
// asked for actually becomes a run.
func (s *Server) handleDecideRun(w http.ResponseWriter, r *http.Request) {
	var req decideRequest
	if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, 1<<20)).Decode(&req); err != nil {
		writeError(w, http.StatusBadRequest, "read the request: "+err.Error())
		return
	}

	info, err := s.DecideRun(req.ID, req.Files, req.Count)
	if err != nil {
		writeError(w, decideStatus(err), err.Error())
		return
	}

	writeJSON(w, http.StatusAccepted, map[string]any{"id": info.ID, "state": string(info.State)})
}

// priorityRequest is a reorder: this run, at this level.
//
// Priority is an ABSOLUTE level, never a step, and the field is a plain int
// rather than a pointer even though its zero value is meaningful: zero IS
// PriorityNormal, and a request that omits the field is asking for normal,
// which is a coherent thing to ask for. Sending a step ("one higher") would
// make two clicks on a stale page walk a torrent somewhere nobody asked for;
// an absolute value applied twice lands in the same place.
type priorityRequest struct {
	ID       string `json:"id"`
	Priority int    `json:"priority"`
}

// handleSetPriority moves one waiting torrent up or down the queue without
// cancelling it (TOR-140). See Server.SetRunPriority for what a priority is
// and for why this can never reach a torrent that is already downloading.
//
// 200 rather than the 202 the start and decide routes answer with: those
// accept something that will happen later, while this one has already
// happened by the time it returns - the queue is reordered under the lock
// this call took, and the position in the body is the one the next dispatch
// will act on.
func (s *Server) handleSetPriority(w http.ResponseWriter, r *http.Request) {
	var req priorityRequest
	if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, 1<<20)).Decode(&req); err != nil {
		writeError(w, http.StatusBadRequest, "read the request: "+err.Error())
		return
	}

	info, err := s.SetRunPriority(req.ID, Priority(req.Priority))
	if err != nil {
		writeError(w, decideStatus(err), err.Error())
		return
	}

	writeJSON(w, http.StatusOK, map[string]any{
		"id": info.ID, "state": string(info.State),
		"priority": int(info.Priority), "queue_position": info.QueuePosition,
	})
}

// decideStatus maps a tick's four failures: a run this server does not hold,
// a request that does not name a selection this torrent can satisfy, a
// server that has closed, and - everything left - a run with nothing left
// for a tick to grow (refuseTick), which is the same conflict CancelRun
// reports for a run that has already ended.
//
// handleSetPriority answers through it too, because a reorder fails in
// exactly those same four ways and means the same thing by each: an id this
// server does not hold, a level outside the band, a closed server, and a run
// that is not waiting for a slot - which for a reorder is the preemption
// refusal, and a 409 is the right shape for it (the request was understood,
// the run is simply not in a state this can act on).
func decideStatus(err error) int {
	switch {
	case errors.Is(err, ErrNoSuchRun):
		return http.StatusNotFound
	case errors.Is(err, errBadRequest):
		return http.StatusBadRequest
	case errors.Is(err, errClosed):
		return http.StatusServiceUnavailable
	default:
		return http.StatusConflict
	}
}

// handleListRuns answers the panel with the live queue plus everything
// already on disk - see Server.listRuns for how the two are merged.
func (s *Server) handleListRuns(w http.ResponseWriter, r *http.Request) {
	writeJSON(w, http.StatusOK, map[string]any{"runs": s.listRuns()})
}

// handleFileDetail answers one video file's frames across every result set
// that torrent has on disk (TOR-69).
//
// This is the per-run detail request walkRuns' own comment set aside as
// future work: the listing stays one run.json per directory, and the
// frame-by-frame reads happen here, for one file, only when a person opens
// it. It is also the only way a page can reach a sibling set at all - the
// event stream carries no params name, and a replay is addressed by one
// directory, so frames of the OTHER set were never announced to this page.
//
// Reads disk and nothing else, which is what keeps a cache hit free of the
// network however it is opened.
func (s *Server) handleFileDetail(w http.ResponseWriter, r *http.Request) {
	index, err := strconv.Atoi(r.PathValue("index"))
	if err != nil {
		writeError(w, http.StatusNotFound, "no such file")
		return
	}

	detail, ok := s.fileDetail(r.PathValue("infohash"), index)
	if !ok {
		writeError(w, http.StatusNotFound, "no such file")
		return
	}
	// Wrapped under a key like every other response here (GET /runs answers
	// {"runs": ...}), so a field can be added beside it later without the
	// body changing shape.
	writeJSON(w, http.StatusOK, map[string]any{"file": detail})
}

// handleCompareSets answers every result set on disk a comparison can be
// pointed at (TOR-109) - see CompareSet for why this is its own request
// rather than more fields on GET /runs.
func (s *Server) handleCompareSets(w http.ResponseWriter, r *http.Request) {
	// An empty list is a 200, not a 404: "nothing has been captured yet" is a
	// true answer to this question, and the picker draws it as such.
	writeJSON(w, http.StatusOK, map[string]any{"sets": s.comparableSets()})
}

// handleCompare answers the positions two result sets share, for a page that
// flips between them (TOR-109). Both arms are query parameters rather than
// path segments because a comparison has two addresses and a path can only be
// one; parseSetAddr is what stops either of them becoming a path this server
// never meant to read.
//
// A malformed arm is a 400 - the request cannot be read - where an arm that
// is well formed and names nothing is a 404, the same split the DELETE on a
// frame already makes between its params parameter and its path.
func (s *Server) handleCompare(w http.ResponseWriter, r *http.Request) {
	a, aOK := parseSetAddr(r.URL.Query().Get("a"))
	b, bOK := parseSetAddr(r.URL.Query().Get("b"))
	if !aOK || !bOK {
		writeError(w, http.StatusBadRequest,
			"each arm must be spelled infohash:params:index")
		return
	}
	if a == b {
		writeError(w, http.StatusBadRequest,
			"the two arms are the same result set; there is nothing to flip between")
		return
	}

	comparison, err := s.comparison(a, b)
	if err != nil {
		writeError(w, http.StatusNotFound, err.Error())
		return
	}

	// Wrapped under a key like every other response here, so a field can be
	// added beside it later without the body changing shape.
	writeJSON(w, http.StatusOK, map[string]any{"comparison": comparison})
}

// handleDeleteFrame removes one frame and answers with the file's refreshed
// detail - the same body GET /runs/{infohash}/files/{index} returns,
// recomputed after the delete, so the page re-renders from disk truth rather
// than from its own idea of what just happened (TOR-70).
//
// The result set is a query parameter rather than a field in the body: a
// DELETE with a body is legal but awkward on both sides (fetch allows one,
// caches and proxies vary in what they do with it), and the set is part of
// the address here, not a payload - infohash, file index, frame index and
// params together name exactly one frame on disk.
//
// The three path segments answer 404 when they are malformed, matching what
// the GET on the same path already does for an infohash that is not hex: a
// path that cannot name a resource names nothing. params is a request
// parameter rather than part of the path, so a malformed one is a 400 about
// the request.
func (s *Server) handleDeleteFrame(w http.ResponseWriter, r *http.Request) {
	index, indexErr := strconv.Atoi(r.PathValue("index"))
	frame, frameErr := strconv.Atoi(r.PathValue("frame"))
	if indexErr != nil || frameErr != nil {
		writeError(w, http.StatusNotFound, "no such frame")
		return
	}

	detail, err := s.DeleteFrame(r.PathValue("infohash"),
		strings.TrimSpace(r.URL.Query().Get("params")), index, frame)
	if err != nil && !errors.Is(err, core.ErrSheetStale) {
		writeError(w, deleteStatus(err), err.Error())
		return
	}

	// Wrapped under the same "file" key the GET answers with, so a page can
	// read either response the same way.
	body := map[string]any{"file": detail}
	if err != nil {
		// The delete happened; something derived from it did not. Answering
		// 200 says the first, and the warning says the second - where a 500
		// said neither truthfully.
		body["warning"] = err.Error()
	}
	writeJSON(w, http.StatusOK, body)
}

// handleClearFile removes everything one file has on disk and answers with
// that file's refreshed detail - the same body the GET on it returns and the
// same body the per-frame DELETE beside it answers with, recomputed after the
// clear, so a page re-renders from disk truth (TOR-183).
//
// A SIBLING ROUTE, not a special case of the one below it: DELETE on
// .../frames clears the file, DELETE on .../frames/{frame} removes one of
// them. Two patterns, two handlers, no argument standing in for a mode - a
// frame index of -1 meaning "all of them" would be exactly the overloaded
// parameter that makes a destructive call one typo away from a different act.
//
// params is OPTIONAL here, where the per-frame delete requires it, and
// Server.ClearFile argues why: absent means every result set this torrent
// holds frames of this file in, which is what the row's merged grid shows and
// what the button beside it offers to clear. It stays a query parameter for
// the reason handleDeleteFrame gives - the set is part of the address, not a
// payload, and a DELETE with a body is awkward on both sides.
//
// A partial failure is a 200 carrying "warning", exactly as it is for one
// frame: the clear happened, the file is out of what the result claims to
// hold, and refusing to re-render would leave frames on screen that the
// record no longer accounts for (TOR-78).
func (s *Server) handleClearFile(w http.ResponseWriter, r *http.Request) {
	index, err := strconv.Atoi(r.PathValue("index"))
	if err != nil {
		writeError(w, http.StatusNotFound, "no such file")
		return
	}

	detail, err := s.ClearFile(r.PathValue("infohash"),
		strings.TrimSpace(r.URL.Query().Get("params")), index)
	if err != nil && !errors.Is(err, core.ErrClearIncomplete) {
		writeError(w, deleteStatus(err), err.Error())
		return
	}

	// Wrapped under the same "file" key the GET and the per-frame DELETE both
	// answer with, so a page can read any of the three the same way.
	body := map[string]any{"file": detail}
	if err != nil {
		// The clear happened; some of what it was meant to remove is still
		// there. Answering 200 says the first and the warning says the
		// second - where a 500 would say neither truthfully.
		body["warning"] = err.Error()
	}
	writeJSON(w, http.StatusOK, body)
}

// deleteStatus maps a removal's failures: nothing there to remove (a frame or
// a whole file), a request that does not name one, and a server with no way
// to remove anything. Anything else is a write that failed, which is the
// server's problem.
//
// Shared by both DELETE routes rather than duplicated, which is the argument
// core.ErrNoSuchFile's own doc makes from the other side: the two sentinels
// exist so a MESSAGE can name the right noun, and they were never two
// statuses.
func deleteStatus(err error) int {
	switch {
	case errors.Is(err, core.ErrNoSuchFrame), errors.Is(err, core.ErrNoSuchFile):
		return http.StatusNotFound
	case errors.Is(err, errBadRequest):
		return http.StatusBadRequest
	case errors.Is(err, errDeleteUnavailable):
		return http.StatusServiceUnavailable
	default:
		return http.StatusInternalServerError
	}
}

// handleDefaults reports what a run does when the request does not say - the
// frame count, and whether this server can hand a .torrent to a local client.
//
// It exists so the page can show the number actually in force rather than a
// copy of it written into the HTML: the assets are static and served
// straight out of the embed, with no templating step to substitute one in,
// so without this the field would read 20 on a server started as -n 6. The
// server does not decide the value; it repeats what the one shared
// core.Config already says (Config.DefaultCount).
//
// watch is the same idea for -watch-dir, and it is a boolean rather than the
// directory itself on purpose: the page needs to know whether to draw the
// button, and the path would be an operational detail travelling to a browser
// for nothing. Without it the page would have to guess, and the requirement
// is that the button is ABSENT when there is no watch directory - not present
// and failing when it is pressed.
func (s *Server) handleDefaults(w http.ResponseWriter, r *http.Request) {
	writeJSON(w, http.StatusOK, map[string]any{
		"count": s.cfg.DefaultCount,
		"watch": s.cfg.WatchDir != "",
	})
}

// handleWatchTorrent copies a run's saved .torrent into the watch directory
// so a client on this host queues it (TOR-73). See Server.SendToWatchDir for
// why it is addressed by a files/{id} handle and why the copy is written
// through a temporary name.
func (s *Server) handleWatchTorrent(w http.ResponseWriter, r *http.Request) {
	dest, err := s.SendToWatchDir(r.PathValue("id"))
	if err != nil {
		writeError(w, watchStatus(err), err.Error())
		return
	}

	// The destination is answered rather than swallowed: it is the operator's
	// own directory on the operator's own host, and seeing which file landed
	// where is how a person confirms the drop worked without going to look.
	writeJSON(w, http.StatusOK, map[string]any{"path": dest})
}

// watchStatus maps the two ways this can be turned away - a handle that names
// no saved torrent, and a server with nowhere to put one - from the writes
// that simply failed, which are the server's problem.
func watchStatus(err error) int {
	switch {
	case errors.Is(err, errNoSuchTorrent):
		return http.StatusNotFound
	case errors.Is(err, errWatchUnavailable):
		return http.StatusServiceUnavailable
	default:
		return http.StatusInternalServerError
	}
}

// handleFile serves one file the run announced: a frame, a contact sheet or a
// manifest. Nothing else on disk is reachable.
func (s *Server) handleFile(w http.ResponseWriter, r *http.Request) {
	path, ok := s.files.lookup(r.PathValue("id"))
	if !ok {
		http.NotFound(w, r)
		return
	}

	// A frame is announced the moment it is renamed into place, so it exists
	// by the time its URL is; a missing one means the run's output was moved
	// or removed underneath us, which is a 404 rather than a server error.
	if _, err := os.Stat(path); err != nil {
		http.NotFound(w, r)
		return
	}

	// A .torrent needs both headers stated, and ServeFile states neither.
	// Go's mime table has no entry for the extension, so the sniffer sees
	// bencode - "d8:announce..." - decides it is text, and the browser
	// renders a page of gibberish instead of saving a file. Content-Type
	// names what it actually is, and Content-Disposition is what turns the
	// link into a save; ServeFile leaves an already-set Content-Type alone,
	// so setting it here wins.
	//
	// The filename is NOT simply the file's own name on disk any more
	// (TOR-170): output.Layout still names it after the infohash on disk,
	// for the reason its own doc comment gives, but a person saving it from
	// the browser wants the torrent's own name, not forty hex characters.
	// torrentContentDisposition reads that name from the run record beside
	// the file and builds the header; see its doc for why that is more than
	// substituting one string for another.
	if isTorrentPath(path) {
		w.Header().Set("Content-Type", "application/x-bittorrent")
		w.Header().Set("Content-Disposition", torrentContentDisposition(path))
	}

	http.ServeFile(w, r, path)
}

// maxTorrentNameBytes bounds the saved name before ".torrent" is appended -
// short enough to leave every common filesystem's own filename limit (255
// bytes, on ext4/APFS/NTFS alike) with headroom for the extension and for a
// filesystem that counts UTF-8 bytes rather than characters, since a single
// CJK or Cyrillic character can cost two or three of those bytes.
const maxTorrentNameBytes = 200

// torrentContentDisposition builds the RFC 6266 Content-Disposition value
// for path, a run's saved .torrent - naming the download after the torrent
// itself rather than after path's own basename, the infohash (TOR-170).
//
// The name comes from cache.Run.Name in run.json, which cache.SaveRun always
// writes into the same directory the .torrent sits in
// (output.Layout.RunDir) - so path's directory is exactly where to look,
// with no need to know this run's infohash or params separately. A run with
// no record yet on disk, or a record whose Name is blank, leaves name at "";
// contentDispositionAttachment below treats that exactly like a name that
// sanitises down to nothing, falling back to path's own basename with the
// extension trimmed - the infohash - which is why this can never fail to
// produce a usable header.
func torrentContentDisposition(path string) string {
	name := ""
	if record, ok := cache.LoadRun(filepath.Dir(path)); ok {
		name = record.Name
	}
	fallback := strings.TrimSuffix(filepath.Base(path), filepath.Ext(path))
	return contentDispositionAttachment(name, fallback)
}

// contentDispositionAttachment builds "attachment; filename=...;
// filename*=..." for a torrent called name, falling back to
// `attachment; filename="<fallback>.torrent"` alone when name sanitises
// down to nothing - including when it is already empty.
//
// Two names travel together per RFC 6266 / RFC 5987: filename is a
// sanitised ASCII fallback that a client which ignores the extended form
// still saves something sane under, and filename* is the real name,
// UTF-8-encoded and then percent-encoded, for a client that reads it - so a
// Cyrillic or CJK torrent name arrives intact instead of being mangled or
// silently dropped. This is why the infohash alone was chosen as the header
// name before TOR-170: it is hex, so nothing in it can break out of the
// quoted header value or turn into a path. A torrent's name is arbitrary
// text from a stranger and gets no such trust - see sanitizeTorrentName for
// what is stripped or replaced before either form of the name is built, and
// why.
//
// The .torrent extension is always appended, even when name already ends in
// something that looks like one: a torrent called "Movie.mkv" saves as
// "Movie.mkv.torrent", not "Movie.torrent" (which would silently swallow
// what the uploader actually called it, on the guess that ".mkv" was an
// extension to strip rather than part of the name) and not "Movie.mkv" with
// no .torrent extension at all (which stops a torrent client from
// recognising the file by extension the way it does for a downloaded one).
func contentDispositionAttachment(name, fallback string) string {
	sanitized := sanitizeTorrentName(name)
	if sanitized == "" {
		return `attachment; filename="` + fallback + `.torrent"`
	}

	asciiName := asciiOnly(sanitized)
	if asciiName == "" {
		// The name survived sanitisation but is entirely non-ASCII (a
		// torrent named purely in Cyrillic or CJK, say) - there is no
		// sane ASCII stand-in for it, so the classic fallback parameter
		// names the infohash instead, exactly as it would for no name at
		// all. filename* below still carries the real name.
		asciiName = fallback
	}

	var b strings.Builder
	b.WriteString(`attachment; filename="`)
	b.WriteString(asciiName)
	b.WriteString(`.torrent"; filename*=UTF-8''`)
	b.WriteString(percentEncodeRFC5987(sanitized))
	b.WriteString(".torrent")
	return b.String()
}

// sanitizeTorrentName strips or replaces what a saved filename cannot
// safely carry, while leaving non-ASCII characters alone - asciiOnly is
// what later strips those, only for the classic fallback parameter that has
// to stay pure ASCII. It also bounds the result to maxTorrentNameBytes of
// UTF-8, truncated on a rune boundary so a name long enough to trip a
// filesystem's own limit is shortened rather than left to fail on save.
//
//   - CR, LF and every other control character are dropped outright, not
//     replaced: none has a visible place in a filename to stand in for, and
//     this is what keeps a torrent's name from ever reaching a raw CR or LF
//     into the header value, however it is later quoted or encoded.
//   - '/' and '\' are replaced with '-': a path separator would let the
//     name read as a directory rather than a file to whatever saves it -
//     the browser, the OS, or SendToWatchDir's own destination directory.
//   - a double quote is replaced with an apostrophe: it is the character
//     that delimits the quoted-string this sits inside, replaced rather
//     than escaped, so nothing downstream has to reason about
//     quoted-pair rules.
//   - ':' '*' '?' '<' '>' '|' are replaced with '-': illegal in a filename
//     on Windows even though nothing else about this project targets it
//     specifically - a name that would fail to save on one common OS is
//     worth avoiding on all of them.
//
// A semicolon is deliberately left alone: RFC 7230's quoted-string grammar
// allows it unescaped, and it is not one of the characters replaced above
// for filename safety either, so there is nothing here for it to break -
// see web_test.go's hostile-name coverage, which asserts exactly that.
func sanitizeTorrentName(name string) string {
	var b strings.Builder
	for _, r := range name {
		switch {
		case r == 0 || unicode.IsControl(r):
			continue
		case r == '/' || r == '\\':
			b.WriteByte('-')
		case r == '"':
			b.WriteByte('\'')
		case r == ':' || r == '*' || r == '?' || r == '<' || r == '>' || r == '|':
			b.WriteByte('-')
		default:
			b.WriteRune(r)
		}
	}
	return truncateUTF8(strings.Trim(b.String(), " ."), maxTorrentNameBytes)
}

// asciiOnly keeps only what the classic, unencoded filename= parameter can
// safely hold: RFC 6266 leaves anything outside US-ASCII to a client's own
// interpretation once it appears unencoded, so this drops it rather than
// approximating it - there is no good ASCII stand-in for "北京" - and the
// filename* parameter built alongside it is what carries the real name
// intact.
func asciiOnly(s string) string {
	var b strings.Builder
	for _, r := range s {
		if r <= 0x7E {
			b.WriteRune(r)
		}
	}
	return strings.Trim(b.String(), " .")
}

// truncateUTF8 shortens s to at most maxBytes of UTF-8, cutting on a rune
// boundary so a multi-byte character at the cut point is dropped whole
// rather than split into invalid UTF-8.
func truncateUTF8(s string, maxBytes int) string {
	if len(s) <= maxBytes {
		return s
	}
	for maxBytes > 0 && !utf8.RuneStart(s[maxBytes]) {
		maxBytes--
	}
	return s[:maxBytes]
}

// rfc5987Hex is upper-case per RFC 3986's own recommendation for
// percent-encoding, which RFC 5987 defers to.
const rfc5987Hex = "0123456789ABCDEF"

// percentEncodeRFC5987 percent-encodes s, byte by byte over its own UTF-8
// encoding, keeping only RFC 5987's attr-char unescaped. This is what the
// value after filename*=UTF-8 and a pair of single quotes carries: s's real
// bytes, safe to sit unquoted in a header parameter value because nothing
// outside attr-char survives unescaped - including a double quote, a
// semicolon or a percent sign, which would otherwise matter to how the
// header is parsed.
func percentEncodeRFC5987(s string) string {
	var b strings.Builder
	for i := 0; i < len(s); i++ {
		c := s[i]
		if isRFC5987AttrChar(c) {
			b.WriteByte(c)
		} else {
			b.WriteByte('%')
			b.WriteByte(rfc5987Hex[c>>4])
			b.WriteByte(rfc5987Hex[c&0x0F])
		}
	}
	return b.String()
}

// isRFC5987AttrChar is RFC 5987's attr-char: ALPHA / DIGIT /
// "!" / "#" / "$" / "&" / "+" / "-" / "." / "^" / "_" / "`" / "|" / "~".
func isRFC5987AttrChar(c byte) bool {
	switch {
	case c >= 'A' && c <= 'Z', c >= 'a' && c <= 'z', c >= '0' && c <= '9':
		return true
	}
	switch c {
	case '-', '.', '_', '~', '!', '#', '$', '&', '+', '^', '`', '|':
		return true
	}
	return false
}

func writeJSON(w http.ResponseWriter, status int, body map[string]any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(body)
}

func writeError(w http.ResponseWriter, status int, message string) {
	writeJSON(w, status, map[string]any{"error": message})
}
