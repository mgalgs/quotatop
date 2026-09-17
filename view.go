package main

import (
	"fmt"
	"math"
	"os"
	"strings"
	"time"

	"github.com/charmbracelet/lipgloss"
)

const (
	sparkWidth  = 12
	minPanel    = 46
	maxLayout   = 132
	panelGap    = 2
	gaugeMinPad = 8
)

// Layout names: what --layout accepts, what the l key will cycle, and what the
// footer will show when the user has switched off the default.
const (
	layoutFull     = "full"
	layoutCompact  = "compact"
	layoutVertical = "vertical"
)

var layouts = []string{layoutFull, layoutCompact, layoutVertical}

// parseLayout maps a --layout value onto a known layout. The empty string is
// the default; an unknown name is an error rather than a silent fallback, so a
// typo in a test fails loudly instead of quietly exercising the default.
func parseLayout(name string) (string, error) {
	if name == "" {
		return layoutFull, nil
	}
	for _, layout := range layouts {
		if layout == name {
			return layout, nil
		}
	}
	return "", fmt.Errorf(`unknown layout %q (valid: %s)`, name, strings.Join(layouts, ", "))
}

var (
	sparkRunes   = []rune("▁▂▃▄▅▆▇█")
	partialRunes = []rune("▏▎▍▌▋▊▉")
)

// gauge draws a continuous bar whose colour is a function of *position*, not of
// the value: the tail of every bar is red, so a bar creeping into the red end is
// legible at a glance without reading the number. The unfilled part is the same
// gradient darkened, which previews where the bar is heading.
func gauge(width int, pct float64) string {
	if width < 1 {
		return ""
	}
	exact := math.Max(0, math.Min(100, pct)) / 100 * float64(width)
	full := int(exact)
	frac := exact - float64(full)

	var out strings.Builder
	for i := 0; i < width; i++ {
		colour := gradientAt((float64(i) + 0.5) / float64(width))
		switch {
		case i < full:
			out.WriteString(lipgloss.NewStyle().Foreground(colour.color()).Render(fullBlock()))
		case i == full && frac >= 0.125:
			// A partial cell: the lit fraction over the dark track, so a bar
			// that is barely moving still shows movement.
			index := int(frac*float64(len(partialRunes))) - 1
			if index < 0 {
				index = 0
			} else if index >= len(partialRunes) {
				index = len(partialRunes) - 1
			}
			out.WriteString(lipgloss.NewStyle().
				Foreground(colour.color()).
				Background(colour.dim(0.25).color()).
				Render(partialBlock(index)))
		default:
			out.WriteString(lipgloss.NewStyle().Foreground(colour.dim(0.25).color()).Render(emptyBlock()))
		}
	}
	return out.String()
}

// sparkline shows the shape of the recent trend. It scales to the range of the
// points shown, not to 0-100, because the interesting motion in a quota bar is
// usually a few percent.
func sparkline(points []float64) string {
	if len(points) < 2 {
		return ""
	}
	low, high := points[0], points[0]
	for _, value := range points {
		low = math.Min(low, value)
		high = math.Max(high, value)
	}
	var out strings.Builder
	for _, value := range points {
		index := len(sparkRunes) / 2 // a flat trend sits mid-height
		if high > low {
			index = int((value - low) / (high - low) * float64(len(sparkRunes)-1))
		}
		out.WriteRune(sparkRunes[index])
	}
	return styleDim.Render(out.String())
}

func percentText(pct float64) string {
	if pct == math.Trunc(pct) {
		return fmt.Sprintf("%.0f%%", pct)
	}
	return fmt.Sprintf("%.1f%%", pct)
}

// compactDuration renders a span at the precision that matters at its scale:
// seconds only when a reset is imminent, days when it is a week out.
func compactDuration(d time.Duration) string {
	if d < 0 {
		d = 0
	}
	switch {
	case d >= 24*time.Hour:
		return fmt.Sprintf("%dd %dh", int(d.Hours())/24, int(d.Hours())%24)
	case d >= time.Hour:
		return fmt.Sprintf("%dh %02dm", int(d.Hours()), int(d.Minutes())%60)
	case d >= time.Minute:
		return fmt.Sprintf("%dm %02ds", int(d.Minutes()), int(d.Seconds())%60)
	default:
		return fmt.Sprintf("%ds", int(d.Seconds()))
	}
}

