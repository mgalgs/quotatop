package main

import (
	"errors"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/charmbracelet/lipgloss"
	"github.com/muesli/termenv"
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

// The fixtures in testdata/themes/ were captured from the pre-theme code with
// the same construction as here. Theme 0 carries exactly the old values, so
// rendering with it must reproduce them byte for byte: if this test fails,
// the refactor changed output it was not supposed to touch.
func TestThemeZeroMatchesPreChangeFixture(t *testing.T) {
	isolateAccountEnv(t)
	forcedColour(t)
	for _, tc := range []struct {
		name     string
		file     string
		showHelp bool
	}{
		{"frame", "testdata/themes/theme-0.txt", false},
		{"help", "testdata/themes/theme-0-help.txt", true},
	} {
		want, err := os.ReadFile(tc.file)
		if err != nil {
			t.Fatalf("reading %s: %v", tc.file, err)
		}
		if got := themeFixtureModel(tc.showHelp).View(); got != strings.TrimRight(string(want), "\n") {
			t.Errorf("%s: theme-0 render differs from the pre-change fixture:\ngot:\n%s\nwant:\n%s",
				tc.name, got, strings.TrimRight(string(want), "\n"))
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
