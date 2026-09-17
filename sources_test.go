package main

import (
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// --- Claude ---------------------------------------------------------------

// claudeStub is the injected doRequest: it records the request and answers
// with a canned response, no socket involved.
type claudeStub struct {
	status int
	body   string
	calls  int
	req    *http.Request
}

func (s *claudeStub) do(req *http.Request) (*http.Response, error) {
	s.calls++
	s.req = req
	return &http.Response{
		StatusCode: s.status,
		Body:       io.NopCloser(strings.NewReader(s.body)),
		Header:     http.Header{},
	}, nil
}

// claudeTestSource wires a source at temp paths with a test token, so neither
// the real credentials file nor the real cache is ever touched.
func claudeTestSource(t *testing.T, stub *claudeStub) claudeSource {
	t.Helper()
	dir := t.TempDir()
	creds := filepath.Join(dir, ".credentials.json")
	tokenDoc := `{"claudeAiOauth":{"accessToken":"sk-test-token"}}`
	if err := os.WriteFile(creds, []byte(tokenDoc), 0o600); err != nil {
		t.Fatal(err)
	}
	return claudeSource{
		doRequest:       stub.do,
		credentialsPath: creds,
		cacheDir:        filepath.Join(dir, "cache"),
	}
}

func writeClaudeCache(t *testing.T, path, payloadJSON string, at time.Time) {
	t.Helper()
	var payload claudePayload
	if err := json.Unmarshal([]byte(payloadJSON), &payload); err != nil {
		t.Fatal(err)
	}
	raw, err := json.Marshal(claudeCache{FetchedAt: float64(at.Unix()), Payload: payload})
	if err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, raw, 0o600); err != nil {
		t.Fatal(err)
	}
}

const realisticClaudePayload = `{
  "limits": [
    {"kind": "session", "percent": 25, "resets_at": "2026-03-01T12:00:00Z"},
    {"kind": "weekly_all", "percent": 43, "resets_at": "2026-03-07T12:00:00Z"},
    {"kind": "weekly_scoped", "percent": 28, "resets_at": "2026-03-07T12:00:00Z",
     "scope": {"model": {"display_name": "Fable"}, "surface": "cli"}}
  ]
}`

func TestClaudeRequestCarriesHeadersAndURL(t *testing.T) {
	stub := &claudeStub{status: 200, body: realisticClaudePayload}
	src := claudeTestSource(t, stub)
	if snap := src.fetch(false); snap.Err != nil {
		t.Fatalf("fetch failed: %v", snap.Err)
	}
	req := stub.req
	if req == nil {
		t.Fatal("the request was never made")
	}
	if got, want := req.URL.String(), "https://api.anthropic.com/api/oauth/usage"; got != want {
		t.Errorf("url = %q, want %q", got, want)
	}
	if req.Method != http.MethodGet {
		t.Errorf("method = %q, want GET", req.Method)
	}
	if got := req.Header.Get("Authorization"); got != "Bearer sk-test-token" {
		t.Errorf("Authorization = %q", got)
	}
	if got := req.Header.Get("anthropic-beta"); got != "oauth-2025-04-20" {
		t.Errorf("anthropic-beta = %q", got)
	}
	if got := req.Header.Get("Content-Type"); got != "application/json" {
		t.Errorf("Content-Type = %q", got)
	}
}

func TestClaudePayloadBecomesWindows(t *testing.T) {
	stub := &claudeStub{status: 200, body: realisticClaudePayload}
	snap := claudeTestSource(t, stub).fetch(false)
	if snap.Err != nil {
		t.Fatalf("fetch failed: %v", snap.Err)
	}
	if len(snap.Windows) != 3 {
		t.Fatalf("got %d windows, want 3: %+v", len(snap.Windows), snap.Windows)
	}
	got := map[string]Window{}
	for _, window := range snap.Windows {
		got[window.Key] = window
	}
	session := got["session"]
	if session.Label != "5-hour" || session.Percent != 25 || session.Length != 5*time.Hour {
		t.Errorf("session window = %+v", session)
	}
	if !session.ResetsAt.Equal(parseISO("2026-03-01T12:00:00Z")) {
		t.Errorf("session ResetsAt = %v", session.ResetsAt)
	}
	if weekly := got["weekly_all"]; weekly.Label != "Weekly" || weekly.Length != 168*time.Hour {
		t.Errorf("weekly window = %+v", weekly)
	}
	if scoped := got["weekly_scoped"]; scoped.Label != "Weekly · Fable · cli" {
		t.Errorf("scoped label = %q", scoped.Label)
	}
}

func TestClaudeNon200LeaksNothing(t *testing.T) {
	stub := &claudeStub{status: 401, body: `{"error":"leaked-body-and sk-test-token"}`}
	snap := claudeTestSource(t, stub).fetch(true)
	if snap.Err == nil {
		t.Fatal("expected an error")
	}
	msg := snap.Err.Error()
	if !strings.Contains(msg, "401") {
		t.Errorf("error = %q, want the status code in it", msg)
	}
	for _, secret := range []string{"sk-test-token", "leaked-body-and"} {
		if strings.Contains(msg, secret) {
			t.Errorf("error = %q leaks %q", msg, secret)
		}
	}
}

func TestClaudeFreshCacheIsUsedWithoutRequest(t *testing.T) {
	stub := &claudeStub{status: 200, body: realisticClaudePayload}
	src := claudeTestSource(t, stub)
	stamp := time.Now().Add(-30 * time.Second)
	writeClaudeCache(t, src.resolvedCachePath(), realisticClaudePayload, stamp)
	snap := src.fetch(false)
	if snap.Err != nil {
		t.Fatalf("fetch failed: %v", snap.Err)
	}
	if stub.calls != 0 {
		t.Errorf("a cache hit still made %d request(s)", stub.calls)
	}
	if !snap.Observed.Equal(time.Unix(stamp.Unix(), 0)) {
		t.Errorf("Observed = %v, want the cache stamp %v", snap.Observed, stamp)
	}
}

func TestClaudeStaleCacheRefetches(t *testing.T) {
	stub := &claudeStub{status: 200, body: realisticClaudePayload}
	src := claudeTestSource(t, stub)
	writeClaudeCache(t, src.resolvedCachePath(), realisticClaudePayload, time.Now().Add(-601*time.Second))
	if snap := src.fetch(false); snap.Err != nil {
		t.Fatalf("fetch failed: %v", snap.Err)
	}
	if stub.calls != 1 {
		t.Errorf("a stale cache made %d requests, want 1", stub.calls)
	}
}

func TestClaudeFreshBypassesYoungCache(t *testing.T) {
	stub := &claudeStub{status: 200, body: realisticClaudePayload}
	src := claudeTestSource(t, stub)
	writeClaudeCache(t, src.resolvedCachePath(), realisticClaudePayload, time.Now().Add(-10*time.Second))
	if snap := src.fetch(true); snap.Err != nil {
		t.Fatalf("fetch failed: %v", snap.Err)
	}
	if stub.calls != 1 {
		t.Errorf("a fresh read of a young cache made %d requests, want 1", stub.calls)
	}
}