// resetText describes the deadline. brief drops the absolute clock time, which
// is what gives way first when a panel is too narrow to hold everything.
func resetText(resetsAt time.Time, now time.Time, brief bool) string {
	if resetsAt.IsZero() {
		return "reset time unknown"
	}
	left := resetsAt.Sub(now)
	if left <= 0 {
		return "reset due"
	}
	if brief {
		return "in " + compactDuration(left)
	}
	if left < 24*time.Hour {
		return "resets in " + compactDuration(left)
	}
	return "resets " + resetsAt.Format("Mon 3:04 PM") + " · in " + compactDuration(left)
}

// projectionText renders the burn forecast. gap is the parenthetical reading
// (" (2d 14h short)"), returned apart from text so the caller can shed it
// when the detail line does not fit — after the absolute reset time, which
// gives way first; it is empty when there is no reset deadline to measure
// against, or when it exceeds windowLength (pass 0 to never suppress it).
func projectionText(projection Projection, resetsAt time.Time, windowLength time.Duration, now time.Time) (string, string, bool) {
	if !projection.Valid {
		return "", "", false
	}
	if projection.RatePerHour < 0.05 {
		return "steady", "", false
	}
	var gap string
	if !projection.FullAt.IsZero() && !resetsAt.IsZero() && resetsAt.After(now) {
		if diff := projection.FullAt.Sub(resetsAt); diff.Abs() >= time.Minute {
			// A gap bigger than a whole window means several more windows
			// would have to pass before the pace came in with room to
			// spare -- not a useful reading, and the headline (full-in or
			// at-reset) already carries the number that matters. In
			// practice this only ever suppresses spare: a short gap is
			// bounded by how much of the window remains, so it never
			// exceeds windowLength.
			if windowLength <= 0 || diff.Abs() <= windowLength {
				if diff < 0 {
					gap = " (" + compactDuration(-diff) + " short)"
				} else {
					gap = " (" + compactDuration(diff) + " spare)"
				}
			}
		}
	}
	rate := fmt.Sprintf("%.1f%%/h", projection.RatePerHour)
	if projection.RatePerHour >= 10 {
		rate = fmt.Sprintf("%.0f%%/h", projection.RatePerHour)
	}
	if projection.Sustained {
		rate += " avg" // the window's own elapsed pace, not the recent slope
	}
	if !projection.ExhaustAt.IsZero() {
		return "+" + rate + " → full in " + compactDuration(projection.ExhaustAt.Sub(now)), gap, true
	}
	return "+" + rate + fmt.Sprintf(" → ~%.0f%% at reset", math.Min(100, projection.AtReset)), gap, false
}

func wrap(text string, width int) []string {
	if width < 8 {
		width = 8
	}
	var lines []string
	line := ""
	for _, word := range strings.Fields(text) {
		candidate := word
		if line != "" {
			candidate = line + " " + word
		}
		if len(candidate) > width && line != "" {
			lines = append(lines, line)
			line = word
			continue
		}
		line = candidate
	}
	if line != "" {
		lines = append(lines, line)
	}
	return lines
}

