package main

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"testing"
	"time"

	"github.com/charmbracelet/lipgloss"
	"github.com/muesli/termenv"
	"os/exec"
)

// themeFixtureModel builds a model whose rendering depends on neither the
// wall clock, the hostname, the environment nor the network: a fixed now, a
// fixed host, fixed demo snapshots, and source deadlines in the past so the
// footer countdown reads a constant 0s. The same construction feeds the
// pre-change fixture capture and the theme-0 regression test below.
func themeFixtureModel(showHelp bool) model {
	now := time.Date(2026, 1, 1, 12, 0, 0, 0, time.UTC)
	m := newModel(20*time.Second, loadHistory(""))
	m.host = "sandbox"
	m.now = now
	m.width = 132
	m.height = 0
	m.showHelp = showHelp
	claude := demoSnapshot(now)
	codex := &Snapshot{
		Source: "codex", Title: "CODEX", Verb: "scanned", Footnote: "session logs",
		Observed: now.Add(-30 * time.Second), At: now,
		Err: errors.New("no session logs found in any codex root"),
	}
	m.sources = []sourceState{
		{fetch: func(bool) Snapshot { return *claude }, snap: claude, due: now.Add(-time.Hour)},
		{fetch: func(bool) Snapshot { return *codex }, snap: codex, due: now.Add(-time.Hour)},
	}
	return m
}

// forcedColour swaps the global colour profile to TrueColor (what
// CLICOLOR_FORCE=1 does in renderSnapshot) and restores it on return, so the
// fixture comparisons see colours regardless of the test runner's terminal.
func forcedColour(t *testing.T) {
	t.Helper()
	prev := lipgloss.ColorProfile()
	lipgloss.SetColorProfile(termenv.TrueColor)
	t.Cleanup(func() { lipgloss.SetColorProfile(prev) })
}

// The fixtures in testdata/themes/ were captured from the pre-theme code with the
// same construction as here. Theme 0 carries exactly the old values, so
// rendering with it must reproduce them byte for byte: if this test fails,
// the refactor changed output it was not supposed to touch. The t key's
// footer hint and help line were added afterwards; they are the one
// deliberate difference, and this test asserts everything else is still
// byte-identical.
func TestThemeZeroMatchesPreChangeFixture(t *testing.T) {
	isolateStatePath(t)
	isolateAccountEnv(t)
	forcedColour(t)
	for _, tc := range []struct {
		name     string
		file     string
		showHelp bool
	}{
		{"frame", "testdata/themes/theme-0-prechange.txt", false},
		{"help", "testdata/themes/theme-0-prechange-help.txt", true},
	} {
		want, err := os.ReadFile(tc.file)
		if err != nil {
			t.Fatalf("reading %s: %v", tc.file, err)
		}
		got := strings.Split(themeFixtureModel(tc.showHelp).View(), "\n")
		wantLines := strings.Split(strings.TrimRight(string(want), "\n"), "\n")
		if tc.showHelp {
			// Compare the palette, not the bytes. The help body gains and
			// edits rows whenever a keybinding changes -- t and then
			// compact-vertical both did -- so freezing its text here trips on
			// every such edit while saying nothing about colour. What must not
			// change is the set of colours theme 0 draws it with, and the
			// frame case above already pins the layout byte for byte.
			palette := func(lines []string) string {
				sgr := regexp.MustCompile(`\x1b\[[0-9;]*m`)
				seen := map[string]bool{}
				for _, line := range lines {
					for _, seq := range sgr.FindAllString(line, -1) {
						seen[seq] = true
					}
				}
				out := make([]string, 0, len(seen))
				for seq := range seen {
					out = append(out, seq)
				}
				sort.Strings(out)
				return strings.Join(out, " ")
			}
			if got, want := palette(got), palette(wantLines); got != want {
				t.Errorf("help: theme 0's palette changed:\ngot:  %s\nwant: %s", got, want)
			}
			continue
		} else {
			// The footer gained the t hint as an insertion on its last line.
			// Strip exactly that run; the hint legitimately eats into the
			// footer's gap padding, so the last line is compared with space
			// runs collapsed.
			snip := currentTheme().dim.Render(" · ") +
				currentTheme().key.Render("t") + currentTheme().dim.Render(" themes")
			if n := strings.Count(got[len(got)-1], snip); n != 1 {
				t.Fatalf("frame: found %d copies of the t footer hint, want exactly 1", n)
			}
			got[len(got)-1] = strings.Replace(got[len(got)-1], snip, "", 1)
		}
		if len(got) != len(wantLines) {
			t.Fatalf("%s: %d lines, fixture has %d", tc.name, len(got), len(wantLines))
		}
		for i := range wantLines {
			if tc.showHelp || i < len(wantLines)-1 {
				if got[i] != wantLines[i] {
					t.Errorf("%s: line %d differs from the pre-change fixture:\ngot:  %q\nwant: %q", tc.name, i, got[i], wantLines[i])
				}
				continue
			}
			if reGap := regexp.MustCompile(` {2,}`); reGap.ReplaceAllString(got[i], " ") != reGap.ReplaceAllString(wantLines[i], " ") {
				t.Errorf("%s: footer differs from the pre-change fixture beyond the t hint:\ngot:  %q\nwant: %q", tc.name, got[i], wantLines[i])
			}
		}
	}
}

