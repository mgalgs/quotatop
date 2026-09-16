package main

import (
	"errors"
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

// encodeJSON builds the schema-1 document from two snapshots. It is pure:
// now and the history are passed in so tests need no clock and no files.
func encodeJSON(snaps []Snapshot, history *History, now time.Time) jsonDoc {
	bySource := make(map[string]Snapshot, len(snaps))
	for _, snap := range snaps {
		if _, exists := bySource[snap.Source]; !exists {
			bySource[snap.Source] = snap
		}
	}

	doc := jsonDoc{
		Schema:           1,
		GeneratedAt:      jsonTime(now),
		GeneratedAtEpoch: now.Unix(),
		Sources:          make([]jsonSource, 0, 2),
	}
	for _, source := range []string{"claude", "codex"} {
		snap, ok := bySource[source]
		if !ok {
			snap = Snapshot{Source: source, Title: sourceTitle(source), Err: errors.New("source unavailable")}
		}
		doc.Sources = append(doc.Sources, encodeJSONSource(snap, history, now))
	}
	return doc
}

func sourceTitle(source string) string {
	if source == "claude" {
		return "CLAUDE"
	}
	return "CODEX"
}

func encodeJSONSource(snap Snapshot, history *History, now time.Time) jsonSource {
	source := jsonSource{
		Source:       snap.Source,
		Title:        snap.Title,
		Plan:         snap.Chip,
		Verb:         snap.Verb,
		Warning:      snap.Warning,
		LimitReached: snap.LimitReached,
		Windows:      make([]jsonWindow, 0),
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
		source.Windows = append(source.Windows, encodeJSONWindow(snap.Source, window, history, now))
	}
	return source
}

func encodeJSONWindow(source string, window Window, history *History, now time.Time) jsonWindow {
	// A projection is itself a percentage claim, so it is withheld for an
	// expired window the same way view.go withholds the gauge and the
	// percent text -- a forecast derived from a discarded reading is worse
	// than no forecast.
	projection := jsonProjection{}
	if !window.Expired {
		projection = encodeJSONProjection(history.Project(source, window, now), window.ResetsAt)
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
