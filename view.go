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

	// gaugeMinRun is the fit rule for the compact forecast overlay: the
	// minimum number of bar cells that must stay untouched for the bar to
	// keep reading as a bar. The text sits at the right-hand end, so the
	// untouched run is the filled part, which is exactly the percentage read
	// the bar exists to give. Picked by rendering at 80, 96 and 132 columns:
	// a bar that keeps eight filled cells (the gauge's own minimum pad) still
	// shows the percentage clearly next to the ~15-cell forecast, while six
	// or fewer cells read as a stub with a label stuck to its end. Below the
	// rule the bar is drawn plain.
	gaugeMinRun = 8

	// compactMinPanel is the packing floor of the compact layout, not of full:
	// full depends on 46 and looks wrong below it, while compact only needs
	// room for a label, a percentage and a short gauge per line.
	compactMinPanel = 30
)

// Layout names: what --layout accepts, what the l key will cycle, and what the
// footer will show when the user has switched off the default.
// A layout is two independent choices: which panel renderer draws a source
// (panel or panelCompact) and how the panels are arranged (grid or one per
// row). The four names are the four combinations; View reads the two
// dimensions separately rather than branching per name.
const (
	layoutFull            = "full"
	layoutCompact         = "compact"
	layoutVertical        = "vertical"
	layoutCompactVertical = "compact-vertical"
)

var layouts = []string{layoutFull, layoutCompact, layoutVertical, layoutCompactVertical}

// isCompact reports whether a layout draws its panels with panelCompact.
func isCompact(name string) bool {
	return name == layoutCompact || name == layoutCompactVertical
}

// isStacked reports whether a layout puts one panel per row instead of
// packing a grid.
func isStacked(name string) bool {
	return name == layoutVertical || name == layoutCompactVertical
}

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

// gaugeCell is one cell of a gauge bar: the glyph to print, the colour it is
// drawn in, and, for a partial cell only, the background it already sits on
// (the lit fraction is drawn over the dark track).
type gaugeCell struct {
	glyph  string
	colour rgb
	bg     *rgb
}

// gaugeCells computes the cells of a continuous bar whose colour is a
// function of *position*, not of the value: the tail of every bar is red, so a
// bar creeping into the red end is legible at a glance without reading the
// number. The unfilled part is the same gradient darkened, which previews
// where the bar is heading.
func gaugeCells(width int, pct float64) []gaugeCell {
	if width < 1 {
		return nil
	}
	exact := math.Max(0, math.Min(100, pct)) / 100 * float64(width)
	full := int(exact)
	frac := exact - float64(full)

	cells := make([]gaugeCell, width)
	for i := 0; i < width; i++ {
		colour := gradientAt((float64(i) + 0.5) / float64(width))
		switch {
		case i < full:
			cells[i] = gaugeCell{glyph: fullBlock(), colour: colour}
		case i == full && frac >= 0.125:
			// A partial cell: the lit fraction over the dark track, so a bar
			// that is barely moving still shows movement.
			index := int(frac*float64(len(partialRunes))) - 1
			if index < 0 {
				index = 0
			} else if index >= len(partialRunes) {
				index = len(partialRunes) - 1
			}
			dimmed := colour.dim(0.25)
			cells[i] = gaugeCell{glyph: partialBlock(index), colour: colour, bg: &dimmed}
		default:
			cells[i] = gaugeCell{glyph: emptyBlock(), colour: colour.dim(0.25)}
		}
	}
	return cells
}

func gauge(width int, pct float64) string {
	var out strings.Builder
	for _, cell := range gaugeCells(width, pct) {
		style := lipgloss.NewStyle().Foreground(cell.colour.color())
		if cell.bg != nil {
			style = style.Background(cell.bg.color())
		}
		out.WriteString(style.Render(cell.glyph))
	}
	return out.String()
}

