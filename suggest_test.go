package main

import (
	"encoding/json"
	"errors"
	"testing"
	"time"
)

// weeklyLongWindow builds a weekly_all-shaped window (168h length, the
// claude family's weekly role) whose sustained-model projection is fully
// determined by its own fields -- no history samples needed -- so tests can
// pin exhausts-before-reset exactly by choosing percent, resetsAt and
// length.
func weeklyLongWindow(percent float64, resetsAt time.Time) Window {
	return Window{Key: "weekly_all", Percent: percent, Length: 168 * time.Hour, ResetsAt: resetsAt}
}

func TestEvaluateSourceExclusionReasons(t *testing.T) {
	now := time.Date(2026, 9, 19, 12, 0, 0, 0, time.UTC)
	history := loadHistory("")

	cases := []struct {
		name   string
		snap   Snapshot
		reason string
	}{
		{
			name:   "fetch error",
			snap:   Snapshot{Source: "claude", Err: errors.New("token refresh failed")},
			reason: "error: token refresh failed",
		},
		{
			name:   "no windows at all",
			snap:   Snapshot{Source: "claude", Windows: []Window{}},
			reason: "no windows",
		},
		{
			name: "no weekly-role window",
			snap: Snapshot{Source: "claude", Windows: []Window{
				{Key: "session", Percent: 10},
			}},
			reason: "no weekly window",
		},
		{
			name: "unknown source family maps to no weekly role",
			snap: Snapshot{Source: "mystery", Windows: []Window{
				{Key: "weekly_all", Percent: 10},
			}},
			reason: "no weekly window",
		},
		{
			name: "weekly window at 100%",
			snap: Snapshot{Source: "claude", Windows: []Window{
				weeklyLongWindow(100, now.Add(24*time.Hour)),
			}},
			reason: "weekly at 100%",
		},
		{
			name: "weekly window over 100%",
			snap: Snapshot{Source: "claude", Windows: []Window{
				weeklyLongWindow(140, now.Add(24*time.Hour)),
			}},
			reason: "weekly at 140%",
		},
		{
			name: "session window at 100% and not expired",
			snap: Snapshot{Source: "claude", Windows: []Window{
				weeklyLongWindow(20, now.Add(24*time.Hour)),
				{Key: "session", Percent: 100},
			}},
			reason: "session at 100%",
		},
		{
			name: "limit reached even with percentages well under 100%",
			snap: Snapshot{Source: "codex", LimitReached: "workspace_member_usage_limit_reached", Windows: []Window{
				{Key: "secondary", Percent: 10, Length: 168 * time.Hour, ResetsAt: now.Add(24 * time.Hour)},
				{Key: "primary", Percent: 5},
			}},
			reason: "blocked: workspace_member_usage_limit_reached",
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			_, reason, ok := evaluateSource(tc.snap, history, now)
			if ok {
				t.Fatalf("evaluateSource() ok = true, want excluded with reason %q", tc.reason)
			}
			if reason != tc.reason {
				t.Fatalf("reason = %q, want %q", reason, tc.reason)
			}
		})
	}
}

// TestEvaluateSourceExpiredSessionUnblocks confirms an expired session
// window's stale percent -- even at or above 100% -- does not exclude the
// source, matching the external script's null-session handling: a missing
// or unusable session reading is not itself a block.
func TestEvaluateSourceExpiredSessionUnblocks(t *testing.T) {
	now := time.Date(2026, 9, 19, 12, 0, 0, 0, time.UTC)
	history := loadHistory("")
	snap := Snapshot{Source: "claude", Windows: []Window{
		weeklyLongWindow(20, now.Add(24*time.Hour)),
		{Key: "session", Percent: 100, Expired: true},
	}}

	ranked, reason, ok := evaluateSource(snap, history, now)
	if !ok {
		t.Fatalf("evaluateSource() excluded with reason %q, want routable", reason)
	}
	if ranked.sessionState != sessionResetPending {
		t.Fatalf("sessionState = %v, want %v", ranked.sessionState, sessionResetPending)
	}
	if ranked.sessionPercent != 0 {
		t.Fatalf("sessionPercent = %v, want 0 (expired reading discarded)", ranked.sessionPercent)
	}
}