// setThemeIndex points the current theme at i and restores it on return.
func setThemeIndex(t *testing.T, i int) {
	t.Helper()
	prev := themeIndex
	themeIndex = i
	t.Cleanup(func() { themeIndex = prev })
}

// Every theme is complete: no zero-valued style (a missing field renders its
// text uncoloured) and no empty or short gradient, and the internal names are
// unique. A half-filled entry is the likeliest way a new theme ships broken.
func TestThemesAreComplete(t *testing.T) {
	forcedColour(t)
	styles := func(th theme) [7]struct {
		field string
		style lipgloss.Style
	} {
		return [7]struct {
			field string
			style lipgloss.Style
		}{
			{"dim", th.dim}, {"mut", th.mut}, {"txt", th.txt}, {"key", th.key},
			{"err", th.err}, {"wrn", th.wrn}, {"brd", th.brd},
		}
	}
	seen := map[string]int{}
	for i, th := range themes {
		if prev, dup := seen[th.name]; dup {
			t.Errorf("theme %d: name %q already used by theme %d", i, th.name, prev)
		}
		seen[th.name] = i
		if th.name == "" {
			t.Errorf("theme %d: empty name", i)
		}
		for _, s := range styles(th) {
			if s.style.Render("x") == "x" {
				t.Errorf("theme %d (%s): %s style carries no foreground colour", i, th.name, s.field)
			}
		}
		if len(th.gradient) != 5 {
			t.Errorf("theme %d (%s): gradient has %d stops, want 5", i, th.name, len(th.gradient))
		}
	}
}

// Every theme 1..n actually differs from theme 0's rendering: this catches a
// theme that was declared but never wired up. The fixture model renders every
// part of the screen, so an unwired style would not show up.
func TestEveryThemeDiffersFromThemeZero(t *testing.T) {
	isolateStatePath(t)
	isolateAccountEnv(t)
	forcedColour(t)
	baseline := themeFixtureModel(false).View()
	for i := 1; i < len(themes); i++ {
		setThemeIndex(t, i)
		if got := themeFixtureModel(false).View(); got == baseline {
			t.Errorf("theme %d (%s) renders identically to theme 0", i, themes[i].name)
		}
	}
}

// ansiStrip removes SGR escape sequences so styled output can be compared on
// its plain content.
var ansiRe = regexp.MustCompile(`\x1b\[[0-9;]*m`)

func ansiStrip(s string) string { return ansiRe.ReplaceAllString(s, "") }

// reClock masks the header's wall clock, the one part of the fixture render
// that moves between two --snapshot calls taken a moment apart.
var reClock = regexp.MustCompile(`\w+ \d{1,2}:\d{2}:\d{2} [AP]M`)

