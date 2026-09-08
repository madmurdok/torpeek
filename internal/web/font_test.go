package web

import (
	"crypto/sha256"
	"encoding/hex"
	"io/fs"
	"net/http"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"testing"
)

// The typeface is compiled in (TOR-120), which makes three things testable that
// would otherwise only be checkable by loading the UI on a machine with no
// network: that the files are really there, that the stylesheet asks for
// exactly them, and that nothing points at a font CDN.
//
// It also makes one licensing invariant testable, which is the real reason this
// file exists. IBM Plex carries a Reserved Font Name, so these subsets - which
// are Modified Versions, because subsetting deletes glyphs - may not present
// "Plex" to users. That is a licence term, not a preference, and "rename it
// back to something recognisable" is a plausible future tidy-up.

type fontStanza struct {
	file, family, weight, subset, sha256, unicodeRange string
	bytes                                              int
}

// fontLock parses third_party/fonts.lock, which is provenance rather than
// shipped data, so it lives on disk rather than in the embedded FS.
func fontLock(t *testing.T) (stanzas []fontStanza, totalFiles, totalBytes int) {
	t.Helper()
	path := filepath.Join("..", "..", "third_party", "fonts.lock")
	b, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("reading %s: %v", path, err)
	}
	text := string(b)

	num := func(key string) int {
		m := regexp.MustCompile(`(?m)^` + key + ` = (\d+)$`).FindStringSubmatch(text)
		if m == nil {
			t.Fatalf("%s declares no %s", path, key)
		}
		n, err := strconv.Atoi(m[1])
		if err != nil {
			t.Fatalf("%s: %s is not a number: %v", path, key, err)
		}
		return n
	}
	totalFiles, totalBytes = num("total_files"), num("total_bytes")

	// A key may hold digits (sha256), so the key pattern has to allow them -
	// [a-z_]+ silently truncates every stanza at its first such line.
	blocks := regexp.MustCompile(`\[([^\]]+)\]\n((?:[a-z0-9_]+\s*=.*\n)+)`).FindAllStringSubmatch(text, -1)
	for _, blk := range blocks {
		kv := map[string]string{}
		for _, line := range strings.Split(strings.TrimSpace(blk[2]), "\n") {
			k, v, ok := strings.Cut(line, "=")
			if !ok {
				continue
			}
			kv[strings.TrimSpace(k)] = strings.TrimSpace(v)
		}
		n, err := strconv.Atoi(kv["bytes"])
		if err != nil {
			t.Fatalf("%s: stanza %s has a bad bytes field %q", path, blk[1], kv["bytes"])
		}
		stanzas = append(stanzas, fontStanza{
			file: blk[1], family: kv["family"], weight: kv["weight"], subset: kv["subset"],
			sha256: kv["sha256"], unicodeRange: kv["unicode_range"], bytes: n,
		})
	}
	if len(stanzas) == 0 {
		t.Fatalf("%s parsed to no stanzas", path)
	}
	return stanzas, totalFiles, totalBytes
}

// TestEveryFontInTheLockIsEmbeddedByteForByte is the provenance check. The lock
// is what THIRD-PARTY-NOTICES.md and docs/licensing.md quote their figures
// from, so a file swapped without updating it would make the shipped notices
// state a size and a hash the archive does not have.
func TestEveryFontInTheLockIsEmbeddedByteForByte(t *testing.T) {
	stanzas, wantFiles, wantBytes := fontLock(t)

	if len(stanzas) != wantFiles {
		t.Errorf("the lock declares total_files = %d but holds %d stanzas", wantFiles, len(stanzas))
	}

	sum := 0
	for _, s := range stanzas {
		b, err := embedded.ReadFile("assets/fonts/" + s.file)
		if err != nil {
			t.Errorf("%s is in the lock but not embedded: %v", s.file, err)
			continue
		}
		sum += len(b)
		if len(b) != s.bytes {
			t.Errorf("%s is %d bytes embedded, %d in the lock", s.file, len(b), s.bytes)
		}
		if got := hex.EncodeToString(sha256Of(b)); got != s.sha256 {
			t.Errorf("%s sha256 is\n  %s embedded\n  %s in the lock", s.file, got, s.sha256)
		}
		if !strings.HasPrefix(string(b), "wOF2") {
			t.Errorf("%s is not a woff2 file (magic %q)", s.file, firstBytes(b, 4))
		}
	}
	if sum != wantBytes {
		t.Errorf("the embedded fonts come to %d bytes, the lock says total_bytes = %d "+
			"- the figure in the release notices comes from that line", sum, wantBytes)
	}
}

func sha256Of(b []byte) []byte { h := sha256.Sum256(b); return h[:] }

func firstBytes(b []byte, n int) string {
	if len(b) < n {
		n = len(b)
	}
	return string(b[:n])
}