// windowLines renders one quota bar: heading with trend and percentage, the
// gauge, then the reset countdown and burn projection.
func windowLines(width int, identity string, window Window, history *History, now time.Time) []string {
	right := percentText(window.Percent)
	rightStyled := lipgloss.NewStyle().Foreground(gradientAt(window.Percent / 100).color()).Bold(true).Render(right)
	barPct := window.Percent
	if window.Expired {
		right = "—"
		rightStyled = styleDim.Render(right)
		barPct = 0
	}

	// A sparkline ending at the current reading is itself a claim about the
	// live window, so it is withheld on expiry for the same reason the gauge
	// and the projection are: it would sit right next to the withheld
	// percentage, plotting the exact number the heading just refused to
	// state.
	spark := ""
	if !window.Expired {
		spark = sparkline(history.Trend(identity, window.Key, window.Percent, sparkWidth))
	}
	label := styleTxt.Render(window.Label)
	fill := width - lipgloss.Width(label) - lipgloss.Width(spark) - lipgloss.Width(right) - 2
	if fill < 1 {
		spark, fill = "", width-lipgloss.Width(label)-lipgloss.Width(right)-1
	}
	if fill < 1 {
		fill = 1
	}
	heading := label + strings.Repeat(" ", fill) + spark + "  " + rightStyled

	// The detail line drops its least important part rather than being cut off
	// mid-word: the absolute reset time goes first, then the gap, then the
	// projection.
	full := styleDim.Render(resetText(window.ResetsAt, now, false))
	brief := styleDim.Render(resetText(window.ResetsAt, now, true))
	candidates := []string{full, brief}
	// An expired window's percentage is already withheld above; a burn
	// projection is itself a percentage claim, so it is withheld too rather
	// than extrapolating from the discarded reading.
	if !window.Expired {
		projection := history.Project(identity, window, now)
		if text, gap, urgent := projectionText(projection, window.ResetsAt, window.Length, now); text != "" {
			style := styleDim
			if urgent {
				style = styleErr
			}
			separator := styleDim.Render(" · ")
			candidates = append([]string{
				full + separator + style.Render(text),
				brief + separator + style.Render(text),
			}, candidates...)
			if gap != "" {
				candidates = append([]string{
					full + separator + style.Render(text+gap),
					brief + separator + style.Render(text+gap),
				}, candidates...)
			}
		}
	}
	detail := candidates[len(candidates)-1]
	for _, candidate := range candidates {
		if lipgloss.Width(candidate) <= width {
			detail = candidate
			break
		}
	}

	note := window.Note
	if window.Expired {
		note = "window reset since this reading"
	}

	lines := []string{heading, gauge(width, barPct), truncate(detail, width)}
	if note != "" {
		lines = append(lines, styleWrn.Render(truncate("· "+note, width)))
	}
	return lines
}

// panel renders one source as a bordered box. The title takes the colour of the
// source's worst window, so which panel needs attention is visible peripherally.
func panel(width int, snap *Snapshot, history *History, now time.Time, loading bool) string {
	content := width - 4
	if snap == nil {
		return box(width, styleDim.Render("···"), "",
			[]string{styleDim.Render("waiting for first reading...")}, "", "")
	}

	worst := 0.0
	for _, window := range snap.Windows {
		if window.Expired {
			continue
		}
		worst = math.Max(worst, window.Percent)
	}
	title := lipgloss.NewStyle().Foreground(gradientAt(worst / 100).color()).Bold(true).Render(snap.Title)

	var body []string
	if snap.Err != nil {
		title = styleErr.Bold(true).Render(snap.Title)
		for _, line := range wrap(snap.Err.Error(), content) {
			body = append(body, styleErr.Render(line))
		}
		body = append(body, "", styleDim.Render("press r to retry"))
	} else {
		for i, window := range snap.Windows {
			if i > 0 {
				body = append(body, "")
			}
			body = append(body, windowLines(content, snap.Identity(), window, history, now)...)
		}
		// A block and a warning are independent facts -- the only warning the
		// codex scanner raises is that a log is cut off, which is a caveat on
		// everything else in the panel, including a block riding on that same
		// truncated log. Neither should swallow the other.
		if snap.LimitReached != "" {
			body = append(body, "", styleErr.Render(truncate("blocked: "+humanizeReason(snap.LimitReached), content)))
		}
		if snap.Warning != "" {
			body = append(body, "", styleWrn.Render(truncate(snap.Warning, content)))
		}
	}

	footer := styleDim.Render("no reading yet")
	if snap.Err == nil && !snap.Observed.IsZero() {
		footer = styleDim.Render(snap.Verb + " " + compactDuration(now.Sub(snap.Observed)) + " ago")
	} else if snap.Err == nil {
		footer = styleDim.Render(snap.Verb + " just now")
	}
	if loading {
		footer = styleKey.Render("refreshing")
	}
	chip, footnote := "", styleDim.Render(snap.Footnote)
	if snap.Chip != "" {
		chip = styleMut.Render(snap.Chip)
	}
	if snap.Err != nil {
		// "cached ≤10m" under a panel that failed to read anything describes
		// data that is not there.
		footnote = ""
	}
	return box(width, title, chip, body, footer, footnote)
}

