package main

import (
	"bufio"
	"encoding/json"
	"math"
	"os"
	"path/filepath"
	"sort"
	"time"
)

// A sample is one observed percentage for one window. Only *changes* are
// stored: a window that sat at 40% for an hour is one sample, and the reader
// supplies the current value, so a flat stretch still reads as a zero rate.
type sample struct {
	T   time.Time `json:"-"`
	Sec int64     `json:"t"`
	Key string    `json:"k"`
	Pct float64   `json:"p"`
}

const (
	maxSamplesPerKey = 400
	compactAtLines   = 4000
	sampleMaxAge     = 9 * 24 * time.Hour // a touch over the longest (weekly) window
)

// historyKey names one window's slot in the trend history: the snapshot's
// identity (a source, or "source/account" once accounts exist) plus the
// window's own key. Every read and write site builds a key through this
// function, so the key format has exactly one definition to change.
func historyKey(identity, windowKey string) string {
	return identity + "/" + windowKey
}

// History keeps the recent trend of every window, on disk so a restart does not
// blank the sparklines and the burn rate.
type History struct {
	path    string
	data    map[string][]sample
	lines   int
	enabled bool
}

func defaultHistoryPath() string {
	if override := setting("QUOTATOP_HISTORY"); override != "" {
		return override
	}
	home, err := os.UserHomeDir()
	if err != nil {
		return ""
	}
	return filepath.Join(home, ".cache", "quotatop", "history.jsonl")
}

// loadHistory reads the trend file. Every failure degrades to an in-memory
// history: a monitor must still run when its cache directory is unwritable.
func loadHistory(path string) *History {
	return loadHistoryMode(path)
}

// loadAppendOnlyHistory is for short-lived JSON callers. The name is retained
// for its call-site intent; its cleanup is now safely shared with the TUI.
func loadAppendOnlyHistory(path string) *History {
	return loadHistoryMode(path)
}

func loadHistoryMode(path string) *History {
	history := &History{path: path, data: map[string][]sample{}, enabled: path != ""}
	if !history.enabled {
		return history
	}
	history.data, history.lines = readHistory(path)
	return history
}

// readHistory returns just the live, per-key bounded records. It is also used
// while holding the writer lock before a compaction, so a process never
// rewrites a snapshot that predates another process's append.
func readHistory(path string) (map[string][]sample, int) {
	data := map[string][]sample{}
	file, err := os.Open(path)
	if err != nil {
		return data, 0
	}
	defer file.Close()
	lines := 0
	cutoff := time.Now().Add(-sampleMaxAge)
	scanner := bufio.NewScanner(file)
	scanner.Buffer(make([]byte, 0, 64*1024), 1024*1024)
	for scanner.Scan() {
		var record sample
		if err := json.Unmarshal(scanner.Bytes(), &record); err != nil || record.Key == "" {
			continue
		}
		lines++
		record.T = time.Unix(record.Sec, 0)
		if record.T.Before(cutoff) {
			continue
		}
		data[record.Key] = append(data[record.Key], record)
	}
	for key, samples := range data {
		sort.Slice(samples, func(i, j int) bool { return samples[i].T.Before(samples[j].T) })
		data[key] = trimHead(samples, maxSamplesPerKey)
	}
	return data, lines
}

func trimHead(samples []sample, max int) []sample {
	if len(samples) > max {
		return samples[len(samples)-max:]
	}
	return samples
}

// Add records a percentage, keeping only the points where it changed.
func (h *History) Add(key string, at time.Time, pct float64) {
	record := sample{T: at, Sec: at.Unix(), Key: key, Pct: pct}
	if !h.enabled {
		samples := h.data[key]
		if n := len(samples); n > 0 && samples[n-1].Pct == pct {
			return
		}
		h.data[key] = trimHead(append(samples, record), maxSamplesPerKey)
		return
	}
	h.append(record)
}