// TestNoFontIsEmbeddedWithoutBeingRecorded is the other direction: a file
// dropped into assets/fonts and never written into the lock ships with no
// provenance and no hash, which is the whole thing the lock exists to prevent.
func TestNoFontIsEmbeddedWithoutBeingRecorded(t *testing.T) {
	stanzas, _, _ := fontLock(t)
	known := map[string]bool{}
	for _, s := range stanzas {
		known[s.file] = true
	}
	entries, err := fs.ReadDir(embedded, "assets/fonts")
	if err != nil {
		t.Fatalf("reading the embedded font directory: %v", err)
	}
	for _, e := range entries {
		if !known[e.Name()] {
			t.Errorf("assets/fonts/%s is embedded but has no stanza in third_party/fonts.lock", e.Name())
		}
	}
}

// TestTheStylesheetAsksForExactlyTheEmbeddedFonts catches a dead @font-face -
// a rule naming a file that is not there fails silently, and the UI falls back
// to the system stack looking almost right.
func TestTheStylesheetAsksForExactlyTheEmbeddedFonts(t *testing.T) {
	css := stylesheet(t)
	stanzas, _, _ := fontLock(t)

	asked := map[string]bool{}
	for _, m := range regexp.MustCompile(`url\("fonts/([^"]+)"\)`).FindAllStringSubmatch(css, -1) {
		asked[m[1]] = true
		if _, err := embedded.ReadFile("assets/fonts/" + m[1]); err != nil {
			t.Errorf("the stylesheet asks for fonts/%s, which is not embedded", m[1])
		}
	}
	for _, s := range stanzas {
		if !asked[s.file] {
			t.Errorf("%s is embedded and recorded but no @font-face asks for it - "+
				"it is dead weight in the binary", s.file)
		}
	}
}

// TestNoFontIsFetchedFromTheNetwork is the point of embedding. The UI runs
// headless on a seedbox behind a proxy, where an external face fails silently
// and differently per machine.
func TestNoFontIsFetchedFromTheNetwork(t *testing.T) {
	// Comments are stripped first: what matters is what the browser acts on,
	// and the stylesheets name the CDN in prose precisely to say it is not
	// used.
	comment := regexp.MustCompile(`(?s)/\*.*?\*/`)
	// Every served stylesheet (stylesheetFiles, theme_test.go - tokens.css
	// from TOR-189 plus the area files TOR-190 split the rest of what used
	// to be app.css into), not just whichever one file happened to hold
	// :root or the typeface: a stray @import from a font CDN could slip into
	// any of them unnoticed, and ReadFile erroring here is now treated as a
	// real failure rather than silently skipped - a missing split file is
	// exactly the kind of thing this loop exists to catch, not excuse.
	names := []string{"assets/index.html", "assets/app.js"}
	for _, css := range stylesheetFiles {
		names = append(names, "assets/"+css)
	}
	for _, name := range names {
		b, err := embedded.ReadFile(name)
		if err != nil {
			t.Fatalf("reading the embedded %s: %v", name, err)
		}
		live := comment.ReplaceAllString(string(b), "")
		for _, bad := range []string{"fonts.googleapis.com", "fonts.gstatic.com", "use.typekit", "@import url(http"} {
			if strings.Contains(live, bad) {
				t.Errorf("%s references %s outside a comment; the UI must not need a "+
					"network for its typeface", name, bad)
			}
		}
	}
}

// TestTheFontFilesAreServed goes through the real handler rather than the
// embedded FS, because being in the binary and being reachable over HTTP are
// different claims - the assets are served by one FileServerFS rooted at
// assets, and a font in a subdirectory is the first thing to test that.
func TestTheFontFilesAreServed(t *testing.T) {
	stanzas, _, _ := fontLock(t)
	fake := &fakeRun{}
	ts := testServer(t, fake.runner)

	for _, s := range stanzas {
		resp, err := http.Get(ts.URL + "/fonts/" + s.file)
		if err != nil {
			t.Fatalf("GET /fonts/%s: %v", s.file, err)
		}
		body := readAll(t, resp)
		resp.Body.Close()

		if resp.StatusCode != http.StatusOK {
			t.Errorf("GET /fonts/%s: status %d, want 200", s.file, resp.StatusCode)
			continue
		}
		if len(body) != s.bytes {
			t.Errorf("GET /fonts/%s served %d bytes, want %d", s.file, len(body), s.bytes)
		}
		if ct := resp.Header.Get("Content-Type"); ct != "font/woff2" {
			t.Errorf("GET /fonts/%s: Content-Type %q, want font/woff2", s.file, ct)
		}
	}
}

// TestNoFaceIsPresentedUnderAReservedFontName is a licence term expressed as a
// test. IBM Plex is released `with Reserved Font Name "Plex"`; OFL clause 3
// forbids a Modified Version from presenting a reserved name to users, and
// every embedded file is a subset, which is a Modified Version. See
// docs/licensing.md.
func TestNoFaceIsPresentedUnderAReservedFontName(t *testing.T) {
	css := stylesheet(t)
	for _, m := range regexp.MustCompile(`(?m)^\s+font-family:\s*"([^"]+)"`).FindAllStringSubmatch(css, -1) {
		if strings.Contains(strings.ToLower(m[1]), "plex") {
			t.Errorf("an @font-face presents the family as %q, but \"Plex\" is a Reserved "+
				"Font Name and these files are subsets, so OFL clause 3 forbids it "+
				"(docs/licensing.md)", m[1])
		}
	}
	for _, token := range []string{"--sans", "--mono"} {
		m := regexp.MustCompile(token + `:\s*([^;]+);`).FindStringSubmatch(css)
		if m == nil {
			t.Fatalf("the stylesheet declares no %s", token)
		}
		if strings.Contains(strings.ToLower(m[1]), "plex") {
			t.Errorf("%s names a Plex family: %s", token, strings.TrimSpace(m[1]))
		}
	}
}

