package main

import (
	"encoding/json"
	"os"
	"path/filepath"
)

// uiState is the set of user preferences quotatop remembers across runs: the
// theme and the layout. It lives in the tool's own small state file, never in
// the config file, which is hand-written by the user and stays read-only to
// the program.
type uiState struct {
	Theme  int    `json:"theme"`
	Layout string `json:"layout"`
}

// statePath works out which file holds the persisted preferences:
// $QUOTATOP_STATE if set, otherwise $XDG_STATE_HOME/quotatop/state.json,
// otherwise ~/.local/state/quotatop/state.json. That is state, not cache:
// the history and quota caches live under ~/.cache/quotatop because they are
// regenerable, and a preference is not -- clearing the cache must not reset
// someone's theme.
func statePath() string {
	if path := os.Getenv("QUOTATOP_STATE"); path != "" {
		return path
	}
	if xdg := os.Getenv("XDG_STATE_HOME"); xdg != "" {
		return filepath.Join(xdg, "quotatop", "state.json")
	}
	if home, err := os.UserHomeDir(); err == nil {
		return filepath.Join(home, ".local", "state", "quotatop", "state.json")
	}
	return ""
}

// loadState reads the persisted theme and layout. Every failure degrades to
// the built-in defaults, the same way loadHistory degrades to an empty one:
// a monitor must still start when its state file is unreadable or its state
// directory has vanished, and a value a different (or hand-edited) version
// persisted that is no longer valid -- a theme index out of range, a layout
// name that no longer exists -- falls back to the default rather than
// erroring.
func loadState(path string) uiState {
	state := uiState{Theme: 0, Layout: layoutFull}
	data, err := os.ReadFile(path)
	if err != nil {
		return state
	}
	var raw uiState
	if err := json.Unmarshal(data, &raw); err != nil {
		return state
	}
	if raw.Theme >= 0 && raw.Theme < len(themes) {
		state.Theme = raw.Theme
	}
	if name, err := parseLayout(raw.Layout); err == nil {
		state.Layout = name
	}
	return state
}

// saveState persists the theme and layout. The write is atomic -- a temp
// file in the same directory, renamed over the target -- so a crash can
// never leave a truncated state file.
//
// It creates the state directory first, mirroring History.append: on a
// fresh machine nothing else has made ~/.local/state/quotatop (or
// $XDG_STATE_HOME/quotatop), and without this the very first save -- the one
// from the user's first t or l keypress -- would fail and the feature would
// silently never work.
//
// Unlike the history file there is no lock on purpose. History protects
// samples, which are data that concurrent writers would lose; this is a UI
// preference, where two instances cycling at once simply end up
// last-writer-wins, and copying the history locking machinery here would add
// real complexity to protect nothing. Do not "fix" this to match history.
//
// Every failure is silent, exactly as in loadState: an unwritable state
// directory must not stop the monitor -- the preference just does not get
// remembered.
func saveState(path string, state uiState) {
	if path == "" {
		return
	}
	data, err := json.Marshal(state)
	if err != nil {
		return
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return
	}
	tmp, err := os.CreateTemp(filepath.Dir(path), ".state-*.tmp")
	if err != nil {
		return
	}
	name := tmp.Name()
	defer os.Remove(name) // a no-op once the rename below has succeeded
	if _, err := tmp.Write(data); err != nil {
		tmp.Close()
		return
	}
	if err := tmp.Close(); err != nil {
		return
	}
	if err := os.Rename(name, path); err != nil {
		return
	}
}
