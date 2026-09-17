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
