package main

import (
	"bytes"
	"encoding/hex"
	"encoding/json"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"
	"time"
)

// TestMain turns the macOS Keychain fallback off for every test, so a test
// that builds the default source on a Mac never reads the real Keychain.
// Tests that exercise the fallback turn it back on with a fake keychain.
func TestMain(m *testing.M) {
	keychainFallback = false
	os.Exit(m.Run())
}

var addPasswordCommand = regexp.MustCompile(`^add-generic-password -U -a "[^"]+" -s "([^"]+)" -X ([0-9a-f]+)\n$`)

// fakeKeychain stands in for security(1). It holds one item per service and
// records every call.
type fakeKeychain struct {
	items map[string][]byte
	calls [][]string
	stdin [][]byte
	// dropWrites makes "-i" exit 0 without storing anything, as security's
	// interactive mode can.
	dropWrites bool
}

func (k *fakeKeychain) run(stdin []byte, args ...string) ([]byte, error) {
	k.calls = append(k.calls, args)
	k.stdin = append(k.stdin, stdin)
	switch {
	case len(args) == 4 && args[0] == "find-generic-password" && args[1] == "-s" && args[3] == "-w":
		item, ok := k.items[args[2]]
		if !ok {
			return nil, os.ErrNotExist
		}
		return append(append([]byte(nil), item...), '\n'), nil
	case len(args) == 1 && args[0] == "-i":
		if k.dropWrites {
			return nil, nil
		}
		match := addPasswordCommand.FindSubmatch(stdin)
		if match == nil {
			return nil, os.ErrInvalid
		}
		password, err := hex.DecodeString(string(match[2]))
		if err != nil {
			return nil, err
		}
		service := string(match[1])
		k.items[service] = password
		return nil, nil
	}
	return nil, os.ErrInvalid
}

func keychainFixture(t *testing.T, expiresAt int64) (claudeSource, *fakeKeychain) {
	t.Helper()
	doc := `{"claudeAiOauth":{"accessToken":"tok-old","refreshToken":"rt-1","expiresAt":` + jsonInt(expiresAt) + `,"scopes":["user:inference"]}}`
	keychain := &fakeKeychain{items: map[string][]byte{claudeKeychainService: []byte(doc)}}
	return claudeSource{
		credentialsPath: keychainPrefix + claudeKeychainService,
		cacheDir:        filepath.Join(t.TempDir(), "cache"),
		runSecurity:     keychain.run,
	}, keychain
}

func jsonInt(n int64) string {
	raw, _ := json.Marshal(n)
	return string(raw)
}

func TestClaudeReadsTokenFromKeychain(t *testing.T) {
	src, keychain := keychainFixture(t, time.Now().Add(time.Hour).UnixMilli())
	src = oauthSource(t, src, func(w http.ResponseWriter, r *http.Request) {
		if got := r.Header.Get("Authorization"); got != "Bearer tok-old" {
			t.Errorf("usage authorization = %q, want the Keychain token", got)
		}
		oauthUsage(w, r)
	}, func(w http.ResponseWriter, r *http.Request) {
		t.Error("a valid token must not be refreshed")
	})
	snap := src.fetch(true)
	if snap.Err != nil {
		t.Fatalf("fetch: %v", snap.Err)
	}
	if snap.CredentialsPath != "keychain:Claude Code-credentials" {
		t.Errorf("CredentialsPath = %q, want the Keychain location unchanged", snap.CredentialsPath)
	}
	if len(keychain.calls) == 0 {
		t.Error("the Keychain was never read")
	}
}

func TestClaudeMissingKeychainItemIsNotSignedIn(t *testing.T) {
	src := claudeSource{
		credentialsPath: keychainPrefix + "absent",
		runSecurity:     (&fakeKeychain{items: map[string][]byte{}}).run,
	}
	if _, err := src.readClaudeCredentials(); err == nil {
		t.Fatal("want an error for a missing Keychain item")
	}
}