func TestClaudeCorruptCacheIsAMiss(t *testing.T) {
	stub := &claudeStub{status: 200, body: realisticClaudePayload}
	src := claudeTestSource(t, stub)
	if err := os.MkdirAll(filepath.Dir(src.resolvedCachePath()), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(src.resolvedCachePath(), []byte("{not json"), 0o600); err != nil {
		t.Fatal(err)
	}
	snap := src.fetch(false)
	if snap.Err != nil {
		t.Fatalf("a corrupt cache turned into an error: %v", snap.Err)
	}
	if stub.calls != 1 {
		t.Errorf("a corrupt cache made %d requests, want 1", stub.calls)
	}
}

// Observed must be the stamp the cache carries, not the wall clock: read the
// cache back and require an exact match.
func TestClaudeObservedIsTheCacheStamp(t *testing.T) {
	stub := &claudeStub{status: 200, body: realisticClaudePayload}
	src := claudeTestSource(t, stub)
	snap := src.fetch(false)
	if snap.Err != nil {
		t.Fatalf("fetch failed: %v", snap.Err)
	}
	raw, err := os.ReadFile(src.resolvedCachePath())
	if err != nil {
		t.Fatalf("the cache was not written: %v", err)
	}
	var cache claudeCache
	if err := json.Unmarshal(raw, &cache); err != nil {
		t.Fatalf("unreadable cache: %v", err)
	}
	if want := time.Unix(int64(cache.FetchedAt), 0); !snap.Observed.Equal(want) {
		t.Errorf("Observed = %v, want the cache stamp %v", snap.Observed, want)
	}
}

// A 200 with no usable limits must not enter the cache: otherwise one
// transient empty response pins the panel red for the whole TTL.
func TestClaudeEmptyLimits200IsNotCached(t *testing.T) {
	stub := &claudeStub{status: 200, body: `{"limits":[]}`}
	src := claudeTestSource(t, stub)
	if snap := src.fetch(false); snap.Err == nil {
		t.Fatal("an empty-limits 200 produced no error")
	}
	if _, err := os.Stat(src.resolvedCachePath()); !os.IsNotExist(err) {
		t.Errorf("an unusable payload was written to the cache (stat err = %v)", err)
	}
	// Recovery: the next poll makes a request again, and a good payload wins.
	stub.body = realisticClaudePayload
	snap := src.fetch(false)
	if snap.Err != nil {
		t.Fatalf("the next poll still failed: %v", snap.Err)
	}
	if stub.calls != 2 {
		t.Errorf("two polls made %d requests, want 2", stub.calls)
	}
}

// A young cache whose payload carries no limits must be treated like a
// miss: one extra request, not a red panel until the stamp expires.
func TestClaudeYoungBadCacheRefetches(t *testing.T) {
	stub := &claudeStub{status: 200, body: realisticClaudePayload}
	src := claudeTestSource(t, stub)
	writeClaudeCache(t, src.resolvedCachePath(), `{"limits":[]}`, time.Now().Add(-30*time.Second))
	snap := src.fetch(false)
	if snap.Err != nil {
		t.Fatalf("a bad cache turned into a red panel: %v", snap.Err)
	}
	if stub.calls != 1 {
		t.Errorf("a bad young cache made %d requests, want 1", stub.calls)
	}
}

func TestClaudeMissingCredentialsAreAnOrdinaryError(t *testing.T) {
	src := claudeSource{
		credentialsPath: filepath.Join(t.TempDir(), "absent.json"),
		cacheDir:        t.TempDir(),
	}
	snap := src.fetch(true)
	if snap.Err == nil || !strings.Contains(snap.Err.Error(), "not signed in to Claude Code") {
		t.Errorf("error = %v", snap.Err)
	}
}

// A successful fetch must carry the credentials file's own path on the
// snapshot: encodeJSONSource sees only a Snapshot, never the source struct
// that produced it, so the path has to travel on the Snapshot itself.
func TestClaudeFetchCarriesCredentialsPathOnSuccess(t *testing.T) {
	stub := &claudeStub{status: 200, body: `{"limits":[{"kind":"session","percent":10}]}`}
	src := claudeTestSource(t, stub)
	snap := src.fetch(true)
	if snap.Err != nil {
		t.Fatalf("fetch failed: %v", snap.Err)
	}
	if snap.CredentialsPath != src.credentialsPath {
		t.Errorf("CredentialsPath = %q, want %q", snap.CredentialsPath, src.credentialsPath)
	}
}

// A fetch that fails -- here, a missing credentials file -- must still carry
// the path it tried to read: the consumer needs to know which account's
// credentials went wrong, not merely that one did.
func TestClaudeFetchCarriesCredentialsPathOnFailure(t *testing.T) {
	src := claudeSource{
		credentialsPath: filepath.Join(t.TempDir(), "absent.json"),
		cacheDir:        t.TempDir(),
	}
	snap := src.fetch(true)
	if snap.Err == nil {
		t.Fatalf("expected an error from a missing credentials file")
	}
	if snap.CredentialsPath != src.credentialsPath {
		t.Errorf("CredentialsPath = %q, want %q", snap.CredentialsPath, src.credentialsPath)
	}
}

// --- Codex ----------------------------------------------------------------

func writeSessionFile(t *testing.T, dir, name, content string) {
	t.Helper()
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, name), []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
}

// codexLine assembles a token_count usage row; an empty argument omits the
// field. limitID, primary and secondary are raw JSON fragments.
func codexLine(ts, limitID, planType, primary, secondary string) string {
	var parts []string
	if limitID != "" {
		parts = append(parts, `"limit_id":`+limitID)
	}
	if planType != "" {
		parts = append(parts, `"plan_type":"`+planType+`"`)
	}
	if primary != "" {
		parts = append(parts, `"primary":`+primary)
	}
	if secondary != "" {
		parts = append(parts, `"secondary":`+secondary)
	}
	return fmt.Sprintf(`{"timestamp":"%s","payload":{"type":"token_count","rate_limits":{%s}}}`,
		ts, strings.Join(parts, ","))
}

func TestCodexNewestRowWinsAcrossRoots(t *testing.T) {
	def := t.TempDir()
	extra := t.TempDir()
	writeSessionFile(t, def, "a.jsonl",
		codexLine("2026-03-01T10:00:00Z", `"codex"`, `plus`, `{"used_percent":13,"window_minutes":300}`, ``)+"\n")
	writeSessionFile(t, extra, "b.jsonl",
		codexLine("2026-03-01T11:00:00Z", ``, `team`, `{"used_percent":55,"window_minutes":300}`, ``)+"\n")

	// The extra root's row is newer: it wins and its root kind shows up.
	snap := codexSource{defaultRoot: def, extraRoots: []string{extra}}.fetch()
	if snap.Err != nil {
		t.Fatalf("fetch failed: %v", snap.Err)
	}
	if want := filepath.Join(extra, "b.jsonl"); snap.Detail != want {
		t.Errorf("Detail = %q, want %q (the newer row)", snap.Detail, want)
	}
	if snap.Footnote != "local · extra" {
		t.Errorf("Footnote = %q, want the extra root kind", snap.Footnote)
	}
	if snap.Chip != "team" {
		t.Errorf("Chip = %q, want the winning row's plan", snap.Chip)
	}
	if want := time.Date(2026, 3, 1, 11, 0, 0, 0, time.UTC); !snap.Observed.Equal(want) {
		t.Errorf("Observed = %v, want the winning row's timestamp %v", snap.Observed, want)
	}
	if len(snap.Windows) != 1 || snap.Windows[0].Percent != 55 {
		t.Errorf("windows = %+v", snap.Windows)
	}

	// With the older row in the extra root, the default root wins back.
	writeSessionFile(t, extra, "b.jsonl",
		codexLine("2026-03-01T09:00:00Z", ``, `team`, `{"used_percent":55}`, ``)+"\n")
	snap = codexSource{defaultRoot: def, extraRoots: []string{extra}}.fetch()
	if want := filepath.Join(def, "a.jsonl"); snap.Detail != want {
		t.Errorf("Detail = %q, want %q", snap.Detail, want)
	}
	if snap.Footnote != "local · interactive" {
		t.Errorf("Footnote = %q, want the default root kind", snap.Footnote)
	}
}

func TestCodexSkipsMalformedLines(t *testing.T) {
	dir := t.TempDir()
	lines := []string{
		`{"timestamp":"2026-03-01T09:00:00Z","payload":{"type":"turn","message":"not a usage report"}`,
		`this is not json at all`,
		`{"timestamp":"2026-03-01T10:00:00Z","payload":{"type":"token_count","rate_limits":{`,
		codexLine("2026-03-01T11:00:00Z", `"codex"`, `plus`, `{"used_percent":42}`, ``),
	}
	writeSessionFile(t, dir, "a.jsonl", strings.Join(lines, "\n")+"\n")
	snap := codexSource{defaultRoot: dir}.fetch()
	if snap.Err != nil {
		t.Fatalf("malformed lines aborted the scan: %v", snap.Err)
	}
	if len(snap.Windows) != 1 || snap.Windows[0].Percent != 42 {
		t.Errorf("windows = %+v, want the one valid row", snap.Windows)
	}
}

