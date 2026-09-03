package cli

import (
	"bytes"
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/madmurdok/torpeek/internal/cache"
)

// TestParseSizeAcceptsHumanSuffixes is parseSize's own acceptance test: the
// suffixes -cache-max-size documents, plus the bare-number and empty cases.
func TestParseSizeAcceptsHumanSuffixes(t *testing.T) {
	cases := []struct {
		in   string
		want int64
	}{
		{"", 0},
		{"0", 0},
		{"1024", 1024},
		{"20G", 20 << 30},
		{"20g", 20 << 30},
		{"1.5G", int64(1.5 * (1 << 30))},
		{"512M", 512 << 20},
		{"512MB", 512 << 20},
		{"1K", 1 << 10},
		{"1T", 1 << 40},
	}
	for _, c := range cases {
		got, err := parseSize(c.in)
		if err != nil {
			t.Errorf("parseSize(%q): %v", c.in, err)
			continue
		}
		if got != c.want {
			t.Errorf("parseSize(%q) = %d, want %d", c.in, got, c.want)
		}
	}
}

// TestParseSizeRejectsGarbage covers what config() reports as a usage error
// through -cache-max-size, mirroring TestBadFlagValuesAreUsageErrors' role
// for -mode and -format.
func TestParseSizeRejectsGarbage(t *testing.T) {
	for _, in := range []string{"not-a-size", "-5G", "20X"} {
		if _, err := parseSize(in); err == nil {
			t.Errorf("parseSize(%q) succeeded, want an error", in)
		}
	}
}

// TestCacheMaxSizeFlowsIntoConfig is -cache-max-size's CLI-to-core wiring,
// the same shape as TestPortFlagsFlowIntoConfig for -torrent-port.
func TestCacheMaxSizeFlowsIntoConfig(t *testing.T) {
	opts, err := parse([]string{
		"-data", t.TempDir(),
		"-cache-max-size", "20G",
		"magnet:?xt=urn:btih:abc",
	}, &bytes.Buffer{})
	if err != nil {
		t.Fatalf("parse: %v", err)
	}

	cfg, err := opts.config()
	if err != nil {
		t.Fatalf("config: %v", err)
	}
	if want := int64(20) << 30; cfg.CacheCeiling != want {
		t.Errorf("CacheCeiling = %d, want %d", cfg.CacheCeiling, want)
	}
}

// TestCacheMaxSizeDefaultsToUnset pins the REQUIREMENTS.md 2.9 default: no
// flag means no ceiling, not some hardcoded one.
func TestCacheMaxSizeDefaultsToUnset(t *testing.T) {
	opts, err := parse([]string{"-data", t.TempDir(), "magnet:?xt=urn:btih:abc"}, &bytes.Buffer{})
	if err != nil {
		t.Fatalf("parse: %v", err)
	}
	cfg, err := opts.config()
	if err != nil {
		t.Fatalf("config: %v", err)
	}
	if cfg.CacheCeiling != 0 {
		t.Errorf("CacheCeiling = %d, want 0 (unset)", cfg.CacheCeiling)
	}
}

// TestBadCacheMaxSizeIsAUsageError is -cache-max-size's half of
// TestBadFlagValuesAreUsageErrors: a garbled size must be caught before a
// run starts, not silently taken as zero.
func TestBadCacheMaxSizeIsAUsageError(t *testing.T) {
	var stdout, stderr bytes.Buffer
	code := Run(context.Background(), []string{
		"-cache-max-size", "not-a-size", "magnet:?xt=urn:btih:abc",
	}, &stdout, &stderr)
	if code != ExitUsage {
		t.Errorf("exit code = %d, want %d (stderr: %s)", code, ExitUsage, stderr.String())
	}
}

// plantSet writes a minimal set directly to disk under root, the same shape
// internal/cache's own evict_test.go uses - these tests are about the CLI
// surface over cache.Scan/Evict/Clear, not about producing a set through a
// real run.
func plantSet(t *testing.T, root, infoHash, params string, size int) string {
	t.Helper()
	dir := filepath.Join(root, infoHash, params)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatalf("create set dir: %v", err)
	}
	if err := cache.SaveRun(dir, cache.Run{Version: cache.Version, InfoHash: infoHash}); err != nil {
		t.Fatalf("save run.json: %v", err)
	}
	if size > 0 {
		if err := os.WriteFile(filepath.Join(dir, "payload.bin"), make([]byte, size), 0o600); err != nil {
			t.Fatalf("write payload: %v", err)
		}
	}
	return dir
}

