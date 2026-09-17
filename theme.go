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

// luminance is perceived luminance on 0..255 channels with the standard
// Rec.709 weights.
func (c rgb) luminance() float64 {
	return 0.2126*(c.r/255) + 0.7152*(c.g/255) + 0.0722*(c.b/255)
}

// parseHexRGB parses a "#rrggbb" colour into rgb. It returns nil for anything
// it does not recognise, so callers keep their fallback.
func parseHexRGB(s string) *rgb {
	s = strings.TrimPrefix(s, "#")
	if len(s) != 6 {
		return nil
	}
	var c rgb
	var r, g, b uint64
	if _, err := fmt.Sscanf(s, "%2x%2x%2x", &r, &g, &b); err != nil {
		return nil
	}
	c = rgb{float64(r), float64(g), float64(b)}
	return &c
}

// overlayEndpoints are the two ink colours the gauge forecast overlay picks
// from: the theme's near-black and near-white, taken from its body-text
// colour. The txt style is an adaptive colour, so its light-background
// variant is the theme's dark ink and its dark-background variant the theme's
// light ink. Taking them from the theme rather than hardcoding #000/#fff
// keeps a non-black-and-white theme from getting hard black or hard white
// stamped into its bars. The literals survive only as the fallback for a
// theme whose txt style is not an adaptive hex colour.
func overlayEndpoints() (dark, light rgb) {
	dark, light = rgb{0, 0, 0}, rgb{255, 255, 255}
	if ac, ok := currentTheme().txt.GetForeground().(lipgloss.AdaptiveColor); ok {
		if c := parseHexRGB(ac.Light); c != nil {
			dark = *c
		}
		if c := parseHexRGB(ac.Dark); c != nil {
			light = *c
		}
	}
	return dark, light
}

