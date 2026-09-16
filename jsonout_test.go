package main

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"
)

func jsonMap(t *testing.T, doc jsonDoc) map[string]any {
	t.Helper()
	raw, err := json.Marshal(doc)
	if err != nil {
		t.Fatal(err)
	}
	var decoded map[string]any
	if err := json.Unmarshal(raw, &decoded); err != nil {
		t.Fatal(err)
	}
	return decoded
}

func jsonSources(t *testing.T, doc jsonDoc) []any {
	t.Helper()
	return jsonMap(t, doc)["sources"].([]any)
}

func sameJSONShape(got, want any) bool {
	switch want := want.(type) {
	case map[string]any:
		got, ok := got.(map[string]any)
		if !ok || len(got) != len(want) {
			return false
		}
		for key, wantValue := range want {
			gotValue, ok := got[key]
			if !ok || !sameJSONShape(gotValue, wantValue) {
				return false
			}
		}
		return true
	case []any:
		got, ok := got.([]any)
		if !ok || len(got) != len(want) {
			return false
		}
		for index := range want {
			if !sameJSONShape(got[index], want[index]) {
				return false
			}
		}
		return true
	default:
		return reflect.TypeOf(got) == reflect.TypeOf(want)
	}
}

func TestEncodeJSONFixtureShape(t *testing.T) {
	fixture, err := os.ReadFile("testdata/jsonout-schema-1.json")
	if err != nil {
		t.Fatal(err)
	}
	var want map[string]any
	if err := json.Unmarshal(fixture, &want); err != nil {
		t.Fatal(err)
	}
	if got := want["schema"]; got != float64(1) {
		t.Fatalf("fixture schema = %v", got)
	}
	if sources := want["sources"].([]any); len(sources) != 2 {
		t.Fatalf("fixture sources = %d, want 2", len(sources))
	}

	zone := time.FixedZone("PDT", -7*60*60)
	now := time.Date(2026, 9, 15, 16, 22, 41, 0, zone)
	history := loadHistory("")
	history.Add("claude/session", now.Add(-time.Hour), 47.8)
	got := jsonMap(t, encodeJSON([]Snapshot{
		{Source: "claude", Title: "CLAUDE", Chip: "max_20x", Verb: "fetched", Observed: now.Add(-2 * time.Second), Windows: []Window{{Key: "session", Label: "5-hour", Percent: 52, ResetsAt: now.Add(2 * time.Hour), Length: 5 * time.Hour}, {Key: "weekly_all", Label: "Weekly", Percent: 47, ResetsAt: now.Add(5 * 24 * time.Hour), Length: 7 * 24 * time.Hour}}},
		{Source: "codex", Title: "CODEX", Chip: "team", Verb: "reported", Observed: now.Add(-time.Second), Warning: "a log is cut off; the reading may be stale", Windows: []Window{{Key: "primary", Label: "5-hour", Percent: 56, ResetsAt: now.Add(time.Hour), Length: 5 * time.Hour}}},
	}, history, now))
	if !sameJSONShape(got, want) {
		t.Fatalf("encoder shape does not match fixture:\n got: %#v\nwant: %#v", got, want)
	}
	if got["schema"] != want["schema"] || got["generated_at"] != "2026-09-15T16:22:41-07:00" || got["generated_at_epoch"] != float64(now.Unix()) {
		t.Fatalf("document header = %#v", got)
	}
	sources := got["sources"].([]any)
	claude := sources[0].(map[string]any)
	if claude["plan"] != "max_20x" || claude["observed_age_seconds"] != float64(2) || len(claude["windows"].([]any)) != 2 {
		t.Fatalf("Claude fixture fields = %#v", claude)
	}
	codex := sources[1].(map[string]any)
	if codex["warning"] != "a log is cut off; the reading may be stale" || codex["observed_age_seconds"] != float64(1) {
		t.Fatalf("Codex fixture fields = %#v", codex)
	}
}

