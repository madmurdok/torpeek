package web

import (
	"fmt"
	"math"
	"regexp"
	"strconv"
	"strings"
	"testing"
)

// torpeek's UI is drawn in one theme, dark, on every machine (TOR-126). A
// second theme shipped briefly and was removed because it followed
// prefers-color-scheme, which meant the design the project had actually chosen
// was invisible to anyone whose system was set to light - and there was no
// toggle to reach it with.
//
// Committing to one visual world is a legitimate choice rather than an
// omission, but it obliges two things, and these tests are those two: nothing
// may vary by the host's preference, and nothing may be inherited from the
// host either, because a page that inherits anything breaks on somebody
// else's default.
//
// They read the stylesheet out of the embedded FS rather than off disk,
// because the embedded copy is the one that ships.

var declRe = regexp.MustCompile(`(?m)^\s+([a-z-]+(?:-[a-z0-9-]+)*):\s*(.+?);`)

// block returns the declarations of the first rule whose text starts at sel,
// up to that rule's closing brace at column zero.
func block(t *testing.T, css, sel string) map[string]string {
	t.Helper()
	i := strings.Index(css, sel)
	if i < 0 {
		t.Fatalf("app.css has no %q rule", sel)
	}
	end := strings.Index(css[i:], "\n}\n")
	if end < 0 {
		t.Fatalf("the %q rule is never closed", sel)
	}
	out := map[string]string{}
	for _, m := range declRe.FindAllStringSubmatch(css[i:i+end], -1) {
		out[m[1]] = m[2]
	}
	if len(out) == 0 {
		t.Fatalf("the %q rule declares nothing this can read", sel)
	}
	return out
}

func stylesheet(t *testing.T) string {
	t.Helper()
	b, err := embedded.ReadFile("assets/app.css")
	if err != nil {
		t.Fatalf("reading the embedded stylesheet: %v", err)
	}
	return string(b)
}

// TestThePageCommitsToOneTheme is the first half: the stylesheet must not vary
// by anything the host decides. A media query or an attribute selector
// creeping back in would reintroduce exactly the split TOR-126 removed, and it
// would do so invisibly on this machine if this machine happened to match.
func TestThePageCommitsToOneTheme(t *testing.T) {
	css := stylesheet(t)

	// Comments are stripped: the token rule explains in prose why nothing
	// reads the OS preference, and naming it there is the point.
	live := regexp.MustCompile(`(?s)/\*.*?\*/`).ReplaceAllString(css, "")

	for _, conditional := range []string{"prefers-color-scheme", "data-theme", "prefers-contrast"} {
		if strings.Contains(live, conditional) {
			t.Errorf("app.css varies by %s. torpeek is drawn dark on every machine; a "+
				"second theme was removed because following the host hid the design "+
				"the project chose (TOR-126)", conditional)
		}
	}

	base := block(t, css, ":root {")
	if got := base["color-scheme"]; got != "dark" {
		t.Errorf("color-scheme is %q, want dark - it is what makes the browser's own "+
			"form controls and scrollbars match the page", got)
	}
}

// TestTheGroundIsPaintedNotInherited is the second half, and it is the one a
// single-theme page gets wrong. The browser paints its own default behind the
// document; a body with no background of its own shows that through, so a page
// committed to dark would come up on a white ground for a reader whose browser
// defaults light.
func TestTheGroundIsPaintedNotInherited(t *testing.T) {
	css := stylesheet(t)
	body := block(t, css, "body {")

	if got := body["background"]; !strings.HasPrefix(got, "var(--") {
		t.Errorf("body background is %q, want a token. A single-theme page that lets "+
			"the host's ground show through is a page that breaks on somebody else's "+
			"default", got)
	}
	if got := body["color"]; !strings.HasPrefix(got, "var(--") {
		t.Errorf("body color is %q, want a token", got)
	}
}