// gaugeWithForecast draws a gauge with a short forecast printed over its
// right-hand end. Each character occupies one of the bar's own cells: the
// cell's usual colour becomes the character's background (the gradient fill
// for a filled cell, the dimmed track for an unfilled one) and the character
// is drawn in overlayContrast of that same colour. A character over the fill
// therefore reads dark-on-orange and one past the fill light-on-dark, from
// one rule with no special-casing of where the fill ends. The bar carries no
// terminal background of its own, so the character needs the explicit
// background -- swapping the glyph alone would print it in the bar's own
// colour, invisible against it.
//
// The overlay replaces glyphs in place and never inserts or removes a cell:
// the result is exactly width display cells, the invariant the panel
// assembly depends on. The fit rule guards the rest of the bar: if the text
// plus gaugeMinRun of untouched bar does not fit, the bar is drawn plain. It
// is never truncated -- a half-written duration ("full in 2d" meaning
// "2d 3h") is wrong, not merely short.
func gaugeWithForecast(width int, pct float64, forecast string) string {
	if lipgloss.Width(forecast)+gaugeMinRun > width {
		return gauge(width, pct)
	}
	cells := gaugeCells(width, pct)
	runes := []rune(forecast)
	start := width - len(runes) // first cell the text occupies
	var out strings.Builder
	for i, cell := range cells {
		if i < start {
			style := lipgloss.NewStyle().Foreground(cell.colour.color())
			if cell.bg != nil {
				style = style.Background(cell.bg.color())
			}
			out.WriteString(style.Render(cell.glyph))
			continue
		}
		// The background the character lands on is what is left of the cell
		// under it: the fill for a full cell, and the dimmed track for a
		// track cell or a partial one, where the character covers the lit
		// fraction entirely.
		bg := cell.colour
		if cell.bg != nil {
			bg = *cell.bg
		}
		out.WriteString(lipgloss.NewStyle().
			Foreground(overlayContrast(bg).color()).
			Background(bg.color()).
			Render(string(runes[i-start])))
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
		for _, window := range snap.Windows {
			body = append(body, windowLines(content, snap.Identity(), window, history, now)...)
		}
		// A block and a warning are independent facts -- the only warning the
		// codex scanner raises is that a log is cut off, which is a caveat on
		// everything else in the panel, including a block riding on that same
		// truncated log. Neither should swallow the other.
		if snap.LimitReached != "" {
			body = append(body, currentTheme().err.Render(truncate("blocked: "+humanizeReason(snap.LimitReached), content)))
		}
		if snap.Warning != "" {
			for _, line := range wrap(snap.Warning, content) {
				body = append(body, currentTheme().wrn.Render(line))
			}
		}
	}

	footer := panelFooter(snap, loading, now)
	chip, footnote := panelChipFootnote(snap)
	return box(width, title, chip, body, footer, footnote)
}

// compactForecastHeadline is the part of projectionText's headline that
// belongs inside the compact bar: the outcome ("full in 2d 3h", "~69% at
// reset", "steady"), not the rate. Compact is short of room by definition,
// and the outcome is the part that carries the warning.
func compactForecastHeadline(text string) string {
	if idx := strings.Index(text, "\u2192 "); idx >= 0 {
		return text[idx+len("\u2192 "):]
	}
	return text
}

// compactColumns is the pair of column widths -- widest label, widest
// percentage -- shared by every compact row on screen, so every gauge bar
// starts and ends at the same column regardless of which panel or window it
// belongs to. It is computed once, globally, by computeCompactColumns.
type compactColumns struct {
	label int
	pct   int
}

// compactWindowPercent is the percentage text a compact row prints: a dash
// for an expired window, matching the value withheld from its gauge.
func compactWindowPercent(window Window) string {
	if window.Expired {
		return "—"
	}
	return percentText(window.Percent)
}

