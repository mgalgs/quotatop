package main

import (
	"encoding/json"
	"os"
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
	isolateAccountEnv(t)

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
	isolateAccountEnv(t, "QUOTATOP_CLAUDE_ACCOUNT_work", "QUOTATOP_CLAUDE_ACCOUNT_personal")

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

// Each QUOTATOP_CLAUDE_ACCOUNT_ source must read its own credentialsPath, not
// a shared one: personal's file exists but carries no token, work's file is
// missing outright, so the two fail with different, file-specific errors --
// proof each source actually read the file its own label points at.
func TestClaudeSourceStatesReadEachAccountsOwnCredentials(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	setConfigValues(t, nil)
	personalPath := filepath.Join(home, "personal.json")
	if err := os.WriteFile(personalPath, []byte(`{"claudeAiOauth":{}}`), 0o600); err != nil {
		t.Fatal(err)
	}
	t.Setenv("QUOTATOP_CLAUDE_ACCOUNT_personal", personalPath)
	t.Setenv("QUOTATOP_CLAUDE_ACCOUNT_work", filepath.Join(home, "work.json"))
	isolateAccountEnv(t, "QUOTATOP_CLAUDE_ACCOUNT_personal", "QUOTATOP_CLAUDE_ACCOUNT_work")

	states := claudeSourceStates()
	if len(states) != 2 {
		t.Fatalf("claudeSourceStates() has %d entries, want 2", len(states))
	}
	personal, work := states[0].fetch(true), states[1].fetch(true)
	if personal.Err == nil || !strings.Contains(personal.Err.Error(), "no Claude token") {
		t.Errorf("personal Err = %v, want the no-token error from its own file", personal.Err)
	}
	if work.Err == nil || !strings.Contains(work.Err.Error(), "no credentials file") {
		t.Errorf("work Err = %v, want the missing-file error from its own path", work.Err)
	}
}

// Declaring two QUOTATOP_CODEX_ACCOUNT_ labels must produce two sources,
// ordered by label ascending, each walking only its own root -- proof each
// source's extraRoots came from its own label's value, not a shared one.
func TestCodexSourceStatesTwoAccountsOrderedByLabel(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("CODEX_HOME", "")
	setConfigValues(t, nil)

	alphaDir, betaDir := t.TempDir(), t.TempDir()
	writeSessionFile(t, alphaDir, "a.jsonl",
		codexLine("2026-03-01T10:00:00Z", `"codex"`, `alpha-plan`, `{"used_percent":10,"window_minutes":300}`, ``)+"\n")
	writeSessionFile(t, betaDir, "b.jsonl",
		codexLine("2026-03-01T10:00:00Z", `"codex"`, `beta-plan`, `{"used_percent":20,"window_minutes":300}`, ``)+"\n")
	t.Setenv("QUOTATOP_CODEX_ACCOUNT_alpha", alphaDir)
	t.Setenv("QUOTATOP_CODEX_ACCOUNT_beta", betaDir)
	isolateAccountEnv(t, "QUOTATOP_CODEX_ACCOUNT_alpha", "QUOTATOP_CODEX_ACCOUNT_beta")

	states := codexSourceStates()
	if len(states) != 2 {
		t.Fatalf("codexSourceStates() has %d entries, want 2", len(states))
	}
	first, second := states[0].fetch(true), states[1].fetch(true)
	if first.Account != "alpha" || second.Account != "beta" {
		t.Fatalf("accounts = [%q %q], want [alpha beta] (ascending byte order)", first.Account, second.Account)
	}
	if first.Chip != "alpha-plan" {
		t.Errorf("alpha Chip = %q, want its own root's plan, not beta's", first.Chip)
	}
	if second.Chip != "beta-plan" {
		t.Errorf("beta Chip = %q, want its own root's plan, not alpha's", second.Chip)
	}
}

// A QUOTATOP_CODEX_ACCOUNT_ glob must be re-expanded on every fetch, the same
// way the unnamed QUOTATOP_CODEX_ROOTS path re-globs via defaultCodexSource()
// on every refresh: a sandbox/agent run directory the pattern matches can
// appear after the source was built, and a long-running TUI has to see it on
// the next tick rather than needing a restart.
func TestCodexSourceStatesReglobsRootsOnEachFetch(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("CODEX_HOME", "")
	setConfigValues(t, nil)

	base := t.TempDir()
	pattern := filepath.Join(base, "runs", "*", "sessions")
	t.Setenv("QUOTATOP_CODEX_ACCOUNT_work", pattern)
	isolateAccountEnv(t, "QUOTATOP_CODEX_ACCOUNT_work")

	states := codexSourceStates()
	if len(states) != 1 {
		t.Fatalf("codexSourceStates() has %d entries, want 1", len(states))
	}
	source := states[0]

	first := source.fetch(true)
	if first.Err == nil {
		t.Fatalf("fetch before the run directory exists: err = nil, want an error")
	}

	sessDir := filepath.Join(base, "runs", "r1", "sessions")
	writeSessionFile(t, sessDir, "s.jsonl",
		codexLine("2026-03-01T10:00:00Z", `"codex"`, `plus`, `{"used_percent":42,"window_minutes":300}`, ``)+"\n")

	second := source.fetch(true)
	if second.Err != nil {
		t.Fatalf("fetch after the run directory appeared: err = %v, want a snapshot", second.Err)
	}
	if len(second.Windows) != 1 || second.Windows[0].Percent != 42 {
		t.Errorf("windows = %+v, want the newly appeared session's reading", second.Windows)
	}
}