func (m model) headerLine(width int) string {
	worst := 0.0
	for _, source := range m.sources {
		snap := source.snap
		if snap == nil || snap.Err != nil {
			continue
		}
		for _, window := range snap.Windows {
			if window.Expired {
				continue
			}
			worst = math.Max(worst, window.Percent)
		}
	}
	mark := lipgloss.NewStyle().Foreground(gradientAt(worst / 100).color()).Render("▌")
	left := mark + styleTxt.Bold(true).Render(" QUOTATOP") + styleDim.Render("  "+m.host)

	right := styleMut.Render(m.now.Format("Mon 3:04:05 PM"))
	if m.busy() {
		right += styleDim.Render("   ") + styleKey.Render(m.spinner.View()+" refreshing")
	} else {
		next := m.nextRefresh().Sub(m.now)
		if next < 0 {
			next = 0
		}
		right += styleDim.Render("   ↻ " + compactDuration(next))
	}

	gap := width - lipgloss.Width(left) - lipgloss.Width(right)
	if gap < 1 {
		return truncate(left, width)
	}
	return left + strings.Repeat(" ", gap) + right
}

// tightest names the window closest to its ceiling across all sources: the one
// number worth carrying away from a glance at this screen.
func (m model) tightest() (string, float64, bool) {
	name, worst, found := "", -1.0, false
	for _, source := range m.sources {
		snap := source.snap
		if snap == nil || snap.Err != nil {
			continue
		}
		for _, window := range snap.Windows {
			if window.Expired {
				continue
			}
			if window.Percent > worst {
				name, worst, found = snap.Title+" "+window.Label, window.Percent, true
			}
		}
	}
	return name, worst, found
}

func (m model) footerLine(width int) string {
	keys := []struct{ key, label string }{
		{"r", "refresh"}, {"R", "force-fresh"}, {"?", "keys"}, {"q", "quit"},
	}
	parts := make([]string, 0, len(keys))
	for _, entry := range keys {
		parts = append(parts, styleKey.Render(entry.key)+styleDim.Render(" "+entry.label))
	}
	left := strings.Join(parts, styleDim.Render(" · "))

	right := ""
	if name, worst, ok := m.tightest(); ok {
		right = styleDim.Render("tightest ") +
			lipgloss.NewStyle().Foreground(gradientAt(worst/100).color()).Render(
				strings.ToLower(name)+" "+percentText(worst))
	}
	gap := width - lipgloss.Width(left) - lipgloss.Width(right)
	if gap < 1 {
		return truncate(left, width)
	}
	return left + strings.Repeat(" ", gap) + right
}

func (m model) helpBody(width int) string {
	rows := [][2]string{
		{"r", "refresh all sources now"},
		{"R", "refresh Claude past its 10-minute cache (hits the API)"},
		{"?", "toggle this help"},
		{"q / esc / ctrl-c", "quit"},
	}
	var body []string
	for _, row := range rows {
		// Wide enough to leave a gap after the longest key list, which is
		// exactly 16 cells and would otherwise run into its description.
		body = append(body, styleKey.Render(row[0])+
			styleDim.Render(strings.Repeat(" ", max(2, 18-lipgloss.Width(row[0])))+row[1]))
	}
	body = append(body, "",
		styleMut.Render("Claude")+styleDim.Render("  live account quota via the usage API, cached 10m"),
		styleMut.Render("Codex ")+styleDim.Render("  latest rate limits recorded in local session logs"),
		"",
		styleDim.Render(fmt.Sprintf("polling every %s · trend and burn rate from %s",
			compactDuration(m.interval), shortenPath(m.history.path))),
		styleDim.Render("5-hour rate from the last 90m · weekly from the window's own elapsed pace"))
	for _, source := range m.sources {
		if source.snap != nil && source.snap.Source == "codex" && source.snap.Detail != "" {
			body = append(body, styleDim.Render("codex source "+shortenPath(source.snap.Detail)))
		}
	}
	return box(width, styleTxt.Bold(true).Render("KEYS"), "", body, "", "")
}

// humanizeReason turns a snake_case reason code into readable words.
func humanizeReason(s string) string {
	return strings.ReplaceAll(s, "_", " ")
}

func shortenPath(path string) string {
	if path == "" {
		return "(memory only)"
	}
	if home, err := os.UserHomeDir(); err == nil && strings.HasPrefix(path, home) {
		return "~" + strings.TrimPrefix(path, home)
	}
	return path
}

// truncationMarker is the last line of a View() that did not fit the
// terminal's height. Overflow is truncated rather than allowed, because
// overflow makes the terminal scroll, which pushes the header off the top and
// leaves the frame jumping around -- the exact symptom a full-screen monitor
// is supposed to prevent. A stable frame that admits it is cut is strictly
// better than an unstable one that hides it.
const truncationMarker = "\u2026"

