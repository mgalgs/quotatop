package main

import (
	"bufio"
	"bytes"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"
)

// TestCodexFakeAppServer is not a test: it is the fake app-server the live
// read tests start in place of codex, the standard helper-process pattern.
// The test binary re-executes itself with QUOTATOP_FAKE_APP_SERVER naming the
// behaviour; without it this returns at once and passes.
func TestCodexFakeAppServer(t *testing.T) {
	behaviour := os.Getenv("QUOTATOP_FAKE_APP_SERVER")
	if behaviour == "" {
		return
	}
	if marker := os.Getenv("QUOTATOP_FAKE_MARKER"); marker != "" {
		os.WriteFile(marker, []byte(os.Getenv("CODEX_HOME")), 0o600)
	}
	switch behaviour {
	case "exit":
		os.Exit(0)
	case "hang":
		time.Sleep(time.Hour)
		os.Exit(0)
	}
	in := bufio.NewScanner(os.Stdin)
	for in.Scan() {
		var request struct {
			ID     *int   `json:"id"`
			Method string `json:"method"`
		}
		if json.Unmarshal(in.Bytes(), &request) != nil || request.ID == nil {
			continue
		}
		switch request.Method {
		case "initialize":
			fmt.Printf(`{"id":%d,"result":{"userAgent":"fake"}}`+"\n", *request.ID)
		case codexLiveMethod:
			// Noise first: a notification, a reply to another id and a
			// line that is not JSON, all of which must be skipped.
			fmt.Println(`{"method":"account/updated","params":{}}`)
			fmt.Println(`{"id":99,"result":{"rateLimits":{"primary":{"usedPercent":1}}}}`)
			fmt.Println(`not json`)
			if behaviour == "error" {
				fmt.Printf(`{"id":%d,"error":{"code":-32000,"message":"secret-ish detail"}}`+"\n", *request.ID)
			} else {
				var result bytes.Buffer
				json.Compact(&result, []byte(os.Getenv("QUOTATOP_FAKE_RESULT")))
				fmt.Printf(`{"id":%d,"result":%s}`+"\n", *request.ID, result.String())
			}
		}
	}
	os.Exit(0)
}

// fakeAppServer points the live read at the helper above and returns its
// argv and the marker file it writes when it starts.
func fakeAppServer(t *testing.T, behaviour, result string) ([]string, string) {
	t.Helper()
	marker := filepath.Join(t.TempDir(), "started")
	t.Setenv("QUOTATOP_FAKE_APP_SERVER", behaviour)
	t.Setenv("QUOTATOP_FAKE_RESULT", result)
	t.Setenv("QUOTATOP_FAKE_MARKER", marker)
	return []string{os.Args[0], "-test.run=^TestCodexFakeAppServer$"}, marker
}

func fakeStarted(marker string) bool {
	_, err := os.Stat(marker)
	return err == nil
}

// fakeJWT is an unsigned token carrying exp; the gate never verifies one.
func fakeJWT(exp time.Time) string {
	enc := base64.RawURLEncoding.EncodeToString
	return enc([]byte(`{"alg":"none"}`)) + "." +
		enc([]byte(fmt.Sprintf(`{"exp":%d}`, exp.Unix()))) + "." + enc([]byte("sig"))
}

// writeCodexAuth writes a CODEX_HOME holding a ChatGPT login whose access
// token is the given string, and returns that home.
func writeCodexAuth(t *testing.T, token string) string {
	t.Helper()
	home := t.TempDir()
	raw, _ := json.Marshal(map[string]any{"tokens": map[string]string{
		"access_token": token, "refresh_token": "rt-test", "id_token": "id-test"}})
	if err := os.WriteFile(filepath.Join(home, "auth.json"), raw, 0o600); err != nil {
		t.Fatal(err)
	}
	return home
}

const fakeLiveResult = `{"rateLimits":{"limitId":"codex","limitName":null,"planType":"pro",
 "primary":{"usedPercent":37,"windowDurationMins":300,"resetsAt":4102444800},
 "secondary":{"usedPercent":61,"windowDurationMins":10080,"resetsAt":4102444800},
 "credits":null,"rateLimitReachedType":null},"rateLimitsByLimitId":null}`

