package web

import (
	"fmt"
	"regexp"
	"sort"
	"strings"
	"testing"
)

// The theme has three states, not two, and the middle one is the default: an
// explicit choice stamps data-theme on the root element, and the "system"
// setting stamps nothing at all, leaving prefers-color-scheme to decide. So a
// custom property declared only inside a media query or only inside the
// attribute selector is missing in one of the three - which is how a page ends
// up drawing one theme's text on the other theme's ground.
//
// These tests read the stylesheet out of the embedded FS rather than off disk,
// because the embedded copy is the one that ships.

var declRe = regexp.MustCompile(`(?m)^\s+(--[a-z0-9-]+):\s*(.+?);`)

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
		t.Fatalf("the %q rule declares no custom properties", sel)
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

// TestEveryThemeTokenIsDeclaredUnconditionally is the guard against the classic
// unreadable-page bug: a colour whose only declaration sits behind a media
// query or an attribute selector.
func TestEveryThemeTokenIsDeclaredUnconditionally(t *testing.T) {
	css := stylesheet(t)
	base := block(t, css, ":root {")

	for _, sel := range []string{
		"@media (prefers-color-scheme: light)",
		`:root[data-theme="light"]`,
	} {
		var only []string
		for name := range block(t, css, sel) {
			if _, ok := base[name]; !ok {
				only = append(only, name)
			}
		}
		sort.Strings(only)
		if len(only) > 0 {
			t.Errorf("%s declares %v, which the bare :root never does; "+
				"in the un-stamped state those tokens do not exist", sel, only)
		}
	}
}

// TestTheTwoLightArmsCannotDrift ties the two ways of reaching the light theme
// together. They are the same design - one selected by the OS, one by an
// explicit stamp - so a value changed in one and not the other is a bug that
// only shows up for whichever half of the users hits the other selector.
func TestTheTwoLightArmsCannotDrift(t *testing.T) {
	css := stylesheet(t)
	media := block(t, css, "@media (prefers-color-scheme: light)")
	attr := block(t, css, `:root[data-theme="light"]`)

	for name, want := range media {
		got, ok := attr[name]
		if !ok {
			t.Errorf(`%s is set for a light OS but not for data-theme="light"`, name)
			continue
		}
		if got != want {
			t.Errorf("%s is %q for a light OS but %q for an explicit light stamp", name, want, got)
		}
	}
	for name := range attr {
		if _, ok := media[name]; !ok {
			t.Errorf(`%s is set for data-theme="light" but not for a light OS`, name)
		}
	}
}

// TestAnExplicitChoiceBeatsTheOperatingSystem checks the guard that makes the
// cascade obey a person over their OS. Without :not([data-theme="dark"]) the
// light media query outranks nothing and simply wins on a light OS, so someone
// who asked for dark would get light.
func TestAnExplicitChoiceBeatsTheOperatingSystem(t *testing.T) {
	css := stylesheet(t)
	i := strings.Index(css, "@media (prefers-color-scheme: light)")
	if i < 0 {
		t.Fatal("app.css has no light media query")
	}
	head := css[i:min(i+200, len(css))]
	if !strings.Contains(head, `:root:not([data-theme="dark"])`) {
		t.Errorf("the light media query is not guarded by :not([data-theme=\"dark\"]); "+
			"an explicit dark choice would lose to a light OS.\ngot: %s",
			strings.SplitN(head, "{", 3)[1])
	}
}

// TestTheLightThemeDoesNotGlow records a design decision as a test, because it
// is the one place the two themes are not the same design with different
// numbers. Glow reads as light being emitted, which means something only on a
// dark ground; on white it is a grey smudge around a letterform.
func TestTheLightThemeDoesNotGlow(t *testing.T) {
	css := stylesheet(t)
	for _, sel := range []string{
		"@media (prefers-color-scheme: light)",
		`:root[data-theme="light"]`,
	} {
		b := block(t, css, sel)
		for _, name := range []string{"--glow", "--stroke-glow"} {
			if got := b[name]; got != "none" {
				t.Errorf("%s sets %s to %q, want none", sel, name, got)
			}
		}
	}
}

// TestEveryColourComesFromAToken is what keeps the themes switchable at all: a
// literal colour written into a component rule is the one thing no token
// redefinition can reach, so it stays put while everything around it changes.
func TestEveryColourComesFromAToken(t *testing.T) {
	css := stylesheet(t)

	// Strip the four blocks that are allowed to hold literals - the token
	// declarations themselves - and comments, then nothing coloured should
	// remain.
	body := css
	for _, sel := range []string{
		":root {",
		"@media (prefers-color-scheme: light)",
		`:root[data-theme="light"]`,
	} {
		i := strings.Index(body, sel)
		if i < 0 {
			continue
		}
		end := strings.Index(body[i:], "\n}\n")
		body = body[:i] + body[i+end:]
	}
	body = regexp.MustCompile(`(?s)/\*.*?\*/`).ReplaceAllString(body, "")

	literal := regexp.MustCompile(`#[0-9a-fA-F]{3,8}\b|\brgba?\(|\bhsla?\(`)
	var found []string
	for _, line := range strings.Split(body, "\n") {
		if m := literal.FindString(line); m != "" {
			found = append(found, fmt.Sprintf("%q in %q", m, strings.TrimSpace(line)))
		}
	}
	if len(found) > 0 {
		t.Errorf("colour literals outside the token blocks, which no theme can "+
			"redefine:\n  %s", strings.Join(found, "\n  "))
	}
}