// TestEveryColourComesFromAToken is what keeps the themes switchable at all: a
// literal colour written into a component rule is the one thing no token
// redefinition can reach, so it stays put while everything around it changes.
func TestEveryColourComesFromAToken(t *testing.T) {
	css := stylesheet(t)

	// Strip the one block allowed to hold literals - the token declarations
	// themselves - and comments, then nothing coloured should remain.
	// Only the token rule may hold a literal, and since TOR-126 there is only
	// one of it - the two blocks a second theme added are gone.
	body := css
	i := strings.Index(body, ":root {")
	if i < 0 {
		t.Fatal("app.css declares no :root token rule")
	}
	end := strings.Index(body[i:], "\n}\n")
	body = body[:i] + body[i+end:]
	body = regexp.MustCompile(`(?s)/\*.*?\*/`).ReplaceAllString(body, "")

	literal := regexp.MustCompile(`#[0-9a-fA-F]{3,8}\b|\brgba?\(|\bhsla?\(`)
	var found []string
	for _, line := range strings.Split(body, "\n") {
		if m := literal.FindString(line); m != "" {
			found = append(found, fmt.Sprintf("%q in %q", m, strings.TrimSpace(line)))
		}
	}
	if len(found) > 0 {
		t.Errorf("colour literals outside the :root token rule, where nothing can "+
			"reach them:\n  %s", strings.Join(found, "\n  "))
	}
}

// ---- TOR-158: measured legibility, not asserted. ----
//
// WCAG 2.x relative luminance and contrast ratio, computed the same way the
// numbers in the ticket's own description were: sRGB channels linearised,
// then the standard 0.2126/0.7152/0.0722 weighting. AA for body text is
// 4.5:1. A ratio recomputed here is worth more than one asserted in a
// comment, because a comment does not fail the build when a later edit
// invalidates it.

func relLuminance(hex string) float64 {
	hex = strings.TrimPrefix(hex, "#")
	r, _ := strconv.ParseInt(hex[0:2], 16, 64)
	g, _ := strconv.ParseInt(hex[2:4], 16, 64)
	b, _ := strconv.ParseInt(hex[4:6], 16, 64)
	lin := func(c int64) float64 {
		v := float64(c) / 255
		if v <= 0.03928 {
			return v / 12.92
		}
		return math.Pow((v+0.055)/1.055, 2.4)
	}
	return 0.2126*lin(r) + 0.7152*lin(g) + 0.0722*lin(b)
}

func contrastRatio(hexA, hexB string) float64 {
	la, lb := relLuminance(hexA), relLuminance(hexB)
	if la < lb {
		la, lb = lb, la
	}
	return (la + 0.05) / (lb + 0.05)
}

// hexToken reads a :root token's value as a plain 6-digit hex literal, the
// only shape this test knows how to score - rgba()/hsla() tokens (--scrim,
// the glows) are not colours this ticket touches.
func hexToken(t *testing.T, root map[string]string, name string) string {
	t.Helper()
	v, ok := root[name]
	if !ok {
		t.Fatalf(":root declares no %s", name)
	}
	v = strings.TrimSpace(v)
	if !regexp.MustCompile(`^#[0-9a-fA-F]{6}$`).MatchString(v) {
		t.Fatalf("%s is %q, want a plain 6-digit hex literal so contrast can be computed", name, v)
	}
	return v
}

// TestInkTokensReachAAOnEverySurface is TOR-158. Measured before the fix:
// --ink-3 scored 4.14 / 3.87 / 3.54 / 3.14 / 3.28 against --bg / --s1 / --s2
// / --s3 / --hover - AA for text is 4.5, so it failed on every surface it
// was actually painted on (every muted caption, the copies/piece line, the
// magnet field's placeholder, and the em dash every absent cell renders).
//
// The fix folds that third ink level into --ink-2 at every point of use
// that is text: two ink levels now carry all of the page's prose, both held
// to AA here. --ink-3 still exists - see its own comment in :root for the
// one non-text use it keeps (the swarm-health dot's neutral fill, which
// only needs the 3.0 a UI part needs, not the 4.5 text needs) - which is
// why it is deliberately not one of the tokens this test scores.
func TestInkTokensReachAAOnEverySurface(t *testing.T) {
	css := stylesheet(t)
	root := block(t, css, ":root {")

	surfaces := []string{"--bg", "--s1", "--s2", "--s3", "--hover"}
	inks := []string{"--ink", "--ink-2"}

	for _, ink := range inks {
		inkHex := hexToken(t, root, ink)
		for _, surf := range surfaces {
			surfHex := hexToken(t, root, surf)
			if ratio := contrastRatio(inkHex, surfHex); ratio < 4.5 {
				t.Errorf("%s (%s) against %s (%s) is %.2f:1, want >= 4.5 (AA for text)",
					ink, inkHex, surf, surfHex, ratio)
			}
		}
	}
}