func TestCodexIgnoresOtherLimitIds(t *testing.T) {
	dir := t.TempDir()
	writeSessionFile(t, dir, "a.jsonl",
		codexLine("2026-03-01T10:00:00Z", `"other"`, ``, `{"used_percent":50}`, ``)+"\n")
	snap := codexSource{defaultRoot: dir}.fetch()
	if snap.Err == nil || snap.Err.Error() != "no Codex usage reports found" {
		t.Errorf("err = %v, want no Codex usage reports found", snap.Err)
	}
}

func TestCodexWindowLabels(t *testing.T) {
	cases := []struct {
		name          string
		primary       string
		secondary     string
		wantPrimary   Window
		wantSecondary Window
	}{
		{
			name:          "named windows",
			primary:       `{"used_percent":13,"window_minutes":300}`,
			secondary:     `{"used_percent":7,"window_minutes":10080}`,
			wantPrimary:   Window{Key: "primary", Label: "5-hour", Percent: 13, Length: 5 * time.Hour},
			wantSecondary: Window{Key: "secondary", Label: "Weekly", Percent: 7, Length: 10080 * time.Minute},
		},
		{
			name:        "another minute count",
			primary:     `{"used_percent":21,"window_minutes":90}`,
			wantPrimary: Window{Key: "primary", Label: "90-minute", Percent: 21, Length: 90 * time.Minute},
		},
		{
			name:          "absent window length",
			primary:       `{"used_percent":33}`,
			secondary:     `{"used_percent":11}`,
			wantPrimary:   Window{Key: "primary", Label: "Primary", Percent: 33},
			wantSecondary: Window{Key: "secondary", Label: "Secondary", Percent: 11},
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			dir := t.TempDir()
			// A fresh timestamp: this test is about label mapping, not
			// expiry, so the reading must not be old enough to expire any
			// of the windows under test.
			ts := time.Now().Add(-time.Minute).UTC().Format(time.RFC3339)
			line := codexLine(ts, `"codex"`, ``, tc.primary, tc.secondary)
			writeSessionFile(t, dir, "a.jsonl", line+"\n")
			snap := codexSource{defaultRoot: dir}.fetch()
			if snap.Err != nil {
				t.Fatalf("fetch failed: %v", snap.Err)
			}
			got := map[string]Window{}
			for _, window := range snap.Windows {
				got[window.Key] = window
			}
			if tc.wantPrimary.Key != "" && got[tc.wantPrimary.Key] != tc.wantPrimary {
				t.Errorf("primary = %+v, want %+v", got[tc.wantPrimary.Key], tc.wantPrimary)
			}
			if tc.wantSecondary.Key != "" && got[tc.wantSecondary.Key] != tc.wantSecondary {
				t.Errorf("secondary = %+v, want %+v", got[tc.wantSecondary.Key], tc.wantSecondary)
			}
		})
	}
}

func TestCodexExtraRootsFromEnvironment(t *testing.T) {
	scratch := t.TempDir()
	extraRoot := filepath.Join(scratch, "claude-fork-sandbox.testrun", "codex-sessions")
	writeSessionFile(t, extraRoot, "s.jsonl",
		codexLine("2026-03-01T10:00:00Z", ``, ``, `{"used_percent":44}`, ``)+"\n")
	t.Setenv("CODEX_HOME", t.TempDir()) // default root is empty
	t.Setenv("QUOTATOP_CODEX_ROOTS",
		filepath.Join(scratch, "claude-fork-sandbox.*", "codex-sessions"))

	snap := fetchCodex()
	if snap.Err != nil {
		t.Fatalf("fetch failed: %v", snap.Err)
	}
	if !strings.HasPrefix(snap.Detail, extraRoot) {
		t.Errorf("Detail = %q, want a file under the glob-matched root %q", snap.Detail, extraRoot)
	}
	if snap.Footnote != "local · extra" {
		t.Errorf("Footnote = %q, want the extra root kind", snap.Footnote)
	}
}

func TestCodexExtraRootPatternMatchingNothingIsIgnored(t *testing.T) {
	home := t.TempDir()
	writeSessionFile(t, filepath.Join(home, "sessions"), "a.jsonl",
		codexLine("2026-03-01T10:00:00Z", ``, `plus`, `{"used_percent":44}`, ``)+"\n")
	t.Setenv("CODEX_HOME", home)
	t.Setenv("QUOTATOP_CODEX_ROOTS", "/nonexistent/nowhere-*")

	snap := fetchCodex()
	if snap.Err != nil {
		t.Fatalf("a matching-nothing pattern became an error: %v", snap.Err)
	}
	if snap.Footnote != "local · interactive" {
		t.Errorf("Footnote = %q, want the default root kind", snap.Footnote)
	}
}

func TestCodexNoReports(t *testing.T) {
	snap := codexSource{defaultRoot: t.TempDir(), extraRoots: []string{t.TempDir()}}.fetch()
	if snap.Err == nil || snap.Err.Error() != "no Codex usage reports found" {
		t.Errorf("err = %v, want no Codex usage reports found", snap.Err)
	}
}

// A usage row whose timestamp does not parse must not win: with a zero time
// it would beat every older dated row and its reading would be reported as
// "just now" no matter how old it actually is.
func TestCodexUndatedRowDoesNotWin(t *testing.T) {
	dir := t.TempDir()
	writeSessionFile(t, dir, "a.jsonl",
		codexLine("2026-03-01T10:00:00Z", `"codex"`, `plus`, `{"used_percent":42}`, ``)+"\n"+
			codexLine("", `"codex"`, `plus`, `{"used_percent":99}`, ``)+"\n")
	snap := codexSource{defaultRoot: dir}.fetch()
	if snap.Err != nil {
		t.Fatalf("fetch failed: %v", snap.Err)
	}
	if want := time.Date(2026, 3, 1, 10, 0, 0, 0, time.UTC); !snap.Observed.Equal(want) {
		t.Errorf("Observed = %v, want the dated row's timestamp %v", snap.Observed, want)
	}
	if len(snap.Windows) != 1 || snap.Windows[0].Percent != 42 {
		t.Errorf("windows = %+v, want the dated row, not the undated one", snap.Windows)
	}

	// An all-undated log has no usable report rather than a false one.
	dir2 := t.TempDir()
	writeSessionFile(t, dir2, "a.jsonl",
		codexLine("", `"codex"`, `plus`, `{"used_percent":99}`, ``)+"\n")
	snap = codexSource{defaultRoot: dir2}.fetch()
	if snap.Err == nil || snap.Err.Error() != "no Codex usage reports found" {
		t.Errorf("err = %v, want no Codex usage reports found", snap.Err)
	}
}

// A scan that blocks must not hold the panel: the deadline returns an error
// snapshot and the next poll retries.
func TestCodexHungScanTimesOut(t *testing.T) {
	started := make(chan struct{})
	blocked := make(chan struct{})
	defer close(blocked)
	src := codexSource{defaultRoot: t.TempDir(), timeout: 100 * time.Millisecond, blockScan: func() {
		close(started)
		<-blocked
	}}
	done := make(chan Snapshot, 1)
	go func() { done <- src.fetch() }()
	<-started
	snap := <-done
	if snap.Err == nil || !strings.Contains(snap.Err.Error(), "timed out") {
		t.Errorf("err = %v, want the timeout error", snap.Err)
	}
}

