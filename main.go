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
	"sort"
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

type snapshotMsg struct {
	index int
	snap  Snapshot
}

// sourceState is one source's own slice of the model: its latest snapshot,
// whether a fetch for it is in flight, when it is next due, and how to fetch
// it. The model holds an ordered slice of these instead of source-named
// fields, so it can carry any number of sources.
type sourceState struct {
	snap    *Snapshot
	loading bool
	due     time.Time
	fetch   func(fresh bool) Snapshot
}

type model struct {
	width, height int
	host          string
	interval      time.Duration
	history       *History
	spinner       spinner.Model
	now           time.Time

	sources  []sourceState
	showHelp bool
}

// defaultSources is the one place that names the sources this build knows how
// to fetch, so the TUI and the non-interactive --json path stay in lockstep:
// whatever is added here shows up in both. Claude's accounts (if any) always
// precede Codex's, preserving today's source order.
func defaultSources() []sourceState {
	return append(claudeSourceStates(), codexSourceStates()...)
}

// sortedLabels returns accounts' keys in ascending byte order, so the panel
// order is stable across runs and machines.
func sortedLabels(accounts map[string]string) []string {
	labels := make([]string, 0, len(accounts))
	for label := range accounts {
		labels = append(labels, label)
	}
	sort.Strings(labels)
	return labels
}

// claudeSourceStates builds one sourceState per QUOTATOP_CLAUDE_ACCOUNT_
// label, sorted by label. Declaring any account replaces today's single
// unnamed entry entirely: QUOTATOP_CLAUDE_CREDENTIALS is not consulted when
// accounts are configured.
func claudeSourceStates() []sourceState {
	accounts := settingsWithPrefix("QUOTATOP_CLAUDE_ACCOUNT_")
	if len(accounts) == 0 {
		return []sourceState{{fetch: fetchClaude}}
	}
	states := make([]sourceState, 0, len(accounts))
	for _, label := range sortedLabels(accounts) {
		source := defaultClaudeSource()
		source.credentialsPath = accounts[label]
		source.account = label
		states = append(states, sourceState{fetch: source.fetch})
	}
	return states
}

// codexSourceStates builds one sourceState per QUOTATOP_CODEX_ACCOUNT_
// label, sorted by label. Each value is parsed with the same glob-list
// grammar as QUOTATOP_CODEX_ROOTS. A named account replaces today's single
// unnamed entry entirely and is not layered onto the interactive default
// root, so two named accounts never scan and report the same session files.
func codexSourceStates() []sourceState {
	accounts := settingsWithPrefix("QUOTATOP_CODEX_ACCOUNT_")
	if len(accounts) == 0 {
		return []sourceState{{fetch: func(bool) Snapshot { return fetchCodex() }}}
	}
	states := make([]sourceState, 0, len(accounts))
	for _, label := range sortedLabels(accounts) {
		source := codexSource{extraRoots: parseCodexRoots(accounts[label]), account: label}
		states = append(states, sourceState{fetch: func(bool) Snapshot { return source.fetch() }})
	}
	return states
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
	due := now.Add(interval)
	sources := defaultSources()
	for i := range sources {
		// Init fires every fetch immediately, so each source has to start in
		// the loading state: otherwise the first tick sees idle sources that
		// are already due and fires a duplicate round.
		sources[i].loading, sources[i].due = true, due
	}
	return model{
		host: host, interval: interval, history: history, spinner: spin, now: now,
		sources: sources,
	}
}

func (m model) busy() bool {
	for _, source := range m.sources {
		if source.loading {
			return true
		}
	}
	return false
}

func (m model) nextRefresh() time.Time {
	next := m.now.Add(m.interval)
	found := false
	for _, source := range m.sources {
		if !found || source.due.Before(next) {
			next, found = source.due, true
		}
	}
	return next
}

func sourceCmd(index int, source sourceState, fresh bool) tea.Cmd {
	return func() tea.Msg { return snapshotMsg{index: index, snap: source.fetch(fresh)} }
}

func tick() tea.Cmd {
	return tea.Tick(time.Second, func(t time.Time) tea.Msg { return tickMsg(t) })
}

func (m model) Init() tea.Cmd {
	cmds := []tea.Cmd{tick(), m.spinner.Tick}
	for i, source := range m.sources {
		cmds = append(cmds, sourceCmd(i, source, false))
	}
	return tea.Batch(cmds...)
}

// refresh starts every source's fetch. Already-running fetches are left alone
// so a held key cannot pile up scans.
func (m *model) refresh(fresh bool) tea.Cmd {
	var cmds []tea.Cmd
	spinning := m.busy() // a second Tick chain would double the spinner's speed
	for i, source := range m.sources {
		if !source.loading {
			m.sources[i].loading = true
			cmds = append(cmds, sourceCmd(i, source, fresh))
		}
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
		for i, source := range m.sources {
			if !source.loading && !m.now.Before(source.due) {
				m.sources[i].loading = true
				cmds = append(cmds, sourceCmd(i, source, false))
			}
		}
		if !spinning && m.busy() {
			cmds = append(cmds, m.spinner.Tick)
		}
		return m, tea.Batch(append(cmds, tick())...)

	case snapshotMsg:
		if msg.index < 0 || msg.index >= len(m.sources) {
			return m, nil
		}
		snap := msg.snap
		m.record(&snap)
		source := &m.sources[msg.index]
		source.snap, source.loading, source.due = &snap, false, time.Now().Add(m.interval)
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

// renderJSON fetches every source from the same list the TUI uses, and
// concurrently, so status-line callers do not wait for one source before
// reading the other.
func renderJSON(history *History, fresh, recordHistory bool) int {
	sources := defaultSources()
	snaps := make([]Snapshot, len(sources))
	var wait sync.WaitGroup
	wait.Add(len(sources))
	for i, source := range sources {
		i, source := i, source
		go func() {
			defer wait.Done()
			snaps[i] = source.fetch(fresh)
		}()
	}
	wait.Wait()
	recordJSONSnapshots(history, snaps, recordHistory)

	encoder := json.NewEncoder(os.Stdout)
	encoder.SetIndent("", "  ")
	if err := encoder.Encode(encodeJSON(snaps, history, time.Now())); err != nil {
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

	failed := false
	for i := range m.sources {
		snap := m.sources[i].fetch(fresh)
		m.record(&snap)
		m.sources[i].snap, m.sources[i].loading = &snap, false
		if snap.Err != nil {
			failed = true
		}
	}

	fmt.Println(strings.TrimRight(m.View(), "\n"))
	if failed {
		return 1
	}
	return 0
}
