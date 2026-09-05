package swarm

import (
	"errors"
	"fmt"
	"reflect"
	"strings"
	"sync"
	"testing"
)

// TestParsePortSetAcceptsWhatAPlatformHandsOut covers the three shapes an
// allocation actually arrives in - one port, a range, and a range with a hole
// - plus the sloppiness a person types around them.
func TestParsePortSetAcceptsWhatAPlatformHandsOut(t *testing.T) {
	cases := []struct {
		name string
		spec string
		want []int
	}{
		{"one port, as the single-client deployment always had", "51413", []int{51413}},
		{"a range, which is what a seedbox allocates", "51000-51004", []int{51000, 51001, 51002, 51003, 51004}},
		{"a list", "51000,51010", []int{51000, 51010}},
		{"a range and a stray", "51000-51002,51010", []int{51000, 51001, 51002, 51010}},
		{"a range of one", "51000-51000", []int{51000}},
		{"sorted, whatever the order typed", "51010,51000-51001", []int{51000, 51001, 51010}},
		{"spaces around the entries", " 51000 - 51001 , 51010 ", []int{51000, 51001, 51010}},
	}

	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			set, err := ParsePortSet(c.spec)
			if err != nil {
				t.Fatalf("ParsePortSet(%q): %v", c.spec, err)
			}
			if got := set.Ports(); !reflect.DeepEqual(got, c.want) {
				t.Errorf("ParsePortSet(%q) = %v, want %v", c.spec, got, c.want)
			}
			if !set.Managed() {
				t.Errorf("ParsePortSet(%q) is not Managed; a configured set is exactly the managed-host case", c.spec)
			}
			if got, want := set.Len(), len(c.want); got != want {
				t.Errorf("Len() = %d, want %d - the size is the bound on concurrent private torrents", got, want)
			}
		})
	}
}

// TestParsePortSetRefusesNonsense: every one of these would otherwise become
// a set that binds something nobody allocated, which is the one thing section
// 4.1 forbids outright.
func TestParsePortSetRefusesNonsense(t *testing.T) {
	cases := []struct {
		name string
		spec string
	}{
		{"empty, which must not silently mean the OS chooses", ""},
		{"only spaces", "   "},
		{"zero, which already means let the OS choose", "0"},
		{"a zero inside a range", "0-51000"},
		{"above the port space", "70000"},
		{"negative", "-51000"},
		{"not a number", "fiftyone thousand"},
		{"a range with no end", "51000-"},
		{"a backwards range", "51004-51000"},
		{"an empty entry", "51000,,51001"},
		{"a trailing comma", "51000,"},
		{"the same port twice", "51000,51000"},
		{"a port already inside a range", "51000-51004,51002"},
	}

	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			set, err := ParsePortSet(c.spec)
			if err == nil {
				t.Fatalf("ParsePortSet(%q) = %v, want an error", c.spec, set.Ports())
			}
			if set.Managed() {
				t.Errorf("ParsePortSet(%q) returned a usable set alongside its error", c.spec)
			}
		})
	}
}

// TestZeroPortSetIsTheLocalCase: nothing configured is unmanaged, not empty.
// The distinction is the whole of decision three - the OS choosing is right
// on a laptop and forbidden on a managed host, so the two must stay
// tellable apart rather than collapsing into one default.
func TestZeroPortSetIsTheLocalCase(t *testing.T) {
	var set PortSet

	if set.Managed() {
		t.Error("the zero PortSet reports itself managed")
	}
	if got := set.Len(); got != 0 {
		t.Errorf("Len() = %d, want 0", got)
	}
	if got := len(set.Ports()); got != 0 {
		t.Errorf("Ports() has %d entries, want none", got)
	}
}