func TestCodexOversizedLineIsSkipped(t *testing.T) {
	oversized := `{"payload":{"type":"turn","message":"` + strings.Repeat("x", 2*1024*1024) + `"}`

	// The valid row after an oversized message is still read and wins.
	dir := t.TempDir()
	writeSessionFile(t, dir, "a.jsonl",
		codexLine("2026-03-01T10:00:00Z", `"codex"`, `plus`, `{"used_percent":42}`, ``)+"\n"+oversized+"\n"+
			codexLine("2026-03-01T11:00:00Z", `"codex"`, `plus`, `{"used_percent":99}`, ``)+"\n")
	snap := codexSource{defaultRoot: dir}.fetch()
	if snap.Err != nil {
		t.Fatalf("fetch failed: %v", snap.Err)
	}
	if len(snap.Windows) != 1 || snap.Windows[0].Percent != 99 {
		t.Errorf("windows = %+v, want the row after the oversized line", snap.Windows)
	}
	if snap.Warning != "" {
		t.Errorf("Warning = %q, want none", snap.Warning)
	}

	// An oversized first row does not prevent later valid rows from being read.
	dir2 := t.TempDir()
	writeSessionFile(t, dir2, "a.jsonl", oversized+"\n"+
		codexLine("2026-03-01T10:00:00Z", `"codex"`, `plus`, `{"used_percent":42}`, ``)+"\n")
	snap = codexSource{defaultRoot: dir2}.fetch()
	if snap.Err != nil || len(snap.Windows) != 1 || snap.Windows[0].Percent != 42 || snap.Warning != "" {
		t.Errorf("first oversized line snapshot = %+v, want the later row without a warning", snap)
	}
}

func TestCodexConsecutiveOversizedLinesAreSkipped(t *testing.T) {
	dir := t.TempDir()
	oversized := strings.Repeat("x", 2*1024*1024)
	writeSessionFile(t, dir, "a.jsonl",
		codexLine("2026-03-01T10:00:00Z", `"codex"`, `plus`, `{"used_percent":42}`, ``)+"\n"+
			oversized+"\n"+oversized+"\n"+
			codexLine("2026-03-01T11:00:00Z", `"codex"`, `plus`, `{"used_percent":99}`, ``)+"\n")

	snap := codexSource{defaultRoot: dir}.fetch()
	if snap.Err != nil || len(snap.Windows) != 1 || snap.Windows[0].Percent != 99 || snap.Warning != "" {
		t.Errorf("snapshot = %+v, want the later row without a warning", snap)
	}
}

func TestCodexOversizedFinalLineIsSkipped(t *testing.T) {
	dir := t.TempDir()
	writeSessionFile(t, dir, "a.jsonl",
		codexLine("2026-03-01T10:00:00Z", `"codex"`, `plus`, `{"used_percent":42}`, ``)+"\n"+
			strings.Repeat("x", 2*1024*1024))

	snap := codexSource{defaultRoot: dir}.fetch()
	if snap.Err != nil || len(snap.Windows) != 1 || snap.Windows[0].Percent != 42 || snap.Warning != "" {
		t.Errorf("snapshot = %+v, want the preceding row without a warning", snap)
	}
}

func TestCodexOnlyOversizedLineContributesNoReport(t *testing.T) {
	dir := t.TempDir()
	writeSessionFile(t, dir, "a.jsonl", strings.Repeat("x", 2*1024*1024))

	snap := codexSource{defaultRoot: dir}.fetch()
	if snap.Err == nil || snap.Warning != "" || len(snap.Windows) != 0 {
		t.Errorf("snapshot = %+v, want no report and no warning", snap)
	}
}

func TestCodexFinalUnterminatedUsageRowWins(t *testing.T) {
	dir := t.TempDir()
	writeSessionFile(t, dir, "a.jsonl",
		codexLine("2026-03-01T10:00:00Z", `"codex"`, `plus`, `{"used_percent":42}`, ``)+"\n"+
			codexLine("2026-03-01T11:00:00Z", `"codex"`, `plus`, `{"used_percent":99}`, ``))

	snap := codexSource{defaultRoot: dir}.fetch()
	if snap.Err != nil || len(snap.Windows) != 1 || snap.Windows[0].Percent != 99 {
		t.Errorf("snapshot = %+v, want the unterminated final row", snap)
	}
}

func TestCodexHalfWrittenFinalLineIsSkippedAcrossRoots(t *testing.T) {
	def := t.TempDir()
	extra := t.TempDir()
	writeSessionFile(t, def, "a.jsonl",
		codexLine("2026-03-01T10:00:00Z", `"codex"`, `plus`, `{"used_percent":42}`, ``)+"\n"+
			`{"timestamp":"2026-03-01T12:00:00Z","payload":`)
	writeSessionFile(t, extra, "b.jsonl",
		codexLine("2026-03-01T11:00:00Z", `"codex"`, `team`, `{"used_percent":99}`, ``)+"\n")

	snap := codexSource{defaultRoot: def, extraRoots: []string{extra}}.fetch()
	if snap.Err != nil || len(snap.Windows) != 1 || snap.Windows[0].Percent != 99 || snap.Detail != filepath.Join(extra, "b.jsonl") {
		t.Errorf("snapshot = %+v, want the newest complete row across roots", snap)
	}
}

// codexBlockedLine builds a token_count row shaped like a real blocked
// signal: limit_id "premium", both windows null, and a
// rate_limit_reached_type. This is the exact shape an archived log carries
// (both windows null is why the limit_id filter must reject the row for
// percentage purposes while still yielding the reached type).
func codexBlockedLine(ts, reachedType string) string {
	return fmt.Sprintf(`{"timestamp":"%s","type":"event_msg","payload":{"type":"token_count","rate_limits":{`+
		`"limit_id":"premium","primary":null,"secondary":null,"plan_type":"team",`+
		`"rate_limit_reached_type":"%s"}}}`, ts, reachedType)
}

// The newest row being a blocked "premium" row with null windows must not
// blank the percentages: they still come from the newest usable "codex" row,
// while the block itself is surfaced separately.
func TestCodexBlockedRowSetsLimitReachedWithoutLosingPercentages(t *testing.T) {
	dir := t.TempDir()
	early := time.Now().Add(-2 * time.Hour).UTC().Format(time.RFC3339)
	late := time.Now().Add(-1 * time.Hour).UTC().Format(time.RFC3339)
	writeSessionFile(t, dir, "a.jsonl",
		codexLine(early, `"codex"`, `plus`, `{"used_percent":42,"window_minutes":300}`, ``)+"\n"+
			codexBlockedLine(late, "workspace_member_usage_limit_reached")+"\n")
	snap := codexSource{defaultRoot: dir}.fetch()
	if snap.Err != nil {
		t.Fatalf("fetch failed: %v", snap.Err)
	}
	if len(snap.Windows) != 1 || snap.Windows[0].Percent != 42 {
		t.Errorf("windows = %+v, want the earlier codex row's percentage", snap.Windows)
	}
	if snap.LimitReached != "workspace_member_usage_limit_reached" {
		t.Errorf("LimitReached = %q, want the reached type from the newer blocked row", snap.LimitReached)
	}
}

// A blocked row followed by a *newer* normal codex row means the block is
// over. This does not exercise the reachedType/reachedAt carry-across in
// codexReport.consider: here the block is strictly older than the only later
// row, so "" is what both the correct code and a build with the carry-across
// deleted produce. TestCodexBlockSurvivesReplacementByATiedNormalRow below is
// the case that actually needs the carry-across.
func TestCodexNewerNormalRowDropsOlderBlock(t *testing.T) {
	dir := t.TempDir()
	writeSessionFile(t, dir, "a.jsonl",
		codexBlockedLine("2026-03-01T10:00:00Z", "workspace_member_usage_limit_reached")+"\n"+
			codexLine("2026-03-01T11:00:00Z", `"codex"`, `plus`, `{"used_percent":77,"window_minutes":300}`, ``)+"\n")
	snap := codexSource{defaultRoot: dir}.fetch()
	if snap.Err != nil {
		t.Fatalf("fetch failed: %v", snap.Err)
	}
	if snap.LimitReached != "" {
		t.Errorf("LimitReached = %q, want empty: the block is older than the reading", snap.LimitReached)
	}
	if len(snap.Windows) != 1 || snap.Windows[0].Percent != 77 {
		t.Errorf("windows = %+v, want the newer row's percentage", snap.Windows)
	}
}