// computeCompactColumns finds the widest label and widest percentage across
// every window panelCompact will actually draw. It is global across every
// panel that will be rendered, not per panel: a Codex panel with no "Weekly ·
// Fable" window must still align its bars with a Claude panel that has one,
// so the maxima come from every source on screen, not each panel's own
// windows. A source with no snapshot yet, or one whose snapshot failed,
// renders no gauge rows and so contributes no window.
//
// minContent is the content width (a panel's outer width, minus its
// borders) of the narrowest panel that will actually be drawn this frame.
// Both adjustments below are derived from it, once, rather than from each
// panel's own width: a window's label is server-supplied (see sources.go)
// and can be arbitrarily long, and two panels that differ by only a cell or
// two -- rowWidths gives an unevenly-divided grid row's last column
// whatever the division rounded away -- must still shrink their label
// column by the same amount, or their bars stop starting at the same offset
// from their own box.
func computeCompactColumns(sources []sourceState, minContent int) compactColumns {
	var cols compactColumns
	for _, source := range sources {
		snap := source.snap
		if snap == nil || snap.Err != nil {
			continue
		}
		for _, window := range snap.Windows {
			cols.label = max(cols.label, lipgloss.Width(window.Label))
			cols.pct = max(cols.pct, lipgloss.Width(compactWindowPercent(window)))
		}
	}
	// A single long label -- one source concatenating scope and surface onto
	// its window label (sources.go) -- must not claim so much of the row that
	// every panel's bar collapses below gaugeMinPad. Give way only by the
	// deficit, i.e. only when the label actually leaves the narrowest panel
	// short of gaugeMinPad, rather than to a fixed fraction of minContent: a
	// flat fraction cuts into labels even when the bar already has room to
	// spare, and does not track gaugeMinPad's own threshold anyway. This is
	// the same rescue compactWindowLine used to compute per panel, moved
	// here so every panel shrinks its label column by the same amount.
	//
	// This only guards gaugeMinPad, the floor a bar needs to read as a bar at
	// all. gaugeWithForecast's own fit rule is stricter -- it wants the
	// forecast text plus gaugeMinRun of untouched bar -- and this rescue does
	// not weigh it: a bar can clear gaugeMinPad and still be too narrow for
	// the overlay, which then falls back to plain (gaugeWithForecast). That is
	// an accepted trade, not an oversight -- uniform bar widths across every
	// panel on screen and the longest bar the narrowest label would allow
	// cannot both hold -- but it does mean a wide label elsewhere on screen
	// can silently cost a narrower panel its forecast overlay.
	if deficit := gaugeMinPad - (minContent - cols.label - cols.pct - 2); deficit > 0 {
		cols.label -= deficit
		if cols.label < 0 {
			cols.label = 0
		}
	}
	return cols
}

// compactRenderPanel builds the renderPanel closure View uses for a compact
// layout: compactColumns computed once, globally, from every source that
// will be drawn and from minContent, the content width of the narrowest
// panel this frame will actually draw (see computeCompactColumns).
func compactRenderPanel(sources []sourceState, minContent int) func(int, *Snapshot, *History, time.Time, bool) string {
	compactCols := computeCompactColumns(sources, minContent)
	return func(width int, snap *Snapshot, history *History, now time.Time, loading bool) string {
		return panelCompact(width, snap, history, now, loading, compactCols)
	}
}