// TestPortSetStringCollapsesRuns: an exhaustion message naming a set of five
// should say 51000-51004, not list them, and should round-trip back through
// the parser so what it prints is something a person can paste into the flag.
func TestPortSetStringCollapsesRuns(t *testing.T) {
	cases := []struct{ spec, want string }{
		{"51413", "51413"},
		{"51000-51004", "51000-51004"},
		{"51000-51002,51010", "51000-51002,51010"},
		{"51000,51001,51003", "51000-51001,51003"},
		{"51010,51000-51001", "51000-51001,51010"},
	}

	for _, c := range cases {
		set, err := ParsePortSet(c.spec)
		if err != nil {
			t.Fatalf("ParsePortSet(%q): %v", c.spec, err)
		}
		if got := set.String(); got != c.want {
			t.Errorf("ParsePortSet(%q).String() = %q, want %q", c.spec, got, c.want)
		}

		again, err := ParsePortSet(set.String())
		if err != nil {
			t.Fatalf("ParsePortSet(%q) does not round-trip: %v", set.String(), err)
		}
		if !reflect.DeepEqual(again.Ports(), set.Ports()) {
			t.Errorf("round trip of %q = %v, want %v", c.spec, again.Ports(), set.Ports())
		}
	}
}

// TestPortPoolHandsOutEachPortOnce is the property the whole release rests
// on: two clients out of one set get two different ports, and both of them
// are ports somebody actually allocated.
func TestPortPoolHandsOutEachPortOnce(t *testing.T) {
	set, err := ParsePortSet("51000-51002")
	if err != nil {
		t.Fatalf("parse: %v", err)
	}
	pool := NewPortPool(set)

	if got, want := pool.Size(), 3; got != want {
		t.Errorf("Size() = %d, want %d", got, want)
	}

	seen := make(map[int]bool)
	for i := 0; i < 3; i++ {
		lease, err := pool.Acquire()
		if err != nil {
			t.Fatalf("Acquire %d of 3: %v", i+1, err)
		}
		port := lease.Port()
		if port < 51000 || port > 51002 {
			t.Fatalf("Acquire handed out %d, which is outside the allocated set %s", port, set)
		}
		if seen[port] {
			t.Fatalf("Acquire handed out %d twice - two clients cannot bind one port", port)
		}
		seen[port] = true
	}

	if got := pool.Free(); got != 0 {
		t.Errorf("Free() = %d after taking every port, want 0", got)
	}
}

// TestPortPoolRefusesWhenSpent is decision two, at the level it is decided:
// running out is a refusal a person can read, never a fall back to whatever
// the OS would hand over.
func TestPortPoolRefusesWhenSpent(t *testing.T) {
	set, err := ParsePortSet("51000-51001")
	if err != nil {
		t.Fatalf("parse: %v", err)
	}
	pool := NewPortPool(set)

	for i := 0; i < 2; i++ {
		if _, err := pool.Acquire(); err != nil {
			t.Fatalf("Acquire %d of 2: %v", i+1, err)
		}
	}

	lease, err := pool.Acquire()
	if err == nil {
		t.Fatalf("Acquire on a spent pool returned port %d, want a refusal", lease.Port())
	}
	if !errors.Is(err, ErrNoPortAvailable) {
		t.Errorf("Acquire error is not ErrNoPortAvailable: %v", err)
	}

	// The message is part of the contract: it is telling somebody about a
	// limit of their hosting environment, so it has to name the limit.
	msg := err.Error()
	for _, want := range []string{"2", "51000-51001", "private"} {
		if !strings.Contains(msg, want) {
			t.Errorf("exhaustion message does not mention %q, so it does not say what ran out or why it bounds anything: %s", want, msg)
		}
	}
}