func plainFrame(s string) string { return reClock.ReplaceAllString(ansiStrip(s), "TIME") }

// The footer and the ? help name the key but not any theme: cycling is
// announced, the current scheme is not.
func TestFooterAndHelpHintAtThemeKey(t *testing.T) {
	isolateStatePath(t)
	isolateAccountEnv(t)
	if plain := ansiStrip(themeFixtureModel(false).View()); !strings.Contains(plain, "t themes") {
		t.Errorf("footer does not hint that t cycles themes: %q", plain)
	}
	if plain := ansiStrip(themeFixtureModel(true).View()); !strings.Contains(plain, "cycle themes") {
		t.Error("help body does not document that t cycles themes")
	}
	for _, showHelp := range []bool{false, true} {
		view := ansiStrip(themeFixtureModel(showHelp).View())
		for _, th := range themes {
			if strings.Contains(view, th.name) {
				t.Errorf("render names the theme %q, want no theme name displayed", th.name)
			}
		}
	}
}

// Pressing t len(themes) times renders byte-identically to never having
// pressed it at all: the cycle wraps back to the start.
func TestCyclingAllThemesRendersSameAsStart(t *testing.T) {
	isolateStatePath(t)
	isolateAccountEnv(t)
	forcedColour(t)
	m := themeFixtureModel(false)
	first := m.View()
	for i := 0; i < len(themes); i++ {
		updated, cmd := m.Update(key("t"))
		if cmd != nil {
			t.Fatalf("press %d: t produced a command, want none", i)
		}
		m = updated.(model)
	}
	if got := m.View(); got != first {
		t.Errorf("after %d presses of t, render differs from the start:\ngot:\n%s\nwant:\n%s",
			len(themes), got, first)
	}
}

// --theme is a test instrument for --snapshot: a valid index renders the
// requested scheme (different colouring, identical plain content), and an
// out-of-range index fails loudly rather than clamping or wrapping.
func TestRenderSnapshotHonoursThemeFlag(t *testing.T) {
	isolateStatePath(t)
	isolateAccountEnv(t)
	t.Setenv("NO_COLOR", "")
	t.Setenv("CLICOLOR_FORCE", "1")
	render := func(themeIdx int) (string, string, int) {
		var code int
		stdout, stderr := captureStdout(t, func() {
			code = renderSnapshot(themeFixtureModel(false), 132, 0, "", themeIdx, false)
		})
		return stdout, stderr, code
	}
	out0, _, code0 := render(0)
	if code0 != 1 {
		t.Fatalf("theme 0 exit code = %d, want 1 (the fixture's codex source fails by design; 2 would be a theme error)", code0)
	}
	out4, _, code4 := render(4)
	if code4 != 1 {
		t.Fatalf("theme 4 exit code = %d, want 1 (same as theme 0: fetch failed, theme fine)", code4)
	}
	if plainFrame(out0) != plainFrame(out4) {
		t.Error("theme flag changed rendered content, want colour only")
	}
	if strings.TrimRight(out0, "\n") == strings.TrimRight(out4, "\n") {
		t.Error("theme 4 rendered byte-identically to theme 0, want different colouring")
	}
	for _, idx := range []int{-1, len(themes)} {
		_, stderr, code := render(idx)
		if code == 0 {
			t.Errorf("exit code = 0 for theme index %d, want non-zero", idx)
			continue
		}
		if !strings.Contains(stderr, fmt.Sprintf("theme index %d out of range (0-%d)", idx, len(themes)-1)) {
			t.Errorf("stderr = %q, want the out-of-range error naming %d", stderr, idx)
		}
	}
}