// compactWindowLine squeezes one window onto a single line: label, percentage
// and gauge sharing a row. It is the whole of the compact layout's height
// economy -- no sparkline, no detail line, no blank spacers between windows --
// while keeping the two things that must survive: the window's current
// percentage (a dash when the window expired), and its bar, which is still
// the fastest read of which window is red. When the window has a burn
// projection, the forecast's headline is printed inside the bar itself, the
// one place a one-line panel has room for it (gaugeWithForecast) -- provided
// the bar is wide enough; cols' shared label and percentage columns (see
// computeCompactColumns) can leave a narrower panel's bar below what
// gaugeWithForecast's own fit rule needs, in which case it draws plain, with
// no signal to the caller that the overlay was dropped.
//
// label and pct are padded to cols' widths -- computed once, globally, across
// every window that will be drawn -- rather than to this window's own text,
// so every bar on screen shares a left and right edge. If the narrowest
// panel on screen is too narrow for both columns and a gauge of gaugeMinPad
// cells, cols' label column has already given way to make room (see
// computeCompactColumns): the percentage is never truncated.
func compactWindowLine(width int, identity string, window Window, history *History, now time.Time, cols compactColumns) string {
	pct, barPct := compactWindowPercent(window), window.Percent
	if window.Expired {
		barPct = 0
	}
	var pctStyled string
	if window.Expired {
		pctStyled = currentTheme().dim.Render(pct)
	} else {
		pctStyled = lipgloss.NewStyle().Foreground(gradientAt(window.Percent / 100).color()).Bold(true).Render(pct)
	}
	label := currentTheme().txt.Render(window.Label)

	// cols.label and cols.pct are already shrunk, once, to fit the narrowest
	// panel that will be drawn this frame (see computeCompactColumns), so
	// every panel uses them as-is rather than deriving its own rescue from
	// its own width: two panels a cell or two apart would otherwise shrink
	// by different amounts and their bars would stop sharing a left edge.
	labelCol, pctCol := cols.label, cols.pct

	gaugeWidth := width - labelCol - pctCol - 2
	if gaugeWidth < 1 {
		gaugeWidth = 1
	}
	bar := gauge(gaugeWidth, barPct)
	// An expired window already withholds its percentage above; a forecast is
	// itself a percentage claim, so it is withheld there too, exactly as the
	// full layout withholds its detail-line projection.
	if !window.Expired {
		projection := history.Project(identity, window, now)
		if text, _, _ := projectionText(projection, window.ResetsAt, window.Length, now); text != "" {
			bar = gaugeWithForecast(gaugeWidth, barPct, compactForecastHeadline(text))
		}
	}
	return pad(label, labelCol) + " " + padLeft(pctStyled, pctCol) + " " + bar
}

// panelCompact renders one source with the layout that squeezes everything
// into a smaller terminal: one line per window, the error squashed to a line,
// no blank spacers. Every window's percentage and any blocked or error state
// still appear -- a compact panel that hides a block is worse than one that
// does not fit.
func panelCompact(width int, snap *Snapshot, history *History, now time.Time, loading bool, cols compactColumns) string {
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
			body = append(body, compactWindowLine(content, snap.Identity(), window, history, now, cols))
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
		{"R", "refresh Claude past its cache and any 429 backoff (hits the API)"},
		{"l", "cycle layout: " + strings.Join(layouts, ", ")},
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
		name := m.layoutName()
		renderPanel, min := panel, minPanel
		if isCompact(name) {
			// Compact reuses the grid's packing arithmetic with its own, smaller
			// floor; the equal-width rule the trailing row obeys is full-grid
			// guidance, not a constraint this layout needs.
			min = compactMinPanel
		}
		if isStacked(name) {
			// One panel per row, at any width: no grid, no side-by-side packing.
			// The width still clamps to maxLayout like everything else -- a
			// panel stretched across a very wide terminal reads badly -- but it
			// is never split. This layout is about stacking, not filling.
			if isCompact(name) {
				renderPanel = compactRenderPanel(m.sources, width-4)
			}
			for _, source := range m.sources {
				rows = append(rows, renderPanel(width, source.snap, m.history, m.now, source.loading))
			}
		} else {
			cols := gridColumns(width, n, min)
			// Sized once for a full row of cols panels: a short trailing row (the
			// last row of an n not divisible by cols) gets the same per-panel
			// width as every row above it, rather than stretching to fill the
			// width, so a gauge's bar length stays comparable at a glance across
			// every panel on screen. The trailing row is simply narrower than the
			// terminal; nothing fills the gap.
			colWidths := rowWidths(width, cols)
			if isCompact(name) {
				// The column widths are computed once here, across every source
				// that will be drawn, so every bar on screen shares a left and
				// right edge even across panels -- panelCompact and
				// compactWindowLine only ever see one panel or one window and
				// cannot compute this themselves. minContent is derived from
				// colWidths, the narrowest panel this frame will actually draw,
				// so a panel a cell or two narrower than its neighbour (rowWidths'
				// last-column remainder) still shrinks its label column by the
				// same amount as every other panel.
				minWidth := colWidths[0]
				for _, w := range colWidths {
					if w < minWidth {
						minWidth = w
					}
				}
				renderPanel = compactRenderPanel(m.sources, minWidth-4)
			}
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
