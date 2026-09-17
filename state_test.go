package main

import (
	"os"
	"path/filepath"
	"testing"
	"time"
)

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