// overlayContrast returns the colour to print a character over a gauge cell
// drawn in c: the theme's dark ink when c is light, the light ink when c is
// dark, switching at the middle of the luminance range.
//
// This is luminance, not inversion. The request was phrased as "invert the
// background colour", and inversion does stand the text out against the cell
// at the extremes -- but only there: inverting a mid-tone returns another
// mid-tone, so text over the amber middle of the default gradient
// (#e9c446) would land at roughly the same luminance as the amber and vanish
// into it. A luminance threshold always returns an endpoint on the opposite
// side of the bar's range, so the character stands out at every position of
// the gradient, mid-tone included.
//
// The 0.5 threshold is the midpoint of the linear 0..1 scale the weights
// produce: every theme's gradient stops sit clearly on one side or the other
// (default green through orange land at 0.60-0.77, red at 0.45, and the dim
// track never above 0.19), so the switch happens between the orange and red
// stops where it should, not inside a run of similar cells.
func overlayContrast(c rgb) rgb {
	dark, light := overlayEndpoints()
	if c.luminance() < 0.5 {
		return light
	}
	return dark
}

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
	{
		// High contrast: near-pure text colours and a saturated gradient, so
		// every element is as loud as the terminal allows.
		name: "contrast",
		dim:  fg(lipgloss.AdaptiveColor{Light: "#4a5160", Dark: "#b8c0cc"}),
		mut:  fg(lipgloss.AdaptiveColor{Light: "#3a4150", Dark: "#c8d0dc"}),
		txt:  fg(lipgloss.AdaptiveColor{Light: "#000000", Dark: "#ffffff"}),
		key:  fg(lipgloss.AdaptiveColor{Light: "#0033cc", Dark: "#4d9fff"}),
		err:  fg(lipgloss.Color("#ff2222")),
		wrn:  fg(lipgloss.Color("#ff9900")),
		brd:  fg(lipgloss.AdaptiveColor{Light: "#333945", Dark: "#a8b2c0"}),
		gradient: [5]rgb{
			{0x12, 0xe0, 0x5a}, // 0%   green
			{0x7a, 0xe8, 0x12}, // 25%  lime
			{0xff, 0xd4, 0x00}, // 50%  yellow
			{0xff, 0x95, 0x00}, // 75%  orange
			{0xff, 0x30, 0x30}, // 100% red
		},
	},
	{
		// Muted: low-contrast, desaturated neutrals; the gauge still moves
		// calm sage to alarm rose so the bars keep their reading.
		name: "muted",
		dim:  fg(lipgloss.AdaptiveColor{Light: "#8a8f98", Dark: "#767c88"}),
		mut:  fg(lipgloss.AdaptiveColor{Light: "#949aa4", Dark: "#6f7684"}),
		txt:  fg(lipgloss.AdaptiveColor{Light: "#3a3f47", Dark: "#c9ccd2"}),
		key:  fg(lipgloss.AdaptiveColor{Light: "#5f7a99", Dark: "#8fa3bd"}),
		err:  fg(lipgloss.Color("#c96a6a")),
		wrn:  fg(lipgloss.Color("#c99a62")),
		brd:  fg(lipgloss.AdaptiveColor{Light: "#a9adb4", Dark: "#565c66"}),
		gradient: [5]rgb{
			{0x86, 0xb8, 0x9b}, // 0%   sage
			{0xa8, 0xbc, 0x7e}, // 25%  olive
			{0xc9, 0xb3, 0x78}, // 50%  sand
			{0xc4, 0x93, 0x6f}, // 75%  clay
			{0xc0, 0x70, 0x70}, // 100% rose
		},
	},
	{
		// Warm: cream-and-amber neutrals; the gauge runs olive through
		// amber into red-orange, staying in the warm family.
		name: "warm",
		dim:  fg(lipgloss.AdaptiveColor{Light: "#8a7a68", Dark: "#a08d78"}),
		mut:  fg(lipgloss.AdaptiveColor{Light: "#7d6f5d", Dark: "#ab9a84"}),
		txt:  fg(lipgloss.AdaptiveColor{Light: "#35291e", Dark: "#f2e8d8"}),
		key:  fg(lipgloss.AdaptiveColor{Light: "#a0522d", Dark: "#e8a06a"}),
		err:  fg(lipgloss.Color("#cf3f2a")),
		wrn:  fg(lipgloss.Color("#d98c2b")),
		brd:  fg(lipgloss.AdaptiveColor{Light: "#b5a48d", Dark: "#6e5f4e"}),
		gradient: [5]rgb{
			{0x6f, 0xae, 0x63}, // 0%   olive green
			{0xb8, 0xb0, 0x4a}, // 25%  chartreuse
			{0xdf, 0xa8, 0x3e}, // 50%  amber
			{0xd9, 0x7b, 0x2e}, // 75%  orange
			{0xd4, 0x3f, 0x2a}, // 100% red-orange
		},
	},
	{
		// Cool: blue-and-teal neutrals; the gauge starts teal and warms
		// only in its final stops, so the alarm end still reads hot.
		name: "cool",
		dim:  fg(lipgloss.AdaptiveColor{Light: "#6d7f8f", Dark: "#7e93a5"}),
		mut:  fg(lipgloss.AdaptiveColor{Light: "#5f7385", Dark: "#8aa2b5"}),
		txt:  fg(lipgloss.AdaptiveColor{Light: "#16222e", Dark: "#dce8f2"}),
		key:  fg(lipgloss.AdaptiveColor{Light: "#2a6db5", Dark: "#6cc0ff"}),
		err:  fg(lipgloss.Color("#ff5c5c")),
		wrn:  fg(lipgloss.Color("#f0a04a")),
		brd:  fg(lipgloss.AdaptiveColor{Light: "#93a5b4", Dark: "#4d6478"}),
		gradient: [5]rgb{
			{0x2f, 0xc6, 0xb0}, // 0%   teal
			{0x57, 0xc2, 0x6e}, // 25%  sea green
			{0x9a, 0xc1, 0x4d}, // 50%  yellow-green
			{0xd0, 0x8b, 0x3c}, // 75%  amber
			{0xe0, 0x48, 0x48}, // 100% red
		},
	},
	{
		// Near-monochrome: greys throughout. The error colour is the one
		// place saturation is allowed; the warning keeps a muted amber so it
		// still stands apart from the body text. The gauge is a brightness
		// ramp, calm dark to alarm bright, which reads on a dark terminal.
		name: "mono",
		dim:  fg(lipgloss.AdaptiveColor{Light: "#6b6b6b", Dark: "#8a8a8a"}),
		mut:  fg(lipgloss.AdaptiveColor{Light: "#787878", Dark: "#7e7e7e"}),
		txt:  fg(lipgloss.AdaptiveColor{Light: "#111111", Dark: "#f0f0f0"}),
		key:  fg(lipgloss.AdaptiveColor{Light: "#333333", Dark: "#bbbbbb"}),
		err:  fg(lipgloss.Color("#e55353")),
		wrn:  fg(lipgloss.Color("#cf9a4f")),
		brd:  fg(lipgloss.AdaptiveColor{Light: "#9a9a9a", Dark: "#555555"}),
		gradient: [5]rgb{
			{0x4a, 0x4a, 0x4a}, // 0%   calm dark grey
			{0x66, 0x66, 0x66}, // 25%
			{0x88, 0x88, 0x88}, // 50%
			{0xaa, 0xaa, 0xaa}, // 75%
			{0xf0, 0xf0, 0xf0}, // 100% alarm bright
		},
	},
	{
		// High saturation: neon palette; the most colourful of the set.
		name: "vivid",
		dim:  fg(lipgloss.AdaptiveColor{Light: "#5560c8", Dark: "#8890ff"}),
		mut:  fg(lipgloss.AdaptiveColor{Light: "#7040b8", Dark: "#b088ff"}),
		txt:  fg(lipgloss.AdaptiveColor{Light: "#1a1040", Dark: "#f0ecff"}),
		key:  fg(lipgloss.AdaptiveColor{Light: "#c020c0", Dark: "#ff7ce8"}),
		err:  fg(lipgloss.Color("#ff1f45")),
		wrn:  fg(lipgloss.Color("#ff8c00")),
		brd:  fg(lipgloss.AdaptiveColor{Light: "#8878e0", Dark: "#6c5ce7"}),
		gradient: [5]rgb{
			{0x00, 0xff, 0xa3}, // 0%   spring green
			{0x7d, 0xff, 0x3a}, // 25%  neon lime
			{0xff, 0xe6, 0x00}, // 50%  neon yellow
			{0xff, 0x95, 0x00}, // 75%  orange
			{0xff, 0x20, 0x40}, // 100% hot pink-red
		},
	},
}

// themeIndex is the index into themes of the current theme. main applies the
// persisted theme here at startup and the t key changes it, so the choice
// survives across runs.
//
// Safety invariant: themeIndex may only be written from main at startup
// (before the program starts or before the single snapshot frame renders),
// from model.Update (the t key), and from renderSnapshot (the --theme flag,
// after which the process exits), and read from View() and its helpers,
// plus once from newModel at construction, before any Update has run. That
// is safe because rendering is single-goroutine: Bubble Tea runs Update and
// View on the same goroutine and nothing inside a tea.Cmd touches a style,
// and there are no t.Parallel() tests in this repo, so no test can race on
// it either. A future t.Parallel() test or a rendering goroutine would break
// this silently; this comment is the only warning a reader will get.
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