func TestEncodeJSONFailureAndNulls(t *testing.T) {
	now := time.Unix(1_700_000_000, 0)
	doc := encodeJSON([]Snapshot{
		{Source: "claude", Title: "CLAUDE", Err: errors.New("offline"), Windows: []Window{{Key: "ignored"}}},
		{Source: "codex", Title: "CODEX", Verb: "reported", Windows: []Window{{Key: "primary", ResetsAt: now.Add(-time.Second)}}},
	}, loadHistory(""), now)
	sources := jsonSources(t, doc)
	if len(sources) != 2 || sources[0].(map[string]any)["source"] != "claude" || sources[1].(map[string]any)["source"] != "codex" {
		t.Fatalf("sources = %#v", sources)
	}
	claude := sources[0].(map[string]any)
	if claude["error"] == "" || len(claude["windows"].([]any)) != 0 {
		t.Fatalf("failed source = %#v", claude)
	}
	if claude["observed_at"] != nil || claude["observed_at_epoch"] != nil || claude["observed_age_seconds"] != nil {
		t.Fatalf("zero observation = %#v", claude)
	}
	window := sources[1].(map[string]any)["windows"].([]any)[0].(map[string]any)
	if window["resets_in_seconds"] != float64(0) {
		t.Fatalf("past reset seconds = %#v", window["resets_in_seconds"])
	}

	zero := encodeJSON([]Snapshot{{Source: "claude", Windows: []Window{{Key: "none"}}}, {Source: "codex"}}, loadHistory(""), now)
	zeroWindow := jsonSources(t, zero)[0].(map[string]any)["windows"].([]any)[0].(map[string]any)
	if zeroWindow["resets_at"] != nil || zeroWindow["resets_in_seconds"] != nil {
		t.Fatalf("zero reset = %#v", zeroWindow)
	}
}

func TestEncodeJSONProjectionContract(t *testing.T) {
	now := time.Unix(1_700_000_000, 0)
	history := loadHistory("")
	history.Add("claude/session", now.Add(-time.Hour), 40)
	window := Window{Key: "session", Percent: 60, ResetsAt: now.Add(3 * time.Hour)}
	doc := encodeJSON([]Snapshot{{Source: "claude", Windows: []Window{window}}, {Source: "codex"}}, history, now)
	projection := jsonSources(t, doc)[0].(map[string]any)["windows"].([]any)[0].(map[string]any)["projection"].(map[string]any)
	if projection["model"] != "slope" || projection["exhausts_before_reset"] != true || projection["gap_seconds"].(float64) >= 0 {
		t.Fatalf("exhausting projection = %#v", projection)
	}

	nonExhausting := Window{Key: "weekly", Percent: 10, Length: 7 * 24 * time.Hour, ResetsAt: now.Add(5 * 24 * time.Hour)}
	doc = encodeJSON([]Snapshot{{Source: "claude", Windows: []Window{nonExhausting}}, {Source: "codex"}}, history, now)
	projection = jsonSources(t, doc)[0].(map[string]any)["windows"].([]any)[0].(map[string]any)["projection"].(map[string]any)
	if projection["model"] != "sustained" || projection["exhausts_before_reset"] != false || projection["gap_seconds"].(float64) <= 0 {
		t.Fatalf("sustained projection = %#v", projection)
	}

	invalid := encodeJSON([]Snapshot{{Source: "claude", Windows: []Window{{Key: "new"}}}, {Source: "codex"}}, history, now)
	projection = jsonSources(t, invalid)[0].(map[string]any)["windows"].([]any)[0].(map[string]any)["projection"].(map[string]any)
	if !reflect.DeepEqual(projection, map[string]any{"valid": false}) {
		t.Fatalf("invalid projection = %#v", projection)
	}
}

// Both new fields are additive to schema 1 via omitempty: the ordinary case
// (no expiry, no block) must be byte-identical to output with neither field
// wired in, and the fields must appear once the values are non-zero.
func TestEncodeJSONExpiredAndLimitReachedAreOmittedWhenZero(t *testing.T) {
	now := time.Unix(1_700_000_000, 0)
	doc := encodeJSON([]Snapshot{
		{Source: "claude", Windows: []Window{{Key: "session", Percent: 10}}},
		{Source: "codex", Windows: []Window{{Key: "primary", Percent: 20}}},
	}, loadHistory(""), now)
	raw, err := json.Marshal(doc)
	if err != nil {
		t.Fatal(err)
	}
	text := string(raw)
	if strings.Contains(text, `"expired"`) {
		t.Errorf("output contains \"expired\" with no expired window: %s", text)
	}
	if strings.Contains(text, `"limit_reached"`) {
		t.Errorf("output contains \"limit_reached\" with no block: %s", text)
	}

	blocked := encodeJSON([]Snapshot{
		{Source: "claude", Windows: []Window{{Key: "session", Percent: 10, Expired: true}}},
		{Source: "codex", LimitReached: "workspace_member_usage_limit_reached", Windows: []Window{{Key: "primary", Percent: 20}}},
	}, loadHistory(""), now)
	claude := jsonSources(t, blocked)[0].(map[string]any)
	window := claude["windows"].([]any)[0].(map[string]any)
	if window["expired"] != true {
		t.Errorf("expired window = %#v, want expired:true present", window)
	}
	codex := jsonSources(t, blocked)[1].(map[string]any)
	if codex["limit_reached"] != "workspace_member_usage_limit_reached" {
		t.Errorf("codex source = %#v, want limit_reached present", codex)
	}
}