// A blocked row newer than the newest normal row must be reported, even when
// it is not literally the last line scanned (the extra root's row makes this
// distinct from the "newest row is blocked" case above).
func TestCodexBlockedRowNewerThanNormalRowAcrossRoots(t *testing.T) {
	def := t.TempDir()
	extra := t.TempDir()
	early := time.Now().Add(-2 * time.Hour).UTC().Format(time.RFC3339)
	late := time.Now().Add(-1 * time.Hour).UTC().Format(time.RFC3339)
	writeSessionFile(t, def, "a.jsonl",
		codexLine(early, `"codex"`, `plus`, `{"used_percent":20,"window_minutes":300}`, ``)+"\n")
	writeSessionFile(t, extra, "b.jsonl",
		codexBlockedLine(late, "workspace_member_usage_limit_reached")+"\n")
	snap := codexSource{defaultRoot: def, extraRoots: []string{extra}}.fetch()
	if snap.Err != nil {
		t.Fatalf("fetch failed: %v", snap.Err)
	}
	if snap.LimitReached != "workspace_member_usage_limit_reached" {
		t.Errorf("LimitReached = %q, want the block from the extra root's newer row", snap.LimitReached)
	}
	if len(snap.Windows) != 1 || snap.Windows[0].Percent != 20 {
		t.Errorf("windows = %+v, want the default root's percentage", snap.Windows)
	}
}

// TRAP regression: a blocked row that ties the newest normal row, with an
// older normal row before it, must survive codexReport.consider
// wholesale-replacing the struct when that newest row wins. reachedType and
// reachedAt are captured while processing the blocked row, then the later
// tied row's struct-literal replacement would silently reset them to zero
// values without the explicit carry-across (the same way scanErr is
// carried). Unlike TestCodexNewerNormalRowDropsOlderBlock, the correct
// answer here is a reported block, so deleting the carry-across changes the
// result instead of leaving it coincidentally unchanged.
func TestCodexBlockSurvivesReplacementByATiedNormalRow(t *testing.T) {
	dir := t.TempDir()
	before := time.Now().Add(-3 * time.Hour)
	tied := time.Now().Add(-2 * time.Hour)
	writeSessionFile(t, dir, "a.jsonl",
		codexLine(before.UTC().Format(time.RFC3339), `"codex"`, `plus`, `{"used_percent":10,"window_minutes":300}`, ``)+"\n"+
			codexBlockedLine(tied.UTC().Format(time.RFC3339), "workspace_member_usage_limit_reached")+"\n"+
			codexLine(tied.UTC().Format(time.RFC3339), `"codex"`, `plus`, `{"used_percent":88,"window_minutes":300}`, ``)+"\n")
	snap := codexSource{defaultRoot: dir}.fetch()
	if snap.Err != nil {
		t.Fatalf("fetch failed: %v", snap.Err)
	}
	if snap.LimitReached != "workspace_member_usage_limit_reached" {
		t.Errorf("LimitReached = %q, want the block: it ties the newest normal row, not older than it", snap.LimitReached)
	}
	if len(snap.Windows) != 1 || snap.Windows[0].Percent != 88 {
		t.Errorf("windows = %+v, want the tied row's percentage (ties go to the later row)", snap.Windows)
	}
}

// A blocked signal with nothing newer in the log goes stale against the wall
// clock, not just against the reading: a week with no fresh row means
// whatever window produced the block has certainly rolled over since.
func TestCodexStaleBlockIsNotReported(t *testing.T) {
	dir := t.TempDir()
	older := time.Now().Add(-8 * 24 * time.Hour).UTC().Format(time.RFC3339)
	old := time.Now().Add(-8*24*time.Hour + time.Minute).UTC().Format(time.RFC3339)
	writeSessionFile(t, dir, "a.jsonl",
		codexLine(older, `"codex"`, `plus`, `{"used_percent":97,"window_minutes":300}`, ``)+"\n"+
			codexBlockedLine(old, "workspace_member_usage_limit_reached")+"\n")
	snap := codexSource{defaultRoot: dir}.fetch()
	if snap.Err != nil {
		t.Fatalf("fetch failed: %v", snap.Err)
	}
	if snap.LimitReached != "" {
		t.Errorf("LimitReached = %q, want empty: an 8-day-old block with nothing fresher is stale", snap.LimitReached)
	}
}

// windowExpired is the single expression of the expiry rule; exercise its
// boundary directly rather than through file I/O and the real clock.
func TestWindowExpiredBoundary(t *testing.T) {
	now := time.Date(2026, 3, 1, 12, 0, 0, 0, time.UTC)
	cases := []struct {
		name     string
		length   time.Duration
		observed time.Time
		want     bool
	}{
		{"age equals length is expired", 5 * time.Hour, now.Add(-5 * time.Hour), true},
		{"one second short of length is not expired", 5 * time.Hour, now.Add(-5*time.Hour + time.Second), false},
		{"zero length is never expired", 0, now.Add(-1000 * time.Hour), false},
		{"well within the window is not expired", 5 * time.Hour, now.Add(-time.Hour), false},
		{"well past the window is expired", 5 * time.Hour, now.Add(-6 * time.Hour), true},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := windowExpired(tc.length, tc.observed, now); got != tc.want {
				t.Errorf("windowExpired(%v, %v, %v) = %v, want %v", tc.length, tc.observed, now, got, tc.want)
			}
		})
	}
}

// Expiry is a per-window property: a 10-hour-old reading expires the 5-hour
// window but leaves the weekly window, from the very same reading, valid.
func TestCodexPerWindowExpiry(t *testing.T) {
	dir := t.TempDir()
	ts := time.Now().Add(-10 * time.Hour).UTC().Format(time.RFC3339)
	writeSessionFile(t, dir, "a.jsonl",
		codexLine(ts, `"codex"`, `plus`,
			`{"used_percent":42,"window_minutes":300}`, `{"used_percent":11,"window_minutes":10080}`)+"\n")
	snap := codexSource{defaultRoot: dir}.fetch()
	if snap.Err != nil {
		t.Fatalf("fetch failed: %v", snap.Err)
	}
	got := map[string]Window{}
	for _, window := range snap.Windows {
		got[window.Key] = window
	}
	if primary := got["primary"]; !primary.Expired {
		t.Errorf("primary (5h) window = %+v, want Expired", primary)
	}
	if secondary := got["secondary"]; secondary.Expired {
		t.Errorf("secondary (weekly) window = %+v, want not Expired", secondary)
	}
}

// --- Config ---------------------------------------------------------------

func writeConfigFile(t *testing.T, content string) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), "config")
	if err := os.WriteFile(path, []byte(content), 0o600); err != nil {
		t.Fatal(err)
	}
	return path
}

// setConfigValues installs a config map for the duration of the test, the
// way main() would have loaded one from a file.
func setConfigValues(t *testing.T, values map[string]string) {
	t.Helper()
	old := configValues
	configValues = values
	t.Cleanup(func() { configValues = old })
}

// isolateAccountEnv restricts settingsWithPrefix, for the rest of the test,
// to see only the named environment variables instead of the real ambient
// environment -- so a QUOTATOP_*_ACCOUNT_* value a developer has exported on
// their own machine for their own use can never leak into a test that
// enumerates a specific, known set of labels (or none at all). Call it after
// any t.Setenv for the keys the test wants visible.
func isolateAccountEnv(t *testing.T, keys ...string) {
	t.Helper()
	old := settingsWithPrefixEnvKeys
	visible := map[string]bool{}
	for _, key := range keys {
		visible[key] = true
	}
	settingsWithPrefixEnvKeys = visible
	t.Cleanup(func() { settingsWithPrefixEnvKeys = old })
}

func TestLoadConfigPlainKeyValues(t *testing.T) {
	path := writeConfigFile(t,
		"QUOTATOP_CODEX_ROOTS=/var/tmp/agent-runs/session.*/codex-sessions\n"+
			"QUOTATOP_HISTORY=/var/tmp/history.jsonl\n")
	got := loadConfig(path)
	if len(got) != 2 {
		t.Fatalf("got %d keys, want 2: %v", len(got), got)
	}
	if want := "/var/tmp/agent-runs/session.*/codex-sessions"; got["QUOTATOP_CODEX_ROOTS"] != want {
		t.Errorf("QUOTATOP_CODEX_ROOTS = %q, want %q", got["QUOTATOP_CODEX_ROOTS"], want)
	}
	if want := "/var/tmp/history.jsonl"; got["QUOTATOP_HISTORY"] != want {
		t.Errorf("QUOTATOP_HISTORY = %q, want %q", got["QUOTATOP_HISTORY"], want)
	}
}

