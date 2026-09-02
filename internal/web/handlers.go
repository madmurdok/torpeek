package web

import (
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/gorilla/websocket"
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
	if errors.Is(err, errClosed) {
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

	req := RunRequest{Source: path, Mode: r.FormValue("mode"), Label: "dropped .torrent"}
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
