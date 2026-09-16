package main

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/charmbracelet/lipgloss"
)

// everyLineWidth checks that a rendered block is a clean rectangle of the width
// it was asked for. Misaligned borders are the classic TUI regression.
func everyLineWidth(t *testing.T, block string, want int, context string) {
	t.Helper()
	for i, line := range strings.Split(block, "\n") {
		if got := lipgloss.Width(line); got != want {
			t.Errorf("%s: line %d width = %d, want %d: %q", context, i, got, want, line)
		}
	}
}

func TestGaugeIsExactlyWidthCells(t *testing.T) {
	for _, width := range []int{1, 2, 7, 20, 45, 120} {
		for _, pct := range []float64{0, 0.4, 3, 9.7, 50, 99.6, 100, 140, -5} {
			if got := lipgloss.Width(gauge(width, pct)); got != width {
				t.Errorf("gauge(%d, %v) width = %d, want %d", width, pct, got, width)
			}
		}
	}
}

func TestBoxIsRectangular(t *testing.T) {
	body := []string{"short", strings.Repeat("x", 200), ""}
	for _, width := range []int{20, 46, 132} {
		block := box(width, "TITLE", "chip", body, "footer left", "footer right")
		everyLineWidth(t, block, width, "box")
	}
}

func demoSnapshot(now time.Time) *Snapshot {
	return &Snapshot{
		Source: "claude", Title: "CLAUDE", Verb: "fetched", Footnote: "account · cache ≤10m",
		Observed: now.Add(-3 * time.Minute), At: now,
		Windows: []Window{
			{Key: "session", Label: "5-hour", Percent: 9, ResetsAt: now.Add(4 * time.Hour)},
			{Key: "weekly_all", Label: "Weekly", Percent: 41, ResetsAt: now.Add(127 * time.Hour)},
			{Key: "weekly_scoped", Label: "Weekly · Fable", Percent: 28, ResetsAt: now.Add(127 * time.Hour),
				Note: "reset time has passed; awaiting a new report"},
		},
	}
}

func TestPanelIsRectangular(t *testing.T) {
	now := time.Now()
	history := loadHistory("")
	expired := demoSnapshot(now)
	expired.Windows[0].Expired = true
	blocked := demoSnapshot(now)
	blocked.LimitReached = "workspace_member_usage_limit_reached"
	blocked.Warning = "a log is cut off; the reading may be stale"
	cases := map[string]*Snapshot{
		"loading": nil,
		"ok":      demoSnapshot(now),
		"error":   {Source: "codex", Title: "CODEX", Err: errors.New(strings.Repeat("failure ", 12))},
		"expired": expired,
		"blocked": blocked,
	}
	for name, snap := range cases {
		for _, width := range []int{34, 46, 66, 132} {
			everyLineWidth(t, panel(width, snap, history, now, false), width, name)
		}
	}
}

// An expired window must not show a percentage or a filled gauge: the
// reading describes a window that has already reset.
func TestWindowLinesExpiredWindowHidesPercentageAndGauge(t *testing.T) {
	now := time.Now()
	history := loadHistory("")
	window := Window{Key: "session", Label: "5-hour", Percent: 97, Expired: true}
	lines := windowLines(60, "claude", window, history, now)
	if strings.Contains(lines[0], "97%") {
		t.Errorf("heading = %q, want no stale percentage", lines[0])
	}
	if !strings.Contains(lines[0], "—") {
		t.Errorf("heading = %q, want a dash where the percentage goes", lines[0])
	}
	if lipgloss.Width(lines[1]) != lipgloss.Width(gauge(60, 0)) {
		t.Errorf("gauge line width mismatch: %q", lines[1])
	}
	if lines[1] != gauge(60, 0) {
		t.Errorf("gauge = %q, want an empty gauge (as if pct were 0)", lines[1])
	}
	found := false
	for _, line := range lines {
		if strings.Contains(line, "window reset since this reading") {
			found = true
		}
	}
	if !found {
		t.Errorf("lines = %v, want a note explaining the expiry", lines)
	}
}