func TestLoadConfigCommentsBlanksAndWhitespace(t *testing.T) {
	path := writeConfigFile(t,
		"# a comment\n"+
			"\n"+
			"   # an indented comment\n"+
			"   QUOTATOP_HISTORY   =   /var/tmp/history.jsonl   \n"+
			"\tQUOTATOP_CODEX_ROOTS\t=/var/tmp/roots\n")
	got := loadConfig(path)
	if len(got) != 2 {
		t.Fatalf("got %d keys, want 2: %v", len(got), got)
	}
	if got["QUOTATOP_HISTORY"] != "/var/tmp/history.jsonl" {
		t.Errorf("QUOTATOP_HISTORY = %q, want the trimmed value", got["QUOTATOP_HISTORY"])
	}
	if got["QUOTATOP_CODEX_ROOTS"] != "/var/tmp/roots" {
		t.Errorf("QUOTATOP_CODEX_ROOTS = %q, want the trimmed value", got["QUOTATOP_CODEX_ROOTS"])
	}
}

func TestLoadConfigValueMayContainEquals(t *testing.T) {
	path := writeConfigFile(t, "QUOTATOP_HISTORY=a=b=c\n")
	got := loadConfig(path)
	if want := "a=b=c"; got["QUOTATOP_HISTORY"] != want {
		t.Errorf("QUOTATOP_HISTORY = %q, want %q (split on the first '=' only)",
			got["QUOTATOP_HISTORY"], want)
	}
}

func TestLoadConfigQuotes(t *testing.T) {
	path := writeConfigFile(t,
		`QUOTATOP_HISTORY="/var/tmp/double-quoted.jsonl"`+"\n"+
			`QUOTATOP_CODEX_ROOTS='/var/tmp/single-quoted'`+"\n"+
			`QUOTATOP_CLAUDE_CREDENTIALS="unmatched`+"\n")
	got := loadConfig(path)
	if want := "/var/tmp/double-quoted.jsonl"; got["QUOTATOP_HISTORY"] != want {
		t.Errorf("QUOTATOP_HISTORY = %q, want the unquoted value", got["QUOTATOP_HISTORY"])
	}
	if want := "/var/tmp/single-quoted"; got["QUOTATOP_CODEX_ROOTS"] != want {
		t.Errorf("QUOTATOP_CODEX_ROOTS = %q, want the unquoted value", got["QUOTATOP_CODEX_ROOTS"])
	}
	// An unmatched quote is not quoting: it is left alone.
	if want := `"unmatched`; got["QUOTATOP_CLAUDE_CREDENTIALS"] != want {
		t.Errorf("QUOTATOP_CLAUDE_CREDENTIALS = %q, want %q (quote left alone)",
			got["QUOTATOP_CLAUDE_CREDENTIALS"], want)
	}
}

func TestLoadConfigTildeExpansion(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	path := writeConfigFile(t,
		"QUOTATOP_HISTORY=~/cache/quotatop/history.jsonl\n"+
			"QUOTATOP_CODEX_ROOTS=~\n"+
			"QUOTATOP_CLAUDE_CREDENTIALS=~user/.credentials.json\n")
	got := loadConfig(path)
	if want := filepath.Join(home, "cache", "quotatop", "history.jsonl"); got["QUOTATOP_HISTORY"] != want {
		t.Errorf("QUOTATOP_HISTORY = %q, want %q", got["QUOTATOP_HISTORY"], want)
	}
	if got["QUOTATOP_CODEX_ROOTS"] != "~" {
		t.Errorf("QUOTATOP_CODEX_ROOTS = %q, want a bare ~ left alone", got["QUOTATOP_CODEX_ROOTS"])
	}
	if got["QUOTATOP_CLAUDE_CREDENTIALS"] != "~user/.credentials.json" {
		t.Errorf("QUOTATOP_CLAUDE_CREDENTIALS = %q, want a ~user form left alone",
			got["QUOTATOP_CLAUDE_CREDENTIALS"])
	}
}

func TestLoadConfigLineWithoutEqualsIsSkipped(t *testing.T) {
	path := writeConfigFile(t,
		"not a setting line\n"+
			"QUOTATOP_HISTORY=/var/tmp/history.jsonl\n")
	got := loadConfig(path)
	if len(got) != 1 || got["QUOTATOP_HISTORY"] != "/var/tmp/history.jsonl" {
		t.Errorf("got %v, want the one key after the malformed line", got)
	}
}

func TestLoadConfigLaterKeyWins(t *testing.T) {
	path := writeConfigFile(t,
		"QUOTATOP_HISTORY=/var/tmp/first.jsonl\n"+
			"QUOTATOP_HISTORY=/var/tmp/second.jsonl\n")
	got := loadConfig(path)
	if want := "/var/tmp/second.jsonl"; got["QUOTATOP_HISTORY"] != want {
		t.Errorf("QUOTATOP_HISTORY = %q, want the later line to win", got["QUOTATOP_HISTORY"])
	}
}

func TestLoadConfigMissingFileIsAnEmptyMap(t *testing.T) {
	got := loadConfig(filepath.Join(t.TempDir(), "absent"))
	if len(got) != 0 {
		t.Errorf("got %v, want an empty map", got)
	}
}

func TestLoadConfigDirectoryIsAnEmptyMap(t *testing.T) {
	got := loadConfig(t.TempDir())
	if len(got) != 0 {
		t.Errorf("got %v, want an empty map", got)
	}
}

func TestConfigPathPrecedence(t *testing.T) {
	custom := filepath.Join(t.TempDir(), "custom")
	xdg := filepath.Join(t.TempDir(), "xdg")
	home := t.TempDir()
	t.Setenv("HOME", home)

	t.Setenv("QUOTATOP_CONFIG", custom)
	if got, want := configPath(), custom; got != want {
		t.Errorf("configPath = %q, want QUOTATOP_CONFIG to win: %q", got, want)
	}

	t.Setenv("QUOTATOP_CONFIG", "")
	t.Setenv("XDG_CONFIG_HOME", xdg)
	if got, want := configPath(), filepath.Join(xdg, "quotatop", "config"); got != want {
		t.Errorf("configPath = %q, want the XDG path %q", got, want)
	}

	t.Setenv("XDG_CONFIG_HOME", "")
	if got, want := configPath(), filepath.Join(home, ".config", "quotatop", "config"); got != want {
		t.Errorf("configPath = %q, want the home fallback %q", got, want)
	}
}

func TestSettingEnvironmentBeatsConfigFile(t *testing.T) {
	t.Setenv("QUOTATOP_HISTORY", "/var/tmp/from-env.jsonl")
	setConfigValues(t, map[string]string{"QUOTATOP_HISTORY": "/var/tmp/from-file.jsonl"})
	if got := setting("QUOTATOP_HISTORY"); got != "/var/tmp/from-env.jsonl" {
		t.Errorf("setting = %q, want the environment to win", got)
	}
}

func TestSettingFallsBackToConfigFile(t *testing.T) {
	t.Setenv("QUOTATOP_HISTORY", "")
	setConfigValues(t, map[string]string{"QUOTATOP_HISTORY": "/var/tmp/from-file.jsonl"})
	if got := setting("QUOTATOP_HISTORY"); got != "/var/tmp/from-file.jsonl" {
		t.Errorf("setting = %q, want the config file value", got)
	}
}

func TestSettingEmptyEnvironmentCountsAsUnset(t *testing.T) {
	t.Setenv("QUOTATOP_HISTORY", "")
	setConfigValues(t, map[string]string{"QUOTATOP_HISTORY": "/var/tmp/from-file.jsonl"})
	if got := setting("QUOTATOP_HISTORY"); got != "/var/tmp/from-file.jsonl" {
		t.Errorf("setting = %q, want the config file to apply to a cleared variable", got)
	}
}