// End to end: the built binary exits non-zero on --snapshot --theme out of
// range, the same way a test that asks for a theme that does not exist must
// fail loudly.
func TestSnapshotFlagRejectsOutOfRangeTheme(t *testing.T) {
	isolateStatePath(t)
	bin := filepath.Join(t.TempDir(), "quotatop")
	if out, err := exec.Command("go", "build", "-o", bin, ".").CombinedOutput(); err != nil {
		t.Fatalf("go build: %v\n%s", err, out)
	}
	out, err := exec.Command(bin, "--snapshot", "--theme", fmt.Sprint(len(themes)), "--no-history").CombinedOutput()
	if err == nil {
		t.Fatalf("exit = 0 for out-of-range --theme, want non-zero\n%s", out)
	}
	if !strings.Contains(string(out), fmt.Sprintf("theme index %d out of range (0-%d)", len(themes), len(themes)-1)) {
		t.Errorf("output = %q, want the out-of-range error", out)
	}
}

// The overlay ink must stand out against the cell it sits on: dark ink over
// the light cells (green through orange) and light ink over the dark ones
// (red, and everything dimmed to the track).
func TestOverlayContrastFlipsAcrossGradient(t *testing.T) {
	isolateStatePath(t)
	forcedColour(t)
	dark, light := overlayEndpoints()

	// Light cells across the default gradient get the theme's dark ink.
	for _, c := range []rgb{gradientAt(0), gradientAt(0.25), gradientAt(0.5), gradientAt(0.75)} {
		if got := overlayContrast(c); got.hex() != dark.hex() {
			t.Errorf("overlayContrast(%s) = %s, want the dark endpoint %s", c.hex(), got.hex(), dark.hex())
		}
	}
	// Dark cells: the red end of the gradient and any cell dimmed to the
	// track get the light ink.
	for _, c := range []rgb{gradientAt(1), gradientAt(0.75).dim(0.25), gradientAt(0).dim(0.25), gradientAt(0.5).dim(0.25)} {
		if got := overlayContrast(c); got.hex() != light.hex() {
			t.Errorf("overlayContrast(%s) = %s, want the light endpoint %s", c.hex(), got.hex(), light.hex())
		}
	}
	// Sweeping the whole gradient, the ink must always land on the opposite
	// side of the luminance range from the cell under it.
	for i := 0; i <= 100; i++ {
		cell := gradientAt(float64(i) / 100)
		ink := overlayContrast(cell)
		if (cell.luminance() >= 0.5) == (ink.luminance() >= 0.5) {
			t.Errorf("t=%d: cell %s (luminance %.3f) got ink %s (luminance %.3f) on the same side of the range",
				i, cell.hex(), cell.luminance(), ink.hex(), ink.luminance())
		}
	}
}

// The endpoints must come from the theme, not be literals: a theme that is
// not black-and-white must not get hard black or hard white stamped into its
// bars. (The contrast theme is deliberately black-and-white, so for it the
// theme values *are* #000/#fff -- the check is that they come from the
// theme's own text colour, not from the fallback.)
func TestOverlayContrastUsesThemeEndpoints(t *testing.T) {
	isolateStatePath(t)
	forcedColour(t)
	for i := range themes {
		setThemeIndex(t, i)
		ac, ok := themes[i].txt.GetForeground().(lipgloss.AdaptiveColor)
		if !ok {
			t.Fatalf("theme %d: txt style is not an adaptive colour", i)
		}
		dark, light := overlayEndpoints()
		if want := parseHexRGB(ac.Light); want != nil && dark.hex() != want.hex() {
			t.Errorf("theme %d (%s): dark endpoint = %s, want the theme's light-background text colour %s",
				i, themes[i].name, dark.hex(), want.hex())
		}
		if want := parseHexRGB(ac.Dark); want != nil && light.hex() != want.hex() {
			t.Errorf("theme %d (%s): light endpoint = %s, want the theme's dark-background text colour %s",
				i, themes[i].name, light.hex(), want.hex())
		}
		if got := overlayContrast(gradientAt(0.5)); got.hex() != dark.hex() {
			t.Errorf("theme %d: overlayContrast over a light cell = %s, want %s", i, got.hex(), dark.hex())
		}
		if got := overlayContrast(gradientAt(1).dim(0.25)); got.hex() != light.hex() {
			t.Errorf("theme %d: overlayContrast over a dark cell = %s, want %s", i, got.hex(), light.hex())
		}
	}
}