func TestCodexLiveReadMapsRateLimits(t *testing.T) {
	argv, marker := fakeAppServer(t, "ok", fakeLiveResult)
	home := t.TempDir()
	limits, err := readCodexLive(argv, home, 10*time.Second)
	if err != nil {
		t.Fatalf("readCodexLive: %v", err)
	}
	if limits.LimitID == nil || *limits.LimitID != "codex" || limits.PlanType == nil || *limits.PlanType != "pro" {
		t.Errorf("limit id / plan = %v / %v", limits.LimitID, limits.PlanType)
	}
	if limits.Primary == nil || *limits.Primary.UsedPercent != 37 || *limits.Primary.WindowMinutes != 300 ||
		*limits.Primary.ResetsAt != 4102444800 {
		t.Errorf("primary = %+v", limits.Primary)
	}
	if limits.Secondary == nil || *limits.Secondary.UsedPercent != 61 || *limits.Secondary.WindowMinutes != 10080 {
		t.Errorf("secondary = %+v", limits.Secondary)
	}
	if limits.RateLimitReachedType != nil {
		t.Errorf("reached type = %v, want nil", *limits.RateLimitReachedType)
	}
	got, err := os.ReadFile(marker)
	if err != nil {
		t.Fatalf("fake never started: %v", err)
	}
	if string(got) != home {
		t.Errorf("app-server ran with CODEX_HOME=%q, want %q", got, home)
	}
}

func TestCodexLiveReadErrorReplyHidesItsBody(t *testing.T) {
	argv, _ := fakeAppServer(t, "error", "")
	_, err := readCodexLive(argv, t.TempDir(), 10*time.Second)
	if err == nil || err.Error() != "account/rateLimits/read failed" {
		t.Errorf("err = %v, want the method name and nothing else", err)
	}
}

func TestCodexLiveReadNullRateLimitsIsAnError(t *testing.T) {
	argv, _ := fakeAppServer(t, "ok", `{"rateLimits":null}`)
	if _, err := readCodexLive(argv, t.TempDir(), 10*time.Second); err == nil {
		t.Error("a reply with no rate limits was accepted")
	}
}

func TestCodexLiveReadChildExitingIsAnError(t *testing.T) {
	argv, _ := fakeAppServer(t, "exit", "")
	if _, err := readCodexLive(argv, t.TempDir(), 10*time.Second); err == nil {
		t.Error("a child that exits without answering was accepted")
	}
}

func TestCodexLiveReadMissingBinaryIsAnError(t *testing.T) {
	argv := []string{filepath.Join(t.TempDir(), "no-such-codex"), "app-server"}
	if _, err := readCodexLive(argv, t.TempDir(), 10*time.Second); err == nil {
		t.Error("a missing binary was accepted")
	}
}

func TestCodexLiveReadHangTimesOut(t *testing.T) {
	argv, _ := fakeAppServer(t, "hang", "")
	start := time.Now()
	_, err := readCodexLive(argv, t.TempDir(), 300*time.Millisecond)
	if err == nil || !strings.Contains(err.Error(), "timed out") {
		t.Errorf("err = %v, want a timeout", err)
	}
	if elapsed := time.Since(start); elapsed > 2*time.Second {
		t.Errorf("read took %v, want about the 300ms timeout", elapsed)
	}
}

func TestCodexTokenUsable(t *testing.T) {
	now := time.Now()
	for _, tc := range []struct {
		name  string
		token string
		want  bool
	}{
		{"days left", fakeJWT(now.Add(191 * time.Hour)), true},
		{"just over an hour", fakeJWT(now.Add(time.Hour + time.Minute)), true},
		{"within the hour", fakeJWT(now.Add(59 * time.Minute)), false},
		{"expired", fakeJWT(now.Add(-time.Hour)), false},
		{"not a jwt", "opaque-token", false},
		{"bad base64", "a.!!!.c", false},
		{"no exp", "e30." + base64.RawURLEncoding.EncodeToString([]byte(`{"sub":"x"}`)) + ".c", false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if got := codexTokenUsable(writeCodexAuth(t, tc.token), now); got != tc.want {
				t.Errorf("codexTokenUsable = %v, want %v", got, tc.want)
			}
		})
	}
	if codexTokenUsable(t.TempDir(), now) {
		t.Error("a home with no auth.json was usable")
	}
	apiKey := t.TempDir()
	os.WriteFile(filepath.Join(apiKey, "auth.json"), []byte(`{"OPENAI_API_KEY":"sk-x","tokens":null}`), 0o600)
	if codexTokenUsable(apiKey, now) {
		t.Error("an API-key login was usable")
	}
	corrupt := t.TempDir()
	os.WriteFile(filepath.Join(corrupt, "auth.json"), []byte(`{not json`), 0o600)
	if codexTokenUsable(corrupt, now) {
		t.Error("an unparseable auth.json was usable")
	}
}

