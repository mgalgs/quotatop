package main

import (
	"errors"
	"sort"
	"time"
)

// jsonDoc is the published, schema-1 machine-readable reading.
type jsonDoc struct {
	Schema           int          `json:"schema"`
	GeneratedAt      string       `json:"generated_at"`
	GeneratedAtEpoch int64        `json:"generated_at_epoch"`
	Sources          []jsonSource `json:"sources"`
}

type jsonSource struct {
	Source             string       `json:"source"`
	ID                 string       `json:"id"`
	Account            string       `json:"account,omitempty"`
	CredentialsPath    string       `json:"credentials_path"`
	Title              string       `json:"title"`
	Plan               string       `json:"plan"`
	Verb               string       `json:"verb"`
	ObservedAt         *string      `json:"observed_at"`
	ObservedAtEpoch    *int64       `json:"observed_at_epoch"`
	ObservedAgeSeconds *int64       `json:"observed_age_seconds"`
	Warning            string       `json:"warning"`
	Error              string       `json:"error"`
	LimitReached       string       `json:"limit_reached,omitempty"`
	Windows            []jsonWindow `json:"windows"`
}

type jsonWindow struct {
	Key             string         `json:"key"`
	Label           string         `json:"label"`
	Percent         float64        `json:"percent"`
	ResetsAt        *string        `json:"resets_at"`
	ResetsInSeconds *int64         `json:"resets_in_seconds"`
	WindowSeconds   *int64         `json:"window_seconds"`
	Note            string         `json:"note"`
	Expired         bool           `json:"expired,omitempty"`
	Projection      jsonProjection `json:"projection"`
}

// Projection fields other than Valid are omitted when no projection is
// available; consumers use the lone valid:false object as that signal.
type jsonProjection struct {
	Valid               bool     `json:"valid"`
	Model               string   `json:"model,omitempty"`
	RatePerHour         *float64 `json:"rate_per_hour,omitempty"`
	PercentAtReset      *float64 `json:"percent_at_reset,omitempty"`
	FullAt              *string  `json:"full_at,omitempty"`
	ExhaustsBeforeReset *bool    `json:"exhausts_before_reset,omitempty"`
	GapSeconds          *int64   `json:"gap_seconds,omitempty"`
}

// expectedSources is the fixed set of sources a document always reports on,
// claude before codex; a source with no snapshot at all still gets an entry,
// synthesised as unavailable.
var expectedSources = []string{"claude", "codex"}

// encodeJSON builds the schema-1 document from the given snapshots. It is
// pure: now and the history are passed in so tests need no clock and no
// files. Every expected source is named even when absent from snaps, and
// snaps may hold more than one snapshot for the same source; either way, the
// output is ordered claude before codex.
func encodeJSON(snaps []Snapshot, history *History, now time.Time) jsonDoc {
	ordered := orderedSnapshots(snaps)
	doc := jsonDoc{
		Schema:           1,
		GeneratedAt:      jsonTime(now),
		GeneratedAtEpoch: now.Unix(),
		Sources:          make([]jsonSource, 0, len(ordered)),
	}
	for _, snap := range ordered {
		doc.Sources = append(doc.Sources, encodeJSONSource(snap, history, now))
	}
	return doc
}

// orderedSnapshots returns snaps in claude-before-codex order, synthesising
// a placeholder for any expected source with no snapshot at all -- keyed off
// which sources are expected, not which are present. The sort is stable and
// keyed on source rank alone, so multiple snapshots of the same source keep
// the relative order they arrived in. This is a deterministic rule over the
// input, never map iteration, which Go randomises.
func orderedSnapshots(snaps []Snapshot) []Snapshot {
	present := make(map[string]bool, len(expectedSources))
	for _, snap := range snaps {
		present[snap.Source] = true
	}
	ordered := append([]Snapshot{}, snaps...)
	for _, source := range expectedSources {
		if !present[source] {
			ordered = append(ordered, Snapshot{Source: source, Title: sourceTitle(source), Err: errors.New("source unavailable")})
		}
	}
	rank := func(source string) int {
		for i, candidate := range expectedSources {
			if candidate == source {
				return i
			}
		}
		return len(expectedSources)
	}
	sort.SliceStable(ordered, func(i, j int) bool {
		return rank(ordered[i].Source) < rank(ordered[j].Source)
	})
	return ordered
}

