// quotatop -- a live TUI for Claude and Codex quota, in one window.
//
// Both panels are read directly by this process: Claude from the OAuth usage
// endpoint (token from ~/.claude/.credentials.json, 10-minute on-disk cache),
// Codex from the newest rate-limit rows in the local session logs.
package main

import (
	"encoding/json"
	"flag"
	"fmt"
	"os"
	"strings"
	"sync"
	"time"

	"github.com/charmbracelet/bubbles/spinner"
	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"
	"github.com/muesli/termenv"
	"golang.org/x/term"
)

type tickMsg time.Time

type snapshotMsg struct{ snap Snapshot }

type model struct {
	width, height int
	host          string
	interval      time.Duration
	history       *History
	spinner       spinner.Model
	now           time.Time

	claude, codex               *Snapshot
	loadingClaude, loadingCodex bool
	claudeDue, codexDue         time.Time
	showHelp                    bool
}

func newModel(interval time.Duration, history *History) model {
	spin := spinner.New()
	spin.Spinner = spinner.Dot
	spin.Style = styleKey

	host, err := os.Hostname()
	if err != nil {
		host = ""
	}
	now := time.Now()
	return model{
		host: host, interval: interval, history: history, spinner: spin, now: now,
		// Init fires both fetches immediately, so the model has to start in the
		// loading state: otherwise the first tick sees idle sources that are
		// already due and fires a duplicate pair.
		loadingClaude: true, loadingCodex: true,
		claudeDue: now.Add(interval),
		codexDue:  now.Add(interval),
	}
}

func (m model) busy() bool { return m.loadingClaude || m.loadingCodex }

func (m model) nextRefresh() time.Time {
	if m.claudeDue.Before(m.codexDue) {
		return m.claudeDue
	}
	return m.codexDue
}

func claudeCmd(fresh bool) tea.Cmd {
	return func() tea.Msg { return snapshotMsg{fetchClaude(fresh)} }
}

func codexCmd() tea.Cmd {
	return func() tea.Msg { return snapshotMsg{fetchCodex()} }
}

func tick() tea.Cmd {
	return tea.Tick(time.Second, func(t time.Time) tea.Msg { return tickMsg(t) })
}

func (m model) Init() tea.Cmd {
	return tea.Batch(tick(), m.spinner.Tick, claudeCmd(false), codexCmd())
}

// refresh starts both fetches. Already-running fetches are left alone so a held
// key cannot pile up scans.
func (m *model) refresh(fresh bool) tea.Cmd {
	var cmds []tea.Cmd
	spinning := m.busy() // a second Tick chain would double the spinner's speed
	if !m.loadingClaude {
		m.loadingClaude = true
		cmds = append(cmds, claudeCmd(fresh))
	}
	if !m.loadingCodex {
		m.loadingCodex = true
		cmds = append(cmds, codexCmd())
	}
	if !spinning && m.busy() {
		cmds = append(cmds, m.spinner.Tick)
	}
	return tea.Batch(cmds...)
}

func (m model) Update(msg tea.Msg) (tea.Model, tea.Cmd) {
	switch msg := msg.(type) {
	case tea.WindowSizeMsg:
		m.width, m.height = msg.Width, msg.Height
		return m, nil

	case tea.KeyMsg:
		switch msg.String() {
		case "q", "esc", "ctrl+c":
			return m, tea.Quit
		case "r":
			return m, m.refresh(false)
		case "R":
			return m, m.refresh(true)
		case "?":
			m.showHelp = !m.showHelp
			return m, nil
		}
		return m, nil

	case tickMsg:
		m.now = time.Time(msg)
		var cmds []tea.Cmd
		spinning := m.busy() // an idle spinner must not be started twice
		if !m.loadingClaude && !m.now.Before(m.claudeDue) {
			m.loadingClaude = true
			cmds = append(cmds, claudeCmd(false))
		}
		if !m.loadingCodex && !m.now.Before(m.codexDue) {
			m.loadingCodex = true
			cmds = append(cmds, codexCmd())
		}
		if !spinning && m.busy() {
			cmds = append(cmds, m.spinner.Tick)
		}
		return m, tea.Batch(append(cmds, tick())...)

	case snapshotMsg:
		snap := msg.snap
		m.record(&snap)
		due := time.Now().Add(m.interval)
		if snap.Source == "claude" {
			m.claude, m.loadingClaude, m.claudeDue = &snap, false, due
		} else {
			m.codex, m.loadingCodex, m.codexDue = &snap, false, due
		}
		return m, nil

	case spinner.TickMsg:
		if !m.busy() {
			return m, nil // idle redraws stay at one per second
		}
		var cmd tea.Cmd
		m.spinner, cmd = m.spinner.Update(msg)
		return m, cmd
	}
	return m, nil
}

// recordSnapshot files every window of one snapshot into history, stamped
// with when the reading was observed rather than when it was collected, so
// the burn rate is computed against real elapsed time.
func recordSnapshot(history *History, snap Snapshot) {
	if snap.Err != nil {
		return
	}
	at := snap.Observed
	if at.IsZero() {
		at = snap.At
	}
	for _, window := range snap.Windows {
		history.Add(historyKey(snap.Identity(), window.Key), at, window.Percent)
	}
}