// TestEvaluateSourceExpiredWeeklyScoresZero confirms an expired weekly
// window's stale percent is scored as 0 for both exclusion and ranking, and
// carries no projection -- a forecast from a discarded reading is worse
// than none.
func TestEvaluateSourceExpiredWeeklyScoresZero(t *testing.T) {
	now := time.Date(2026, 9, 19, 12, 0, 0, 0, time.UTC)
	history := loadHistory("")
	snap := Snapshot{Source: "claude", Windows: []Window{
		{Key: "weekly_all", Percent: 80, Length: 168 * time.Hour, ResetsAt: now.Add(24 * time.Hour), Expired: true},
	}}

	ranked, reason, ok := evaluateSource(snap, history, now)
	if !ok {
		t.Fatalf("evaluateSource() excluded with reason %q, want routable", reason)
	}
	if ranked.weeklyPercent != 0 {
		t.Fatalf("weeklyPercent = %v, want 0", ranked.weeklyPercent)
	}
	if ranked.weeklyProjectionValid {
		t.Fatalf("weeklyProjectionValid = true, want false for an expired window")
	}
	if ranked.weeklyExhaustsBeforeReset {
		t.Fatalf("weeklyExhaustsBeforeReset = true, want false for an expired window")
	}
}

// TestEvaluateSourceMissingSessionWindowIsRoutable confirms a family whose
// weekly-role window is present but whose session-role window is entirely
// absent (Codex's primary is nil, say) is still routable, scored as
// sessionAbsent rather than excluded.
func TestEvaluateSourceMissingSessionWindowIsRoutable(t *testing.T) {
	now := time.Date(2026, 9, 19, 12, 0, 0, 0, time.UTC)
	history := loadHistory("")
	snap := Snapshot{Source: "codex", Windows: []Window{
		{Key: "secondary", Percent: 20, Length: 168 * time.Hour, ResetsAt: now.Add(24 * time.Hour)},
	}}

	ranked, reason, ok := evaluateSource(snap, history, now)
	if !ok {
		t.Fatalf("evaluateSource() excluded with reason %q, want routable", reason)
	}
	if ranked.sessionState != sessionAbsent {
		t.Fatalf("sessionState = %v, want %v", ranked.sessionState, sessionAbsent)
	}
}

// TestEvaluateSourceInvalidProjectionIsNoProjection confirms a short window
// with no history behind it (so Project cannot fit a rate) reports
// weeklyProjectionValid=false and weeklyExhaustsBeforeReset=false -- an
// absent projection counts as not-exhausting, matching the external script.
func TestEvaluateSourceInvalidProjectionIsNoProjection(t *testing.T) {
	now := time.Date(2026, 9, 19, 12, 0, 0, 0, time.UTC)
	history := loadHistory("")
	// Length 0 keeps the sustained model out (it needs >=24h) and an empty
	// history leaves the live model with a single point, which Project
	// treats as insufficient to fit a rate.
	snap := Snapshot{Source: "claude", Windows: []Window{
		{Key: "weekly_all", Percent: 30, ResetsAt: now.Add(24 * time.Hour)},
	}}

	ranked, reason, ok := evaluateSource(snap, history, now)
	if !ok {
		t.Fatalf("evaluateSource() excluded with reason %q, want routable", reason)
	}
	if ranked.weeklyProjectionValid {
		t.Fatalf("weeklyProjectionValid = true, want false with no history to fit a rate")
	}
	if ranked.weeklyExhaustsBeforeReset {
		t.Fatalf("weeklyExhaustsBeforeReset = true, want false for an invalid projection")
	}
}

func TestBuildSuggestionExhaustsBeforeResetOutranksLowerPercent(t *testing.T) {
	now := time.Date(2026, 9, 19, 12, 0, 0, 0, time.UTC)
	history := loadHistory("")

	// holds: current 95%, but only 1h left in a 168h window it is 167h into
	// -- the residual rate is too low to reach 100% before reset.
	holds := Snapshot{Source: "claude", Account: "holds", Windows: []Window{
		weeklyLongWindow(95, now.Add(1*time.Hour)),
	}}
	// exhausts: current only 10%, but 156h still left in a window it is
	// just 12h into -- that pace reaches 100% long before reset.
	exhausts := Snapshot{Source: "claude", Account: "exhausts", Windows: []Window{
		weeklyLongWindow(10, now.Add(156*time.Hour)),
	}}

	sugg := buildSuggestion([]Snapshot{exhausts, holds}, history, now)
	if len(sugg.ranked) != 2 {
		t.Fatalf("ranked = %d entries, want 2", len(sugg.ranked))
	}
	if !sugg.ranked[1].weeklyExhaustsBeforeReset {
		t.Fatalf("precondition failed: %q should exhaust before reset", sugg.ranked[1].account)
	}
	if sugg.ranked[0].weeklyExhaustsBeforeReset {
		t.Fatalf("precondition failed: %q should hold to reset", sugg.ranked[0].account)
	}
	if sugg.ranked[0].account != "holds" {
		t.Fatalf("ranked[0].account = %q, want %q (holds-to-reset beats a lower percent that exhausts)", sugg.ranked[0].account, "holds")
	}
	if sugg.ranked[1].account != "exhausts" {
		t.Fatalf("ranked[1].account = %q, want %q", sugg.ranked[1].account, "exhausts")
	}
}