// TestPortPoolTakesReleasedPortsBack: a client that closes gives its port up,
// and the set is not permanently smaller for having been used.
func TestPortPoolTakesReleasedPortsBack(t *testing.T) {
	set, err := ParsePortSet("51000")
	if err != nil {
		t.Fatalf("parse: %v", err)
	}
	pool := NewPortPool(set)

	first, err := pool.Acquire()
	if err != nil {
		t.Fatalf("acquire the only port: %v", err)
	}
	if _, err := pool.Acquire(); err == nil {
		t.Fatal("a one-port pool handed out a second port")
	}

	first.Release()
	if got := pool.Free(); got != 1 {
		t.Fatalf("Free() = %d after releasing the only lease, want 1", got)
	}

	second, err := pool.Acquire()
	if err != nil {
		t.Fatalf("acquire after release: %v", err)
	}
	if got := second.Port(); got != 51000 {
		t.Errorf("reacquired port %d, want the released 51000", got)
	}

	// Releasing twice must not conjure a port that does not exist.
	first.Release()
	if got := pool.Free(); got != 0 {
		t.Errorf("Free() = %d after a double release, want 0", got)
	}
}

// TestPortPoolPrefersTheLongestIdlePort: a released port goes to the back of
// the queue. Session.Close waits for the socket before releasing, but a
// pool that handed the just-released port straight back would be racing that
// wait every time instead of only when there is nothing else left.
func TestPortPoolPrefersTheLongestIdlePort(t *testing.T) {
	set, err := ParsePortSet("51000-51002")
	if err != nil {
		t.Fatalf("parse: %v", err)
	}
	pool := NewPortPool(set)

	first, err := pool.Acquire()
	if err != nil {
		t.Fatalf("acquire: %v", err)
	}
	released := first.Port()
	first.Release()

	next, err := pool.Acquire()
	if err != nil {
		t.Fatalf("acquire after release: %v", err)
	}
	if next.Port() == released {
		t.Errorf("Acquire returned %d, the port released a moment ago, while %d others sat idle", released, pool.Free())
	}
}

// TestUnmanagedPoolNeverRunsOut: with nothing configured the pool is a pass
// through to "let the OS choose", so a hundred clients are as fine as one.
// This is what keeps local runs and the test suite from needing a port range.
func TestUnmanagedPoolNeverRunsOut(t *testing.T) {
	pool := NewPortPool(PortSet{})

	if pool.Managed() {
		t.Error("a pool over the zero PortSet reports itself managed")
	}

	for i := 0; i < 100; i++ {
		lease, err := pool.Acquire()
		if err != nil {
			t.Fatalf("Acquire %d: %v - an unconfigured pool bounds nothing", i+1, err)
		}
		if got := lease.Port(); got != 0 {
			t.Fatalf("Acquire returned port %d, want 0 so the OS chooses", got)
		}
		lease.Release()
	}
}

// TestPortPoolIsSafeForConcurrentUse: one pool is shared by every client in
// the process, so the whole guarantee is void if two goroutines can be handed
// the same port.
func TestPortPoolIsSafeForConcurrentUse(t *testing.T) {
	const size = 64

	set, err := ParsePortSet("51000-51063")
	if err != nil {
		t.Fatalf("parse: %v", err)
	}
	if set.Len() != size {
		t.Fatalf("set holds %d ports, want %d", set.Len(), size)
	}
	pool := NewPortPool(set)

	var (
		mu    sync.Mutex
		seen  = make(map[int]bool)
		fails []string
		wg    sync.WaitGroup
	)
	for i := 0; i < size; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			lease, err := pool.Acquire()
			mu.Lock()
			defer mu.Unlock()
			if err != nil {
				fails = append(fails, err.Error())
				return
			}
			if seen[lease.Port()] {
				fails = append(fails, fmt.Sprintf("port %d handed out twice", lease.Port()))
			}
			seen[lease.Port()] = true
		}()
	}
	wg.Wait()

	if len(fails) > 0 {
		t.Fatalf("%d of %d concurrent acquisitions went wrong: %v", len(fails), size, fails)
	}
	if len(seen) != size {
		t.Errorf("%d distinct ports handed out, want %d", len(seen), size)
	}
}