// record files a snapshot into the model's own history.
func (m model) record(snap *Snapshot) {
	recordSnapshot(m.history, *snap)
}

func terminalWidth(fallback int) int {
	for _, fd := range []int{int(os.Stdout.Fd()), int(os.Stderr.Fd())} {
		if width, _, err := term.GetSize(fd); err == nil && width > 0 {
			return width
		}
	}
	return fallback
}

const usageText = `quotatop -- live Claude and Codex quota in one window.

Usage: quotatop [options]

Keys:  r refresh · R refresh Claude past its cache · ? keys · q quit

Options:
`

func main() {
	// The config file supplies defaults for the QUOTATOP_* environment
	// variables; a variable on the command line always wins. It is loaded
	// before anything reads a setting, and every failure in it is invisible:
	// a monitor must start with or without it.
	configValues = loadConfig(configPath())

	flag.Usage = func() {
		fmt.Fprint(flag.CommandLine.Output(), usageText)
		flag.PrintDefaults()
	}
	interval := flag.Duration("interval", 20*time.Second, "how often to poll both sources")
	snapshot := flag.Bool("snapshot", false, "render one frame to stdout and exit (no TUI)")
	jsonOutput := flag.Bool("json", false, "write one JSON document to stdout and exit (no TUI)")
	fresh := flag.Bool("fresh", false, "bypass the Claude 10-minute quota cache on the first read")
	width := flag.Int("width", 0, "width for --snapshot (0 = detect, fall back to the widest layout)")
	noHistory := flag.Bool("no-history", false, "do not read or write the trend history file")
	flag.Parse()
	if flag.NArg() > 0 {
		fmt.Fprintf(os.Stderr, "quotatop: unexpected argument %q\n", flag.Arg(0))
		flag.Usage()
		os.Exit(2)
	}
	if *interval < time.Second {
		fmt.Fprintln(os.Stderr, "quotatop: --interval must be at least 1s")
		os.Exit(2)
	}
	if *jsonOutput && *snapshot {
		fmt.Fprintln(os.Stderr, "quotatop: --json and --snapshot cannot be used together")
		os.Exit(2)
	}

	path := defaultHistoryPath()
	if *noHistory {
		path = ""
	}
	if *jsonOutput {
		os.Exit(renderJSON(loadAppendOnlyHistory(path), *fresh, !*noHistory))
	}

	m := newModel(*interval, loadHistory(path))

	if *snapshot {
		os.Exit(renderSnapshot(m, *width, *fresh))
	}

	if _, err := tea.NewProgram(m, tea.WithAltScreen()).Run(); err != nil {
		fmt.Fprintln(os.Stderr, "quotatop:", err)
		os.Exit(1)
	}
}

// renderJSON fetches the two independent sources concurrently so status-line
// callers do not wait for one source before reading the other.
func renderJSON(history *History, fresh, recordHistory bool) int {
	var claude, codex Snapshot
	var wait sync.WaitGroup
	wait.Add(2)
	go func() {
		defer wait.Done()
		claude = fetchClaude(fresh)
	}()
	go func() {
		defer wait.Done()
		codex = fetchCodex()
	}()
	wait.Wait()
	recordJSONSnapshots(history, []Snapshot{claude, codex}, recordHistory)

	encoder := json.NewEncoder(os.Stdout)
	encoder.SetIndent("", "  ")
	if err := encoder.Encode(encodeJSON([]Snapshot{claude, codex}, history, time.Now())); err != nil {
		fmt.Fprintln(os.Stderr, "quotatop:", err)
		return 1
	}
	return 0
}

func recordJSONSnapshots(history *History, snaps []Snapshot, record bool) {
	if !record {
		return
	}
	history.Cleanup()
	for _, snap := range snaps {
		recordSnapshot(history, snap)
	}
}

// renderSnapshot prints a single frame. Handy for a quick non-interactive look,
// and it is how the layout is checked without driving a terminal.
func renderSnapshot(m model, width int, fresh bool) int {
	if os.Getenv("NO_COLOR") != "" {
		lipgloss.SetColorProfile(termenv.Ascii)
	} else if os.Getenv("CLICOLOR_FORCE") != "" {
		lipgloss.SetColorProfile(termenv.TrueColor)
	}
	if width <= 0 {
		// Fall back to the widest layout rather than a narrower guess: with no
		// terminal to measure (a pipe, a hook, a status line) the detail lines
		// should still have room for the full reading, including the reset-gap
		// parenthetical, which needs roughly 110 columns side by side.
		width = terminalWidth(maxLayout)
	}
	m.width = width
	m.now = time.Now()

	claude := fetchClaude(fresh)
	codex := fetchCodex()
	m.record(&claude)
	m.record(&codex)
	m.claude, m.codex = &claude, &codex
	m.loadingClaude, m.loadingCodex = false, false

	fmt.Println(strings.TrimRight(m.View(), "\n"))
	if claude.Err != nil || codex.Err != nil {
		return 1
	}
	return 0
}
