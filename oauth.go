package main

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"os"
	"path/filepath"
	"time"
)

const (
	claudeOAuthURL      = "https://console.anthropic.com/v1/oauth/token"
	claudeOAuthClientID = "9d1c250a-e61b-44d9-88ed-5944d1962f5e"
	claudeRefreshSkew   = time.Minute
)

type claudeCredentials struct {
	accessToken  string
	refreshToken string
	expiresAt    int64
}

func (s claudeSource) readClaudeCredentials() (claudeCredentials, error) {
	raw, err := os.ReadFile(s.credentialsPath)
	if err != nil {
		return claudeCredentials{}, errors.New("not signed in to Claude Code (no credentials file)")
	}
	var doc struct {
		ClaudeAiOauth struct {
			AccessToken  string `json:"accessToken"`
			RefreshToken string `json:"refreshToken"`
			ExpiresAt    int64  `json:"expiresAt"`
		} `json:"claudeAiOauth"`
	}
	if err := json.Unmarshal(raw, &doc); err != nil || doc.ClaudeAiOauth.AccessToken == "" {
		return claudeCredentials{}, errors.New("not signed in to Claude Code (credentials have no Claude token)")
	}
	return claudeCredentials{doc.ClaudeAiOauth.AccessToken, doc.ClaudeAiOauth.RefreshToken, doc.ClaudeAiOauth.ExpiresAt}, nil
}

func (s claudeSource) refreshMaterial() (claudeCredentials, error) {
	credentials, err := s.readClaudeCredentials()
	if err != nil || credentials.refreshToken == "" || credentials.expiresAt == 0 {
		return claudeCredentials{}, errors.New("Claude usage request failed with HTTP 401")
	}
	return credentials, nil
}

func (c claudeCredentials) nearExpiry(now time.Time) bool {
	return now.UnixMilli() >= c.expiresAt-claudeRefreshSkew.Milliseconds()
}

// usableToken refreshes only at the point a live request needs the token.
func (s claudeSource) usableToken(force bool) (string, error) {
	credentials, err := s.readClaudeCredentials()
	if err != nil {
		return "", err
	}
	if credentials.refreshToken == "" || credentials.expiresAt == 0 || (!force && !credentials.nearExpiry(time.Now())) {
		return credentials.accessToken, nil
	}
	if s.cacheDir == "" {
		return "", errors.New("could not create Claude token refresh lock")
	}
	sum := sha256.Sum256([]byte(s.credentialsPath))
	lockBase := filepath.Join(s.cacheDir, fmt.Sprintf("claude-refresh-%x", sum[:]))
	if err := os.MkdirAll(s.cacheDir, 0o700); err != nil {
		return "", fmt.Errorf("could not create Claude token refresh lock: %w", err)
	}
	var token string
	err = withHistoryLock(lockBase, func() error {
		current, err := s.readClaudeCredentials()
		if err != nil {
			return err
		}
		if !force && !current.nearExpiry(time.Now()) {
			token = current.accessToken
			return nil
		}
		refreshed, err := s.redeemClaudeRefreshToken(current.refreshToken)
		if err != nil {
			if status, ok := oauthHTTPStatus(err); ok && (status == http.StatusBadRequest || status == http.StatusUnauthorized) {
				// Claude Code may have redeemed the old refresh token while this
				// process waited for its own lock. Its new file wins.
				if winner, rereadErr := s.readClaudeCredentials(); rereadErr == nil && !winner.nearExpiry(time.Now()) {
					token = winner.accessToken
					return nil
				}
				return fmt.Errorf("Claude token expired and refresh failed (HTTP %d); run a claude session on this account to re-login", status)
			}
			return err
		}
		if err := s.writeRefreshedClaudeCredentials(refreshed); err != nil {
			return err
		}
		token = refreshed.accessToken
		return nil
	})
	if err != nil {
		return "", err
	}
	return token, nil
}

type oauthHTTPError struct{ status int }

func (e oauthHTTPError) Error() string {
	return fmt.Sprintf("Claude token refresh failed with HTTP %d", e.status)
}

func oauthHTTPStatus(err error) (int, bool) {
	var status oauthHTTPError
	if errors.As(err, &status) {
		return status.status, true
	}
	return 0, false
}

type oauthTokenResponse struct {
	AccessToken  string `json:"access_token"`
	RefreshToken string `json:"refresh_token"`
	ExpiresIn    int64  `json:"expires_in"`
}

