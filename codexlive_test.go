package main

import (
	"bufio"
	"bytes"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
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
