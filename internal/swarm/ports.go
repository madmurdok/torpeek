package swarm

import (
	"errors"
	"fmt"
	"sort"
	"strconv"
	"strings"
	"sync"
)

// ErrNoPortAvailable means every BitTorrent port the operator allocated is
// already held by a client. It is a refusal, never a reason to fall back to
// an OS-assigned port: that port would be outside the allocated range, which
// REQUIREMENTS.md section 4.1 forbids outright.
var ErrNoPortAvailable = errors.New("no BitTorrent port available")

// PortSet is the set of BitTorrent ports torpeek is allowed to bind, exactly
// as the hosting environment allocated them.
//
// Why a set and not a port: anacrolix binds TCP, uTP and (with DHT on) the
// DHT server to one port per client, and DHT and PEX are client-wide
// switches. So every public torrent can share one client - and therefore one
// port, however many of them there are - while a private torrent must have a
// client of its own, with DHT off, and so a port of its own. The size of this
// set is what bounds how many private torrents can fetch at once. That is a
// real product limit handed down by the hosting environment, and callers are
// expected to say so when they hit it (see PortPool.Acquire).
//
// The zero value is the unmanaged set: nothing was configured, so the OS
// chooses a port per client and there is no bound. That is what makes local
// development and the test suite pleasant, and it is exactly what section 4.1
// forbids on a managed host - which is why it is a distinguishable state
// (Managed) rather than a default that quietly stands in for a real range.
type PortSet struct {
	// ports is sorted ascending and free of duplicates. Nil means unmanaged.
	ports []int
}

// ParsePortSet reads a port set as an operator types it: one port, an
// inclusive range, a comma-separated list, or any mixture.
//
//	51413              one port - what a single-client deployment had before
//	51000-51004        five, the shape a seedbox actually allocates
//	51000-51002,51010  a range plus a stray, for an allocation with a hole
//
// The range form is first among equals because section 4.1's allocation is a
// range; the list form exists because not every host hands out a contiguous
// one, and "base plus a count" is only a range spelled less directly.
//
// An empty spec is an error rather than the unmanaged set. The two are
// genuinely different - "I did not configure ports" versus "I configured
// none" - and collapsing them is how a managed host silently ends up on an
// OS-assigned port: an env-driven unit line expands an unset variable to an
// empty argument, and that must fail loudly rather than pick a random port
// outside the allocation. Callers express the unmanaged case by not calling
// this at all (the zero PortSet).
func ParsePortSet(spec string) (PortSet, error) {
	if strings.TrimSpace(spec) == "" {
		return PortSet{}, errors.New("no ports given; omit the flag entirely to let the OS choose one")
	}

	seen := make(map[int]bool)
	var ports []int
	add := func(port int) error {
		if seen[port] {
			return fmt.Errorf("port %d is listed twice; each port can hold one client, so a repeat is a typo rather than two slots", port)
		}
		seen[port] = true
		ports = append(ports, port)
		return nil
	}

	for _, item := range strings.Split(spec, ",") {
		item = strings.TrimSpace(item)
		if item == "" {
			return PortSet{}, fmt.Errorf("empty entry in %q", spec)
		}

		lo, hi, isRange := strings.Cut(item, "-")
		if !isRange {
			port, err := parsePort(item)
			if err != nil {
				return PortSet{}, err
			}
			if err := add(port); err != nil {
				return PortSet{}, err
			}
			continue
		}

		first, err := parsePort(strings.TrimSpace(lo))
		if err != nil {
			return PortSet{}, err
		}
		last, err := parsePort(strings.TrimSpace(hi))
		if err != nil {
			return PortSet{}, err
		}
		if first > last {
			return PortSet{}, fmt.Errorf("range %s runs backwards", item)
		}
		for port := first; port <= last; port++ {
			if err := add(port); err != nil {
				return PortSet{}, err
			}
		}
	}

	sort.Ints(ports)
	return PortSet{ports: ports}, nil
}

// parsePort rejects everything that is not a port a client could bind.
// Zero is refused on purpose: zero already means "let the OS choose", and a
// set holding it would be a range that quietly contains its own opposite.
func parsePort(s string) (int, error) {
	port, err := strconv.Atoi(s)
	if err != nil {
		return 0, fmt.Errorf("%q is not a port number", s)
	}
	if port < 1 || port > 65535 {
		return 0, fmt.Errorf("port %d is outside 1-65535", port)
	}
	return port, nil
}

// Managed reports whether ports were configured at all. False is the local
// case - the OS chooses, nothing is bounded - and true is the managed-host
// case section 4.1 describes.
func (s PortSet) Managed() bool { return len(s.ports) > 0 }