// liveTestSource is a source with one old log row, a home whose token has
// days to run, a temp cache and the fake app-server in the given behaviour.
func liveTestSource(t *testing.T, behaviour, result string) (codexSource, string) {
	t.Helper()
	logs := t.TempDir()
	writeSessionFile(t, logs, "a.jsonl",
		codexLine("2026-03-01T10:00:00Z", `"codex"`, `plus`, `{"used_percent":13,"window_minutes":300}`, ``)+"\n")
	argv, marker := fakeAppServer(t, behaviour, result)
	return codexSource{
		defaultRoot: logs,
		liveHome:    writeCodexAuth(t, fakeJWT(time.Now().Add(191*time.Hour))),
		cacheDir:    filepath.Join(t.TempDir(), "cache"),
		liveCommand: argv,
	}, marker
}

// assertLogReading checks snap is the old log row liveTestSource writes.
func assertLogReading(t *testing.T, snap Snapshot) {
	t.Helper()
	if snap.Err != nil {
		t.Fatalf("fetch failed: %v", snap.Err)
	}
	if snap.Footnote != "local · interactive" || len(snap.Windows) != 1 || snap.Windows[0].Percent != 13 {
		t.Errorf("footnote %q windows %+v, want the log reading", snap.Footnote, snap.Windows)
	}
}