// An expired window must not extrapolate a burn projection from the very
// percentage it just withheld: the panel would then refuse to state the
// number and immediately forecast from it in the next line.
func TestWindowLinesExpiredWindowSuppressesProjection(t *testing.T) {
	now := time.Now()
	history := loadHistory("")
	history.data["claude/session"] = []sample{{T: now.Add(-time.Hour), Pct: 60}}
	window := Window{Key: "session", Label: "5-hour", Percent: 90, Length: 5 * time.Hour,
		ResetsAt: now.Add(-time.Hour), Expired: true}
	lines := windowLines(60, "claude", window, history, now)
	for _, line := range lines {
		if strings.Contains(line, "%/h") {
			t.Errorf("lines = %v, want no burn projection for an expired window", lines)
		}
	}
}

// The footer's "tightest" readout, the panel title colour and the header
// mark all rank windows by Percent to decide what needs attention; none of
// them may let an expired window's stale percentage win that ranking over a
// live one -- that is the dead-window-reported-as-current bug relocated from
// the bar to the rest of the screen.
func TestTightestIgnoresExpiredWindows(t *testing.T) {
	now := time.Now()
	m := newModel(20*time.Second, loadHistory(""))
	m.now = now
	m.claude = &Snapshot{Source: "claude", Title: "CLAUDE", Observed: now,
		Windows: []Window{{Key: "session", Label: "5-hour", Percent: 97, Expired: true}}}
	m.codex = &Snapshot{Source: "codex", Title: "CODEX", Observed: now,
		Windows: []Window{{Key: "primary", Label: "5-hour", Percent: 10}}}
	name, worst, found := m.tightest()
	if !found || worst != 10 || name != "CODEX 5-hour" {
		t.Errorf("tightest() = (%q, %v, %v), want the live 10%% window, not the expired 97%%", name, worst, found)
	}
}

// A blocked source must say so, in the prominent slot, ahead of an ordinary
// Warning when both are present -- the block is the actionable one.
func TestPanelBlockedSourceWinsOverWarning(t *testing.T) {
	now := time.Now()
	history := loadHistory("")
	snap := demoSnapshot(now)
	snap.LimitReached = "workspace_member_usage_limit_reached"
	snap.Warning = "a log is cut off; the reading may be stale"
	rendered := panel(66, snap, history, now, false)
	if !strings.Contains(rendered, "workspace member usage limit reached") {
		t.Errorf("panel does not humanize/render the block:\n%s", rendered)
	}
	if strings.Contains(rendered, "a log is cut off") {
		t.Errorf("panel rendered the Warning even though a block took the slot:\n%s", rendered)
	}
}

func TestViewFitsTerminalWidth(t *testing.T) {
	now := time.Now()
	for _, width := range []int{60, 94, 100, 200} {
		for _, help := range []bool{false, true} {
			m := newModel(20*time.Second, loadHistory(""))
			m.width, m.now, m.showHelp = width, now, help
			m.claude = demoSnapshot(now)
			m.codex = &Snapshot{Source: "codex", Title: "CODEX", Chip: "team", Verb: "reported",
				Observed: now, Windows: []Window{{Key: "primary", Label: "5-hour", Percent: 13}}}
			for i, line := range strings.Split(m.View(), "\n") {
				if got := lipgloss.Width(line); got > width {
					t.Errorf("width %d help %v: line %d overflows by %d: %q",
						width, help, i, got-width, line)
				}
			}
		}
	}
}

func TestTruncateLeavesNoResetOnPlainText(t *testing.T) {
	if got := truncate("hello world", 5); got != "hello" {
		t.Errorf("truncate plain = %q, want %q", got, "hello")
	}
	styled := lipgloss.NewStyle().Bold(true).Render("hello world")
	if got := truncate(styled, 5); lipgloss.Width(got) != 5 {
		t.Errorf("truncate styled width = %d, want 5", lipgloss.Width(got))
	}
}

func TestHistoryRoundTripsThroughDisk(t *testing.T) {
	path := filepath.Join(t.TempDir(), "history.jsonl")
	now := time.Now()
	writer := loadHistory(path)
	writer.Add("claude/session", now.Add(-time.Hour), 10)
	writer.Add("claude/session", now.Add(-30*time.Minute), 10) // unchanged: not stored
	writer.Add("claude/session", now, 20)

	raw, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("history not written: %v", err)
	}
	if lines := strings.Count(strings.TrimSpace(string(raw)), "\n") + 1; lines != 2 {
		t.Errorf("stored %d lines, want 2 (only changes are kept)", lines)
	}

	reloaded := loadHistory(path)
	if got := reloaded.Trend("claude/session", 20, 10); len(got) != 2 || got[0] != 10 || got[1] != 20 {
		t.Errorf("reloaded trend = %v, want [10 20]", got)
	}
}

