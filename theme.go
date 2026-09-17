package main

import (
	"fmt"
	"math"
	"strings"

	"github.com/charmbracelet/lipgloss"
	"github.com/muesli/termenv"
)

// rgb is a plain linear-ish RGB triple used for the gauge gradient. Gauges are
// coloured per cell, so the gradient has to be computed rather than picked from
// a palette of styles.
type rgb struct{ r, g, b float64 }

func (c rgb) hex() string {
	clamp := func(v float64) int { return int(math.Max(0, math.Min(255, math.Round(v)))) }
	return fmt.Sprintf("#%02x%02x%02x", clamp(c.r), clamp(c.g), clamp(c.b))
}

func (c rgb) color() lipgloss.Color { return lipgloss.Color(c.hex()) }

// dim scales a colour towards black. Used for the unfilled part of a gauge, so
// the track is a dark preview of the danger gradient rather than dead grey.
func (c rgb) dim(f float64) rgb { return rgb{c.r * f, c.g * f, c.b * f} }

// theme is one of the pre-built colour schemes: the seven text styles the
// renderer uses everywhere, plus the gauge's gradient stops. The gauges are
// the most colourful thing on screen, so a scheme recolors them too. name is
// an internal identifier for the source and the tests; it is never displayed.
type theme struct {
	name                              string
	dim, mut, txt, key, err, wrn, brd lipgloss.Style
	gradient                          [5]rgb // calm at 0% -> alarming at 100%
}

// fg builds one of a theme's text styles from a colour.
func fg(c lipgloss.TerminalColor) lipgloss.Style { return lipgloss.NewStyle().Foreground(c) }

// themes is every scheme the t key can cycle through. themes[0] carries
// exactly the pre-theme values.
var themes = []theme{
	{
		name: "default",
		dim:  fg(lipgloss.AdaptiveColor{Light: "#9096a2", Dark: "#6b7280"}),
		mut:  fg(lipgloss.AdaptiveColor{Light: "#6a7180", Dark: "#9aa3b2"}),
		txt:  fg(lipgloss.AdaptiveColor{Light: "#1f2430", Dark: "#e6e9ef"}),
		key:  fg(lipgloss.AdaptiveColor{Light: "#3b6ea5", Dark: "#7aa2f7"}),
		err:  fg(lipgloss.Color("#e55353")),
		wrn:  fg(lipgloss.Color("#f0a13c")),
		brd:  fg(lipgloss.AdaptiveColor{Light: "#b3b9c4", Dark: "#4c566a"}),
		gradient: [5]rgb{
			{0x3f, 0xd1, 0x8b}, // 0%   green
			{0x86, 0xd7, 0x56}, // 25%  lime
			{0xe9, 0xc4, 0x46}, // 50%  amber
			{0xf0, 0x8a, 0x3c}, // 75%  orange
			{0xe5, 0x53, 0x53}, // 100% red
		},
	},
}

// themeIndex is the index into themes of the current theme. It starts at 0 on
// every launch and is not persisted: the choice lasts as long as the process.
//
// Safety invariant: themeIndex may only be written from model.Update (the t
// key) and read from View() and its helpers, plus once from newModel at
// construction, before any Update has run. That is safe because rendering is
// single-goroutine: Bubble Tea runs Update and View on the same goroutine and
// nothing inside a tea.Cmd touches a style, and there are no t.Parallel()
// tests in this repo, so no test can race on it either. A future
// t.Parallel() test or a rendering goroutine would break this silently; this
// comment is the only warning a reader will get.
var themeIndex = 0

func currentTheme() theme { return themes[themeIndex] }

// cycleTheme advances to the next theme, wrapping at the end: the t key is
// the whole theme control -- cycle only, no menu, no selection.
func cycleTheme() { themeIndex = (themeIndex + 1) % len(themes) }

// gradientAt samples the current theme's gauge gradient at t in [0,1]. Every
// theme's stops run calm -> alarming across 0..100% usage: a full bar must
// always read more alarming than an empty one.
func gradientAt(t float64) rgb {
	stops := currentTheme().gradient
	t = math.Max(0, math.Min(1, t))
	span := t * float64(len(stops)-1)
	i := int(span)
	if i >= len(stops)-1 {
		return stops[len(stops)-1]
	}
	f := span - float64(i)
	a, b := stops[i], stops[i+1]
	return rgb{a.r + (b.r-a.r)*f, a.g + (b.g-a.g)*f, a.b + (b.b-a.b)*f}
}

// pad right-pads s with spaces to n display cells, or truncates it if it is
// wider. Every panel line goes through this so the right border stays aligned.
func pad(s string, n int) string {
	w := lipgloss.Width(s)
	if w > n {
		return truncate(s, n)
	}
	return s + strings.Repeat(" ", n-w)
}

// truncate cuts a styled string to n display cells. It walks runes and tracks
// ANSI escapes so colours survive the cut.
func truncate(s string, n int) string {
	if lipgloss.Width(s) <= n {
		return s
	}
	var out strings.Builder
	width, inEscape, styled := 0, false, false
	for _, r := range s {
		if r == '\x1b' {
			inEscape, styled = true, true
		}
		if inEscape {
			out.WriteRune(r)
			if r == 'm' {
				inEscape = false
			}
			continue
		}
		if width+1 > n {
			break
		}
		out.WriteRune(r)
		width++
	}
	if styled {
		// Only close a sequence that was actually opened: on a terminal with no
		// colour an unconditional reset would print as visible garbage.
		out.WriteString("\x1b[0m")
	}
	return out.String()
}

// borderLine builds one horizontal edge of a panel: a corner, an optional
// inset label on the left, fill, an optional inset label on the right, corner.
func borderLine(width int, openRune, closeRune string, left, right string) string {
	inner := width - 2
	leftPart, rightPart := "", ""
	if left != "" {
		leftPart = "─ " + left + " "
	}
	if right != "" {
		rightPart = " " + right + " ─"
	}
	fill := inner - lipgloss.Width(leftPart) - lipgloss.Width(rightPart)
	if fill < 0 {
		// Labels do not fit: drop the right one, then trim the left.
		rightPart = ""
		fill = inner - lipgloss.Width(leftPart)
		if fill < 0 {
			leftPart = truncate(leftPart, inner)
			fill = 0
		}
	}
	return currentTheme().brd.Render(openRune) + leftPart + currentTheme().brd.Render(strings.Repeat("─", fill)) +
		rightPart + currentTheme().brd.Render(closeRune)
}

// box draws a rounded panel with an inset title (and optional chip) on the top
// edge and an inset footer on the bottom edge.
func box(width int, title, chip string, body []string, footerLeft, footerRight string) string {
	lines := []string{borderLine(width, "╭", "╮", title, chip)}
	bar := currentTheme().brd.Render("│")
	for _, line := range body {
		lines = append(lines, bar+" "+pad(line, width-4)+" "+bar)
	}
	lines = append(lines, borderLine(width, "╰", "╯", footerLeft, footerRight))
	return strings.Join(lines, "\n")
}

// The gauge normally distinguishes used from remaining by colour alone, which
// reads as one smooth bar. On a terminal with no colour that would be a solid
// block of nothing, so fall back to distinct glyphs there.
func monochrome() bool { return lipgloss.ColorProfile() == termenv.Ascii }

func fullBlock() string { return "█" }

func emptyBlock() string {
	if monochrome() {
		return "░"
	}
	return "█"
}

func partialBlock(index int) string {
	if monochrome() {
		return "▌"
	}
	return string(partialRunes[index])
}
