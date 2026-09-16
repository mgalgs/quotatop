package main

import (
	"strings"
	"testing"
	"time"

	tea "github.com/charmbracelet/bubbletea"
)

func key(s string) tea.KeyMsg { return tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune(s)} }

func TestQuitKeysReturnQuit(t *testing.T) {
	for _, msg := range []tea.KeyMsg{key("q"), {Type: tea.KeyEsc}, {Type: tea.KeyCtrlC}} {
		_, cmd := newModel(time.Second, loadHistory("")).Update(msg)
		if cmd == nil {
			t.Fatalf("%v returned no command", msg)
		}
		if _, ok := cmd().(tea.QuitMsg); !ok {
			t.Errorf("%v did not quit", msg)
		}
	}
}

func TestQuestionMarkTogglesHelp(t *testing.T) {
	updated, _ := newModel(time.Second, loadHistory("")).Update(key("?"))
	if !updated.(model).showHelp {
		t.Error("? did not open help")
	}
	updated, _ = updated.(model).Update(key("?"))
	if updated.(model).showHelp {
		t.Error("? did not close help")
	}
}

// The model starts in the loading state because Init fires both fetches. A tick
// arriving before those land must not start a second pair.
func TestPollingDoesNotDoubleFetch(t *testing.T) {
	m := newModel(20*time.Second, loadHistory(""))
	if !m.sources[0].loading || !m.sources[1].loading {
		t.Fatal("a freshly built model is not marked as loading")
	}

	updated, _ := m.Update(tickMsg(time.Now()))
	m = updated.(model)
	if !m.sources[0].loading || !m.sources[1].loading {
		t.Error("a tick cleared the in-flight markers")
	}

	updated, _ = m.Update(snapshotMsg{index: 0, snap: Snapshot{Source: "claude", Title: "CLAUDE"}})
	m = updated.(model)
	if m.sources[0].loading {
		t.Error("the Claude fetch is still marked in flight after its result")
	}
	if !m.sources[0].due.After(time.Now()) {
		t.Error("the next Claude poll is already due")
	}

	// Not due yet: stays idle.
	updated, _ = m.Update(tickMsg(time.Now()))
	if updated.(model).sources[0].loading {
		t.Error("polled again before the interval elapsed")
	}

	// Due: starts again.
	updated, _ = m.Update(tickMsg(m.sources[0].due.Add(time.Second)))
	if !updated.(model).sources[0].loading {
		t.Error("did not poll once the interval elapsed")
	}
}

// A snapshotMsg is routed by index, not by Source: two sources sharing the
// same Source string (a future round's two Claude accounts) must still land
// in the slot the message names, and every other slot must stay untouched.
func TestSnapshotMsgRoutesByIndexNotSource(t *testing.T) {
	m := newModel(20*time.Second, loadHistory(""))
	m.sources = []sourceState{
		{loading: true, snap: nil, fetch: m.sources[0].fetch},
		{loading: true, snap: nil, fetch: m.sources[1].fetch},
	}

	updated, _ := m.Update(snapshotMsg{index: 1, snap: Snapshot{Source: "claude", Title: "SECOND"}})
	m = updated.(model)
	if m.sources[0].snap != nil {
		t.Errorf("index 0 was touched by a message for index 1: %+v", m.sources[0].snap)
	}
	if m.sources[1].snap == nil || m.sources[1].snap.Title != "SECOND" {
		t.Errorf("index 1 was not updated: %+v", m.sources[1].snap)
	}
	if m.sources[1].loading {
		t.Error("index 1 still marked loading after its result")
	}
	if !m.sources[0].loading {
		t.Error("index 0's loading flag was disturbed by a message for index 1")
	}
}

// A message naming a slot that does not exist must be ignored, not panic --
// this path will grow more senders as more sources are added.
func TestSnapshotMsgOutOfRangeIndexIsIgnored(t *testing.T) {
	m := newModel(20*time.Second, loadHistory(""))
	before := append([]sourceState(nil), m.sources...)
	updated, cmd := m.Update(snapshotMsg{index: 5, snap: Snapshot{Source: "claude"}})
	after := updated.(model)
	if cmd != nil {
		t.Error("an out-of-range snapshotMsg produced a command")
	}
	if len(after.sources) != len(before) {
		t.Fatalf("sources slice length changed: %d -> %d", len(before), len(after.sources))
	}
	for i := range before {
		if after.sources[i].loading != before[i].loading || after.sources[i].due != before[i].due {
			t.Errorf("source %d changed: %+v -> %+v", i, before[i], after.sources[i])
		}
	}
}

func TestNextRefreshWithNoSourcesReturnsFutureTime(t *testing.T) {
	m := newModel(20*time.Second, loadHistory(""))
	m.now = time.Now()
	m.sources = nil
	next := m.nextRefresh()
	if !next.After(m.now) {
		t.Errorf("nextRefresh() with no sources = %v, want a time after now (%v)", next, m.now)
	}
	if want := m.now.Add(m.interval); next.Before(want.Add(-time.Second)) || next.After(want.Add(time.Second)) {
		t.Errorf("nextRefresh() with no sources = %v, want ~now+interval (%v)", next, want)
	}
}

func TestSpinnerTicksAreDroppedWhenIdle(t *testing.T) {
	m := newModel(time.Second, loadHistory(""))
	m.sources[0].loading, m.sources[1].loading = false, false
	if _, cmd := m.Update(m.spinner.Tick()); cmd != nil {
		t.Error("an idle model kept the spinner ticking, redrawing for nothing")
	}
}

// The longest key list is exactly as wide as the column it sat in, so it used
// to run straight into its description.
func TestHelpRowsKeepAGapAfterTheKeyList(t *testing.T) {
	m := newModel(time.Second, loadHistory(""))
	m.width, m.now, m.showHelp = 100, time.Now(), true
	if view := m.View(); !strings.Contains(view, "q / esc / ctrl-c  quit") {
		t.Error("the quit row has no gap between its keys and its description")
	}
}