func TestHistoryRetainsSampleWhenPersistenceSetupFails(t *testing.T) {
	parent := filepath.Join(t.TempDir(), "not-a-directory")
	if err := os.WriteFile(parent, nil, 0o644); err != nil {
		t.Fatal(err)
	}
	history := loadHistory(filepath.Join(parent, "history.jsonl"))
	history.Add("claude/session", time.Now(), 10)

	if history.enabled {
		t.Fatal("history persistence remained enabled after setup failure")
	}
	if got := history.Trend("claude/session", 10, 10); len(got) != 1 || got[0] != 10 {
		t.Errorf("in-memory trend = %v, want [10]", got)
	}
}

func TestHistoryCompactionMergesConcurrentWriterData(t *testing.T) {
	path := filepath.Join(t.TempDir(), "history.jsonl")
	now := time.Now().Truncate(time.Second)
	var raw strings.Builder
	for i := 0; i < compactAtLines-1; i++ {
		fmt.Fprintf(&raw, `{"t":%d,"k":"claude/session","p":1}`+"\n", now.Add(-time.Duration(compactAtLines-i)*time.Second).Unix())
	}
	if err := os.WriteFile(path, []byte(raw.String()), 0o644); err != nil {
		t.Fatal(err)
	}

	// Both handles were loaded before either writer ran. The first triggers a
	// rewrite; the second must merge that rewrite before appending its sample.
	first, second := loadHistory(path), loadAppendOnlyHistory(path)
	first.Add("claude/session", now, 2)
	second.Add("claude/session", now.Add(time.Second), 3)

	got := loadHistory(path).Trend("claude/session", 3, maxSamplesPerKey+2)
	if n := len(got); n < 2 || got[n-2] != 2 || got[n-1] != 3 {
		t.Fatalf("concurrent writer samples were not preserved: %v", got)
	}
}

func TestProjectUsesOnlyTheCurrentWindow(t *testing.T) {
	now := time.Now()
	history := loadHistory("")
	// A window that ran to 80%, reset, and has since climbed 10 -> 20 in an hour.
	history.Add("claude/session", now.Add(-4*time.Hour), 60)
	history.Add("claude/session", now.Add(-3*time.Hour), 80)
	history.Add("claude/session", now.Add(-time.Hour), 10)
	window := Window{Key: "session", Percent: 20, ResetsAt: now.Add(2 * time.Hour)}
	projection := history.Project("claude", window, now)
	if !projection.Valid || projection.Sustained {
		t.Fatal("short window did not take the live model")
	}
	if projection.RatePerHour < 9 || projection.RatePerHour > 11 {
		t.Errorf("rate = %v%%/h, want ~10 (the drop at reset must not be averaged in)",
			projection.RatePerHour)
	}
	if projection.AtReset < 39 || projection.AtReset > 41 {
		t.Errorf("at reset = %v%%, want ~40", projection.AtReset)
	}
	if !projection.ExhaustAt.IsZero() {
		t.Errorf("exhaust predicted at %v, but the window resets first", projection.ExhaustAt)
	}
}

func TestProjectPredictsExhaustion(t *testing.T) {
	now := time.Now()
	history := loadHistory("")
	history.Add("claude/weekly_all", now.Add(-time.Hour), 40)
	window := Window{Key: "weekly_all", Percent: 50, ResetsAt: now.Add(24 * time.Hour)}
	projection := history.Project("claude", window, now)
	if !projection.Valid || projection.Sustained {
		t.Fatal("a window with no known length must take the live model")
	}
	if projection.ExhaustAt.IsZero() {
		t.Fatal("no exhaustion predicted at 10%/h with 24h to go")
	}
	if hours := projection.ExhaustAt.Sub(now).Hours(); hours < 4.5 || hours > 5.5 {
		t.Errorf("exhaustion in %.1fh, want ~5h", hours)
	}
}

func TestProjectIgnoresTooShortASpan(t *testing.T) {
	now := time.Now()
	history := loadHistory("")
	history.Add("codex/primary", now.Add(-time.Minute), 10)
	window := Window{Key: "primary", Percent: 11, ResetsAt: now.Add(time.Hour)}
	if history.Project("codex", window, now).Valid {
		t.Error("projected a rate from one minute of history")
	}
}

