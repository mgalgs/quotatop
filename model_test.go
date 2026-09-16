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
	if !m.loadingClaude || !m.loadingCodex {
		t.Fatal("a freshly built model is not marked as loading")
	}

	updated, _ := m.Update(tickMsg(time.Now()))
	m = updated.(model)
	if !m.loadingClaude || !m.loadingCodex {
		t.Error("a tick cleared the in-flight markers")
	}

	updated, _ = m.Update(snapshotMsg{Snapshot{Source: "claude", Title: "CLAUDE"}})
	m = updated.(model)
	if m.loadingClaude {
		t.Error("the Claude fetch is still marked in flight after its result")
	}
	if !m.claudeDue.After(time.Now()) {
		t.Error("the next Claude poll is already due")
	}

	// Not due yet: stays idle.
	updated, _ = m.Update(tickMsg(time.Now()))
	if updated.(model).loadingClaude {
		t.Error("polled again before the interval elapsed")
	}

	// Due: starts again.
	updated, _ = m.Update(tickMsg(m.claudeDue.Add(time.Second)))
	if !updated.(model).loadingClaude {
		t.Error("did not poll once the interval elapsed")
	}
}

func TestSpinnerTicksAreDroppedWhenIdle(t *testing.T) {
	m := newModel(time.Second, loadHistory(""))
	m.loadingClaude, m.loadingCodex = false, false
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