// writeLiveState plants a state file for src: a good reading of usedPercent
// fetched at fetchedAt (none when zero) and a last attempt at attemptedAt.
func writeLiveState(t *testing.T, src codexSource, used float64, reached string, fetchedAt, attemptedAt time.Time) {
	t.Helper()
	state := codexLiveState{Home: src.liveHome, AttemptedAt: float64(attemptedAt.Unix())}
	if !fetchedAt.IsZero() {
		id, plan, minutes := "codex", "pro", 300.0
		state.FetchedAt = float64(fetchedAt.Unix())
		state.RateLimits = &codexRateLimits{LimitID: &id, PlanType: &plan,
			Primary: &codexRLWindow{UsedPercent: &used, WindowMinutes: &minutes}}
		if reached != "" {
			state.RateLimits.RateLimitReachedType = &reached
		}
	}
	raw, _ := json.Marshal(state)
	if err := os.MkdirAll(src.cacheDir, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(src.resolvedLivePath(), raw, 0o600); err != nil {
		t.Fatal(err)
	}
}

func TestCodexLiveReadingNewerThanLogWins(t *testing.T) {
	src, marker := liveTestSource(t, "ok", fakeLiveResult)
	before := time.Now().Add(-time.Second)
	snap := src.fetch()
	if snap.Err != nil {
		t.Fatalf("fetch failed: %v", snap.Err)
	}
	if !fakeStarted(marker) {
		t.Fatal("app-server was never started")
	}
	if snap.Footnote != "live" {
		t.Errorf("Footnote = %q, want live", snap.Footnote)
	}
	if snap.Chip != "pro" {
		t.Errorf("Chip = %q, want the live plan", snap.Chip)
	}
	if snap.Observed.Before(before) {
		t.Errorf("Observed = %v, want the moment of the live read", snap.Observed)
	}
	if !strings.Contains(snap.Detail, "codex app-server") || !strings.Contains(snap.Detail, src.liveHome) {
		t.Errorf("Detail = %q, want app-server and its home", snap.Detail)
	}
	if len(snap.Windows) != 2 || snap.Windows[0].Label != "5-hour" || snap.Windows[0].Percent != 37 ||
		snap.Windows[1].Label != "Weekly" || snap.Windows[1].Percent != 61 {
		t.Errorf("windows = %+v, want the live 37%% and 61%%", snap.Windows)
	}
	if snap.Windows[0].Expired || snap.Windows[1].Expired {
		t.Errorf("windows = %+v, a fresh live reading is not expired", snap.Windows)
	}
	raw, err := os.ReadFile(src.resolvedLivePath())
	if err != nil {
		t.Fatalf("no state file: %v", err)
	}
	if strings.Contains(string(raw), "rt-test") || strings.Contains(string(raw), "id-test") {
		t.Errorf("state file holds a token: %s", raw)
	}
	if info, err := os.Stat(src.resolvedLivePath()); err == nil && runtime.GOOS != "windows" && info.Mode().Perm() != 0o600 {
		t.Errorf("state file mode = %v, want 0600", info.Mode().Perm())
	}
}

func TestCodexLogNewerThanCachedLiveWins(t *testing.T) {
	src, marker := liveTestSource(t, "ok", fakeLiveResult)
	now := time.Now()
	writeSessionFile(t, src.defaultRoot, "b.jsonl",
		codexLine(now.Add(-time.Minute).UTC().Format(time.RFC3339), `"codex"`, `plus`, `{"used_percent":21,"window_minutes":300}`, ``)+"\n")
	writeLiveState(t, src, 80, "", now.Add(-time.Hour), now)
	snap := src.fetch()
	if fakeStarted(marker) {
		t.Error("app-server started inside the TTL")
	}
	if snap.Footnote != "local · interactive" || len(snap.Windows) != 1 || snap.Windows[0].Percent != 21 {
		t.Errorf("footnote %q windows %+v, want the newer log row", snap.Footnote, snap.Windows)
	}
}

func TestCodexCachedLiveReadingKeepsItsObservedTime(t *testing.T) {
	src, _ := liveTestSource(t, "ok", fakeLiveResult)
	fetched := time.Now().Add(-2 * time.Minute).Truncate(time.Second)
	writeLiveState(t, src, 80, "", fetched, fetched)
	snap := src.fetch()
	if snap.Footnote != "live" || !snap.Observed.Equal(fetched) {
		t.Errorf("footnote %q observed %v, want the cached live reading at %v", snap.Footnote, snap.Observed, fetched)
	}
}

func TestCodexLiveReachedTypeGoesThroughConsider(t *testing.T) {
	blocked := strings.Replace(fakeLiveResult, `"rateLimitReachedType":null`, `"rateLimitReachedType":"rate_limit_reached"`, 1)
	src, _ := liveTestSource(t, "ok", blocked)
	if snap := src.fetch(); snap.LimitReached != "rate_limit_reached" {
		t.Errorf("LimitReached = %q, want the live reached type", snap.LimitReached)
	}

	// A normal log row newer than the blocked live reading clears it.
	src, _ = liveTestSource(t, "ok", fakeLiveResult)
	now := time.Now()
	writeLiveState(t, src, 100, "rate_limit_reached", now.Add(-10*time.Minute), now)
	if snap := src.fetch(); snap.LimitReached != "rate_limit_reached" {
		t.Fatalf("LimitReached = %q, want the cached block before a newer row", snap.LimitReached)
	}
	writeSessionFile(t, src.defaultRoot, "b.jsonl",
		codexLine(now.Add(-time.Minute).UTC().Format(time.RFC3339), `"codex"`, `plus`, `{"used_percent":5,"window_minutes":300}`, ``)+"\n")
	if snap := src.fetch(); snap.LimitReached != "" {
		t.Errorf("LimitReached = %q, want it cleared by the newer normal row", snap.LimitReached)
	}
}

func TestCodexLiveGateSkipsTheSpawn(t *testing.T) {
	for _, tc := range []struct {
		name  string
		write func(home string)
	}{
		{"token expires within the hour", func(home string) {
			raw, _ := json.Marshal(map[string]any{"tokens": map[string]string{"access_token": fakeJWT(time.Now().Add(30 * time.Minute))}})
			os.WriteFile(filepath.Join(home, "auth.json"), raw, 0o600)
		}},
		{"no auth.json", func(home string) { os.Remove(filepath.Join(home, "auth.json")) }},
		{"unparseable token", func(home string) {
			os.WriteFile(filepath.Join(home, "auth.json"), []byte(`{"tokens":{"access_token":"not.a-jwt"}}`), 0o600)
		}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			src, marker := liveTestSource(t, "ok", fakeLiveResult)
			tc.write(src.liveHome)
			assertLogReading(t, src.fetch())
			if fakeStarted(marker) {
				t.Error("app-server started despite the gate")
			}
		})
	}
}