// The empty account (today's only account) must keep the exact history key
// format quotatop has always written: no doubled separator, no suffix.
// Every future non-empty-account key still goes through the same helper, so
// pinning this string is what stops the write and read sites from ever
// drifting apart again.
func TestHistoryKeyEmptyAccountFormat(t *testing.T) {
	identity := Snapshot{Source: "claude"}.Identity()
	if identity != "claude" {
		t.Fatalf("Identity() with no account = %q, want %q", identity, "claude")
	}
	if got, want := historyKey(identity, "weekly_all"), "claude/weekly_all"; got != want {
		t.Errorf("historyKey = %q, want %q", got, want)
	}
}

// encodeJSON must be able to represent two snapshots of the same source --
// the capability this round exists to add -- and must do so deterministically,
// not via map iteration order.
func TestEncodeJSONSameSourceTwiceReturnsBoth(t *testing.T) {
	now := time.Unix(1_700_000_000, 0)
	first := Snapshot{Source: "claude", Account: "work", Title: "CLAUDE (work)", Windows: []Window{{Key: "session", Percent: 10}}}
	second := Snapshot{Source: "claude", Account: "personal", Title: "CLAUDE (personal)", Windows: []Window{{Key: "session", Percent: 20}}}
	doc := encodeJSON([]Snapshot{first, second, {Source: "codex"}}, loadHistory(""), now)
	if len(doc.Sources) != 3 {
		t.Fatalf("len(doc.Sources) = %d, want 3", len(doc.Sources))
	}
	if doc.Sources[0].Title != "CLAUDE (work)" || doc.Sources[1].Title != "CLAUDE (personal)" {
		t.Fatalf("sources = %#v, want both claude snapshots in input order", doc.Sources)
	}
	if doc.Sources[2].Source != "codex" {
		t.Fatalf("third source = %#v, want codex", doc.Sources[2])
	}
}

// A source entirely absent from the given snapshots -- not merely one that
// errored -- must still be synthesised as unavailable, keyed off which
// sources are expected rather than which are present.
func TestEncodeJSONSynthesizesPlaceholderForAbsentSource(t *testing.T) {
	now := time.Unix(1_700_000_000, 0)
	doc := encodeJSON([]Snapshot{{Source: "claude", Windows: []Window{{Key: "session", Percent: 10}}}}, loadHistory(""), now)
	if len(doc.Sources) != 2 {
		t.Fatalf("len(doc.Sources) = %d, want 2", len(doc.Sources))
	}
	codex := doc.Sources[1]
	if codex.Source != "codex" || codex.Error == "" {
		t.Fatalf("synthesised codex source = %#v, want a placeholder with an error", codex)
	}
}

func TestJSONHistoryDoesNotRecordWhenDisabledAndCleansUp(t *testing.T) {
	path := filepath.Join(t.TempDir(), "history.jsonl")
	now := time.Now().Truncate(time.Second)
	snap := Snapshot{Source: "claude", Observed: now, Windows: []Window{{Key: "session", Percent: 10}}}
	recordJSONSnapshots(loadAppendOnlyHistory(path), []Snapshot{snap}, false)
	if _, err := os.Stat(path); !os.IsNotExist(err) {
		t.Fatalf("disabled history stat error = %v, want absent", err)
	}

	lines := make([]byte, 0, compactAtLines*2)
	for i := 0; i < compactAtLines; i++ {
		lines = append(lines, []byte(fmt.Sprintf("{\"t\":%d,\"k\":\"old\",\"p\":1}\n", now.Unix()))...)
	}
	if err := os.WriteFile(path, lines, 0o644); err != nil {
		t.Fatal(err)
	}
	history := loadAppendOnlyHistory(path)
	before, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	recordJSONSnapshots(history, []Snapshot{snap}, true)
	after, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if len(after) >= len(before) {
		t.Fatalf("append-only history was not compacted: %d >= %d bytes", len(after), len(before))
	}
	if got := loadHistory(path).Trend("claude/session", 10, 10); len(got) != 1 || got[0] != 10 {
		t.Fatalf("cleaned history lost new JSON sample: %v", got)
	}
}