// redeemClaudeRefreshToken is intentionally the sole OAuth wire mapping.
func (s claudeSource) redeemClaudeRefreshToken(refreshToken string) (claudeCredentials, error) {
	body, err := json.Marshal(map[string]string{"grant_type": "refresh_token", "refresh_token": refreshToken, "client_id": claudeOAuthClientID})
	if err != nil {
		return claudeCredentials{}, err
	}
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	url := s.oauthURL
	if url == "" {
		url = claudeOAuthURL
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, url, bytes.NewReader(body))
	if err != nil {
		return claudeCredentials{}, err
	}
	req.Header.Set("Content-Type", "application/json")
	do := s.doRequest
	if do == nil {
		do = http.DefaultClient.Do
	}
	resp, err := do(req)
	if err != nil {
		return claudeCredentials{}, err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return claudeCredentials{}, oauthHTTPError{resp.StatusCode}
	}
	var result oauthTokenResponse
	if err := json.NewDecoder(resp.Body).Decode(&result); err != nil || result.AccessToken == "" || result.RefreshToken == "" || result.ExpiresIn <= 0 {
		return claudeCredentials{}, oauthHTTPError{resp.StatusCode}
	}
	return claudeCredentials{accessToken: result.AccessToken, refreshToken: result.RefreshToken, expiresAt: time.Now().UnixMilli() + result.ExpiresIn*1000}, nil
}

// writeRefreshedClaudeCredentials changes only the three OAuth fields. Raw
// JSON values are emitted directly so unknown fields retain their bytes.
func (s claudeSource) writeRefreshedClaudeCredentials(credentials claudeCredentials) error {
	raw, err := os.ReadFile(s.credentialsPath)
	if err != nil {
		return fmt.Errorf("refreshed Claude token but could not write %s: %w", s.credentialsPath, err)
	}
	info, statErr := os.Stat(s.credentialsPath)
	mode := os.FileMode(0o600)
	if statErr == nil {
		mode = info.Mode().Perm()
	}
	var doc map[string]json.RawMessage
	if err := json.Unmarshal(raw, &doc); err != nil {
		return fmt.Errorf("refreshed Claude token but could not write %s: %w", s.credentialsPath, err)
	}
	var oauth map[string]json.RawMessage
	if err := json.Unmarshal(doc["claudeAiOauth"], &oauth); err != nil {
		return fmt.Errorf("refreshed Claude token but could not write %s: %w", s.credentialsPath, err)
	}
	for key, value := range map[string]any{"accessToken": credentials.accessToken, "refreshToken": credentials.refreshToken, "expiresAt": credentials.expiresAt} {
		encoded, err := json.Marshal(value)
		if err != nil {
			return fmt.Errorf("refreshed Claude token but could not write %s: %w", s.credentialsPath, err)
		}
		oauth[key] = encoded
	}
	oauthRaw, err := marshalRawObject(oauth)
	if err != nil {
		return fmt.Errorf("refreshed Claude token but could not write %s: %w", s.credentialsPath, err)
	}
	doc["claudeAiOauth"] = oauthRaw
	output, err := marshalRawObject(doc)
	if err != nil {
		return fmt.Errorf("refreshed Claude token but could not write %s: %w", s.credentialsPath, err)
	}
	dir := filepath.Dir(s.credentialsPath)
	tmp, err := os.CreateTemp(dir, ".credentials-*.tmp")
	if err != nil {
		return fmt.Errorf("refreshed Claude token but could not write %s: %w", s.credentialsPath, err)
	}
	name := tmp.Name()
	defer os.Remove(name)
	if _, err = tmp.Write(output); err == nil {
		err = tmp.Close()
	} else {
		tmp.Close()
	}
	if err == nil {
		err = os.Chmod(name, mode)
	}
	if err == nil {
		err = os.Rename(name, s.credentialsPath)
	}
	if err != nil {
		return fmt.Errorf("refreshed Claude token but could not write %s: %w", s.credentialsPath, err)
	}
	return nil
}

func marshalRawObject(values map[string]json.RawMessage) ([]byte, error) {
	var out bytes.Buffer
	out.WriteByte('{')
	first := true
	for key, value := range values {
		if !json.Valid(value) {
			return nil, errors.New("invalid credential JSON")
		}
		if !first {
			out.WriteByte(',')
		}
		first = false
		name, _ := json.Marshal(key)
		out.Write(name)
		out.WriteByte(':')
		out.Write(value)
	}
	out.WriteByte('}')
	return out.Bytes(), nil
}
