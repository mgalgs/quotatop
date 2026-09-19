package main

import (
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"
)

func oauthFixture(t *testing.T, mode os.FileMode, refresh bool, expiresAt int64) (claudeSource, string) {
	t.Helper()
	dir := t.TempDir()
	path := filepath.Join(dir, "credentials.json")
	rt := ""
	if refresh {
		rt = `,"refreshToken":"rt-1","expiresAt":` + strconv.FormatInt(expiresAt, 10)
	}
	doc := `{"unknownTop":{"kept":true},"claudeAiOauth":{"accessToken":"tok-old"` + rt + `,"unknownNested":[1, 2]}}`
	if err := os.WriteFile(path, []byte(doc), mode); err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(path, mode); err != nil {
		t.Fatal(err)
	}
	return claudeSource{credentialsPath: path, cacheDir: filepath.Join(dir, "cache")}, path
}

func oauthDoc(t *testing.T, path string) map[string]json.RawMessage {
	t.Helper()
	raw, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	var doc map[string]json.RawMessage
	if err := json.Unmarshal(raw, &doc); err != nil {
		t.Fatal(err)
	}
	return doc
}

func oauthSource(t *testing.T, src claudeSource, usage http.HandlerFunc, token http.HandlerFunc) claudeSource {
	t.Helper()
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/usage":
			usage(w, r)
		case "/token":
			token(w, r)
		default:
			http.NotFound(w, r)
		}
	}))
	t.Cleanup(server.Close)
	src.doRequest = server.Client().Do
	src.usageURL = server.URL + "/usage"
	src.oauthURL = server.URL + "/token"
	return src
}

func oauthUsage(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "application/json")
	io.WriteString(w, `{"limits":[{"kind":"session","percent":10}]}`)
}

func TestClaudeExpiredTokenRefreshesThenUsesAndWritesNewCredentials(t *testing.T) {
	src, path := oauthFixture(t, 0o600, true, time.Now().Add(-time.Hour).UnixMilli())
	src = oauthSource(t, src, func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Authorization") != "Bearer tok-new" {
			t.Errorf("usage authorization = %q", r.Header.Get("Authorization"))
		}
		oauthUsage(w, r)
	}, func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost {
			t.Errorf("method = %s", r.Method)
		}
		io.WriteString(w, `{"access_token":"tok-new","refresh_token":"rt-2","expires_in":28800}`)
	})
	if snap := src.fetch(true); snap.Err != nil {
		t.Fatalf("fetch: %v", snap.Err)
	}
	doc := oauthDoc(t, path)
	var oauth struct {
		Access  string `json:"accessToken"`
		Refresh string `json:"refreshToken"`
		Expires int64  `json:"expiresAt"`
	}
	json.Unmarshal(doc["claudeAiOauth"], &oauth)
	if oauth.Access != "tok-new" || oauth.Refresh != "rt-2" || oauth.Expires <= time.Now().UnixMilli() {
		t.Errorf("credentials = %+v", oauth)
	}
}

func TestClaudeRefreshPreservesUnknownFieldsAndMode(t *testing.T) {
	for _, mode := range []os.FileMode{0o600, 0o644} {
		t.Run(mode.String(), func(t *testing.T) {
			src, path := oauthFixture(t, mode, true, 0)
			before := oauthDoc(t, path)
			var beforeOauth map[string]json.RawMessage
			json.Unmarshal(before["claudeAiOauth"], &beforeOauth)
			src = oauthSource(t, src, oauthUsage, func(w http.ResponseWriter, r *http.Request) {
				io.WriteString(w, `{"access_token":"tok-new","refresh_token":"rt-2","expires_in":28800}`)
			})
			if snap := src.fetch(true); snap.Err != nil {
				t.Fatal(snap.Err)
			}
			after := oauthDoc(t, path)
			var afterOauth map[string]json.RawMessage
			json.Unmarshal(after["claudeAiOauth"], &afterOauth)
			if string(after["unknownTop"]) != string(before["unknownTop"]) || string(afterOauth["unknownNested"]) != string(beforeOauth["unknownNested"]) {
				t.Error("unknown fields changed")
			}
			info, err := os.Stat(path)
			if err != nil {
				t.Fatal(err)
			}
			if info.Mode().Perm() != mode {
				t.Errorf("mode = %o, want %o", info.Mode().Perm(), mode)
			}
		})
	}
}

func TestClaudeValidTokenDoesNotRefresh(t *testing.T) {
	src, _ := oauthFixture(t, 0o600, true, time.Now().Add(time.Hour).UnixMilli())
	var calls int
	src = oauthSource(t, src, oauthUsage, func(w http.ResponseWriter, r *http.Request) { calls++; t.Error("token endpoint called") })
	if snap := src.fetch(true); snap.Err != nil {
		t.Fatal(snap.Err)
	}
	if calls != 0 {
		t.Errorf("refresh calls = %d", calls)
	}
}

