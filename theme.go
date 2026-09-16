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

// gradientStops runs calm green -> amber -> alarm red across 0..100% usage.
var gradientStops = []rgb{
	{0x3f, 0xd1, 0x8b}, // 0%   green
	{0x86, 0xd7, 0x56}, // 25%  lime
	{0xe9, 0xc4, 0x46}, // 50%  amber
	{0xf0, 0x8a, 0x3c}, // 75%  orange
	{0xe5, 0x53, 0x53}, // 100% red
}

// gradientAt samples the gradient at t in [0,1].
func gradientAt(t float64) rgb {
	t = math.Max(0, math.Min(1, t))
	span := t * float64(len(gradientStops)-1)
	i := int(span)
	if i >= len(gradientStops)-1 {
		return gradientStops[len(gradientStops)-1]
	}
	f := span - float64(i)
	a, b := gradientStops[i], gradientStops[i+1]
	return rgb{a.r + (b.r-a.r)*f, a.g + (b.g-a.g)*f, a.b + (b.b-a.b)*f}
}

var (
	styleDim = lipgloss.NewStyle().Foreground(lipgloss.AdaptiveColor{Light: "#9096a2", Dark: "#6b7280"})
	styleMut = lipgloss.NewStyle().Foreground(lipgloss.AdaptiveColor{Light: "#6a7180", Dark: "#9aa3b2"})
	styleTxt = lipgloss.NewStyle().Foreground(lipgloss.AdaptiveColor{Light: "#1f2430", Dark: "#e6e9ef"})
	styleKey = lipgloss.NewStyle().Foreground(lipgloss.AdaptiveColor{Light: "#3b6ea5", Dark: "#7aa2f7"})
	styleErr = lipgloss.NewStyle().Foreground(lipgloss.Color("#e55353"))
	styleWrn = lipgloss.NewStyle().Foreground(lipgloss.Color("#f0a13c"))
	styleBrd = lipgloss.NewStyle().Foreground(lipgloss.AdaptiveColor{Light: "#b3b9c4", Dark: "#4c566a"})
)

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
	return styleBrd.Render(openRune) + leftPart + styleBrd.Render(strings.Repeat("─", fill)) +
		rightPart + styleBrd.Render(closeRune)
}

// box draws a rounded panel with an inset title (and optional chip) on the top
// edge and an inset footer on the bottom edge.
func box(width int, title, chip string, body []string, footerLeft, footerRight string) string {
	lines := []string{borderLine(width, "╭", "╮", title, chip)}
	bar := styleBrd.Render("│")
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