// TestCacheListWorksWithNoTorrentArgument is the acceptance criterion this
// ticket states explicitly: -cache-list must print sets and exit without a
// magnet or .torrent on the command line, the way -version already does.
func TestCacheListWorksWithNoTorrentArgument(t *testing.T) {
	out := t.TempDir()
	plantSet(t, out, "1111111111111111111111111111111111111a", "1111111111111111", 2048)

	var stdout, stderr bytes.Buffer
	code := Run(context.Background(), []string{"-out", out, "-cache-list"}, &stdout, &stderr)
	if code != ExitOK {
		t.Fatalf("exit code = %d, want %d (stderr: %s)", code, ExitOK, stderr.String())
	}
	if !strings.Contains(stdout.String(), "1111111111111111111111111111111111111a/1111111111111111") {
		t.Errorf("listing does not name the set: %q", stdout.String())
	}
}

// TestCacheListOnAnEmptyCacheSaysSo covers the other end: nothing planted,
// nothing to list, still exits OK with no torrent argument.
func TestCacheListOnAnEmptyCacheSaysSo(t *testing.T) {
	var stdout, stderr bytes.Buffer
	code := Run(context.Background(), []string{"-out", t.TempDir(), "-cache-list"}, &stdout, &stderr)
	if code != ExitOK {
		t.Fatalf("exit code = %d, want %d (stderr: %s)", code, ExitOK, stderr.String())
	}
	if !strings.Contains(stdout.String(), "empty") {
		t.Errorf("stdout = %q, want it to say the cache is empty", stdout.String())
	}
}

// TestCacheClearRemovesOneSetWithNoTorrentArgument is -cache-clear's version
// of the same criterion, and checks the result on disk rather than trusting
// the exit code.
func TestCacheClearRemovesOneSetWithNoTorrentArgument(t *testing.T) {
	out := t.TempDir()
	kept := plantSet(t, out, "1111111111111111111111111111111111111a", "1111111111111111", 100)
	gone := plantSet(t, out, "2222222222222222222222222222222222222b", "2222222222222222", 100)

	var stdout, stderr bytes.Buffer
	code := Run(context.Background(), []string{
		"-out", out, "-cache-clear", "2222222222222222222222222222222222222b/2222222222222222",
	}, &stdout, &stderr)
	if code != ExitOK {
		t.Fatalf("exit code = %d, want %d (stderr: %s)", code, ExitOK, stderr.String())
	}
	if _, err := os.Stat(gone); !os.IsNotExist(err) {
		t.Errorf("%s should have been removed, stat err=%v", gone, err)
	}
	if _, err := os.Stat(kept); err != nil {
		t.Errorf("%s should still be on disk: %v", kept, err)
	}
}

// TestCacheClearAllRemovesEverySetWithNoTorrentArgument is -cache-clear-all's
// version, again checked on disk.
func TestCacheClearAllRemovesEverySetWithNoTorrentArgument(t *testing.T) {
	out := t.TempDir()
	a := plantSet(t, out, "1111111111111111111111111111111111111a", "1111111111111111", 100)
	b := plantSet(t, out, "2222222222222222222222222222222222222b", "2222222222222222", 100)

	var stdout, stderr bytes.Buffer
	code := Run(context.Background(), []string{"-out", out, "-cache-clear-all"}, &stdout, &stderr)
	if code != ExitOK {
		t.Fatalf("exit code = %d, want %d (stderr: %s)", code, ExitOK, stderr.String())
	}
	for _, dir := range []string{a, b} {
		if _, err := os.Stat(dir); !os.IsNotExist(err) {
			t.Errorf("%s should have been removed, stat err=%v", dir, err)
		}
	}
}

// TestCacheClearRejectsAMalformedID checks the usage error path for a
// -cache-clear value that is not "infohash/params".
func TestCacheClearRejectsAMalformedID(t *testing.T) {
	var stdout, stderr bytes.Buffer
	code := Run(context.Background(), []string{
		"-out", t.TempDir(), "-cache-clear", "not-a-valid-id",
	}, &stdout, &stderr)
	if code != ExitUsage {
		t.Errorf("exit code = %d, want %d", code, ExitUsage)
	}
}