func TestBuildSuggestionPercentTiebreak(t *testing.T) {
	now := time.Date(2026, 9, 19, 12, 0, 0, 0, time.UTC)
	history := loadHistory("")

	high := Snapshot{Source: "claude", Account: "high", Windows: []Window{{Key: "weekly_all", Percent: 50}}}
	low := Snapshot{Source: "claude", Account: "low", Windows: []Window{{Key: "weekly_all", Percent: 30}}}

	sugg := buildSuggestion([]Snapshot{high, low}, history, now)
	if len(sugg.ranked) != 2 || sugg.ranked[0].account != "low" || sugg.ranked[1].account != "high" {
		t.Fatalf("ranked = %+v, want low then high", sugg.ranked)
	}
}

func TestBuildSuggestionStableOrderTiebreak(t *testing.T) {
	now := time.Date(2026, 9, 19, 12, 0, 0, 0, time.UTC)
	history := loadHistory("")

	first := Snapshot{Source: "claude", Account: "first", Windows: []Window{{Key: "weekly_all", Percent: 40}}}
	second := Snapshot{Source: "claude", Account: "second", Windows: []Window{{Key: "weekly_all", Percent: 40}}}

	sugg := buildSuggestion([]Snapshot{first, second}, history, now)
	if len(sugg.ranked) != 2 || sugg.ranked[0].account != "first" || sugg.ranked[1].account != "second" {
		t.Fatalf("ranked = %+v, want defaultSources() order preserved (first, second)", sugg.ranked)
	}
}

func TestRenderSuggestTextFrozenFormat(t *testing.T) {
	sugg := suggestion{
		ranked: []rankedSource{
			{
				source: "claude", account: "mitch", weeklyPercent: 35,
				weeklyProjectionValid: true, weeklyExhaustsBeforeReset: false,
				sessionState: sessionOK, sessionPercent: 9,
			},
			{
				source: "codex", account: "plus", weeklyPercent: 39,
				weeklyProjectionValid: true, weeklyExhaustsBeforeReset: false,
				sessionState: sessionResetPending,
			},
		},
		excluded: []excludedSource{
			{source: "claude", account: "work", reason: "error: token refresh failed"},
		},
	}

	want := "pick: claude/mitch weekly=35% holds-to-reset session=9%\n" +
		"  2. codex/plus weekly=39% holds-to-reset session=reset-pending\n" +
		"excluded: claude/work — error: token refresh failed\n"

	if got := renderSuggestText(sugg); got != want {
		t.Fatalf("renderSuggestText() =\n%q\nwant\n%q", got, want)
	}
}

func TestRenderSuggestTextAgeSuffixAndNoSession(t *testing.T) {
	sugg := suggestion{
		ranked: []rankedSource{
			{
				source: "codex", weeklyPercent: 12,
				weeklyProjectionValid: false, weeklyExhaustsBeforeReset: false,
				sessionState: sessionAbsent, observedAgeSeconds: 2*3600 + 1800,
			},
		},
	}

	want := "pick: codex weekly=12% no-projection session=- [reading 2h old]\n"
	if got := renderSuggestText(sugg); got != want {
		t.Fatalf("renderSuggestText() = %q, want %q", got, want)
	}
}

