package web

import (
	"context"
	"net"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

// tokenServer is testServer plus a pinned token, for the tests below that
// exercise authGuard directly rather than Start's auto-generation.
func tokenServer(t *testing.T, runner Runner, token string) *httptest.Server {
	t.Helper()

	cfg := DefaultConfig()
	cfg.Token = token
	srv := newServer(context.Background(), cfg, runner, nil, nil)
	ts := httptest.NewServer(srv.Handler())
	t.Cleanup(func() {
		ts.Close()
		srv.Close()
	})
	return ts
}

// TestUnauthenticatedAPIRequestIsRejectedWithATokenSet is one half of
// TOR-30's acceptance criterion: with a token configured, POST /runs with no
// token at all must not start a run.
func TestUnauthenticatedAPIRequestIsRejectedWithATokenSet(t *testing.T) {
	fake := &fakeRun{}
	ts := tokenServer(t, fake.runner, "s3cret")

	resp := post(t, ts.URL, "/runs", `{"source":"magnet:?xt=urn:btih:abc"}`)
	if resp.StatusCode != http.StatusUnauthorized {
		t.Errorf("POST /runs with no token: status %d, want 401", resp.StatusCode)
	}
	if fake.starts != 0 {
		t.Errorf("the runner ran %d times for an unauthenticated request, want 0", fake.starts)
	}
}

// TestUnauthenticatedSocketIsRejectedWithATokenSet is the other half - and
// the one a token implementation is likeliest to leave open by accident,
// since a WebSocket upgrade does not look like "the API" the way a POST
// does. Section 3.3 calls this out by name: the socket leaks what the owner
// is doing to anyone who can reach it, token or no token on the REST routes.
func TestUnauthenticatedSocketIsRejectedWithATokenSet(t *testing.T) {
	fake := &fakeRun{}
	ts := tokenServer(t, fake.runner, "s3cret")

	resp, err := dialExpectingRejection(t, ts.URL)
	if err == nil {
		t.Fatal("dialing /events with no token succeeded, want a rejection")
	}
	if resp == nil || resp.StatusCode != http.StatusUnauthorized {
		status := 0
		if resp != nil {
			status = resp.StatusCode
		}
		t.Errorf("status = %d, want 401", status)
	}
}

// TestGETRunsRequiresAuth is GET /runs' share of TOR-30's rule: authGuard
// wraps every route that serves the API, this one included - the listing is
// as much a way to see what the owner is doing as the event stream is.
func TestGETRunsRequiresAuth(t *testing.T) {
	fake := &fakeRun{}
	ts := tokenServer(t, fake.runner, "s3cret")

	if resp := get(t, ts.URL, "/runs"); resp.StatusCode != http.StatusUnauthorized {
		t.Errorf("GET /runs with no token: status %d, want 401", resp.StatusCode)
	}
	if resp := get(t, ts.URL, "/runs?token=s3cret"); resp.StatusCode != http.StatusOK {
		t.Errorf("GET /runs?token=s3cret: status %d, want 200", resp.StatusCode)
	}
}

// TestCorrectTokenReachesTheAPIAndTheSocket proves the token that is
// accepted is not merely "some non-empty value": it has to be the
// configured one, on both surfaces.
func TestCorrectTokenReachesTheAPIAndTheSocket(t *testing.T) {
	fake := &fakeRun{}
	ts := tokenServer(t, fake.runner, "s3cret")

	if resp := post(t, ts.URL, "/runs?token=s3cret", `{"source":"magnet:?xt=urn:btih:abc"}`); resp.StatusCode != http.StatusAccepted {
		t.Errorf("POST /runs?token=s3cret: status %d, want 202", resp.StatusCode)
	}

	conn := dial(t, ts.URL+"/?token=s3cret")
	if got := next(t, conn); got["type"] != "run_state" {
		t.Errorf("with the right token the socket said %v, want run_state", got)
	}
}

// TestWrongTokenIsRejected keeps a near-miss from passing: "some token
// present" is not the bar, "the configured token" is.
func TestWrongTokenIsRejected(t *testing.T) {
	fake := &fakeRun{}
	ts := tokenServer(t, fake.runner, "s3cret")

	if resp := post(t, ts.URL, "/runs?token=wrong", `{"source":"magnet:?xt=urn:btih:abc"}`); resp.StatusCode != http.StatusUnauthorized {
		t.Errorf("POST /runs?token=wrong: status %d, want 401", resp.StatusCode)
	}
	if fake.starts != 0 {
		t.Errorf("the runner ran %d times for a wrong token, want 0", fake.starts)
	}

	resp, err := dialExpectingRejection(t, ts.URL+"/?token=wrong")
	if err == nil {
		t.Fatal("dialing /events with a wrong token succeeded, want a rejection")
	}
	if resp == nil || resp.StatusCode != http.StatusUnauthorized {
		status := 0
		if resp != nil {
			status = resp.StatusCode
		}
		t.Errorf("status = %d, want 401", status)
	}
}

// TestNoTokenConfiguredLeavesTheAPIAndSocketOpen is the localhost default
// this task must not regress: nothing in Config asked for a token, so
// nothing is asked of the caller either - exactly the desktop case
// (REQUIREMENTS.md section 3.3: "локально он не нужен").
func TestNoTokenConfiguredLeavesTheAPIAndSocketOpen(t *testing.T) {
	fake := &fakeRun{}
	ts := testServer(t, fake.runner)

	if resp := post(t, ts.URL, "/runs", `{"source":"magnet:?xt=urn:btih:abc"}`); resp.StatusCode != http.StatusAccepted {
		t.Errorf("POST /runs with no token configured: status %d, want 202", resp.StatusCode)
	}

	conn := dial(t, ts.URL)
	if got := next(t, conn); got["type"] != "run_state" {
		t.Errorf("with no token configured the socket said %v, want run_state", got)
	}
}

// TestStartLeavesLoopbackUnprotectedByDefault is TestNoTokenConfiguredLeaves-
// TheAPIAndSocketOpen at the level a person actually runs: through Start,
// binding loopback, no -base-path - the plain desktop invocation - Start
// must not invent a token nobody asked for.
func TestStartLeavesLoopbackUnprotectedByDefault(t *testing.T) {
	fake := &fakeRun{}

	port := freeWebPort(t)
	cfg := DefaultConfig()
	cfg.Addr = net.JoinHostPort("127.0.0.1", port)

	srv, err := Start(context.Background(), cfg, fake.runner, nil, nil)
	if err != nil {
		t.Fatalf("Start: %v", err)
	}
	t.Cleanup(func() { srv.Close() })

	if srv.cfg.Token != "" {
		t.Fatalf("Start generated a token for a plain loopback bind: %q", srv.cfg.Token)
	}
	if strings.Contains(srv.URL(), "token=") {
		t.Errorf("URL() = %q carries a token nobody asked for", srv.URL())
	}
}

// TestStartHonoursAPinnedToken proves Config.Token (what -web-token sets)
// wins outright, even on loopback where nothing would otherwise be
// required: an operator who wants a token - to pin one across restarts
// under systemd, for instance - gets exactly the value given, not a
// generated one.
func TestStartHonoursAPinnedToken(t *testing.T) {
	fake := &fakeRun{}

	port := freeWebPort(t)
	cfg := DefaultConfig()
	cfg.Addr = net.JoinHostPort("127.0.0.1", port)
	cfg.Token = "pinned-value"

	srv, err := Start(context.Background(), cfg, fake.runner, nil, nil)
	if err != nil {
		t.Fatalf("Start: %v", err)
	}
	t.Cleanup(func() { srv.Close() })

	if srv.cfg.Token != "pinned-value" {
		t.Fatalf("Start replaced a pinned token: got %q", srv.cfg.Token)
	}

	// root, not srv.URL(): the latter already carries "?token=pinned-value"
	// itself, and post() would otherwise concatenate a second path onto that
	// query string rather than after it.
	root := "http://" + cfg.Addr
	if resp := post(t, root, "/runs", `{"source":"magnet:?xt=urn:btih:abc"}`); resp.StatusCode != http.StatusUnauthorized {
		t.Errorf("POST /runs with no token against a pinned-token server: status %d, want 401", resp.StatusCode)
	}
	if resp := post(t, root, "/runs?token=pinned-value", `{"source":"magnet:?xt=urn:btih:abc"}`); resp.StatusCode != http.StatusAccepted {
		t.Errorf("POST /runs?token=pinned-value: status %d, want 202", resp.StatusCode)
	}
}

// TestIsLoopbackHost covers the classification needsToken's "bound to
// anything else" decision rests on.
func TestIsLoopbackHost(t *testing.T) {
	cases := []struct {
		addr string
		want bool
	}{
		{"127.0.0.1:8765", true},
		{"127.0.0.53:8765", true}, // all of 127.0.0.0/8 is loopback
		{"localhost:8765", true},
		{"[::1]:8765", true},
		{"0.0.0.0:8765", false},
		{"192.168.1.5:8765", false},
		{":8765", false}, // wildcard: every interface
		{"", false},      // defensive: never mistaken for loopback
		{"example.com:8765", false},
	}
	for _, tc := range cases {
		if got := isLoopbackHost(tc.addr); got != tc.want {
			t.Errorf("isLoopbackHost(%q) = %v, want %v", tc.addr, got, tc.want)
		}
	}
}

// TestNeedsToken covers the decision this task's writeup calls out
// explicitly: a loopback bind behind a configured base path is the seedbox
// deployment TOR-30 targets (REQUIREMENTS.md section 4.1) and must still
// come back true, exactly like a non-loopback bind would on its own.
func TestNeedsToken(t *testing.T) {
	cases := []struct {
		addr, basePath string
		want           bool
	}{
		{"127.0.0.1:8765", "", false},
		{"127.0.0.1:8765", "/torpeek", true}, // the seedbox-behind-nginx case
		{"127.0.0.1:8765", "torpeek", true},  // unnormalized still counts
		{"0.0.0.0:8765", "", true},
		{"192.168.1.5:8765", "", true},
		{":8765", "", true},
		{"localhost:8765", "", false},
	}
	for _, tc := range cases {
		if got := needsToken(tc.addr, tc.basePath); got != tc.want {
			t.Errorf("needsToken(%q, %q) = %v, want %v", tc.addr, tc.basePath, got, tc.want)
		}
	}
}
