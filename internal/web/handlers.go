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

	"github.com/gorilla/websocket"

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

// decideRequest is a picker's answer: this run, these files, this many
// frames each.
//
// It names the run by id rather than by infohash, unlike the reopen request
// next to it: this is about one entry in this process's registry - the one
// parked and waiting - not about a torrent's results on disk, and the same
// torrent may well have been added twice.
type decideRequest struct {
	ID    string   `json:"id"`
	Files []string `json:"files"`
	// Count is the intake's frames-per-file at the moment the button was
	// pressed, so the number a person was looking at while ticking boxes is
	// the number the run uses. Absent (or zero) leaves the run with whatever
	// the original request carried.
	Count int `json:"count,omitempty"`
}

// handleDecideRun puts a parked torrent back in the queue with the files
// someone ticked (TOR-67). The answer is the same {id, state} shape POST
// /runs gives, because that is what this is: the moment the run someone
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

// decideStatus maps a decision's four failures: a run this server does not
// hold, a request that does not name a selection this torrent can satisfy, a
// server that has closed, and - everything left - a run that is not waiting
// to be told anything, which is the same conflict CancelRun reports for a
// run that has already ended.
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
	if err != nil {
		writeError(w, deleteStatus(err), err.Error())
		return
	}

	// Wrapped under the same "file" key the GET answers with, so a page can
	// read either response the same way.
	writeJSON(w, http.StatusOK, map[string]any{"file": detail})
}

// deleteStatus maps a delete's three failures: nothing there to remove, a
// request that does not name a frame, and a server with no way to remove one.
// Anything else is a write that failed, which is the server's problem.
func deleteStatus(err error) int {
	switch {
	case errors.Is(err, core.ErrNoSuchFrame):
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
	// so setting it here wins. The filename is the file's own name on disk,
	// which output.Layout deliberately made the infohash: unique in whatever
	// download folder it lands in, and hex, so nothing in it can break out
	// of the quoted header value.
	if isTorrentPath(path) {
		w.Header().Set("Content-Type", "application/x-bittorrent")
		w.Header().Set("Content-Disposition", `attachment; filename="`+filepath.Base(path)+`"`)
	}

	http.ServeFile(w, r, path)
}

func writeJSON(w http.ResponseWriter, status int, body map[string]any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(body)
}

func writeError(w http.ResponseWriter, status int, message string) {
	writeJSON(w, status, map[string]any{"error": message})
}