func TestEncodeSuggestJSONFields(t *testing.T) {
	now := time.Date(2026, 9, 19, 12, 0, 0, 0, time.UTC)
	sessionPct := 9.0
	sugg := suggestion{
		ranked: []rankedSource{
			{
				source: "claude", account: "mitch", credentialsPath: "/home/mitch/.claude/.credentials.json",
				weeklyPercent: 35, weeklyWindowKey: "weekly_all",
				weeklyProjectionValid: true, weeklyExhaustsBeforeReset: false,
				sessionState: sessionOK, sessionPercent: sessionPct,
				observedAgeSeconds: 153,
			},
			{
				source: "codex", weeklyPercent: 39, weeklyWindowKey: "secondary",
				weeklyProjectionValid: false, weeklyExhaustsBeforeReset: true,
				sessionState: sessionAbsent,
			},
		},
		excluded: []excludedSource{
			{source: "claude", account: "work", reason: "error: token refresh failed"},
		},
	}

	doc := encodeSuggestJSON(sugg, now)
	raw, err := json.Marshal(doc)
	if err != nil {
		t.Fatal(err)
	}
	var decoded map[string]any
	if err := json.Unmarshal(raw, &decoded); err != nil {
		t.Fatal(err)
	}

	if decoded["schema"] != float64(1) {
		t.Fatalf("schema = %v", decoded["schema"])
	}
	pick := decoded["pick"].(map[string]any)
	if pick["source"] != "claude" || pick["account"] != "mitch" || pick["rank"] != float64(1) {
		t.Fatalf("pick = %+v", pick)
	}
	if pick["credentials_path"] != "/home/mitch/.claude/.credentials.json" {
		t.Fatalf("pick.credentials_path = %v", pick["credentials_path"])
	}
	if pick["session_percent"] != 9.0 || pick["session_state"] != "ok" {
		t.Fatalf("pick session fields = %v / %v", pick["session_percent"], pick["session_state"])
	}
	if pick["observed_age_seconds"] != float64(153) {
		t.Fatalf("pick.observed_age_seconds = %v", pick["observed_age_seconds"])
	}

	ranked := decoded["ranked"].([]any)
	if len(ranked) != 2 {
		t.Fatalf("ranked = %d entries, want 2", len(ranked))
	}
	second := ranked[1].(map[string]any)
	if second["rank"] != float64(2) || second["weekly_exhausts_before_reset"] != true {
		t.Fatalf("ranked[1] = %+v", second)
	}
	if _, present := second["credentials_path"]; present {
		t.Fatalf("ranked[1].credentials_path should be omitted for a source with none, got %v", second["credentials_path"])
	}
	if _, present := second["account"]; present {
		t.Fatalf("ranked[1].account should be omitted when empty, got %v", second["account"])
	}
	if second["session_percent"] != nil {
		t.Fatalf("ranked[1].session_percent = %v, want null (no session window)", second["session_percent"])
	}

	excluded := decoded["excluded"].([]any)
	if len(excluded) != 1 {
		t.Fatalf("excluded = %d entries, want 1", len(excluded))
	}
	exc := excluded[0].(map[string]any)
	if exc["source"] != "claude" || exc["account"] != "work" || exc["reason"] != "error: token refresh failed" {
		t.Fatalf("excluded[0] = %+v", exc)
	}
}

func TestEncodeSuggestJSONNothingRoutable(t *testing.T) {
	now := time.Date(2026, 9, 19, 12, 0, 0, 0, time.UTC)
	sugg := suggestion{excluded: []excludedSource{{source: "claude", reason: "no windows"}}}

	doc := encodeSuggestJSON(sugg, now)
	if doc.Pick != nil {
		t.Fatalf("Pick = %+v, want nil when nothing is routable", doc.Pick)
	}
	if doc.Ranked == nil || len(doc.Ranked) != 0 {
		t.Fatalf("Ranked = %+v, want an empty (non-nil) slice", doc.Ranked)
	}

	raw, err := json.Marshal(doc)
	if err != nil {
		t.Fatal(err)
	}
	var decoded map[string]any
	if err := json.Unmarshal(raw, &decoded); err != nil {
		t.Fatal(err)
	}
	if decoded["pick"] != nil {
		t.Fatalf(`decoded["pick"] = %v, want null`, decoded["pick"])
	}
	if ranked, ok := decoded["ranked"].([]any); !ok || len(ranked) != 0 {
		t.Fatalf(`decoded["ranked"] = %v, want []`, decoded["ranked"])
	}

	if code := suggestExitCode(sugg); code != 2 {
		t.Fatalf("suggestExitCode() = %d, want 2", code)
	}
}

func TestSuggestExitCode(t *testing.T) {
	if code := suggestExitCode(suggestion{ranked: []rankedSource{{source: "claude"}}}); code != 0 {
		t.Fatalf("suggestExitCode() = %d, want 0 with a routable source", code)
	}
	if code := suggestExitCode(suggestion{}); code != 2 {
		t.Fatalf("suggestExitCode() = %d, want 2 with nothing routable", code)
	}
}