// fitHeight is the backstop that keeps View()'s height invariant: when height
// is positive the result never exceeds height lines. Overflow is truncated
// and the marker becomes the final line, so the cut is visible without
// itself pushing the output over the limit. A height of 0 or less means no
// limit, leaving piped --snapshot and harnesses that never report a size
// untouched.
func fitHeight(view string, height int) string {
	if height <= 0 {
		return view
	}
	lines := strings.Split(view, "\n")
	if len(lines) <= height {
		return view
	}
	if height < 2 {
		return styleDim.Render(truncationMarker)
	}
	kept := append([]string(nil), lines[:height-1]...)
	kept = append(kept, styleDim.Render(truncationMarker))
	return strings.Join(kept, "\n")
}

// gridColumns is how many panels fit side by side at width, given panels no
// narrower than minPanel with panelGap between them -- clamped to at least
// one column and at most n, the number of panels there are to place.
func gridColumns(width, n int) int {
	cols := (width + panelGap) / (minPanel + panelGap)
	if cols < 1 {
		cols = 1
	}
	if cols > n {
		cols = n
	}
	return cols
}

// rowWidths splits width across a row of k panels with panelGap between
// each. Every panel gets usable/k, except the last, which takes whatever
// division rounded away, so a row's widths always sum to width exactly.
// k < 1 is clamped to 1, matching gridColumns' own floor, so a caller that
// forgets to check does not panic on widths[-1].
func rowWidths(width, k int) []int {
	if k < 1 {
		k = 1
	}
	usable := width - (k-1)*panelGap
	each := usable / k
	widths := make([]int, k)
	for i := range widths {
		widths[i] = each
	}
	widths[k-1] = usable - each*(k-1)
	return widths
}

func (m model) View() string {
	width := m.width
	if width == 0 {
		// A terminal that never reported a size -- some pty harnesses, and a
		// resize message that has not landed yet -- would otherwise show a
		// blank screen rather than the numbers it was opened for.
		width = 80
	}
	if width < 24 {
		// Nothing renders sensibly below this; keep the maths out of the weeds.
		width = 24
	}
	if width > maxLayout {
		width = maxLayout
	}

	var rows []string
	if n := len(m.sources); n > 0 {
		cols := gridColumns(width, n)
		// Sized once for a full row of cols panels: a short trailing row (the
		// last row of an n not divisible by cols) gets the same per-panel
		// width as every row above it, rather than stretching to fill the
		// width, so a gauge's bar length stays comparable at a glance across
		// every panel on screen. The trailing row is simply narrower than the
		// terminal; nothing fills the gap.
		colWidths := rowWidths(width, cols)
		for start := 0; start < n; start += cols {
			end := start + cols
			if end > n {
				end = n
			}
			row := m.sources[start:end]
			widths := colWidths[:len(row)]
			parts := make([]string, 0, len(row)*2-1)
			for i, source := range row {
				if i > 0 {
					parts = append(parts, strings.Repeat(" ", panelGap))
				}
				parts = append(parts, panel(widths[i], source.snap, m.history, m.now, source.loading))
			}
			joined := lipgloss.JoinHorizontal(lipgloss.Top, parts...)
			// A short trailing row is narrower than width by design (see above),
			// so it is padded out here rather than left for PlaceHorizontal
			// below: that helper centres each line independently, and would
			// otherwise float this row's panels away from the column they sit
			// under.
			rows = append(rows, lipgloss.NewStyle().Width(width).Render(joined))
		}
	}
	panels := strings.Join(rows, "\n\n")

	sections := []string{m.headerLine(width), "", panels, ""}
	if m.showHelp {
		sections = append(sections, m.helpBody(width))
	} else {
		sections = append(sections, m.footerLine(width))
	}
	view := strings.Join(sections, "\n")
	if m.width > width {
		view = lipgloss.PlaceHorizontal(m.width, lipgloss.Center, view)
	}
	// Final backstop, so the invariant -- m.height > 0 means View() returns at
	// most m.height lines -- holds for every layout, including ones added
	// later, rather than being a calculation each layout is trusted to get
	// right.
	if m.height > 0 {
		view = fitHeight(view, m.height)
	}
	return view
}