// TestEveryWeightTheCSSAsksForIsCovered is the one that catches a silent
// downgrade. A font-weight with no matching @font-face does not fail: the
// browser takes the nearest face it has and synthesises the difference, which
// looks like a slightly wrong font rather than like a bug.
func TestEveryWeightTheCSSAsksForIsCovered(t *testing.T) {
	css := stylesheet(t)

	// What each declared face covers: family -> the weights it answers for,
	// either a single value or a variable range "400 700".
	covered := map[string][]int{}
	for _, blk := range regexp.MustCompile(`(?s)@font-face \{(.*?)\}`).FindAllStringSubmatch(css, -1) {
		fam := regexp.MustCompile(`font-family:\s*"([^"]+)"`).FindStringSubmatch(blk[1])
		wt := regexp.MustCompile(`font-weight:\s*(\d+)(?:\s+(\d+))?`).FindStringSubmatch(blk[1])
		if fam == nil || wt == nil {
			t.Errorf("an @font-face declares no family or no weight:%s", blk[1])
			continue
		}
		lo, _ := strconv.Atoi(wt[1])
		hi := lo
		if wt[2] != "" {
			hi, _ = strconv.Atoi(wt[2])
		}
		covered[fam[1]] = append(covered[fam[1]], lo, hi)
	}
	if len(covered) == 0 {
		t.Fatal("the stylesheet declares no @font-face at all")
	}

	// Which family each token resolves to, taking the first name in the stack.
	primary := func(token string) string {
		m := regexp.MustCompile(token + `:\s*"([^"]+)"`).FindStringSubmatch(css)
		if m == nil {
			t.Fatalf("%s does not start with a quoted family", token)
		}
		return m[1]
	}
	sans, mono := primary("--sans"), primary("--mono")

	// Every rule that sets a weight, and which family that rule is in. A rule
	// with no font-family of its own inherits, and the body is --sans.
	type ask struct {
		sel    string
		weight int
		family string
	}
	var asks []ask
	for _, m := range regexp.MustCompile(`(?m)font-weight:\s*(\d+);`).FindAllStringSubmatchIndex(css, -1) {
		open := strings.LastIndex(css[:m[0]], "{")
		if open < 0 {
			continue
		}
		close := strings.Index(css[m[1]:], "}")
		block := css[open : m[1]+close]
		if strings.Contains(block, "src:") {
			continue // an @font-face declaring itself, not a rule asking
		}
		sel := strings.TrimSpace(lastLine(css[:open]))
		w, _ := strconv.Atoi(css[m[2]:m[3]])
		fam := sans
		if strings.Contains(block, "var(--mono)") {
			fam = mono
		}
		asks = append(asks, ask{sel, w, fam})
	}
	if len(asks) == 0 {
		t.Fatal("no rule in the stylesheet asks for a weight; the scan is broken, not the CSS")
	}

	for _, a := range asks {
		ok := false
		for i := 0; i+1 < len(covered[a.family]); i += 2 {
			if a.weight >= covered[a.family][i] && a.weight <= covered[a.family][i+1] {
				ok = true
				break
			}
		}
		if !ok {
			have := covered[a.family]
			sort.Ints(have)
			t.Errorf("%s asks for weight %d of %q, which no @font-face covers (covered: %v); "+
				"the browser will synthesise it instead of failing",
				a.sel, a.weight, a.family, have)
		}
	}
}

func lastLine(s string) string {
	s = strings.TrimRight(s, " \t\n")
	if i := strings.LastIndex(s, "\n"); i >= 0 {
		return s[i+1:]
	}
	return s
}

// TestTheFallbackStackSurvivesAStrippedBuild keeps the embedded face from
// becoming a hard dependency: the tokens must name a generic family last, so a
// build with assets/fonts emptied still renders text.
func TestTheFallbackStackSurvivesAStrippedBuild(t *testing.T) {
	css := stylesheet(t)
	for token, generic := range map[string]string{"--sans": "sans-serif", "--mono": "monospace"} {
		m := regexp.MustCompile(token + `:\s*([^;]+);`).FindStringSubmatch(css)
		if m == nil {
			t.Fatalf("the stylesheet declares no %s", token)
		}
		stack := strings.TrimSpace(m[1])
		if !strings.HasSuffix(stack, generic) {
			t.Errorf("%s does not end in %s, so a build without the embedded fonts "+
				"has nothing to fall back to: %s", token, generic, stack)
		}
		if n := len(strings.Split(stack, ",")); n < 3 {
			t.Errorf("%s has only %d families; the embedded face plus one generic is not "+
				"a stack: %s", token, n, stack)
		}
	}
}
