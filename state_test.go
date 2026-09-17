package main

import (
	"flag"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strings"
	"testing"
	"time"
)

// reCountdown masks the header's refresh countdown ("↻ 20s" and friends),
// the second time-dependent part of a --snapshot frame after the clock.
var reCountdown = regexp.MustCompile("\u21bb [0-9][0-9dhms ]*")

// isolateStatePath points the tool's state file at a fresh per-test file for
// the rest of the test, and returns its path. Without this, go test on a
// developer's machine would read and write their real saved preferences, so
// the suite would behave differently depending on who runs it and what they
// last pressed. Call it from every test that constructs a model or presses a
// key: since the t and l keys persist, a keypress now touches the state file.
func isolateStatePath(t *testing.T) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), "state.json")
	t.Setenv("QUOTATOP_STATE", path)
	return path
}

func writeStateFile(t *testing.T, path, content string) {
	t.Helper()
	if err := os.WriteFile(path, []byte(content), 0o600); err != nil {
		t.Fatal(err)
	}
}

func wantState(theme int, layout string) uiState { return uiState{Theme: theme, Layout: layout} }

func TestStatePathResolution(t *testing.T) {
	// An explicit override wins, same shape as QUOTATOP_HISTORY.
	empty := t.TempDir()
	t.Setenv("QUOTATOP_STATE", "")
	t.Setenv("XDG_STATE_HOME", "")
	t.Setenv("HOME", empty)
	if got, want := statePath(), filepath.Join(empty, ".local", "state", "quotatop", "state.json"); got != want {
		t.Errorf("no overrides: statePath() = %q, want %q", got, want)
	}
	t.Setenv("XDG_STATE_HOME", filepath.Join(empty, "xdg"))
	if got, want := statePath(), filepath.Join(empty, "xdg", "quotatop", "state.json"); got != want {
		t.Errorf("XDG_STATE_HOME: statePath() = %q, want %q", got, want)
	}
	override := filepath.Join(empty, "elsewhere.json")
	t.Setenv("QUOTATOP_STATE", override)
	if got := statePath(); got != override {
		t.Errorf("QUOTATOP_STATE: statePath() = %q, want %q", got, override)
	}
}

// A value the tool wrote must come back unchanged: the whole feature in one
// line.
func TestStateRoundTrip(t *testing.T) {
	path := isolateStatePath(t)
	want := uiState{Theme: 3, Layout: "compact-vertical"}
	saveState(path, want)
	if got := loadState(path); got != want {
		t.Errorf("round trip: got %+v, want %+v", got, want)
	}
}

// No file at all loads the built-in defaults, with no error: the first run
// of the tool always goes through this path.
func TestLoadStateMissingFile(t *testing.T) {
	path := isolateStatePath(t)
	want := wantState(0, layoutFull)
	if got := loadState(path); got != want {
		t.Errorf("missing file: got %+v, want defaults %+v", got, want)
	}
}

// A torn or hand-mangled file loads the built-in defaults, with no error.
func TestLoadStateMalformedJSON(t *testing.T) {
	path := isolateStatePath(t)
	for _, content := range []string{"not json at all", `{"theme": }`, `{"theme": "three"}`} {
		writeStateFile(t, path, content)
		want := wantState(0, layoutFull)
		if got := loadState(path); got != want {
			t.Errorf("content %q: got %+v, want defaults %+v", content, got, want)
		}
	}
}

// A theme index out of range -- written by a version with more themes, or
// hand-edited -- falls back to the default theme. The layout in the same
// file is still honoured: the two values are validated independently.
func TestLoadStateOutOfRangeTheme(t *testing.T) {
	path := isolateStatePath(t)
	for _, content := range []string{`{"theme": 99, "layout": "compact"}`, `{"theme": -1, "layout": "compact"}`} {
		writeStateFile(t, path, content)
		want := wantState(0, layoutCompact)
		if got := loadState(path); got != want {
			t.Errorf("content %q: got %+v, want theme 0 with the valid layout kept", content, got)
		}
	}
}