func TestClaudeRefreshWritesBeforeUsage(t *testing.T) {
	src, path := oauthFixture(t, 0o600, true, 0)
	src = oauthSource(t, src, func(w http.ResponseWriter, r *http.Request) {
		var oauth struct {
			Access string `json:"accessToken"`
		}
		json.Unmarshal(oauthDoc(t, path)["claudeAiOauth"], &oauth)
		if oauth.Access != "tok-new" {
			t.Errorf("credentials at usage = %q", oauth.Access)
		}
		oauthUsage(w, r)
	}, func(w http.ResponseWriter, r *http.Request) {
		io.WriteString(w, `{"access_token":"tok-new","refresh_token":"rt-2","expires_in":28800}`)
	})
	if snap := src.fetch(true); snap.Err != nil {
		t.Fatal(snap.Err)
	}
}

func TestClaudeConcurrentFetchersRedeemOnce(t *testing.T) {
	src, _ := oauthFixture(t, 0o600, true, 0)
	var mu sync.Mutex
	refreshes := 0
	src = oauthSource(t, src, func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Authorization") != "Bearer tok-new" {
			t.Error("usage did not get new token")
		}
		oauthUsage(w, r)
	}, func(w http.ResponseWriter, r *http.Request) {
		mu.Lock()
		refreshes++
		mu.Unlock()
		io.WriteString(w, `{"access_token":"tok-new","refresh_token":"rt-2","expires_in":28800}`)
	})
	var wg sync.WaitGroup
	errs := make(chan error, 2)
	for range 2 {
		wg.Add(1)
		go func() { defer wg.Done(); errs <- src.fetch(true).Err }()
	}
	wg.Wait()
	close(errs)
	for err := range errs {
		if err != nil {
			t.Error(err)
		}
	}
	if refreshes != 1 {
		t.Errorf("refreshes = %d, want 1", refreshes)
	}
}

func TestClaudeCodeWinningRefreshRaceUsesFreshFile(t *testing.T) {
	src, path := oauthFixture(t, 0o600, true, 0)
	src = oauthSource(t, src, func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Authorization") != "Bearer tok-cc" {
			t.Error("did not use Claude Code token")
		}
		oauthUsage(w, r)
	}, func(w http.ResponseWriter, r *http.Request) {
		if err := os.WriteFile(path, []byte(`{"claudeAiOauth":{"accessToken":"tok-cc","refreshToken":"rt-cc","expiresAt":9999999999999}}`), 0o600); err != nil {
			t.Error(err)
		}
		w.WriteHeader(http.StatusBadRequest)
	})
	if snap := src.fetch(true); snap.Err != nil {
		t.Fatal(snap.Err)
	}
}

func TestClaudeExpiredTokenRefreshFailureGuidesReloginWithoutSecrets(t *testing.T) {
	src, _ := oauthFixture(t, 0o600, true, 0)
	src = oauthSource(t, src, oauthUsage, func(w http.ResponseWriter, r *http.Request) { w.WriteHeader(http.StatusBadRequest) })
	snap := src.fetch(true)
	if snap.Err == nil || !strings.Contains(snap.Err.Error(), "re-login") {
		t.Errorf("error = %v", snap.Err)
	}
	if snap.Err != nil && (strings.Contains(snap.Err.Error(), "tok-old") || strings.Contains(snap.Err.Error(), "rt-1")) {
		t.Errorf("error leaks secret: %v", snap.Err)
	}
}

func TestClaudeUsage401RefreshesAndRetriesOnlyOnce(t *testing.T) {
	src, _ := oauthFixture(t, 0o600, true, time.Now().Add(time.Hour).UnixMilli())
	var usageCalls, refreshes int
	src = oauthSource(t, src, func(w http.ResponseWriter, r *http.Request) {
		usageCalls++
		if r.Header.Get("Authorization") == "Bearer tok-old" {
			w.WriteHeader(http.StatusUnauthorized)
			return
		}
		if usageCalls == 2 {
			oauthUsage(w, r)
			return
		}
		w.WriteHeader(http.StatusUnauthorized)
	}, func(w http.ResponseWriter, r *http.Request) {
		refreshes++
		io.WriteString(w, `{"access_token":"tok-new","refresh_token":"rt-2","expires_in":28800}`)
	})
	if snap := src.fetch(true); snap.Err != nil {
		t.Fatal(snap.Err)
	}
	if refreshes != 1 || usageCalls != 2 {
		t.Errorf("refreshes, usage = %d, %d", refreshes, usageCalls)
	}
	if _, err := src.requestUsage(); err == nil || refreshes != 1 {
		t.Errorf("second 401 got err %v and refreshes %d", err, refreshes)
	}
}

func TestClaudeWithoutRefreshTokenDoesNotAttemptRefresh(t *testing.T) {
	src, _ := oauthFixture(t, 0o600, false, 0)
	var refreshes int
	src = oauthSource(t, src, func(w http.ResponseWriter, r *http.Request) { w.WriteHeader(http.StatusUnauthorized) }, func(w http.ResponseWriter, r *http.Request) { refreshes++; t.Error("unexpected refresh") })
	snap := src.fetch(true)
	if snap.Err == nil || !strings.Contains(snap.Err.Error(), "HTTP 401") {
		t.Errorf("error = %v", snap.Err)
	}
	if refreshes != 0 {
		t.Errorf("refreshes = %d", refreshes)
	}
}
