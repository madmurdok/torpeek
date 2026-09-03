package cli

import (
	"fmt"
	"io"
	"sort"
	"strings"
	"time"

	"github.com/madmurdok/torpeek/internal/cache"
)

// runCache answers -cache-list, -cache-clear and -cache-clear-all: the
// person-facing half of REQUIREMENTS.md 2.9's last bullet ("a person sees
// what is in the cache and clears it themselves"), which exists precisely
// because automatic eviction (cache.Evict, wired into core.Engine) is off by
// default and a ceiling is the only thing that ever triggers it.
//
// It never opens a torrent session - out is the only thing it needs, the
// same -out a live run writes into - so these three flags work with no
// torrent argument at all, exactly like -version.
func runCache(opts Options, out string, stdout, stderr io.Writer) int {
	switch {
	case opts.CacheClearAll:
		result, err := cache.ClearAll(out)
		if err != nil {
			fmt.Fprintf(stderr, "torpeek: %v\n", err)
			return ExitFailed
		}
		fmt.Fprintf(stdout, "removed %d result set(s), freed %s\n", len(result.Removed), humanBytes(result.Freed))
		return ExitOK

	case opts.CacheClear != "":
		infoHash, params, ok := splitSetID(opts.CacheClear)
		if !ok {
			fmt.Fprintf(stderr, "torpeek: -cache-clear wants infohash/params (see -cache-list), got %q\n", opts.CacheClear)
			return ExitUsage
		}
		if err := cache.Clear(out, infoHash, params); err != nil {
			fmt.Fprintf(stderr, "torpeek: %v\n", err)
			return ExitFailed
		}
		fmt.Fprintf(stdout, "removed %s/%s\n", infoHash, params)
		return ExitOK

	default: // opts.CacheList
		sets, err := cache.Scan(out)
		if err != nil {
			fmt.Fprintf(stderr, "torpeek: %v\n", err)
			return ExitFailed
		}
		if len(sets) == 0 {
			fmt.Fprintln(stdout, "cache is empty")
			return ExitOK
		}

		// Oldest first, the same order Evict would remove them in - so a
		// person deciding what to clear by hand sees the eviction candidates
		// first.
		sort.Slice(sets, func(i, j int) bool { return sets[i].CreatedAt.Before(sets[j].CreatedAt) })

		var total int64
		for _, s := range sets {
			when := "unknown (run.json unreadable)"
			if s.Aged {
				when = s.CreatedAt.Format(time.RFC3339)
			}
			fmt.Fprintf(stdout, "%s/%s  %9s  %s\n", s.InfoHash, s.Params, humanBytes(s.Bytes), when)
			total += s.Bytes
		}
		fmt.Fprintf(stdout, "\n%d set(s), %s total; remove one with -cache-clear infohash/params, or all with -cache-clear-all\n",
			len(sets), humanBytes(total))
		return ExitOK
	}
}

// splitSetID parses -cache-clear's "infohash/params" argument, the same pair
// output.Layout addresses a set by and the form -cache-list prints each row
// under.
func splitSetID(s string) (infoHash, params string, ok bool) {
	i := strings.IndexByte(s, '/')
	if i <= 0 || i == len(s)-1 {
		return "", "", false
	}
	return s[:i], s[i+1:], true
}
