package main

import (
	"strings"
	"testing"
	"time"

	tea "github.com/charmbracelet/bubbletea"
)

func key(s string) tea.KeyMsg { return tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune(s)} }

func TestQuitKeysReturnQuit(t *testing.T) {
	isolateAccountEnv(t)
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
	isolateAccountEnv(t)
	updated, _ := newModel(time.Second, loadHistory("")).Update(key("?"))
	if !updated.(model).showHelp {
		t.Error("? did not open help")
	}
	updated, _ = updated.(model).Update(key("?"))
	if updated.(model).showHelp {
		t.Error("? did not close help")
	}
}

// l advances the layout one step and wraps at the end: full → compact →
// vertical → full. The zero-value model starts on the default, full.
func TestLayoutKeyCyclesAndWraps(t *testing.T) {
	isolateAccountEnv(t)
	m := newModel(time.Second, loadHistory(""))
	for i, want := range []string{layoutCompact, layoutVertical, layoutFull, layoutCompact} {
		updated, cmd := m.Update(key("l"))
		m = updated.(model)
		if cmd != nil {
			t.Errorf("press %d: l produced a command, want none (no fetch is started)", i)
		}
		if got := m.layoutName(); got != want {
			t.Fatalf("press %d: layout = %q, want %q (cycle full → compact → vertical → full)", i, got, want)
		}
	}
}

// t advances the theme one step and wraps at the end: pressing it
// len(themes) times lands back where it started, and like l it starts no
// fetch.
func TestThemeKeyCyclesAndWraps(t *testing.T) {
	isolateAccountEnv(t)
	defer func(prev int) { themeIndex = prev }(themeIndex)
	m := newModel(time.Second, loadHistory(""))
	for i := 1; i <= len(themes); i++ {
		updated, cmd := m.Update(key("t"))
		m = updated.(model)
		if cmd != nil {
			t.Errorf("press %d: t produced a command, want none (no fetch is started)", i)
		}
		if themeIndex != i%len(themes) {
			t.Fatalf("press %d: themeIndex = %d, want %d (cycle wraps at the end)", i, themeIndex, i%len(themes))
		}
	}
}

// The footer must name the layout the user just switched to, and hint at the
// key; the default stays unannounced so full's footer keeps today's content.
func TestFooterNamesTheCurrentLayout(t *testing.T) {
	isolateAccountEnv(t)
	now := time.Now()
	m := newModel(time.Second, loadHistory(""))
	m.now, m.sources = now, gridSources(now, 1)

	footer := m.footerLine(100)
	if !strings.Contains(footer, "layout") {
		t.Errorf("footer has no hint for the l key: %q", footer)
	}
	if strings.Contains(footer, " full") {
		t.Errorf("footer shows the default layout's name, want it hidden: %q", footer)
	}
	m.layout = layoutCompact
	if footer = m.footerLine(100); !strings.Contains(footer, "compact") {
		t.Errorf("footer does not name the current layout: %q", footer)
	}
	m.layout = layoutVertical
	if footer = m.footerLine(100); !strings.Contains(footer, "vertical") {
		t.Errorf("footer does not name the current layout: %q", footer)
	}
}

func TestHelpBodyListsTheLayoutKey(t *testing.T) {
	isolateAccountEnv(t)
	m := newModel(time.Second, loadHistory(""))
	m.now = time.Now()
	body := m.helpBody(100)
	if !strings.Contains(body, "cycle layout: full, compact, vertical") {
		t.Errorf("help body is missing the l key: %s", body)
	}
}

// The model starts in the loading state because Init fires both fetches. A tick
// arriving before those land must not start a second pair.
func TestPollingDoesNotDoubleFetch(t *testing.T) {
	isolateAccountEnv(t)
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
	isolateAccountEnv(t)
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
	isolateAccountEnv(t)
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
	isolateAccountEnv(t)
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
	isolateAccountEnv(t)
	m := newModel(time.Second, loadHistory(""))
	m.sources[0].loading, m.sources[1].loading = false, false
	if _, cmd := m.Update(m.spinner.Tick()); cmd != nil {
		t.Error("an idle model kept the spinner ticking, redrawing for nothing")
	}
}

// The longest key list is exactly as wide as the column it sat in, so it used
// to run straight into its description.
func TestHelpRowsKeepAGapAfterTheKeyList(t *testing.T) {
	isolateAccountEnv(t)
	m := newModel(time.Second, loadHistory(""))
	m.width, m.now, m.showHelp = 100, time.Now(), true
	if view := m.View(); !strings.Contains(view, "q / esc / ctrl-c  quit") {
		t.Error("the quit row has no gap between its keys and its description")
	}
}