func (h *History) append(record sample) {
	if !h.enabled {
		return
	}
	if err := os.MkdirAll(filepath.Dir(h.path), 0o755); err != nil {
		h.enabled = false
		h.addToMemory(record)
		return
	}
	added := false
	if err := withHistoryLock(h.path, func() error {
		// Do not compact from the data loaded at process startup: another
		// process may have appended since then.
		h.data, h.lines = readHistory(h.path)
		samples := h.data[record.Key]
		if n := len(samples); n > 0 && samples[n-1].Pct == record.Pct {
			return nil
		}
		h.data[record.Key] = trimHead(append(samples, record), maxSamplesPerKey)
		added = true
		if h.lines+1 >= compactAtLines {
			return h.compact()
		}
		return h.appendLine(record)
	}); err != nil {
		h.enabled = false
		if !added {
			h.addToMemory(record)
		}
	}
}

// addToMemory retains a sample after persistence becomes unavailable.
func (h *History) addToMemory(record sample) {
	samples := h.data[record.Key]
	if n := len(samples); n > 0 && samples[n-1].Pct == record.Pct {
		return
	}
	h.data[record.Key] = trimHead(append(samples, record), maxSamplesPerKey)
}

func (h *History) appendLine(record sample) error {
	file, err := os.OpenFile(h.path, os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0o644)
	if err != nil {
		return err
	}
	defer file.Close()
	line, err := json.Marshal(record)
	if err != nil {
		return err
	}
	if _, err := file.Write(append(line, '\n')); err != nil {
		return err
	}
	h.lines++
	return nil
}

// Cleanup bounds files maintained exclusively by short-lived --json calls.
// It is deliberately called even when the current reading did not change.
func (h *History) Cleanup() {
	if !h.enabled || h.lines < compactAtLines {
		return
	}
	if err := withHistoryLock(h.path, func() error {
		h.data, h.lines = readHistory(h.path)
		if h.lines < compactAtLines {
			return nil
		}
		return h.compact()
	}); err != nil {
		h.enabled = false
	}
}

// compact rewrites the file from what is still in memory, which is already
// capped and age-filtered. Written to a temp file first so an interrupted
// compaction cannot truncate the history.
func (h *History) compact() error {
	var all []sample
	for _, samples := range h.data {
		all = append(all, samples...)
	}
	sort.Slice(all, func(i, j int) bool { return all[i].T.Before(all[j].T) })
	tmp := h.path + ".tmp"
	file, err := os.Create(tmp)
	if err != nil {
		return err
	}
	writer := bufio.NewWriter(file)
	for _, record := range all {
		line, err := json.Marshal(record)
		if err != nil {
			continue
		}
		if _, err := writer.Write(append(line, '\n')); err != nil {
			file.Close()
			os.Remove(tmp)
			return err
		}
	}
	if err := writer.Flush(); err != nil {
		file.Close()
		os.Remove(tmp)
		return err
	}
	if err := file.Close(); err != nil {
		os.Remove(tmp)
		return err
	}
	if err := os.Rename(tmp, h.path); err != nil {
		os.Remove(tmp)
		return err
	}
	h.lines = len(all)
	return nil
}

// Trend returns the recent percentages for a key, oldest first, with the
// current value appended so the newest point is always live.
func (h *History) Trend(key string, current float64, limit int) []float64 {
	samples := h.data[key]
	points := make([]float64, 0, len(samples)+1)
	for _, record := range samples {
		points = append(points, record.Pct)
	}
	if n := len(points); n == 0 || points[n-1] != current {
		points = append(points, current)
	}
	if len(points) > limit {
		points = points[len(points)-limit:]
	}
	return points
}

// Projection is the answer to "if this keeps up, what happens before the reset?"
type Projection struct {
	RatePerHour float64
	AtReset     float64   // projected percentage when the window resets
	ExhaustAt   time.Time // when it would hit 100%, if that is before the reset
	FullAt      time.Time // when it would hit 100% at this rate, reset or no reset
	Valid       bool
	Sustained   bool // the rate came from the window's own elapsed pace, not the recent slope
}

