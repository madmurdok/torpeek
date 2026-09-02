package web

import (
	"crypto/rand"
	"crypto/subtle"
	"encoding/base64"
	"net"
	"net/http"
)

// needsToken decides whether Start must guarantee a token is set, given the
// configured listen address and base path.
//
// The obvious rule - protect anything that is not a loopback bind - gets the
// deployment REQUIREMENTS.md section 4.1 documents exactly backwards: there
// the UI binds 127.0.0.1, loopback by every definition net.IP knows, and is
// still reachable at a public https:// URL because nginx sits in front of it
// on the same machine and connects to that loopback address itself. A rule
// keyed on the bind address alone would call that deployment "local" and
// leave it open - and it is the deployment this whole feature targets
// (TOR-30's own framing).
//
// BasePath is the signal that closes the gap. It exists for exactly this
// deployment - Config.BasePath's doc comment and the -base-path flag's both
// say "behind a reverse proxy" - and for no other reason: nobody configures
// a subpath to serve themselves on their own desktop. So the rule is: a
// non-loopback bind needs a token on its own, and a base path needs one
// regardless of bind address, because setting a base path is this codebase's
// own declaration "I am behind a reverse proxy."
func needsToken(addr, basePath string) bool {
	return !isLoopbackHost(addr) || normalizeBasePath(basePath) != ""
}

// isLoopbackHost reports whether addr's host names only the machine itself:
// 127.0.0.0/8, ::1, or the literal "localhost". A host that fails to parse as
// an address and is not "localhost" is treated as not loopback - a typo in a
// hostname should not silently disable the one protection this package
// provides on its own.
func isLoopbackHost(addr string) bool {
	host, _, err := net.SplitHostPort(addr)
	if err != nil {
		host = addr
	}
	if host == "" {
		// A bare ":8765" binds every interface - the opposite of loopback.
		return false
	}
	if host == "localhost" {
		return true
	}
	ip := net.ParseIP(host)
	return ip != nil && ip.IsLoopback()
}

// generateToken makes a random access token for Start to fill in when one is
// needed and the caller did not pin one (Config.Token). 24 bytes is 192 bits,
// far more than guessing it could ever be a real attack surface; base64's
// URL-safe alphabet needs no escaping to sit in a query string, which is
// where every request carries it (see authGuard and app.js's url()).
func generateToken() (string, error) {
	buf := make([]byte, 24)
	if _, err := rand.Read(buf); err != nil {
		return "", err
	}
	return base64.RawURLEncoding.EncodeToString(buf), nil
}

// authGuard wraps one route with Config.Token's check, rejecting a request
// whose "token" query parameter does not match. want is compared in constant
// time (crypto/subtle) rather than with ==, since a route guarded by a
// secret is exactly the place a timing side-channel on that comparison would
// matter.
//
// It is applied per route (see Handler) rather than once around the whole
// mux, because the two halves of this package's surface need different
// treatment. The embedded shell - index.html, app.js, app.css - is plain
// markup with no build step, and a relative reference such as
// <script src="app.js"> does not inherit the page URL's own query string; a
// token-bearing page could not even load its own script if the shell were
// gated too. The routes here are never referenced that way: every URL the
// frontend builds for them - fetch calls, the WebSocket, frame image src
// attributes - goes through app.js's url() helper at runtime, which attaches
// the token itself. And nothing sensitive lives in the shell anyway; it is
// markup and code, not run state or file content. Exempting it costs
// nothing TOR-30's acceptance criterion asks for, since that criterion names
// exactly what authGuard covers: "the API and the event stream", on every
// route that serves either.
func (s *Server) authGuard(next http.HandlerFunc) http.HandlerFunc {
	want := s.cfg.Token
	if want == "" {
		return next
	}
	wantBytes := []byte(want)
	return func(w http.ResponseWriter, r *http.Request) {
		given := []byte(r.URL.Query().Get("token"))
		if subtle.ConstantTimeCompare(given, wantBytes) != 1 {
			writeError(w, http.StatusUnauthorized, "missing or wrong token")
			return
		}
		next(w, r)
	}
}