func TestCodexLiveCacheHonoursTheTTL(t *testing.T) {
	src, marker := liveTestSource(t, "ok", fakeLiveResult)
	if snap := src.fetch(); snap.Footnote != "live" {
		t.Fatalf("first fetch footnote = %q, want live", snap.Footnote)
	}
	os.Remove(marker)
	snap := src.fetch()
	if fakeStarted(marker) {
		t.Error("a second fetch inside the TTL started app-server")
	}
	if snap.Footnote != "live" || len(snap.Windows) != 2 || snap.Windows[0].Percent != 37 {
		t.Errorf("footnote %q windows %+v, want the cached live reading", snap.Footnote, snap.Windows)
	}

	// Past the TTL it reads again.
	state := src.readLiveState()
	state.AttemptedAt -= (codexLiveTTL + time.Second).Seconds()
	raw, _ := json.Marshal(state)
	os.WriteFile(src.resolvedLivePath(), raw, 0o600)
	src.fetch()
	if !fakeStarted(marker) {
		t.Error("a fetch after the TTL did not start app-server")
	}
}

func TestCodexFailedLiveReadKeepsTheLastGoodReading(t *testing.T) {
	src, marker := liveTestSource(t, "error", "")
	now := time.Now()
	fetched := now.Add(-10 * time.Minute).Truncate(time.Second)
	writeLiveState(t, src, 80, "", fetched, now.Add(-codexLiveTTL-time.Second))
	snap := src.fetch()
	if !fakeStarted(marker) {
		t.Fatal("app-server was not started after the TTL")
	}
	if snap.Footnote != "live" || !snap.Observed.Equal(fetched) || snap.Windows[0].Percent != 80 {
		t.Errorf("footnote %q observed %v windows %+v, want the last good reading", snap.Footnote, snap.Observed, snap.Windows)
	}
	state := src.readLiveState()
	if time.Since(time.Unix(int64(state.AttemptedAt), 0)) > time.Minute {
		t.Error("the failed attempt was not stamped, so the TTL would not hold retries off")
	}
	if state.FetchedAt != float64(fetched.Unix()) || state.RateLimits == nil {
		t.Errorf("state = %+v, want the previous good reading kept", state)
	}
}

func TestCodexCorruptLiveStateIsAMiss(t *testing.T) {
	src, marker := liveTestSource(t, "ok", fakeLiveResult)
	os.MkdirAll(src.cacheDir, 0o700)
	os.WriteFile(src.resolvedLivePath(), []byte(`{garbage`), 0o600)
	if snap := src.fetch(); snap.Err != nil || snap.Footnote != "live" || !fakeStarted(marker) {
		t.Errorf("err %v footnote %q, want a fresh live read", snap.Err, snap.Footnote)
	}
}

func TestCodexUnwritableLiveStateIsNotAnError(t *testing.T) {
	src, _ := liveTestSource(t, "ok", fakeLiveResult)
	blocker := filepath.Join(t.TempDir(), "file")
	os.WriteFile(blocker, nil, 0o600)
	src.cacheDir = filepath.Join(blocker, "cache") // a file where a directory must go
	if snap := src.fetch(); snap.Err != nil || snap.Footnote != "live" {
		t.Errorf("err %v footnote %q, want the live reading served anyway", snap.Err, snap.Footnote)
	}
}

func TestCodexHungLiveReadFallsBackToTheLog(t *testing.T) {
	src, _ := liveTestSource(t, "hang", "")
	src.liveTimeout = 300 * time.Millisecond
	start := time.Now()
	snap := src.fetch()
	if elapsed := time.Since(start); elapsed > 2*time.Second {
		t.Errorf("fetch took %v, want about the 300ms live timeout", elapsed)
	}
	assertLogReading(t, snap)
}

func TestCodexFailedLiveReadsFallBackToTheLog(t *testing.T) {
	for _, behaviour := range []string{"error", "exit"} {
		t.Run(behaviour, func(t *testing.T) {
			src, marker := liveTestSource(t, behaviour, "")
			assertLogReading(t, src.fetch())
			if !fakeStarted(marker) {
				t.Error("the fake was never started, so nothing was tested")
			}
		})
	}
	t.Run("missing binary", func(t *testing.T) {
		src, _ := liveTestSource(t, "ok", fakeLiveResult)
		src.liveCommand = []string{filepath.Join(t.TempDir(), "no-such-codex")}
		assertLogReading(t, src.fetch())
	})
	t.Run("codex not on PATH", func(t *testing.T) {
		src, _ := liveTestSource(t, "ok", fakeLiveResult)
		src.liveCommand = nil
		t.Setenv("PATH", t.TempDir())
		assertLogReading(t, src.fetch())
	})
}