// The headline case: a weekly window 41h into 168h at 41%, with an empty
// history file. The sustained rate is 41/41 = 1.0%/h, so the window is full
// in 59h. This must work with no samples stored.
func TestProjectSustainedWeeklyWindow(t *testing.T) {
	now := time.Now()
	history := loadHistory("")
	window := Window{Key: "weekly_all", Percent: 41, Length: 168 * time.Hour,
		ResetsAt: now.Add(127 * time.Hour)}
	projection := history.Project("claude", window, now)
	if !projection.Valid || !projection.Sustained {
		t.Fatalf("weekly window 41h in did not take the sustained model: %+v", projection)
	}
	if projection.RatePerHour < 0.9 || projection.RatePerHour > 1.1 {
		t.Errorf("sustained rate = %v%%/h, want ~1.0", projection.RatePerHour)
	}
	if projection.AtReset < 165 || projection.AtReset > 171 {
		t.Errorf("at reset = %v%%, want ~168", projection.AtReset)
	}
	if projection.ExhaustAt.IsZero() {
		t.Fatal("no exhaustion predicted")
	}
	if hours := projection.ExhaustAt.Sub(now).Hours(); hours < 58 || hours > 60 {
		t.Errorf("exhaustion in %.1fh, want ~59h", hours)
	}
}

// Six hours into a weekly window the sustained denominator is too small, so
// the live model takes over (it has samples here, so it also becomes valid).
func TestProjectSustainedFallsBackEarlyInWindow(t *testing.T) {
	now := time.Now()
	history := loadHistory("")
	history.Add("claude/weekly_all", now.Add(-2*time.Hour), 30)
	window := Window{Key: "weekly_all", Percent: 41, Length: 168 * time.Hour,
		ResetsAt: now.Add(162 * time.Hour)}
	projection := history.Project("claude", window, now)
	if !projection.Valid {
		t.Fatalf("live model should still project with 2h of history: %+v", projection)
	}
	if projection.Sustained {
		t.Error("6h into the window took the sustained model")
	}
	if projection.RatePerHour < 5.0 || projection.RatePerHour > 6.0 {
		t.Errorf("rate = %v%%/h, want ~5.5 from the recent slope", projection.RatePerHour)
	}
}

// A 5-hour window can never reach the 12h elapsed floor, but the >= 24h rule
// keeps it out of the sustained path at any elapsed time.
func TestProjectShortWindowKeepsLiveModel(t *testing.T) {
	now := time.Now()
	history := loadHistory("")
	history.Add("claude/session", now.Add(-2*time.Hour), 30)
	window := Window{Key: "session", Percent: 40, Length: 5 * time.Hour,
		ResetsAt: now.Add(3 * time.Hour)}
	projection := history.Project("claude", window, now)
	if !projection.Valid || projection.Sustained {
		t.Fatalf("5-hour window did not take the live model: %+v", projection)
	}
	if projection.RatePerHour < 4.5 || projection.RatePerHour > 5.5 {
		t.Errorf("rate = %v%%/h, want ~5 from the recent slope", projection.RatePerHour)
	}
}

// No known length means the live model, never a guessed one.
func TestProjectZeroLengthKeepsLiveModel(t *testing.T) {
	now := time.Now()
	history := loadHistory("")
	history.Add("codex/primary", now.Add(-time.Hour), 40)
	window := Window{Key: "primary", Percent: 50, ResetsAt: now.Add(24 * time.Hour)}
	projection := history.Project("codex", window, now)
	if !projection.Valid || projection.Sustained {
		t.Fatalf("zero-length window did not take the live model: %+v", projection)
	}
	if projection.RatePerHour < 9 || projection.RatePerHour > 11 {
		t.Errorf("rate = %v%%/h, want ~10 from the recent slope", projection.RatePerHour)
	}
}

// A sustained pace that survives to the reset leaves no exhaustion estimate.
func TestProjectSustainedWithoutExhaustion(t *testing.T) {
	now := time.Now()
	window := Window{Key: "weekly_all", Percent: 20, Length: 168 * time.Hour,
		ResetsAt: now.Add(128 * time.Hour)}
	projection := loadHistory("").Project("claude", window, now)
	if !projection.Valid || !projection.Sustained {
		t.Fatalf("expected a sustained projection: %+v", projection)
	}
	if projection.RatePerHour < 0.4 || projection.RatePerHour > 0.6 {
		t.Errorf("sustained rate = %v%%/h, want ~0.5", projection.RatePerHour)
	}
	if projection.AtReset < 80 || projection.AtReset > 88 {
		t.Errorf("at reset = %v%%, want ~84", projection.AtReset)
	}
	if !projection.ExhaustAt.IsZero() {
		t.Errorf("exhaustion predicted at %v although the window resets first", projection.ExhaustAt)
	}
}

