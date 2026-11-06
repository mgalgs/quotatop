package main

import (
	"bufio"
	"bytes"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"time"
)

// codexLiveTimeout bounds one live read, from spawning app-server to its
// reply. It sits under codexScanTimeout so the live read and the log scan,
// which run side by side, finish inside the same bound.
const codexLiveTimeout = 20 * time.Second

// codexLiveTokenMargin is how long the ChatGPT access token must still have
// to run before app-server is started at all. Codex refreshes the token
// itself when it is within 5 minutes of expiry
// (CHATGPT_ACCESS_TOKEN_REFRESH_WINDOW_MINUTES), and refresh tokens are
// single use: a refresh by our app-server racing one by the user's own codex
// logs one of them out. An hour is a deliberate margin over those 5 minutes
// plus codexLiveTimeout. Do not lower it toward 5 minutes; that reintroduces
// the race.
const codexLiveTokenMargin = time.Hour

// codexLiveMethod is the one request the live read makes. Errors name it and
// nothing else: an RPC error body is never shown.
const codexLiveMethod = "account/rateLimits/read"

// codexLiveWindow and codexLiveLimits are app-server's camelCase rate-limit
// shape. They are converted to the log rows' shape at once, so every rule
// about a reading lives in codexReport.consider and nowhere else.
type codexLiveWindow struct {
	UsedPercent        *float64 `json:"usedPercent"`
	WindowDurationMins *float64 `json:"windowDurationMins"`
	ResetsAt           *float64 `json:"resetsAt"` // unix epoch seconds, as in the log
}

type codexLiveLimits struct {
	LimitID              *string          `json:"limitId"`
	PlanType             *string          `json:"planType"`
	Primary              *codexLiveWindow `json:"primary"`
	Secondary            *codexLiveWindow `json:"secondary"`
	RateLimitReachedType *string          `json:"rateLimitReachedType"`
}

func (w *codexLiveWindow) rowWindow() *codexRLWindow {
	if w == nil {
		return nil
	}
	return &codexRLWindow{UsedPercent: w.UsedPercent, WindowMinutes: w.WindowDurationMins, ResetsAt: w.ResetsAt}
}

func (l codexLiveLimits) rowLimits() codexRateLimits {
	return codexRateLimits{
		LimitID:              l.LimitID,
		PlanType:             l.PlanType,
		Primary:              l.Primary.rowWindow(),
		Secondary:            l.Secondary.rowWindow(),
		RateLimitReachedType: l.RateLimitReachedType,
	}
}

// codexLiveRow dresses a live reading as the log row it stands in for,
// observed at fetchedAt, so codexReport.consider can weigh it against the
// log's rows by the same rules.
func codexLiveRow(limits codexRateLimits, fetchedAt time.Time) codexRow {
	row := codexRow{Timestamp: fetchedAt.UTC().Format(time.RFC3339)}
	row.Payload = &struct {
		Type       string           `json:"type"`
		RateLimits *codexRateLimits `json:"rate_limits"`
	}{Type: "token_count", RateLimits: &limits}
	return row
}

// codexTokenUsable reports whether home's ChatGPT access token is safe to
// hand to app-server: its exp parses and lies more than codexLiveTokenMargin
// ahead of now. Anything else -- no auth.json (keyring storage, logged out),
// an API-key login, an unreadable token -- is false. The JWT is decoded, not
// verified: the only question is when codex itself will consider it due for
// a refresh. The token never leaves this function.
func codexTokenUsable(home string, now time.Time) bool {
	raw, err := os.ReadFile(filepath.Join(home, "auth.json"))
	if err != nil {
		return false
	}
	var auth struct {
		Tokens *struct {
			AccessToken string `json:"access_token"`
		} `json:"tokens"`
	}
	if json.Unmarshal(raw, &auth) != nil || auth.Tokens == nil {
		return false
	}
	parts := strings.Split(auth.Tokens.AccessToken, ".")
	if len(parts) != 3 {
		return false
	}
	payload, err := base64.RawURLEncoding.DecodeString(strings.TrimRight(parts[1], "="))
	if err != nil {
		return false
	}
	var claims struct {
		Exp *float64 `json:"exp"`
	}
	if json.Unmarshal(payload, &claims) != nil || claims.Exp == nil {
		return false
	}
	return time.Unix(int64(*claims.Exp), 0).After(now.Add(codexLiveTokenMargin))
}

// codexLiveCommand is the app-server invocation: codex from PATH. A missing
// binary is an error the caller treats as "no live read", not a failure.
func codexLiveCommand() ([]string, error) {
	path, err := exec.LookPath("codex")
	if err != nil {
		return nil, err
	}
	return []string{path, "app-server", "--listen", "stdio://"}, nil
}