func TestCodexNoReadingAtAllNamesTheLiveFailure(t *testing.T) {
	src, _ := liveTestSource(t, "error", "")
	src.defaultRoot = t.TempDir()
	snap := src.fetch()
	if snap.Err == nil || snap.Err.Error() != "no Codex usage reports found; account/rateLimits/read failed" {
		t.Errorf("err = %v, want the no-reports error with the live reason", snap.Err)
	}
}

func TestCodexScanTimeoutStillShowsTheLiveReading(t *testing.T) {
	src, _ := liveTestSource(t, "ok", fakeLiveResult)
	blocked := make(chan struct{})
	defer close(blocked)
	src.timeout = 100 * time.Millisecond
	src.blockScan = func() { <-blocked }
	snap := src.fetch()
	if snap.Err != nil || snap.Footnote != "live" || len(snap.Windows) != 2 {
		t.Errorf("err %v footnote %q windows %+v, want the live reading", snap.Err, snap.Footnote, snap.Windows)
	}
}

func TestCodexLiveSettings(t *testing.T) {
	home := t.TempDir()
	codexHome := filepath.Join(home, "codex-home")
	t.Setenv("HOME", home)
	t.Setenv("CODEX_HOME", codexHome)
	t.Setenv("QUOTATOP_CODEX_ROOTS", "")
	setConfigValues(t, nil)

	t.Setenv("QUOTATOP_CODEX_LIVE", "")
	src := defaultCodexSource()
	if src.liveHome != codexHome {
		t.Errorf("default liveHome = %q, want $CODEX_HOME %q", src.liveHome, codexHome)
	}
	if want := filepath.Join(home, ".cache", "quotatop", "codex-live.json"); src.resolvedLivePath() != want {
		t.Errorf("state file = %q, want %q", src.resolvedLivePath(), want)
	}
	t.Setenv("QUOTATOP_CODEX_LIVE", "on")
	if defaultCodexSource().liveHome != codexHome {
		t.Error("QUOTATOP_CODEX_LIVE=on turned live reads off")
	}

	isolateAccountEnv(t)
	if named := namedCodexSource("work", ""); named.liveHome != "" {
		t.Errorf("named account without a home live-reads %q", named.liveHome)
	}
	t.Setenv("QUOTATOP_CODEX_HOME_work", "~/work-codex")
	isolateAccountEnv(t, "QUOTATOP_CODEX_HOME_work")
	named := namedCodexSource("work", "")
	if want := filepath.Join(home, "work-codex"); named.liveHome != want {
		t.Errorf("named liveHome = %q, want the tilde-expanded %q", named.liveHome, want)
	}
	if want := filepath.Join(home, ".cache", "quotatop", "codex-live-work.json"); named.resolvedLivePath() != want {
		t.Errorf("named state file = %q, want %q", named.resolvedLivePath(), want)
	}

	t.Setenv("QUOTATOP_CODEX_LIVE", "off")
	if src := defaultCodexSource(); src.liveHome != "" {
		t.Errorf("QUOTATOP_CODEX_LIVE=off still live-reads %q", src.liveHome)
	}
	if named := namedCodexSource("work", ""); named.liveHome != "" {
		t.Errorf("QUOTATOP_CODEX_LIVE=off still live-reads %q for a named account", named.liveHome)
	}
}

// With live reads off, a fully usable home and command still never spawn.
func TestCodexLiveOffNeverSpawns(t *testing.T) {
	src, marker := liveTestSource(t, "ok", fakeLiveResult)
	t.Setenv("HOME", t.TempDir())
	t.Setenv("CODEX_HOME", src.liveHome)
	t.Setenv("QUOTATOP_CODEX_ROOTS", src.defaultRoot)
	t.Setenv("QUOTATOP_CODEX_LIVE", "off")
	setConfigValues(t, nil)
	off := defaultCodexSource()
	off.liveCommand = src.liveCommand
	snap := off.fetch()
	if fakeStarted(marker) {
		t.Error("app-server started with QUOTATOP_CODEX_LIVE=off")
	}
	if snap.Err != nil || snap.Footnote != "local · extra" {
		t.Errorf("err %v footnote %q, want the log reading", snap.Err, snap.Footnote)
	}
}