func TestProjectionTextLabelsSustainedRate(t *testing.T) {
	now := time.Now()
	sustained := Projection{RatePerHour: 1.0, AtReset: 168, ExhaustAt: now.Add(59 * time.Hour),
		Valid: true, Sustained: true}
	text, _, urgent := projectionText(sustained, now.Add(127*time.Hour), now)
	if !urgent {
		t.Error("sustained exhaustion should render as urgent")
	}
	if !strings.Contains(text, "%/h avg") {
		t.Errorf("sustained rate text = %q, want the 'avg' marker", text)
	}
	live := Projection{RatePerHour: 1.0, AtReset: 60, Valid: true}
	if text, _, _ := projectionText(live, time.Time{}, now); strings.Contains(text, "avg") {
		t.Errorf("live rate text = %q must not carry the 'avg' marker", text)
	}
}

func TestResetTextDegradesToBrief(t *testing.T) {
	now := time.Now()
	week := now.Add(127 * time.Hour)
	if got := resetText(week, now, false); !strings.HasPrefix(got, "resets ") {
		t.Errorf("full reset text = %q", got)
	}
	if got := resetText(week, now, true); got != "in 5d 7h" {
		t.Errorf("brief reset text = %q, want %q", got, "in 5d 7h")
	}
	if got := resetText(time.Time{}, now, false); got != "reset time unknown" {
		t.Errorf("unknown reset = %q", got)
	}
	if got := resetText(now.Add(-time.Minute), now, false); got != "reset due" {
		t.Errorf("past reset = %q", got)
	}
}

// The spare case: the pace would only reach 100% after the reset. FullAt is
// still computed so the gap can be read off it, and ExhaustAt must stay zero
// — it is the signal that the reset wins.
func TestProjectFullAtSpareLeavesExhaustAtZero(t *testing.T) {
	now := time.Now()
	window := Window{Key: "weekly_all", Percent: 20, Length: 168 * time.Hour,
		ResetsAt: now.Add(128 * time.Hour)}
	projection := loadHistory("").Project("claude", window, now)
	if !projection.Valid || !projection.Sustained {
		t.Fatalf("expected a sustained projection: %+v", projection)
	}
	if !projection.ExhaustAt.IsZero() {
		t.Errorf("exhaustion predicted at %v although the window resets first", projection.ExhaustAt)
	}
	if projection.FullAt.IsZero() {
		t.Fatal("FullAt not computed although the pace is positive")
	}
	if !projection.FullAt.After(window.ResetsAt) {
		t.Errorf("FullAt %v does not land after the reset at %v", projection.FullAt, window.ResetsAt)
	}
	// 0.5%/h from 20% needs 160h to reach 100%.
	if hours := projection.FullAt.Sub(now).Hours(); hours < 158 || hours > 162 {
		t.Errorf("full in %.1fh, want ~160h", hours)
	}
}

// The short case: the pace runs dry before the reset. FullAt now always
// carries the crossing time, so ExhaustAt and FullAt must agree when the
// former is set.
func TestProjectFullAtEqualsExhaustAt(t *testing.T) {
	now := time.Now()
	window := Window{Key: "weekly_all", Percent: 41, Length: 168 * time.Hour,
		ResetsAt: now.Add(127 * time.Hour)}
	projection := loadHistory("").Project("claude", window, now)
	if !projection.Valid || !projection.Sustained {
		t.Fatalf("expected a sustained projection: %+v", projection)
	}
	if projection.ExhaustAt.IsZero() {
		t.Fatal("exhaustion should be predicted")
	}
	if !projection.FullAt.Equal(projection.ExhaustAt) {
		t.Errorf("FullAt %v, want it to equal ExhaustAt %v", projection.FullAt, projection.ExhaustAt)
	}
}

