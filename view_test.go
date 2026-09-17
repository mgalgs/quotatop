package main

import (
	"errors"
	"fmt"
	"math"
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

// --layout accepts the known names, defaults the empty string to full, and
// rejects a typo with a message that names the bad value and the valid set --
// a typo in a test must fail loudly, not fall back.
func TestParseLayout(t *testing.T) {
	for _, name := range []string{"", layoutFull, layoutCompact, layoutVertical} {
		want := name
		if want == "" {
			want = layoutFull
		}
		got, err := parseLayout(name)
		if err != nil {
			t.Errorf("parseLayout(%q) error = %v, want nil", name, err)
		} else if got != want {
			t.Errorf("parseLayout(%q) = %q, want %q", name, got, want)
		}
	}
	for _, name := range []string{"grid", "Full", "COMPACT", "full "} {
		if got, err := parseLayout(name); err == nil {
			t.Errorf("parseLayout(%q) = %q, want an error", name, got)
		} else if !strings.Contains(err.Error(), `"`+name+`"`) {
			t.Errorf("parseLayout(%q) error = %v, want the offending name quoted", name, err)
		} else if !strings.Contains(err.Error(), "full, compact, vertical") {
			t.Errorf("parseLayout(%q) error = %v, want the valid names listed", name, err)
		}
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
	m.sources[0].snap = &Snapshot{Source: "claude", Title: "CLAUDE", Observed: now,
		Windows: []Window{{Key: "session", Label: "5-hour", Percent: 97, Expired: true}}}
	m.sources[1].snap = &Snapshot{Source: "codex", Title: "CODEX", Observed: now,
		Windows: []Window{{Key: "primary", Label: "5-hour", Percent: 10}}}
	name, worst, found := m.tightest()
	if !found || worst != 10 || name != "CODEX 5-hour" {
		t.Errorf("tightest() = (%q, %v, %v), want the live 10%% window, not the expired 97%%", name, worst, found)
	}
}

// tightest() must name the account, not just the source, when two panels
// share a source: the header's whole point is to say which panel needs
// attention, and "CLAUDE 5-hour" is ambiguous the moment there are two.
func TestTightestNamesTheRightAccount(t *testing.T) {
	now := time.Now()
	m := newModel(20*time.Second, loadHistory(""))
	m.now = now
	m.sources = []sourceState{
		{snap: &Snapshot{Source: "claude", Account: "work", Title: "CLAUDE · work", Observed: now,
			Windows: []Window{{Key: "session", Label: "5-hour", Percent: 90}}}},
		{snap: &Snapshot{Source: "claude", Account: "personal", Title: "CLAUDE · personal", Observed: now,
			Windows: []Window{{Key: "session", Label: "5-hour", Percent: 10}}}},
	}
	name, worst, found := m.tightest()
	if !found || worst != 90 || name != "CLAUDE · work 5-hour" {
		t.Errorf("tightest() = (%q, %v, %v), want (%q, 90, true)", name, worst, found, "CLAUDE · work 5-hour")
	}
}

// A blocked source must say so, in the prominent slot, ahead of an ordinary
// Warning when both are present -- the block is the actionable one.
func TestPanelBlockedSourceAlsoShowsWarning(t *testing.T) {
	now := time.Now()
	history := loadHistory("")
	snap := demoSnapshot(now)
	snap.LimitReached = "workspace_member_usage_limit_reached"
	snap.Warning = "a log is cut off; the reading may be stale"
	rendered := panel(66, snap, history, now, false)
	if !strings.Contains(rendered, "workspace member usage limit reached") {
		t.Errorf("panel does not humanize/render the block:\n%s", rendered)
	}
	if !strings.Contains(rendered, "a log is cut off") {
		t.Errorf("panel dropped the Warning even though a block is also present:\n%s", rendered)
	}
}

func TestViewFitsTerminalWidth(t *testing.T) {
	now := time.Now()
	for _, width := range []int{60, 94, 100, 200} {
		for _, help := range []bool{false, true} {
			m := newModel(20*time.Second, loadHistory(""))
			m.width, m.now, m.showHelp = width, now, help
			m.sources[0].snap = demoSnapshot(now)
			m.sources[1].snap = &Snapshot{Source: "codex", Title: "CODEX", Chip: "team", Verb: "reported",
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

func TestGridColumnsAndRowWidthsMatchOldTwoPanelLadder(t *testing.T) {
	for _, width := range []int{94, 132} {
		if cols := gridColumns(width, 2); cols != 2 {
			t.Errorf("gridColumns(%d, 2) = %d, want 2", width, cols)
		}
		half := (width - panelGap) / 2
		other := width - panelGap - half
		widths := rowWidths(width, 2)
		if len(widths) != 2 || widths[0] != half || widths[1] != other {
			t.Errorf("rowWidths(%d, 2) = %v, want [%d %d]", width, widths, half, other)
		}
	}

	width := 60
	if cols := gridColumns(width, 2); cols != 1 {
		t.Errorf("gridColumns(%d, 2) = %d, want 1", width, cols)
	}
	widths := rowWidths(width, 1)
	if len(widths) != 1 || widths[0] != width {
		t.Errorf("rowWidths(%d, 1) = %v, want [%d]", width, widths, width)
	}
}

// gridSources builds n sourceStates with distinct fixed snapshots, for tests
// that need View() itself to render a specific panel count.
func gridSources(now time.Time, n int) []sourceState {
	sources := make([]sourceState, n)
	for i := range sources {
		sources[i] = sourceState{snap: &Snapshot{
			Source: "claude", Title: fmt.Sprintf("S%d", i), Observed: now,
			Windows: []Window{{Key: "session", Label: "5-hour", Percent: 10}},
		}}
	}
	return sources
}

// threeWindowSources builds n sources, each with the three windows a Claude
// account has, carrying distinct percentages so a test can assert every
// window's reading survived a layout.
func threeWindowSources(now time.Time, n int) []sourceState {
	perSource := [][3]float64{{7, 33, 58}, {12, 45, 61}, {3, 24, 77}, {18, 40, 66}, {26, 52, 81}}
	labels := []string{"5-hour", "Weekly", "Weekly · Fable"}
	sources := make([]sourceState, n)
	for i := range sources {
		windows := make([]Window, 3)
		for j := range windows {
			windows[j] = Window{Key: fmt.Sprintf("w%d", j), Label: labels[j],
				Percent: perSource[i%len(perSource)][j], ResetsAt: now.Add(24 * time.Hour)}
		}
		sources[i] = sourceState{snap: &Snapshot{Source: "claude", Title: fmt.Sprintf("S%d", i),
			Observed: now, Windows: windows}}
	}
	return sources
}

// The invariants every layout must hold at any size: at most m.height lines
// (when height > 0), and no line wider than the terminal. These are checked
// across a table of sizes rather than one magic geometry: the invariant is
// what matters, and a test that only checks 80x24 passes while the layout
// breaks at 81x25.
func TestViewFitsHeightAndWidthInEveryLayout(t *testing.T) {
	now := time.Now()
	for _, layout := range layouts {
		for _, count := range []int{3, 5} {
			for _, help := range []bool{false, true} {
				for _, size := range []struct{ width, height int }{
					{80, 24}, {96, 30}, {100, 28}, {120, 40}, {132, 50},
					{60, 24}, {132, 24}, {132, 10}, {100, 8}, {80, 1},
				} {
					m := newModel(20*time.Second, loadHistory(""))
					m.width, m.height, m.now, m.layout, m.showHelp = size.width, size.height, now, layout, help
					m.sources = threeWindowSources(now, count)
					lines := strings.Split(m.View(), "\n")
					if len(lines) > size.height {
						t.Errorf("layout %s, %d sources, %dx%d: %d lines, want at most %d",
							layout, count, size.width, size.height, len(lines), size.height)
					}
					for i, line := range lines {
						if got := lipgloss.Width(line); got > size.width {
							t.Errorf("layout %s, %d sources, %dx%d: line %d is %d cells wide, want at most %d: %q",
								layout, count, size.width, size.height, i, got, size.width, line)
						}
					}
				}
			}
		}
	}
}

// A terminal shorter than the content must be met with truncation and a
// visible marker on the last retained line -- not overflow, and not silence.
func TestViewTruncatesWithMarkerWhenTallerThanTerminal(t *testing.T) {
	now := time.Now()
	m := newModel(20*time.Second, loadHistory(""))
	m.width, m.now = 80, now
	m.sources = threeWindowSources(now, 3)

	// The unclamped full view of three stacked three-window panels is far
	// taller than 10 rows, so a height of 10 must cut it and say so.
	m.height = 10
	lines := strings.Split(m.View(), "\n")
	if len(lines) > 10 {
		t.Fatalf("height 10 rendered %d lines, want at most 10", len(lines))
	}
	if !strings.Contains(lines[len(lines)-1], truncationMarker) {
		t.Errorf("last line = %q, want the truncation marker", lines[len(lines)-1])
	}
	if got := lipgloss.Width(lines[len(lines)-1]); got > 80 {
		t.Errorf("marker line is %d cells wide, must not itself push over the width", got)
	}

	// A tall terminal keeps everything and carries no marker.
	m.height = 50
	view := m.View()
	if strings.Contains(view, truncationMarker) {
		t.Error("tall terminal render carries a truncation marker, want none")
	}

	// No reported height keeps today's behaviour: no limit, no marker.
	m.height = 0
	view = m.View()
	if strings.Contains(view, truncationMarker) {
		t.Error("unheighted render carries a truncation marker, want none")
	}

	// The degenerate one-row terminal still gets a single line, not a panic.
	m.height = 1
	lines = strings.Split(m.View(), "\n")
	if len(lines) != 1 || !strings.Contains(lines[0], truncationMarker) {
		t.Errorf("height 1 rendered %d lines %v, want exactly one with the marker", len(lines), lines)
	}
}

func TestFitHeight(t *testing.T) {
	view := strings.Repeat("line\n", 19) + "line" // 20 lines

	if got := fitHeight(view, 0); got != view {
		t.Error("height 0 must leave the view untouched")
	}
	if got := fitHeight(view, -3); got != view {
		t.Error("negative height must leave the view untouched")
	}
	if got := fitHeight(view, 20); got != view {
		t.Error("an exactly-fitting height must leave the view untouched")
	}
	for _, height := range []int{1, 2, 10, 19} {
		got := strings.Split(fitHeight(view, height), "\n")
		if len(got) != height {
			t.Errorf("height %d produced %d lines, want exactly %d", height, len(got), height)
		}
		if !strings.Contains(got[len(got)-1], truncationMarker) {
			t.Errorf("height %d: last line %q, want the marker", height, got[len(got)-1])
		}
		if height >= 2 && !strings.Contains(got[height-2], "line") {
			t.Errorf("height %d: line before the marker is %q, want retained content", height, got[height-2])
		}
	}
	if got := strings.Split(fitHeight(view, 1), "\n"); len(got) != 1 || !strings.Contains(got[0], truncationMarker) {
		t.Errorf("height 1 = %v, want one marker line", got)
	}
}

// topBorderCounts renders m and returns, for every line that opens a panel
// row (contains the box's top-left corner), how many panels start on that
// line -- i.e. the row's panel count, in row order.
func topBorderCounts(view string) []int {
	var counts []int
	for _, line := range strings.Split(view, "\n") {
		if n := strings.Count(line, "╭"); n > 0 {
			counts = append(counts, n)
		}
	}
	return counts
}

// This exercises View() itself, not a copy of its chunking loop: a
// regression in the loop at view.go (e.g. one panel per row) would change
// what actually gets rendered, and only a test that calls View() can catch
// that.
func TestViewRendersThreePanelsAsTwoRows(t *testing.T) {
	now := time.Now()
	m := newModel(20*time.Second, loadHistory(""))
	m.width, m.now = 132, now
	m.sources = gridSources(now, 3)

	got := topBorderCounts(m.View())
	if want := []int{2, 1}; len(got) != len(want) || got[0] != want[0] || got[1] != want[1] {
		t.Fatalf("row panel counts = %v, want %v", got, want)
	}
}

func TestViewRendersFivePanelsAsTwoTwoOne(t *testing.T) {
	now := time.Now()
	m := newModel(20*time.Second, loadHistory(""))
	m.width, m.now = 132, now
	m.sources = gridSources(now, 5)

	got := topBorderCounts(m.View())
	want := []int{2, 2, 1}
	if len(got) != len(want) {
		t.Fatalf("row panel counts = %v, want %v", got, want)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Errorf("row %d has %d panels, want %d (all rows=%v)", i, got[i], want[i], got)
		}
	}
}

// visualColumn returns the on-screen column at which substr starts in line,
// accounting for ANSI escapes (which contribute no width) ahead of it.
func visualColumn(t *testing.T, line, substr string) int {
	t.Helper()
	idx := strings.Index(line, substr)
	if idx < 0 {
		t.Fatalf("substring %q not found in %q", substr, line)
	}
	return lipgloss.Width(line[:idx])
}

// A trailing row narrower than a full row must still start its panel under
// the same column as the row above it, and that panel must keep a full row's
// column width rather than stretching to fill the terminal. This only shows
// up once the terminal is wider than maxLayout, which is where View() hands
// a ragged-width block to lipgloss.PlaceHorizontal -- a helper that recentres
// each line independently, so a short line drifts away from the column it
// belongs under unless every line was padded to the same width first.
func TestViewTrailingRowStaysUnderFullRowColumn(t *testing.T) {
	now := time.Now()
	m := newModel(20*time.Second, loadHistory(""))
	m.width, m.now = 180, now
	m.sources = gridSources(now, 3)

	cols := gridColumns(maxLayout, 3)
	colWidths := rowWidths(maxLayout, cols)

	var topBorders []string
	for _, line := range strings.Split(m.View(), "\n") {
		if strings.Contains(line, "╭") {
			topBorders = append(topBorders, line)
		}
	}
	if len(topBorders) != 2 {
		t.Fatalf("got %d top-border lines, want 2 (a full row and a trailing row)", len(topBorders))
	}
	fullRow, trailingRow := topBorders[0], topBorders[1]

	fullStart := visualColumn(t, fullRow, "╭")
	trailingStart := visualColumn(t, trailingRow, "╭")
	if trailingStart != fullStart {
		t.Errorf("trailing row panel starts at column %d, full row's first panel at %d; want them aligned",
			trailingStart, fullStart)
	}

	startByte := strings.Index(trailingRow, "╭")
	endByte := strings.Index(trailingRow, "╮") + len("╮")
	trailingWidth := lipgloss.Width(trailingRow[startByte:endByte])
	if trailingWidth != colWidths[0] {
		t.Errorf("trailing row panel width = %d, want %d (a full row's column width, not stretched)",
			trailingWidth, colWidths[0])
	}
}

// The grid-arithmetic tests above only ever use synthetic "S0"/"S1"/"S2"
// titles; this drives View() with the realistic mix this round adds -- two
// Claude accounts plus a plain Codex panel -- to check both the row shape
// and that the short trailing row keeps a full row's per-panel width rather
// than stretching to fill it.
func TestViewRendersTwoClaudeAccountsPlusCodexRealistically(t *testing.T) {
	now := time.Now()
	m := newModel(20*time.Second, loadHistory(""))
	m.width, m.now = 132, now
	m.sources = []sourceState{
		{snap: &Snapshot{Source: "claude", Account: "work", Title: "CLAUDE · work", Observed: now,
			Windows: []Window{{Key: "session", Label: "5-hour", Percent: 10}}}},
		{snap: &Snapshot{Source: "claude", Account: "personal", Title: "CLAUDE · personal", Observed: now,
			Windows: []Window{{Key: "session", Label: "5-hour", Percent: 10}}}},
		{snap: &Snapshot{Source: "codex", Title: "CODEX", Observed: now,
			Windows: []Window{{Key: "primary", Label: "5-hour", Percent: 10}}}},
	}

	view := m.View()
	got := topBorderCounts(view)
	want := []int{2, 1}
	if len(got) != len(want) || got[0] != want[0] || got[1] != want[1] {
		t.Fatalf("row panel counts = %v, want %v", got, want)
	}

	cols := gridColumns(maxLayout, 3)
	colWidths := rowWidths(maxLayout, cols)

	var topBorders []string
	for _, line := range strings.Split(view, "\n") {
		if strings.Contains(line, "╭") {
			topBorders = append(topBorders, line)
		}
	}
	if len(topBorders) != 2 {
		t.Fatalf("got %d top-border lines, want 2 (a full row and a trailing row)", len(topBorders))
	}
	fullRow, trailingRow := topBorders[0], topBorders[1]

	fullStart := strings.Index(fullRow, "╭")
	fullEnd := strings.Index(fullRow, "╮") + len("╮")
	fullWidth := lipgloss.Width(fullRow[fullStart:fullEnd])
	if fullWidth != colWidths[0] {
		t.Errorf("full row first panel width = %d, want %d", fullWidth, colWidths[0])
	}

	trailingStart := strings.Index(trailingRow, "╭")
	trailingEnd := strings.Index(trailingRow, "╮") + len("╮")
	trailingWidth := lipgloss.Width(trailingRow[trailingStart:trailingEnd])
	if trailingWidth != colWidths[0] {
		t.Errorf("trailing row panel width = %d, want %d (a full row's column width, not stretched)",
			trailingWidth, colWidths[0])
	}
}

// The help panel's codex source line must list every codex source, not stop
// after the first: a model can carry more than one, and going silent about
// the rest is a silently wrong help line rather than a missing one.
func TestHelpBodyListsEveryCodexSourceDetail(t *testing.T) {
	now := time.Now()
	m := newModel(20*time.Second, loadHistory(""))
	m.now, m.showHelp = now, true
	m.sources = []sourceState{
		{snap: &Snapshot{Source: "codex", Title: "CODEX 1", Detail: "/first/session.jsonl"}},
		{snap: &Snapshot{Source: "codex", Title: "CODEX 2", Detail: "/second/session.jsonl"}},
	}
	body := m.helpBody(100)
	if !strings.Contains(body, "/first/session.jsonl") {
		t.Errorf("help body missing the first codex source's detail:\n%s", body)
	}
	if !strings.Contains(body, "/second/session.jsonl") {
		t.Errorf("help body missing the second codex source's detail:\n%s", body)
	}
}

func TestRowWidthsSumToFullWidthIncludingGaps(t *testing.T) {
	width := 132
	for _, k := range []int{1, 2, 3} {
		widths := rowWidths(width, k)
		sum := 0
		for _, w := range widths {
			sum += w
		}
		if got := sum + (k-1)*panelGap; got != width {
			t.Errorf("k=%d: widths %v sum to %d plus gaps = %d, want %d", k, widths, sum, got, width)
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
	if got := reloaded.Trend("claude", "session", 20, 10); len(got) != 2 || got[0] != 10 || got[1] != 20 {
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
	if got := history.Trend("claude", "session", 10, 10); len(got) != 1 || got[0] != 10 {
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

	got := loadHistory(path).Trend("claude", "session", 3, maxSamplesPerKey+2)
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

// The headline fix: a window that sat idle before use began must be measured
// from when use began, not from when the window opened, or the rate is
// diluted by the idle stretch and a real warning can hide behind a calm
// number. The bar had already accrued 3% by the time activity was dated, so
// that much is charged to the idle stretch before the anchor, not to the
// post-anchor slope: the rate is (12-3)/24, not 12/24.
func TestProjectSustainedMeasuresFromFirstActivity(t *testing.T) {
	now := time.Now()
	window := Window{Key: "weekly_all", Percent: 12, Length: 168 * time.Hour,
		ResetsAt: now.Add(72 * time.Hour)} // 96h elapsed since the window opened
	history := loadHistory("")
	windowStart := now.Add(-96 * time.Hour)
	history.Add("claude/weekly_all", windowStart, 0)            // the rollover zero
	history.Add("claude/weekly_all", now.Add(-95*time.Hour), 0) // still zero; deduped away by Add
	history.Add("claude/weekly_all", now.Add(-48*time.Hour), 0) // still zero; deduped away by Add
	history.Add("claude/weekly_all", now.Add(-25*time.Hour), 0) // still zero; deduped away by Add
	history.Add("claude/weekly_all", now.Add(-24*time.Hour), 3) // bar leaves zero: activity begins
	history.Add("claude/weekly_all", now.Add(-12*time.Hour), 8)
	projection := history.Project("claude", window, now)
	if !projection.Valid || !projection.Sustained {
		t.Fatalf("expected a sustained projection: %+v", projection)
	}
	if projection.RatePerHour < 0.35 || projection.RatePerHour > 0.4 {
		t.Errorf("rate = %v%%/h, want ~0.375 ((12-3)/24 since first activity, not 12/96 from window open)",
			projection.RatePerHour)
	}
}

// The promise this fix must keep: a machine with no history at all reproduces
// today's number exactly, because there is nothing to measure activity from.
func TestProjectSustainedEmptyHistoryFallsBackToWindowOpen(t *testing.T) {
	now := time.Now()
	window := Window{Key: "weekly_all", Percent: 41, Length: 168 * time.Hour,
		ResetsAt: now.Add(127 * time.Hour)} // 41h elapsed since the window opened
	projection := loadHistory("").Project("claude", window, now)
	if !projection.Valid || !projection.Sustained {
		t.Fatalf("expected a sustained projection: %+v", projection)
	}
	if projection.RatePerHour < 0.9 || projection.RatePerHour > 1.1 {
		t.Errorf("rate = %v%%/h, want ~1.0 from window open (no history to measure activity from)",
			projection.RatePerHour)
	}
}

// History that exists but never reaches a zero reading inside this window
// (it starts mid-use, or was trimmed) cannot date first activity either, so
// it falls back the same way empty history does.
func TestProjectSustainedNoZeroSampleFallsBackToWindowOpen(t *testing.T) {
	now := time.Now()
	window := Window{Key: "weekly_all", Percent: 20, Length: 168 * time.Hour,
		ResetsAt: now.Add(68 * time.Hour)} // 100h elapsed since the window opened
	history := loadHistory("")
	history.Add("claude/weekly_all", now.Add(-90*time.Hour), 5) // never reads zero
	projection := history.Project("claude", window, now)
	if !projection.Valid || !projection.Sustained {
		t.Fatalf("expected a sustained projection: %+v", projection)
	}
	if projection.RatePerHour < 0.19 || projection.RatePerHour > 0.21 {
		t.Errorf("rate = %v%%/h, want ~0.2 (20/100, measured from window open)", projection.RatePerHour)
	}
}

// A zero reading that belongs to the *previous* window must not be mistaken
// for this window's first activity.
func TestProjectSustainedIgnoresZeroSampleFromPreviousWindow(t *testing.T) {
	now := time.Now()
	window := Window{Key: "weekly_all", Percent: 25, Length: 168 * time.Hour,
		ResetsAt: now.Add(118 * time.Hour)} // 50h elapsed since the window opened
	history := loadHistory("")
	history.Add("claude/weekly_all", now.Add(-60*time.Hour), 0) // the previous window's zero sample
	projection := history.Project("claude", window, now)
	if !projection.Valid || !projection.Sustained {
		t.Fatalf("expected a sustained projection: %+v", projection)
	}
	if projection.RatePerHour < 0.45 || projection.RatePerHour > 0.55 {
		t.Errorf("rate = %v%%/h, want ~0.5 (25/50, the earlier window's zero must not count)",
			projection.RatePerHour)
	}
}

// First activity under the 12h floor does not disqualify the sustained model
// by itself: the window has been open 38h, long enough on its own to smooth
// over the same nights and weekends the model exists for, so it falls back
// to measuring from the window's own open (pct 0) instead of abandoning the
// model for the volatile live slope. Only a window that is itself still
// young (TestProjectSustainedFallsBackEarlyInWindow) falls through.
func TestProjectSustainedFloorsDenominatorWhenActivityIsRecent(t *testing.T) {
	now := time.Now()
	window := Window{Key: "weekly_all", Percent: 8, Length: 168 * time.Hour,
		ResetsAt: now.Add(130 * time.Hour)} // 38h elapsed since the window opened
	history := loadHistory("")
	history.Add("claude/weekly_all", now.Add(-8*time.Hour), 0) // first activity only 8h ago
	history.Add("claude/weekly_all", now.Add(-time.Hour), 4)
	projection := history.Project("claude", window, now)
	if !projection.Valid || !projection.Sustained {
		t.Fatalf("expected a sustained projection: %+v", projection)
	}
	// The anchor is 1h old, so the denominator floors at sustainedMinElapsed:
	// (8-4)/12 rather than the window-open 8/38 this used to re-anchor to.
	if projection.RatePerHour < 0.31 || projection.RatePerHour > 0.35 {
		t.Errorf("rate = %v%%/h, want ~0.33 ((8-4)/12, the floored denominator)",
			projection.RatePerHour)
	}
}

// The denominator floor used to be implemented by re-anchoring to the window's
// own open, which made the reading jump as the anchor aged past 12h: the same
// data read calm on one side of the boundary and red on the other.
func TestProjectSustainedIsContinuousAcrossTheDenominatorFloor(t *testing.T) {
	now := time.Now()
	rateAt := func(anchorAge time.Duration) float64 {
		window := Window{Key: "weekly_all", Percent: 42, Length: 168 * time.Hour,
			ResetsAt: now.Add(68 * time.Hour)} // 100h elapsed since the window opened
		history := loadHistory("")
		history.Add("claude/weekly_all", now.Add(-100*time.Hour), 0)
		history.Add("claude/weekly_all", now.Add(-anchorAge), 0.5)
		projection := history.Project("claude", window, now)
		if !projection.Valid || !projection.Sustained {
			t.Fatalf("expected a sustained projection at anchor age %v: %+v", anchorAge, projection)
		}
		return projection.RatePerHour
	}
	justUnder, justOver := rateAt(11*time.Hour+59*time.Minute), rateAt(12*time.Hour+time.Minute)
	if diff := math.Abs(justUnder - justOver); diff > 0.05 {
		t.Errorf("rate jumped %v%%/h across the 12h floor (%v -> %v); it must be continuous",
			diff, justUnder, justOver)
	}
}

// The live model guards a negative rate; the sustained model computes the same
// (current - anchor) / span shape and must guard it too, or a source revising a
// percentage downward publishes a negative percent_at_reset over --json.
func TestProjectSustainedNeverReportsANegativeRate(t *testing.T) {
	now := time.Now()
	window := Window{Key: "weekly_all", Percent: 5, Length: 168 * time.Hour,
		ResetsAt: now.Add(68 * time.Hour)} // 100h elapsed since the window opened
	history := loadHistory("")
	history.Add("claude/weekly_all", now.Add(-100*time.Hour), 0)
	history.Add("claude/weekly_all", now.Add(-50*time.Hour), 20) // anchor above the current reading
	projection := history.Project("claude", window, now)
	if projection.Valid && projection.RatePerHour < 0 {
		t.Errorf("rate = %v%%/h, want no negative rate on a valid projection", projection.RatePerHour)
	}
	if projection.Valid && projection.AtReset < 0 {
		t.Errorf("at reset = %v%%, want no negative projected percentage", projection.AtReset)
	}
}

func TestProjectionTextLabelsSustainedRate(t *testing.T) {
	now := time.Now()
	sustained := Projection{RatePerHour: 1.0, AtReset: 168, ExhaustAt: now.Add(59 * time.Hour),
		Valid: true, Sustained: true}
	text, _, urgent := projectionText(sustained, now.Add(127*time.Hour), 168*time.Hour, now)
	if !urgent {
		t.Error("sustained exhaustion should render as urgent")
	}
	if !strings.Contains(text, "%/h avg") {
		t.Errorf("sustained rate text = %q, want the 'avg' marker", text)
	}
	live := Projection{RatePerHour: 1.0, AtReset: 60, Valid: true}
	if text, _, _ := projectionText(live, time.Time{}, 168*time.Hour, now); strings.Contains(text, "avg") {
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
	text, gap, urgent := projectionText(short, now.Add(116*time.Hour), 168*time.Hour, now)
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
	text, gap, urgent = projectionText(spare, now.Add(87*time.Hour), 168*time.Hour, now)
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
	if _, gap, _ = projectionText(short, time.Time{}, 168*time.Hour, now); gap != "" {
		t.Errorf("gap = %q with an unknown reset, want empty", gap)
	}
	// Deadlines a hair apart are noise.
	if _, gap, _ = projectionText(short, short.FullAt.Add(30*time.Second), 168*time.Hour, now); gap != "" {
		t.Errorf("gap = %q with deadlines under a minute apart, want empty", gap)
	}
}

// A gap bigger than a whole window (several more windows would have to pass
// before the pace ran dry or came in with room to spare) is not a useful
// reading, so it is suppressed -- but the headline itself must survive.
func TestProjectionTextSuppressesGapBeyondAWindowLength(t *testing.T) {
	now := time.Now()
	weekly := 168 * time.Hour
	resetsAt := now.Add(20 * time.Hour)

	// 190h gap on a 168h window: suppressed.
	huge := Projection{RatePerHour: 0.5, AtReset: 90, FullAt: now.Add(210 * time.Hour), Valid: true}
	text, gap, _ := projectionText(huge, resetsAt, weekly, now)
	if gap != "" {
		t.Errorf("gap = %q for a 190h gap on a 168h window, want suppressed", gap)
	}
	if text == "" {
		t.Error("headline text must survive even when the gap is suppressed")
	}

	// 167h gap on a 168h window: still under a window length, so it renders.
	justUnder := Projection{RatePerHour: 0.5, AtReset: 90, FullAt: now.Add(187 * time.Hour), Valid: true}
	if _, gap, _ := projectionText(justUnder, resetsAt, weekly, now); gap == "" {
		t.Error("gap just under a window length was suppressed, want it kept")
	}

	// windowLength of 0 means unknown -- never suppress.
	if _, gap, _ := projectionText(huge, resetsAt, 0, now); gap == "" {
		t.Error("gap = empty with windowLength 0, want no suppression")
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
