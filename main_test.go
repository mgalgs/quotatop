package main

import (
	"encoding/json"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// With no QUOTATOP_CLAUDE_ACCOUNT_* / QUOTATOP_CODEX_ACCOUNT_* settings at
// all, multi-account configuration must be entirely invisible: one Claude
// source, one Codex source, an empty Account, the unchanged panel title, and
// no "account" field anywhere in --json. This is the baseline every other
// multi-account test is a departure from.
func TestDefaultSourcesWithNoAccountsMatchesTodaysBehavior(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("CODEX_HOME", "")
	setConfigValues(t, nil)

	claude := claudeSourceStates()
	if len(claude) != 1 {
		t.Fatalf("claudeSourceStates() has %d entries, want 1", len(claude))
	}
	claudeSnap := claude[0].fetch(true)
	if claudeSnap.Account != "" {
		t.Errorf("claude Account = %q, want empty", claudeSnap.Account)
	}
	if got, want := claudeSnap.Identity(), "claude"; got != want {
		t.Errorf("claude Identity() = %q, want %q", got, want)
	}
	if got, want := claudeSnap.Title, "CLAUDE"; got != want {
		t.Errorf("claude Title = %q, want %q", got, want)
	}

	codex := codexSourceStates()
	if len(codex) != 1 {
		t.Fatalf("codexSourceStates() has %d entries, want 1", len(codex))
	}
	codexSnap := codex[0].fetch(true)
	if codexSnap.Account != "" {
		t.Errorf("codex Account = %q, want empty", codexSnap.Account)
	}
	if got, want := codexSnap.Identity(), "codex"; got != want {
		t.Errorf("codex Identity() = %q, want %q", got, want)
	}
	if got, want := codexSnap.Title, "CODEX"; got != want {
		t.Errorf("codex Title = %q, want %q", got, want)
	}

	raw, err := json.Marshal(encodeJSON([]Snapshot{claudeSnap, codexSnap}, loadHistory(""), time.Now()))
	if err != nil {
		t.Fatalf("json.Marshal: %v", err)
	}
	if strings.Contains(string(raw), `"account"`) {
		t.Errorf("json = %s, want no \"account\" field when no accounts are configured", raw)
	}
}

// Declaring two QUOTATOP_CLAUDE_ACCOUNT_ labels must produce two sources,
// ordered by label ascending, each carrying its own account and Identity().
func TestClaudeSourceStatesTwoAccountsOrderedByLabel(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	setConfigValues(t, nil)
	t.Setenv("QUOTATOP_CLAUDE_ACCOUNT_work", filepath.Join(home, "work.json"))
	t.Setenv("QUOTATOP_CLAUDE_ACCOUNT_personal", filepath.Join(home, "personal.json"))

	states := claudeSourceStates()
	if len(states) != 2 {
		t.Fatalf("claudeSourceStates() has %d entries, want 2", len(states))
	}
	first, second := states[0].fetch(true), states[1].fetch(true)
	if first.Account != "personal" || second.Account != "work" {
		t.Fatalf("accounts = [%q %q], want [personal work] (ascending byte order)", first.Account, second.Account)
	}
	if got, want := first.Identity(), "claude/personal"; got != want {
		t.Errorf("first Identity() = %q, want %q", got, want)
	}
	if got, want := second.Identity(), "claude/work"; got != want {
		t.Errorf("second Identity() = %q, want %q", got, want)
	}
}