func TestClaudeRefreshWritesKeychainThroughStdin(t *testing.T) {
	src, keychain := keychainFixture(t, time.Now().Add(-time.Hour).UnixMilli())
	src = oauthSource(t, src, oauthUsage, func(w http.ResponseWriter, r *http.Request) {
		io.WriteString(w, `{"access_token":"tok-new","refresh_token":"rt-2","expires_in":28800}`)
	})
	if snap := src.fetch(true); snap.Err != nil {
		t.Fatalf("fetch: %v", snap.Err)
	}
	var doc struct {
		ClaudeAiOauth struct {
			AccessToken  string   `json:"accessToken"`
			RefreshToken string   `json:"refreshToken"`
			Scopes       []string `json:"scopes"`
		} `json:"claudeAiOauth"`
	}
	if err := json.Unmarshal(keychain.items[claudeKeychainService], &doc); err != nil {
		t.Fatalf("stored item is not JSON: %v", err)
	}
	if doc.ClaudeAiOauth.AccessToken != "tok-new" || doc.ClaudeAiOauth.RefreshToken != "rt-2" {
		t.Errorf("stored oauth = %+v, want the refreshed tokens", doc.ClaudeAiOauth)
	}
	if len(doc.ClaudeAiOauth.Scopes) != 1 {
		t.Errorf("scopes = %v, want the unknown field kept", doc.ClaudeAiOauth.Scopes)
	}
	// No token may appear in an argument list, where ps can read it.
	for _, args := range keychain.calls {
		for _, arg := range args {
			if strings.Contains(arg, "tok-new") || strings.Contains(arg, "rt-2") || strings.Contains(arg, hex.EncodeToString([]byte("rt-2"))) {
				t.Errorf("security argument %q carries the credential", arg)
			}
		}
	}
	wrote := false
	for _, stdin := range keychain.stdin {
		wrote = wrote || bytes.HasPrefix(stdin, []byte("add-generic-password -U "))
	}
	if !wrote {
		t.Error("the refreshed token never went to add-generic-password on stdin")
	}
}

func TestClaudeRefreshReportsKeychainWriteThatDidNotTake(t *testing.T) {
	src, keychain := keychainFixture(t, time.Now().Add(-time.Hour).UnixMilli())
	keychain.dropWrites = true
	src = oauthSource(t, src, oauthUsage, func(w http.ResponseWriter, r *http.Request) {
		io.WriteString(w, `{"access_token":"tok-new","refresh_token":"rt-2","expires_in":28800}`)
	})
	snap := src.fetch(true)
	if snap.Err == nil || !strings.Contains(snap.Err.Error(), "did not take") {
		t.Fatalf("fetch error = %v, want the lost Keychain write reported", snap.Err)
	}
}

func TestDefaultClaudeSourceFallsBackToKeychain(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("QUOTATOP_CLAUDE_CREDENTIALS", "")
	setConfigValues(t, nil)
	keychainFallback = true
	t.Cleanup(func() { keychainFallback = false })

	// No credentials file: the Keychain item.
	if got := defaultClaudeSource().credentialsPath; got != "keychain:Claude Code-credentials" {
		t.Errorf("credentialsPath = %q, want the Keychain item", got)
	}

	// A credentials file without a Claude token: still the Keychain item.
	file := filepath.Join(home, ".claude", ".credentials.json")
	if err := os.MkdirAll(filepath.Dir(file), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(file, []byte(`{"mcpOAuth":{}}`), 0o600); err != nil {
		t.Fatal(err)
	}
	if got := defaultClaudeSource().credentialsPath; got != "keychain:Claude Code-credentials" {
		t.Errorf("credentialsPath = %q, want the Keychain item over a tokenless file", got)
	}

	// A credentials file with a Claude token: the file wins.
	if err := os.WriteFile(file, []byte(`{"claudeAiOauth":{"accessToken":"tok"}}`), 0o600); err != nil {
		t.Fatal(err)
	}
	if got := defaultClaudeSource().credentialsPath; got != file {
		t.Errorf("credentialsPath = %q, want the existing file %q", got, file)
	}

	// An explicit override wins over both.
	t.Setenv("QUOTATOP_CLAUDE_CREDENTIALS", "keychain:other")
	if got := defaultClaudeSource().credentialsPath; got != "keychain:other" {
		t.Errorf("credentialsPath = %q, want the override", got)
	}
}