// A layout name that no longer exists, or never did, falls back to full; the
// theme in the same file is still honoured.
func TestLoadStateUnknownLayout(t *testing.T) {
	path := isolateStatePath(t)
	writeStateFile(t, path, `{"theme": 2, "layout": "grid"}`)
	want := wantState(2, layoutFull)
	if got := loadState(path); got != want {
		t.Errorf("unknown layout: got %+v, want theme 2 with the default layout", got)
	}
}

// An unwritable state path must break nothing: the save fails silently and
// the model still works. The path's parent cannot be created, so the temp
// file for the atomic write cannot be made.
func TestSaveStateUnwritablePathBreaksNothing(t *testing.T) {
	path := filepath.Join(t.TempDir(), "does-not-exist", "sub", "state.json")
	t.Setenv("QUOTATOP_STATE", path)
	isolateAccountEnv(t)
	defer func(prev int) { themeIndex = prev }(themeIndex)

	saveState(path, uiState{Theme: 3, Layout: layoutCompact})
	if _, err := os.Lstat(path); !os.IsNotExist(err) {
		t.Errorf("state file appeared at an unwritable path: %v", err)
	}

	m := newModel(time.Second, loadHistory(""))
	updated, cmd := m.Update(key("t"))
	if cmd != nil {
		t.Errorf("t produced a command, want none")
	}
	if updated.(model).layoutName() != layoutFull {
		t.Errorf("the model state changed after a failed save")
	}
	if themeIndex != 1 {
		t.Errorf("themeIndex = %d after t, want 1: the cycle still happens, only the save is lost", themeIndex)
	}
}

// flagWasSet is what makes --theme 0 usable at all: the flag package cannot
// distinguish "not given" from "given the zero value" by the value alone, so
// without this distinction an explicit --theme 0 would be read as no flag
// and the persisted theme would silently win.
func TestFlagWasSetDistinguishesExplicitZero(t *testing.T) {
	// Given --theme 0: the flag's value equals its default, yet the flag
	// must still count as set, while the ungiven layout must not.
	fs := flag.NewFlagSet("given", flag.ContinueOnError)
	fs.Int("theme", 0, "")
	fs.String("layout", "", "")
	if err := fs.Parse([]string{"--theme", "0"}); err != nil {
		t.Fatal(err)
	}
	if !flagWasSet(fs, "theme") {
		t.Error("--theme 0 not seen as set: an explicit zero would lose to the persisted state")
	}
	if flagWasSet(fs, "layout") {
		t.Error("layout seen as set when it was not given")
	}

	// No flags at all: nothing is set, so the persisted state wins.
	empty := flag.NewFlagSet("absent", flag.ContinueOnError)
	empty.Int("theme", 0, "")
	empty.String("layout", "", "")
	if err := empty.Parse(nil); err != nil {
		t.Fatal(err)
	}
	if flagWasSet(empty, "theme") || flagWasSet(empty, "layout") {
		t.Error("a flag not on the command line was seen as set")
	}
}

// childEnv is a child-process environment that cannot inherit the developer's
// own HOME, codex roots, colour or state overrides: appending overrides to
// a copy of os.Environ() would leave the developer's values first in the
// list, where they would win. HOME is the fake home, CLICOLOR_FORCE forces
// colours so theme differences survive the compare, and QUOTATOP_STATE pins
// the state file.
func childEnv(home, statePath string) []string {
	var env []string
	for _, kv := range os.Environ() {
		if k, _, _ := strings.Cut(kv, "="); k == "HOME" || k == "CODEX_HOME" || k == "NO_COLOR" || k == "CLICOLOR_FORCE" || k == "QUOTATOP_STATE" {
			continue
		}
		env = append(env, kv)
	}
	return append(env, "HOME="+home, "CODEX_HOME=", "NO_COLOR=", "CLICOLOR_FORCE=1", "QUOTATOP_STATE="+statePath)
}