// readCodexLive asks an app-server, started as argv with CODEX_HOME=home,
// for the account's current rate limits.
//
// The child is never killed after it answers: closing its stdin is how it is
// told to go, and it exits on its own at EOF. Killing it is reserved for a
// read that outlives timeout. Either way it is reaped on a goroutine, so a
// long-running TUI collects no zombies and the caller never waits on its
// exit.
func readCodexLive(argv []string, home string, timeout time.Duration) (codexRateLimits, error) {
	cmd := exec.Command(argv[0], argv[1:]...)
	cmd.Env = append(os.Environ(), "CODEX_HOME="+home)
	stdin, err := cmd.StdinPipe()
	if err != nil {
		return codexRateLimits{}, err
	}
	stdout, err := cmd.StdoutPipe()
	if err != nil {
		return codexRateLimits{}, err
	}
	if err := cmd.Start(); err != nil {
		return codexRateLimits{}, fmt.Errorf("codex app-server did not start: %w", err)
	}
	type outcome struct {
		limits codexRateLimits
		err    error
	}
	done := make(chan outcome, 1)
	go func() {
		limits, err := converseCodexLive(stdin, stdout)
		done <- outcome{limits, err}
		stdin.Close()
		// Notifications may still arrive; draining them keeps the child
		// from blocking on a full pipe before it sees EOF.
		io.Copy(io.Discard, stdout)
		cmd.Wait()
	}()
	timer := time.NewTimer(timeout)
	defer timer.Stop()
	select {
	case result := <-done:
		return result.limits, result.err
	case <-timer.C:
		cmd.Process.Kill()
		// Closing stdin as well reaches a grandchild (a wrapper script's
		// real binary) that the kill does not, so the drain above ends.
		stdin.Close()
		return codexRateLimits{}, errors.New("codex app-server timed out")
	}
}

// codexRPCReply is any line app-server writes; only replies carry an id.
type codexRPCReply struct {
	ID     json.RawMessage `json:"id"`
	Result json.RawMessage `json:"result"`
	Error  json.RawMessage `json:"error"`
}

// converseCodexLive runs the handshake and the one request over the pipes.
func converseCodexLive(stdin io.Writer, stdout io.Reader) (codexRateLimits, error) {
	reader := bufio.NewReaderSize(stdout, 64*1024)
	send := func(message string) error {
		_, err := io.WriteString(stdin, message+"\n")
		return err
	}
	clientInfo, _ := json.Marshal(map[string]string{"name": "quotatop", "title": "quotatop", "version": version})
	if err := send(`{"id":1,"method":"initialize","params":{"clientInfo":` + string(clientInfo) + `}}`); err != nil {
		return codexRateLimits{}, errors.New("codex app-server closed its input")
	}
	if _, err := awaitCodexReply(reader, "1"); err != nil {
		return codexRateLimits{}, err
	}
	if send(`{"method":"initialized","params":{}}`) != nil ||
		send(`{"id":2,"method":"`+codexLiveMethod+`","params":{"excludeResetCreditDetails":true}}`) != nil {
		return codexRateLimits{}, errors.New("codex app-server closed its input")
	}
	reply, err := awaitCodexReply(reader, "2")
	if err != nil {
		return codexRateLimits{}, err
	}
	if len(reply.Error) > 0 && !bytes.Equal(reply.Error, []byte("null")) {
		return codexRateLimits{}, errors.New(codexLiveMethod + " failed")
	}
	var result struct {
		RateLimits *codexLiveLimits `json:"rateLimits"`
	}
	if json.Unmarshal(reply.Result, &result) != nil || result.RateLimits == nil {
		return codexRateLimits{}, errors.New(codexLiveMethod + " returned no rate limits")
	}
	return result.RateLimits.rowLimits(), nil
}

// awaitCodexReply reads lines until the reply with the given id. Anything
// else -- notifications, other ids, lines that do not parse -- is skipped.
func awaitCodexReply(reader *bufio.Reader, id string) (codexRPCReply, error) {
	for {
		line, skipped, err := readLine(reader, codexMaxLineBytes)
		if !skipped && len(line) > 0 {
			var reply codexRPCReply
			if json.Unmarshal(line, &reply) == nil && string(bytes.TrimSpace(reply.ID)) == id {
				return reply, nil
			}
		}
		if err != nil {
			return codexRPCReply{}, errors.New("codex app-server exited before answering")
		}
	}
}