func TestSettingNilConfigMapIsTheEnvironmentAlone(t *testing.T) {
	setConfigValues(t, nil)
	t.Setenv("QUOTATOP_HISTORY", "")
	if got := setting("QUOTATOP_HISTORY"); got != "" {
		t.Errorf("setting = %q, want \"\" with a nil config map", got)
	}
	t.Setenv("QUOTATOP_HISTORY", "/var/tmp/from-env.jsonl")
	if got := setting("QUOTATOP_HISTORY"); got != "/var/tmp/from-env.jsonl" {
		t.Errorf("setting = %q, want the environment value", got)
	}
}

// End to end: the file names a temp dir as a Codex root and defaultCodexSource
// picks it up through the same code path main() uses.
func TestConfigFileSuppliesCodexRoots(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("CODEX_HOME", t.TempDir())
	t.Setenv("QUOTATOP_CODEX_ROOTS", "")
	t.Setenv("QUOTATOP_CONFIG", "")
	scratch := t.TempDir()
	root := filepath.Join(scratch, "claude-fork-sandbox.run", "codex-sessions")
	writeSessionFile(t, root, "s.jsonl",
		codexLine("2026-03-01T10:00:00Z", ``, ``, `{"used_percent":44}`, ``)+"\n")

	t.Setenv("QUOTATOP_CONFIG", writeConfigFile(t,
		"QUOTATOP_CODEX_ROOTS="+filepath.Join(scratch, "claude-fork-sandbox.*", "codex-sessions")+"\n"))
	setConfigValues(t, loadConfig(configPath()))

	src := defaultCodexSource()
	if len(src.extraRoots) != 1 || src.extraRoots[0] != root {
		t.Fatalf("extraRoots = %v, want the glob-matched root %q", src.extraRoots, root)
	}
	snap := fetchCodex()
	if snap.Err != nil {
		t.Fatalf("fetch failed: %v", snap.Err)
	}
	if snap.Detail != filepath.Join(root, "s.jsonl") {
		t.Errorf("Detail = %q, want a file from the file-named root", snap.Detail)
	}
	if snap.Footnote != "local · extra" {
		t.Errorf("Footnote = %q, want the extra root kind", snap.Footnote)
	}
}

// The two-root case: a home-relative second element must have its ~/ expanded
// per element, not once for the whole value, or it would be handed to
// filepath.Glob literally and match nothing.
func TestConfigFileExpandsTildePerRootElement(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("CODEX_HOME", t.TempDir())
	t.Setenv("QUOTATOP_CODEX_ROOTS", "")
	t.Setenv("QUOTATOP_CONFIG", "")
	scratch := t.TempDir()
	absolute := filepath.Join(scratch, "claude-fork-sandbox.run", "codex-sessions")
	expanded := filepath.Join(home, ".claude", "codex-quota")
	writeSessionFile(t, absolute, "a.jsonl",
		codexLine("2026-03-01T10:00:00Z", ``, ``, `{"used_percent":44}`, ``)+"\n")
	writeSessionFile(t, expanded, "b.jsonl",
		codexLine("2026-03-01T11:00:00Z", ``, ``, `{"used_percent":55}`, ``)+"\n")

	t.Setenv("QUOTATOP_CONFIG", writeConfigFile(t,
		"QUOTATOP_CODEX_ROOTS="+filepath.Join(scratch, "claude-fork-sandbox.*", "codex-sessions")+
			":~/.claude/codex-quota\n"))
	setConfigValues(t, loadConfig(configPath()))

	src := defaultCodexSource()
	if len(src.extraRoots) != 2 {
		t.Fatalf("extraRoots = %v, want both roots (the tilde element expanded)", src.extraRoots)
	}
	snap := fetchCodex()
	if snap.Err != nil {
		t.Fatalf("fetch failed: %v", snap.Err)
	}
	if want := filepath.Join(expanded, "b.jsonl"); snap.Detail != want {
		t.Errorf("Detail = %q, want the newer row from the tilde-expanded root %q", snap.Detail, want)
	}
}

// An explicit variable on the command line overrides the file's value: that
// is the whole point of having both.
func TestEnvironmentOverridesConfigFileCodexRoots(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("CODEX_HOME", t.TempDir())
	t.Setenv("QUOTATOP_CONFIG", "")
	fileRoot := filepath.Join(t.TempDir(), "from-file")
	envRoot := filepath.Join(t.TempDir(), "from-env")
	writeSessionFile(t, fileRoot, "a.jsonl",
		codexLine("2026-03-01T10:00:00Z", ``, ``, `{"used_percent":44}`, ``)+"\n")
	writeSessionFile(t, envRoot, "b.jsonl",
		codexLine("2026-03-01T10:00:00Z", ``, ``, `{"used_percent":55}`, ``)+"\n")

	t.Setenv("QUOTATOP_CONFIG", writeConfigFile(t, "QUOTATOP_CODEX_ROOTS="+fileRoot+"\n"))
	setConfigValues(t, loadConfig(configPath()))
	t.Setenv("QUOTATOP_CODEX_ROOTS", envRoot)

	src := defaultCodexSource()
	if len(src.extraRoots) != 1 || src.extraRoots[0] != envRoot {
		t.Fatalf("extraRoots = %v, want only the environment's root %q", src.extraRoots, envRoot)
	}
}

func TestClaudeCredentialsOverride(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	absent := filepath.Join(t.TempDir(), "absent.json")

	// The environment override wins over the default path.
	t.Setenv("QUOTATOP_CLAUDE_CREDENTIALS", absent)
	setConfigValues(t, nil)
	if got := defaultClaudeSource().credentialsPath; got != absent {
		t.Errorf("credentialsPath = %q, want the environment override %q", got, absent)
	}

	// The config file applies when the variable is unset.
	t.Setenv("QUOTATOP_CLAUDE_CREDENTIALS", "")
	setConfigValues(t, map[string]string{"QUOTATOP_CLAUDE_CREDENTIALS": absent})
	if got := defaultClaudeSource().credentialsPath; got != absent {
		t.Errorf("credentialsPath = %q, want the config file value %q", got, absent)
	}

	// Neither set: the default path under the home directory.
	setConfigValues(t, nil)
	got := defaultClaudeSource().credentialsPath
	if want := filepath.Join(home, ".claude", ".credentials.json"); got != want {
		t.Errorf("credentialsPath = %q, want the default %q", got, want)
	}
}

// The empty account (today's only account) must keep the exact cache
// filename quotatop has always used: a future non-empty account gets a
// filename of its own, but this round adds no way to set one, so the
// default must be pinned unchanged.
func TestClaudeDefaultCachePathIsUnchangedForEmptyAccount(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	setConfigValues(t, nil)
	got := defaultClaudeSource().resolvedCachePath()
	want := filepath.Join(home, ".cache", "quotatop", "claude-quota.json")
	if got != want {
		t.Errorf("resolvedCachePath() = %q, want the unchanged default %q", got, want)
	}
}

// resolvedCachePath must reflect the account actually set on the source, not
// whatever was set (typically none) at construction time: an account
// assigned after defaultClaudeSource returns -- the only way one can be set
// until multi-account configuration lands -- still has to land in its own
// cache file, not silently share the account-less one.
func TestClaudeResolvedCachePathTracksAccountSetAfterConstruction(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	setConfigValues(t, nil)
	source := defaultClaudeSource()
	source.account = "work"
	got := source.resolvedCachePath()
	want := filepath.Join(home, ".cache", "quotatop", "claude-quota-work.json")
	if got != want {
		t.Errorf("resolvedCachePath() = %q, want the account-specific path %q", got, want)
	}
}

