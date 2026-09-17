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

	// compactMinPanel is the packing floor of the compact layout, not of full:
	// full depends on 46 and looks wrong below it, while compact only needs
	// room for a label, a percentage and a short gauge per line.
	compactMinPanel = 30
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
	return currentTheme().dim.Render(out.String())
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
		rightStyled = currentTheme().dim.Render(right)
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
	label := currentTheme().txt.Render(window.Label)
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
	full := currentTheme().dim.Render(resetText(window.ResetsAt, now, false))
	brief := currentTheme().dim.Render(resetText(window.ResetsAt, now, true))
	candidates := []string{full, brief}
	// An expired window's percentage is already withheld above; a burn
	// projection is itself a percentage claim, so it is withheld too rather
	// than extrapolating from the discarded reading.
	if !window.Expired {
		projection := history.Project(identity, window, now)
		if text, gap, urgent := projectionText(projection, window.ResetsAt, window.Length, now); text != "" {
			style := currentTheme().dim
			if urgent {
				style = currentTheme().err
			}
			separator := currentTheme().dim.Render(" · ")
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
		lines = append(lines, currentTheme().wrn.Render(truncate("· "+note, width)))
	}
	return lines
}

// panel renders one source as a bordered box. The title takes the colour of the
// source's worst window, so which panel needs attention is visible peripherally.
func panel(width int, snap *Snapshot, history *History, now time.Time, loading bool) string {
	content := width - 4
	if snap == nil {
		return box(width, currentTheme().dim.Render("···"), "",
			[]string{currentTheme().dim.Render("waiting for first reading...")}, "", "")
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
		title = currentTheme().err.Bold(true).Render(snap.Title)
		for _, line := range wrap(snap.Err.Error(), content) {
			body = append(body, currentTheme().err.Render(line))
		}
		body = append(body, "", currentTheme().dim.Render("press r to retry"))
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
			body = append(body, "", currentTheme().err.Render(truncate("blocked: "+humanizeReason(snap.LimitReached), content)))
		}
		if snap.Warning != "" {
			body = append(body, "", currentTheme().wrn.Render(truncate(snap.Warning, content)))
		}
	}

	footer := panelFooter(snap, loading, now)
	chip, footnote := panelChipFootnote(snap)
	return box(width, title, chip, body, footer, footnote)
}

// compactWindowLine squeezes one window onto a single line: label, percentage
// and gauge sharing a row. It is the whole of the compact layout's height
// economy -- no sparkline, no detail line, no blank spacers between windows --
// while keeping the two things that must survive: the window's current
// percentage (a dash when the window expired), and its bar, which is still
// the fastest read of which window is red.
func compactWindowLine(width int, window Window) string {
	pct, barPct := percentText(window.Percent), window.Percent
	if window.Expired {
		pct, barPct = "—", 0
	}
	var pctStyled string
	if window.Expired {
		pctStyled = currentTheme().dim.Render(pct)
	} else {
		pctStyled = lipgloss.NewStyle().Foreground(gradientAt(window.Percent / 100).color()).Bold(true).Render(pct)
	}
	label := currentTheme().txt.Render(window.Label)
	gaugeWidth := width - lipgloss.Width(label) - lipgloss.Width(pct) - 2
	if gaugeWidth < 1 {
		gaugeWidth = 1
	}
	return label + " " + pctStyled + " " + gauge(gaugeWidth, barPct)
}

// panelCompact renders one source with the layout that squeezes everything
// into a smaller terminal: one line per window, the error squashed to a line,
// no blank spacers. Every window's percentage and any blocked or error state
// still appear -- a compact panel that hides a block is worse than one that
// does not fit.
func panelCompact(width int, snap *Snapshot, history *History, now time.Time, loading bool) string {
	content := width - 4
	if snap == nil {
		return box(width, currentTheme().dim.Render("···"), "",
			[]string{currentTheme().dim.Render("waiting for first reading...")}, "", "")
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
		title = currentTheme().err.Bold(true).Render(snap.Title)
		body = append(body, currentTheme().err.Render(truncate(snap.Err.Error(), content)))
	} else {
		for _, window := range snap.Windows {
			body = append(body, compactWindowLine(content, window))
		}
		if snap.LimitReached != "" {
			body = append(body, currentTheme().err.Render(truncate("blocked: "+humanizeReason(snap.LimitReached), content)))
		}
		if snap.Warning != "" {
			body = append(body, currentTheme().wrn.Render(truncate(snap.Warning, content)))
		}
	}

	footer := panelFooter(snap, loading, now)
	chip, footnote := panelChipFootnote(snap)
	return box(width, title, chip, body, footer, footnote)
}

// panelFooter is the bottom-edge content every layout shares: when the
// reading was observed, or a refresh marker while a fetch is in flight.
func panelFooter(snap *Snapshot, loading bool, now time.Time) string {
	footer := currentTheme().dim.Render("no reading yet")
	if snap.Err == nil && !snap.Observed.IsZero() {
		footer = currentTheme().dim.Render(snap.Verb + " " + compactDuration(now.Sub(snap.Observed)) + " ago")
	} else if snap.Err == nil {
		footer = currentTheme().dim.Render(snap.Verb + " just now")
	}
	if loading {
		footer = currentTheme().key.Render("refreshing")
	}
	return footer
}