func TestFinishProjectionFullAtEdgeCases(t *testing.T) {
	now := time.Now()

	// A flat or falling window has no pace, so no crossing time.
	if p := finishProjection(50, 0, now.Add(24*time.Hour), now); !p.FullAt.IsZero() {
		t.Errorf("FullAt %v at zero rate, want zero", p.FullAt)
	}
	if p := finishProjection(50, -1, now.Add(24*time.Hour), now); !p.FullAt.IsZero() {
		t.Errorf("FullAt %v at negative rate, want zero", p.FullAt)
	}

	// Already full: the crossing is now.
	if p := finishProjection(100, 2, now.Add(24*time.Hour), now); !p.FullAt.Equal(now) {
		t.Errorf("FullAt %v at 100%%, want now", p.FullAt)
	}

	// An unknown reset time leaves nothing to compare against, but the field
	// is still populated: 2%/h from 50% is 25h away.
	if p := finishProjection(50, 2, time.Time{}, now); p.FullAt.IsZero() {
		t.Fatal("FullAt not computed with an unknown reset time")
	} else if hours := p.FullAt.Sub(now).Hours(); hours < 24.5 || hours > 25.5 {
		t.Errorf("full in %.1fh, want ~25h", hours)
	}
	if p := finishProjection(50, 2, time.Time{}, now); !p.ExhaustAt.IsZero() {
		t.Errorf("ExhaustAt %v with an unknown reset, want zero", p.ExhaustAt)
	}
}

func TestProjectionTextGap(t *testing.T) {
	now := time.Now()

	// Short: full in 54h, reset 116h away → 62h = 2d 14h short.
	short := Projection{RatePerHour: 0.9, AtReset: 127, ExhaustAt: now.Add(54 * time.Hour),
		FullAt: now.Add(54 * time.Hour), Valid: true, Sustained: true}
	text, gap, urgent := projectionText(short, now.Add(116*time.Hour), now)
	if !urgent {
		t.Error("short case should render as urgent")
	}
	if gap != " (2d 14h short)" {
		t.Errorf("short gap = %q, want %q", gap, " (2d 14h short)")
	}
	if !strings.Contains(text, "full in 2d 6h") {
		t.Errorf("short headline = %q, want the full-in deadline kept", text)
	}

	// Spare: full in 95h, reset 87h away → 8h spare, headline still at-reset.
	spare := Projection{RatePerHour: 0.5, AtReset: 72, FullAt: now.Add(95 * time.Hour),
		Valid: true, Sustained: true}
	text, gap, urgent = projectionText(spare, now.Add(87*time.Hour), now)
	if urgent {
		t.Error("spare case must not render as urgent")
	}
	if gap != " (8h 00m spare)" {
		t.Errorf("spare gap = %q, want %q", gap, " (8h 00m spare)")
	}
	if !strings.Contains(text, "~72% at reset") || strings.Contains(text, "full in") {
		t.Errorf("spare headline = %q, want ~72%% at reset with no full-in claim", text)
	}

	// No reset deadline to measure against.
	if _, gap, _ = projectionText(short, time.Time{}, now); gap != "" {
		t.Errorf("gap = %q with an unknown reset, want empty", gap)
	}
	// Deadlines a hair apart are noise.
	if _, gap, _ = projectionText(short, short.FullAt.Add(30*time.Second), now); gap != "" {
		t.Errorf("gap = %q with deadlines under a minute apart, want empty", gap)
	}
}

// The truncation ladder: the absolute reset time gives way first, then the
// gap, and the projection survives — so at a width that fits the projection
// but not the gap, windowLines keeps the forecast and drops the gap.
func TestWindowLinesDropsGapToKeepProjection(t *testing.T) {
	now := time.Now()
	window := Window{Key: "weekly_all", Percent: 41, Length: 168 * time.Hour,
		ResetsAt: now.Add(127 * time.Hour)}
	history := loadHistory("")

	// Detail line: the absolute reset time is the first to go when the panel
	// narrows, and the gap is the last kept — so 45 (brief reset + projection
	// is ~37 cells, the same with the gap ~53) must land on the projection
	// without the gap.
	lines := windowLines(45, "claude", window, history, now)
	if len(lines) != 3 {
		t.Fatalf("windowLines produced %d lines, want 3", len(lines))
	}
	if detail := lines[2]; !strings.Contains(detail, "full in") {
		t.Errorf("narrow detail line = %q, want it to keep the projection", detail)
	}
	if detail := lines[2]; strings.Contains(detail, "short") {
		t.Errorf("narrow detail line = %q, want the gap dropped", detail)
	}

	// Wide enough for everything: the gap comes back.
	lines = windowLines(80, "claude", window, history, now)
	if detail := lines[2]; !strings.Contains(detail, "(2d 20h short)") {
		t.Errorf("wide detail line = %q, want the gap rendered", detail)
	}
}