// Len is how many clients this set can hold at once, and therefore how many
// private torrents can fetch at once. Zero for the unmanaged set, which is
// unbounded rather than empty.
func (s PortSet) Len() int { return len(s.ports) }

// Ports returns the set as a sorted slice the caller may keep.
func (s PortSet) Ports() []int {
	out := make([]int, len(s.ports))
	copy(out, s.ports)
	return out
}

// String renders the set back into the spec form ParsePortSet accepts,
// collapsing consecutive ports into ranges - so an error about a set of five
// says "51000-51004" rather than listing them. The unmanaged set renders
// empty; a caller printing that should be naming the OS-assigned case in
// words anyway.
func (s PortSet) String() string {
	var parts []string
	for i := 0; i < len(s.ports); {
		j := i
		for j+1 < len(s.ports) && s.ports[j+1] == s.ports[j]+1 {
			j++
		}
		switch {
		case j == i:
			parts = append(parts, strconv.Itoa(s.ports[i]))
		default:
			parts = append(parts, fmt.Sprintf("%d-%d", s.ports[i], s.ports[j]))
		}
		i = j + 1
	}
	return strings.Join(parts, ",")
}

// PortPool hands the ports of a PortSet out one at a time, so that several
// clients can exist without two of them asking for the same port. It is safe
// for concurrent use: one pool is shared by every client in a process, which
// is the only way it can bound anything.
//
// A pool over the unmanaged (zero) PortSet hands out port 0 for ever: the OS
// chooses each client's port, so there is nothing to run out of. That keeps
// the local case working through exactly the same code path as the managed
// one, while Managed still tells them apart.
type PortPool struct {
	set PortSet

	mu sync.Mutex
	// free is a queue, not a stack: a port handed back goes to the end, so
	// the next client gets one that has been idle longest. See
	// Session.Close for why a just-released port is the worst candidate.
	free []int
}

// NewPortPool returns a pool over set. The pool, not the set, is the thing
// shared between clients - a PortSet is a value and copying it would hand two
// callers the same ports.
func NewPortPool(set PortSet) *PortPool {
	return &PortPool{set: set, free: set.Ports()}
}

// Acquire takes a port out of the pool for one client.
//
// The error, when the set is spent, is the whole point of this type. It names
// the size of the set and the set itself, because the person reading it is
// being told a limit of their hosting environment rather than a bug: the way
// out is a bigger allocation or one fewer private torrent at a time, and
// neither is guessable from "could not start".
func (p *PortPool) Acquire() (*PortLease, error) {
	if !p.set.Managed() {
		return &PortLease{pool: p}, nil
	}

	p.mu.Lock()
	defer p.mu.Unlock()

	if len(p.free) == 0 {
		return nil, fmt.Errorf(
			"%w: all %d allocated BitTorrent ports (%s) are in use - every public torrent shares one client and one port, but each private torrent needs a client, and so a port, of its own, so this set is what bounds how many can fetch at once",
			ErrNoPortAvailable, p.set.Len(), p.set)
	}

	port := p.free[0]
	p.free = p.free[1:]
	return &PortLease{pool: p, port: port}, nil
}

// Managed reports whether this pool bounds anything - see PortSet.Managed.
func (p *PortPool) Managed() bool { return p.set.Managed() }

// Size is how many ports the pool holds in total, free or not.
func (p *PortPool) Size() int { return p.set.Len() }

// Free is how many ports are not currently leased. Always zero for an
// unmanaged pool, which holds no ports and never refuses one either.
func (p *PortPool) Free() int {
	p.mu.Lock()
	defer p.mu.Unlock()
	return len(p.free)
}

// Set returns the ports this pool was built over.
func (p *PortPool) Set() PortSet { return p.set }

func (p *PortPool) put(port int) {
	if port == 0 {
		return
	}
	p.mu.Lock()
	defer p.mu.Unlock()
	p.free = append(p.free, port)
}

// PortLease is one client's claim on one port, held for as long as that
// client is alive. A lease over an unmanaged pool carries port 0, which is
// how "let the OS choose" travels through the same plumbing as a pinned port.
type PortLease struct {
	pool *PortPool
	port int
}

// Port is the port to bind, or 0 for "let the OS choose". Nil-safe, so a
// caller with no pool at all can ask without a branch.
func (l *PortLease) Port() int {
	if l == nil {
		return 0
	}
	return l.port
}

// Release hands the port back. Idempotent, and nil-safe for the same reason
// Port is: an error path that does not know whether a lease was taken can
// call it unconditionally.
//
// Call it only once the socket is actually gone, not merely once Close was
// asked for - see Session.Close.
func (l *PortLease) Release() {
	if l == nil || l.pool == nil {
		return
	}
	l.pool.put(l.port)
	l.pool = nil
}