// panelChipFootnote is the chip and footnote every layout shares. A failed
// reading drops its footnote: "cached ≤10m" under a panel that failed to read
// anything describes data that is not there.
func panelChipFootnote(snap *Snapshot) (chip, footnote string) {
	chip, footnote = "", currentTheme().dim.Render(snap.Footnote)
	if snap.Chip != "" {
		chip = currentTheme().mut.Render(snap.Chip)
	}
	if snap.Err != nil {
		footnote = ""
	}
	return chip, footnote
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
	left := mark + currentTheme().txt.Bold(true).Render(" QUOTATOP") + currentTheme().dim.Render("  "+m.host)

	right := currentTheme().mut.Render(m.now.Format("Mon 3:04:05 PM"))
	if m.busy() {
		right += currentTheme().dim.Render("   ") + currentTheme().key.Render(m.spinner.View()+" refreshing")
	} else {
		next := m.nextRefresh().Sub(m.now)
		if next < 0 {
			next = 0
		}
		right += currentTheme().dim.Render("   ↻ " + compactDuration(next))
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
		{"r", "refresh"}, {"R", "force-fresh"}, {"l", "layout"}, {"t", "themes"}, {"?", "keys"}, {"q", "quit"},
	}
	parts := make([]string, 0, len(keys))
	for _, entry := range keys {
		parts = append(parts, currentTheme().key.Render(entry.key)+currentTheme().dim.Render(" "+entry.label))
	}
	left := strings.Join(parts, currentTheme().dim.Render(" · "))
	// The current layout's name, so the user knows what l just switched to.
	// It is only shown off the default: in full the footer keeps exactly
	// today's content, and there is nothing that was just switched.
	if name := m.layoutName(); name != layoutFull {
		left += currentTheme().dim.Render(" · ") + currentTheme().mut.Render(name)
	}

	right := ""
	if name, worst, ok := m.tightest(); ok {
		right = currentTheme().dim.Render("tightest ") +
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
		{"l", "cycle layout: full, compact, vertical"},
		{"t", "cycle themes"},
		{"?", "toggle this help"},
		{"q / esc / ctrl-c", "quit"},
	}
	var body []string
	for _, row := range rows {
		// Wide enough to leave a gap after the longest key list, which is
		// exactly 16 cells and would otherwise run into its description.
		body = append(body, currentTheme().key.Render(row[0])+
			currentTheme().dim.Render(strings.Repeat(" ", max(2, 18-lipgloss.Width(row[0])))+row[1]))
	}
	body = append(body, "",
		currentTheme().mut.Render("Claude")+currentTheme().dim.Render("  live account quota via the usage API, cached 10m"),
		currentTheme().mut.Render("Codex ")+currentTheme().dim.Render("  latest rate limits recorded in local session logs"),
		"",
		currentTheme().dim.Render(fmt.Sprintf("polling every %s · trend and burn rate from %s",
			compactDuration(m.interval), shortenPath(m.history.path))),
		currentTheme().dim.Render("5-hour rate from the last 90m · weekly from the window's own elapsed pace"))
	for _, source := range m.sources {
		if source.snap != nil && source.snap.Source == "codex" && source.snap.Detail != "" {
			body = append(body, currentTheme().dim.Render("codex source "+shortenPath(source.snap.Detail)))
		}
	}
	return box(width, currentTheme().txt.Bold(true).Render("KEYS"), "", body, "", "")
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
		return currentTheme().dim.Render(truncationMarker)
	}
	kept := append([]string(nil), lines[:height-1]...)
	kept = append(kept, currentTheme().dim.Render(truncationMarker))
	return strings.Join(kept, "\n")
}

// layoutName is the model's layout, reading the zero value as the default.
func (m model) layoutName() string {
	if m.layout == "" {
		return layoutFull
	}
	return m.layout
}

// cycleLayout advances the layout one step, wrapping at the end: the l key is
// the whole layout control -- cycle only, no menu.
func (m *model) cycleLayout() {
	name := m.layoutName()
	for i, layout := range layouts {
		if layout == name {
			m.layout = layouts[(i+1)%len(layouts)]
			return
		}
	}
}

// gridColumns is how many panels fit side by side at width, given panels no
// narrower than min with panelGap between them -- clamped to at least one
// column and at most n, the number of panels there are to place. Each layout
// passes its own floor: full wants minPanel, compact wants compactMinPanel.
func gridColumns(width, n, min int) int {
	cols := (width + panelGap) / (min + panelGap)
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
		if m.layoutName() == layoutVertical {
			// One panel per row, at any width: no grid, no side-by-side packing.
			// The width still clamps to maxLayout like everything else -- a
			// panel stretched across a very wide terminal reads badly -- but it
			// is never split. This layout is about stacking, not filling.
			for _, source := range m.sources {
				rows = append(rows, panel(width, source.snap, m.history, m.now, source.loading))
			}
		} else {
			renderPanel := panel
			min := minPanel
			if m.layoutName() == layoutCompact {
				// Compact reuses the grid's packing arithmetic with its own, smaller
				// floor; the equal-width rule the trailing row obeys is full-grid
				// guidance, not a constraint this layout needs.
				renderPanel, min = panelCompact, compactMinPanel
			}
			cols := gridColumns(width, n, min)
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
					parts = append(parts, renderPanel(widths[i], source.snap, m.history, m.now, source.loading))
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