func sourceTitle(source string) string {
	if source == "claude" {
		return "CLAUDE"
	}
	return "CODEX"
}

func encodeJSONSource(snap Snapshot, history *History, now time.Time) jsonSource {
	source := jsonSource{
		Source:          snap.Source,
		ID:              snap.Identity(),
		Account:         snap.Account,
		CredentialsPath: snap.CredentialsPath,
		Title:           snap.Title,
		Plan:            snap.Chip,
		Verb:            snap.Verb,
		Warning:         snap.Warning,
		LimitReached:    snap.LimitReached,
		Windows:         make([]jsonWindow, 0),
	}
	if !snap.Observed.IsZero() {
		observedAt := jsonTime(snap.Observed)
		observedEpoch := snap.Observed.Unix()
		age := int64(now.Sub(snap.Observed) / time.Second)
		if age < 0 {
			age = 0
		}
		source.ObservedAt = &observedAt
		source.ObservedAtEpoch = &observedEpoch
		source.ObservedAgeSeconds = &age
	}
	if snap.Err != nil {
		source.Error = snap.Err.Error()
		return source
	}
	for _, window := range snap.Windows {
		source.Windows = append(source.Windows, encodeJSONWindow(snap.Identity(), window, history, now))
	}
	return source
}

func encodeJSONWindow(identity string, window Window, history *History, now time.Time) jsonWindow {
	// A projection is itself a percentage claim, so it is withheld for an
	// expired window the same way view.go withholds the gauge and the
	// percent text -- a forecast derived from a discarded reading is worse
	// than no forecast.
	projection := jsonProjection{}
	if !window.Expired {
		projection = encodeJSONProjection(history.Project(identity, window, now), window.ResetsAt)
	}
	encoded := jsonWindow{
		Key:        window.Key,
		Label:      window.Label,
		Percent:    window.Percent,
		Note:       window.Note,
		Expired:    window.Expired,
		Projection: projection,
	}
	if !window.ResetsAt.IsZero() {
		resetsAt := jsonTime(window.ResetsAt)
		seconds := int64(window.ResetsAt.Sub(now) / time.Second)
		if seconds < 0 {
			seconds = 0
		}
		encoded.ResetsAt = &resetsAt
		encoded.ResetsInSeconds = &seconds
	}
	if window.Length != 0 {
		seconds := int64(window.Length / time.Second)
		encoded.WindowSeconds = &seconds
	}
	return encoded
}

func encodeJSONProjection(projection Projection, resetsAt time.Time) jsonProjection {
	encoded := jsonProjection{Valid: projection.Valid}
	if !projection.Valid {
		return encoded
	}
	encoded.Model = "slope"
	if projection.Sustained {
		encoded.Model = "sustained"
	}
	rate, atReset := projection.RatePerHour, projection.AtReset
	exhausts := !projection.ExhaustAt.IsZero()
	encoded.RatePerHour = &rate
	encoded.PercentAtReset = &atReset
	encoded.ExhaustsBeforeReset = &exhausts
	if !projection.FullAt.IsZero() {
		fullAt := jsonTime(projection.FullAt)
		encoded.FullAt = &fullAt
	}
	if !projection.FullAt.IsZero() && !resetsAt.IsZero() {
		gap := int64(projection.FullAt.Sub(resetsAt) / time.Second)
		encoded.GapSeconds = &gap
	}
	return encoded
}

func jsonTime(value time.Time) string { return value.Format(time.RFC3339) }