const (
	rateLookback        = 90 * time.Minute
	rateMinSpan         = 12 * time.Minute
	resetDrop           = 3.0            // a fall this large means the window rolled over
	sustainedMinLength  = 24 * time.Hour // "long window" for the sustained model
	sustainedMinElapsed = 12 * time.Hour // under this the denominator is too small
)

// sustainedRate is the burn rate measured over the window's own elapsed time,
// current_percent / hours since the window opened. It exists because a weekly
// window's elapsed time already contains the nights, weekends and days away
// from the keyboard that a recent slope does not: extrapolating a working-hours
// rate across mostly-sleep time overstates the burn. It needs no history at all,
// so it is correct on a machine with an empty history file.
//
// It applies only to long windows (Length >= 24h) with a known reset deadline,
// once at least 12h of the window have passed (the denominator is too small
// before that and the projection explodes), and only while we are still inside
// that window. It returns ok=false when any of that is missing, so the caller
// falls back to the live slope.
func sustainedRate(window Window, now time.Time) (float64, bool) {
	if window.Length < sustainedMinLength {
		return 0, false // short windows keep the live model
	}
	if window.ResetsAt.IsZero() {
		return 0, false // without a reset time we cannot date the window's start
	}
	elapsed := now.Sub(window.ResetsAt.Add(-window.Length))
	if elapsed < sustainedMinElapsed {
		return 0, false // too early in the window
	}
	if elapsed >= window.Length {
		return 0, false // not inside the window we think we are
	}
	if window.Percent <= 0 {
		return 0, false // nothing to measure
	}
	return window.Percent / elapsed.Hours(), true
}

// finishProjection extends a rate out to the reset and, if the pace reaches
// 100% first, marks when. Shared by both models.
func finishProjection(current, rate float64, resetsAt, now time.Time) Projection {
	projection := Projection{RatePerHour: rate, AtReset: current, Valid: true}
	if rate > 0 {
		if current >= 100 {
			projection.FullAt = now // it is already full
		} else {
			projection.FullAt = now.Add(time.Duration((100 - current) / rate * float64(time.Hour)))
		}
	}
	if resetsAt.IsZero() || !resetsAt.After(now) {
		return projection
	}
	hoursLeft := resetsAt.Sub(now).Hours()
	projection.AtReset = current + rate*hoursLeft
	if rate > 0 && projection.AtReset >= 100 {
		projection.ExhaustAt = projection.FullAt
	}
	return projection
}

// Project answers "if this keeps up, what happens before the reset?". Long
// windows are projected with the sustained model (their own elapsed pace); short
// windows and any window the sustained model cannot date fall through to the
// live slope below.
//
// The live model fits a rate over the samples since the window last reset. A
// window reset is a sharp drop, and averaging across one would report a
// meaningless negative burn, so everything before the last drop is discarded.
func (h *History) Project(identity string, window Window, now time.Time) Projection {
	if rate, ok := sustainedRate(window, now); ok {
		projection := finishProjection(window.Percent, rate, window.ResetsAt, now)
		projection.Sustained = true
		return projection
	}

	points := append([]sample{}, h.data[historyKey(identity, window.Key)]...)
	current := window.Percent
	if n := len(points); n == 0 || points[n-1].Pct != current {
		points = append(points, sample{T: now, Pct: current})
	} else {
		points[n-1].T = now
	}

	start := 0
	for i := 1; i < len(points); i++ {
		if points[i].Pct < points[i-1].Pct-resetDrop {
			start = i
		}
	}
	points = points[start:]
	if len(points) < 2 {
		return Projection{}
	}

	last := points[len(points)-1]
	cutoff := last.T.Add(-rateLookback)
	anchor := points[0]
	for _, point := range points {
		if !point.T.After(cutoff) {
			anchor = point // the newest sample at or before the lookback edge
		}
	}
	span := last.T.Sub(anchor.T)
	if span < rateMinSpan {
		return Projection{}
	}

	rate := (last.Pct - anchor.Pct) / span.Hours()
	if rate < 0 || math.IsNaN(rate) || math.IsInf(rate, 0) {
		return Projection{}
	}
	return finishProjection(current, rate, window.ResetsAt, now)
}