// Two named accounts must never resolve to the same cache file as each
// other or as the no-account default: a shared cache would make a plain
// read report the wrong account's quota.
func TestClaudeResolvedCachePathDistinctBetweenTwoAccounts(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	setConfigValues(t, nil)
	base := defaultClaudeSource()
	noAccount := base.resolvedCachePath()

	work := base
	work.account = "work"
	personal := base
	personal.account = "personal"
	workPath, personalPath := work.resolvedCachePath(), personal.resolvedCachePath()

	if workPath == personalPath {
		t.Errorf("work and personal both resolved to %q, want distinct cache files", workPath)
	}
	if workPath == noAccount {
		t.Errorf("work resolved to the no-account path %q", noAccount)
	}
	if personalPath == noAccount {
		t.Errorf("personal resolved to the no-account path %q", noAccount)
	}
}

// A snapshot with no account keeps its title exactly as today; one with an
// account is qualified with the " · " separator the codebase already uses
// for a qualified label.
func TestPanelTitle(t *testing.T) {
	if got, want := panelTitle("CLAUDE", ""), "CLAUDE"; got != want {
		t.Errorf("panelTitle(CLAUDE, \"\") = %q, want %q", got, want)
	}
	if got, want := panelTitle("CLAUDE", "work"), "CLAUDE · work"; got != want {
		t.Errorf("panelTitle(CLAUDE, work) = %q, want %q", got, want)
	}
}

// An account is user-config input, not a trusted path fragment: it must stay
// a single filename component so it can never steer the cache outside its
// directory or into an arbitrary subdirectory.
func TestClaudeCacheFileNameRejectsPathTraversal(t *testing.T) {
	for _, account := range []string{"../../secrets", "a/b", "/etc/passwd", "..", `a\b`} {
		name := claudeCacheFileName(account)
		if strings.ContainsAny(name, `/\`) {
			t.Errorf("claudeCacheFileName(%q) = %q, want no path separator", account, name)
		}
		if filepath.Base(name) != name {
			t.Errorf("claudeCacheFileName(%q) = %q, want a single path component", account, name)
		}
	}
}

// The escaping has to be injective: two accounts that only differ in how
// they'd naively collide after sanitisation must still land on distinct
// filenames, or one account's cache silently answers for both.
func TestClaudeCacheFileNameDoesNotCollide(t *testing.T) {
	pairs := [][2]string{
		{"a/b", "a_b"},
		{"a%2Fb", "a/b"},
		{`a\b`, "a_b"},
	}
	for _, pair := range pairs {
		if got := claudeCacheFileName(pair[0]); got == claudeCacheFileName(pair[1]) {
			t.Errorf("claudeCacheFileName(%q) and claudeCacheFileName(%q) both = %q, want distinct names", pair[0], pair[1], got)
		}
	}
}

// If the home directory cannot be resolved, the cache must be disabled
// outright rather than falling back to a path relative to the current
// working directory: a scrubbed-environment invocation (no HOME) must never
// read or write a cache file wherever it happened to be started.
func TestClaudeEmptyCacheDirDisablesCache(t *testing.T) {
	stub := &claudeStub{status: 200, body: realisticClaudePayload}
	src := claudeSource{
		doRequest:       stub.do,
		credentialsPath: filepath.Join(t.TempDir(), "absent.json"),
	}
	if got := src.resolvedCachePath(); got != "" {
		t.Fatalf("resolvedCachePath() = %q, want empty with no cacheDir", got)
	}
	cwd, err := os.Getwd()
	if err != nil {
		t.Fatal(err)
	}
	before, err := os.ReadDir(cwd)
	if err != nil {
		t.Fatal(err)
	}
	src.writeCache(claudePayload{}, time.Now())
	after, err := os.ReadDir(cwd)
	if err != nil {
		t.Fatal(err)
	}
	if len(after) != len(before) {
		t.Errorf("writeCache with no cacheDir touched the working directory: before %v, after %v", before, after)
	}
}

// The config file's QUOTATOP_HISTORY reaches the history path, with the
// leading ~/ expanded by the loader.
func TestConfigFileSuppliesHistoryPath(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("QUOTATOP_HISTORY", "")
	t.Setenv("QUOTATOP_CONFIG", "")
	t.Setenv("QUOTATOP_CONFIG", writeConfigFile(t, "QUOTATOP_HISTORY=~/.cache/quotatop/history.jsonl\n"))
	setConfigValues(t, loadConfig(configPath()))
	if got, want := defaultHistoryPath(), filepath.Join(home, ".cache", "quotatop", "history.jsonl"); got != want {
		t.Errorf("defaultHistoryPath = %q, want the file's value %q", got, want)
	}
}

func TestSettingsWithPrefixFromConfigFileOnly(t *testing.T) {
	setConfigValues(t, map[string]string{
		"QUOTATOP_CLAUDE_ACCOUNT_work":     "/creds/work.json",
		"QUOTATOP_CLAUDE_ACCOUNT_personal": "/creds/personal.json",
		"QUOTATOP_CODEX_ROOTS":             "/irrelevant",
	})
	isolateAccountEnv(t)
	got := settingsWithPrefix("QUOTATOP_CLAUDE_ACCOUNT_")
	want := map[string]string{"work": "/creds/work.json", "personal": "/creds/personal.json"}
	if len(got) != len(want) || got["work"] != want["work"] || got["personal"] != want["personal"] {
		t.Errorf("settingsWithPrefix = %#v, want %#v", got, want)
	}
}

func TestSettingsWithPrefixFromEnvironmentOnly(t *testing.T) {
	setConfigValues(t, nil)
	t.Setenv("QUOTATOP_CLAUDE_ACCOUNT_work", "/creds/work.json")
	got := settingsWithPrefix("QUOTATOP_CLAUDE_ACCOUNT_")
	if got["work"] != "/creds/work.json" {
		t.Errorf("settingsWithPrefix = %#v, want the environment value", got)
	}
}

func TestSettingsWithPrefixEnvironmentOverridesFileForSameLabel(t *testing.T) {
	setConfigValues(t, map[string]string{"QUOTATOP_CLAUDE_ACCOUNT_work": "/creds/from-file.json"})
	t.Setenv("QUOTATOP_CLAUDE_ACCOUNT_work", "/creds/from-env.json")
	got := settingsWithPrefix("QUOTATOP_CLAUDE_ACCOUNT_")
	if got["work"] != "/creds/from-env.json" {
		t.Errorf("settingsWithPrefix[work] = %q, want the environment value to win", got["work"])
	}
}

func TestSettingsWithPrefixEmptyEnvironmentDisablesFileDeclaredLabel(t *testing.T) {
	setConfigValues(t, map[string]string{"QUOTATOP_CLAUDE_ACCOUNT_work": "/creds/from-file.json"})
	t.Setenv("QUOTATOP_CLAUDE_ACCOUNT_work", "")
	got := settingsWithPrefix("QUOTATOP_CLAUDE_ACCOUNT_")
	if _, ok := got["work"]; ok {
		t.Errorf("settingsWithPrefix = %#v, want an empty environment variable to remove the label", got)
	}
}

func TestSettingsWithPrefixEmptySuffixIsSkipped(t *testing.T) {
	setConfigValues(t, map[string]string{"QUOTATOP_CLAUDE_ACCOUNT_": "/creds/no-label.json"})
	isolateAccountEnv(t)
	got := settingsWithPrefix("QUOTATOP_CLAUDE_ACCOUNT_")
	if len(got) != 0 {
		t.Errorf("settingsWithPrefix = %#v, want the empty-suffix entry skipped", got)
	}
}

func TestSettingsWithPrefixNilConfigValues(t *testing.T) {
	setConfigValues(t, nil)
	isolateAccountEnv(t)
	got := settingsWithPrefix("QUOTATOP_CLAUDE_ACCOUNT_")
	if len(got) != 0 {
		t.Errorf("settingsWithPrefix with nil configValues and no matching env = %#v, want empty", got)
	}
	t.Setenv("QUOTATOP_CLAUDE_ACCOUNT_work", "/creds/work.json")
	isolateAccountEnv(t, "QUOTATOP_CLAUDE_ACCOUNT_work")
	got = settingsWithPrefix("QUOTATOP_CLAUDE_ACCOUNT_")
	if got["work"] != "/creds/work.json" {
		t.Errorf("settingsWithPrefix with nil configValues = %#v, want the environment alone", got)
	}
}
