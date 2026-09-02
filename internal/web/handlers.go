package web

import (
	"encoding/json"
	"errors"
	"net/http"
	"os"
	"time"

	"github.com/gorilla/websocket"
)

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

// upgrader keeps gorilla's default origin check, which requires the Origin
// header to match the Host the request arrived on. Behind a reverse proxy
// that sets Host correctly this still passes, and on a desktop it is what
// keeps another site in the same browser from opening this socket.
var upgrader = websocket.Upgrader{
	ReadBufferSize:  1 << 10,
	WriteBufferSize: 8 << 10,
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

	if err := s.StartRun(req); err != nil {
		status := http.StatusBadRequest
		if errors.Is(err, ErrRunInProgress) {
			status = http.StatusConflict
		}
		writeError(w, status, err.Error())
		return
	}

	// The result arrives as events, not as this response's body.
	writeJSON(w, http.StatusAccepted, map[string]any{"started": true})
}

// handleCancelRun stops the run in progress, keeping what it produced.
func (s *Server) handleCancelRun(w http.ResponseWriter, r *http.Request) {
	if err := s.CancelRun(); err != nil {
		writeError(w, http.StatusConflict, err.Error())
		return
	}
	writeJSON(w, http.StatusAccepted, map[string]any{"cancelling": true})
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