// snapshotRun runs the built binary's --snapshot against one state file
// content and returns the frame, normalizing the two parts that move between
// calls taken a moment apart: the header's wall clock and its refresh
// countdown.
func snapshotRun(t *testing.T, bin, statePath, stateContent string, extraArgs ...string) string {
	t.Helper()
	writeStateFile(t, statePath, stateContent)
	args := append([]string{"--snapshot", "--width", "100", "--no-history"}, extraArgs...)
	cmd := exec.Command(bin, args...)
	cmd.Env = childEnv(filepath.Dir(statePath), statePath)
	out, _ := cmd.CombinedOutput() // fetches fail by design under the fake HOME; the frame still prints
	s := string(out)
	s = reClock.ReplaceAllString(s, "TIME")
	s = reCountdown.ReplaceAllString(s, "DUR")
	if !strings.Contains(s, "QUOTATOP") {
		t.Fatalf("snapshot output has no frame:\n%s", s)
	}
	return s
}

// End to end, the precedence that decides what a --snapshot frame shows:
// an explicitly given flag, then the persisted state, then the built-in
// default. Two runs that differ only in their state file must render
// identically when the same flags are given to both -- the flags shadow the
// state -- and two runs with no flags must differ, with the persisted
// layout named in the footer.
func TestSnapshotFlagsBeatPersistedState(t *testing.T) {
	bin := filepath.Join(t.TempDir(), "quotatop")
	if out, err := exec.Command("go", "build", "-o", bin, ".").CombinedOutput(); err != nil {
		t.Fatalf("go build: %v\n%s", err, out)
	}
	statePath := filepath.Join(t.TempDir(), "state.json")

	theme3Vertical := `{"theme": 3, "layout": "vertical"}`
	theme0Full := `{"theme": 0, "layout": "full"}`

	// Explicit --theme 0 (the zero value: the trap) and --layout compact.
	withFlags := []string{"--theme", "0", "--layout", "compact"}
	a := snapshotRun(t, bin, statePath, theme3Vertical, withFlags...)
	b := snapshotRun(t, bin, statePath, theme0Full, withFlags...)
	if a != b {
		t.Errorf("flags shadowing state failed: the state file changed the frame\nstate A:\n%s\nstate B:\n%s", a, b)
	}
	if !strings.Contains(a, "compact") {
		t.Errorf("flag --layout compact not in the frame:\n%s", a)
	}

	// No flags: the persisted state shows, so the two state files diverge,
	// and the persisted (non-default) layout is named in the footer.
	c := snapshotRun(t, bin, statePath, theme3Vertical)
	d := snapshotRun(t, bin, statePath, theme0Full)
	if c == d {
		t.Errorf("no flags: the persisted state did not reach the frame:\n%s", c)
	}
	if !strings.Contains(c, "vertical") {
		t.Errorf("persisted layout vertical not named in the footer:\n%s", c)
	}
}

// --json renders no frame, so it must neither read nor write the state file.
// It runs every 60 seconds from the tmux status widget in the background, so
// a --json call touching the file would be both pointless and a write
// amplification: a state file that does not exist must not come into
// existence as a side effect of one.
func TestJSONDoesNotCreateStateFile(t *testing.T) {
	bin := filepath.Join(t.TempDir(), "quotatop")
	if out, err := exec.Command("go", "build", "-o", bin, ".").CombinedOutput(); err != nil {
		t.Fatalf("go build: %v\n%s", err, out)
	}
	dir := t.TempDir()
	statePath := filepath.Join(dir, "state.json")
	cmd := exec.Command(bin, "--json", "--no-history")
	cmd.Env = childEnv(dir, statePath)
	out, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("quotatop --json: %v\n%s", err, out)
	}
	if _, err := os.Lstat(statePath); !os.IsNotExist(err) {
		t.Errorf("--json created the state file: %v", err)
	}
}
