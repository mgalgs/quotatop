package main

import (
	"bytes"
	"encoding/hex"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"os/user"
	"runtime"
	"strings"
)

// keychainPrefix marks a credentials location that is a macOS Keychain
// item, not a file: "keychain:<service>". It goes wherever a credentials path
// goes (QUOTATOP_CLAUDE_CREDENTIALS, QUOTATOP_CLAUDE_ACCOUNT_<label>), so a
// Keychain account needs no setting of its own.
const keychainPrefix = "keychain:"

// claudeKeychainService is the generic-password item Claude Code writes on
// macOS for the default config directory.
const claudeKeychainService = "Claude Code-credentials"

// keychainFallback makes the default Claude source read the Keychain when
// ~/.claude/.credentials.json does not exist. Only macOS has one. Tests turn
// it off so no test can ever read the real Keychain.
var keychainFallback = runtime.GOOS == "darwin"

// securityPath is the absolute path, not a PATH lookup: the credential must
// never go to whatever "security" comes first on PATH. It is also the
// program Claude Code itself uses, so the item's access list already trusts
// it and no Keychain prompt appears.
const securityPath = "/usr/bin/security"

// keychainNotFound is the exit status security(1) returns when no item
// matches.
const keychainNotFound = 44

// keychainService returns the service named by a "keychain:" location.
func keychainService(location string) (string, bool) {
	service, ok := strings.CutPrefix(location, keychainPrefix)
	return service, ok
}

// runSecurity runs security(1) with stdin and returns its stdout. stderr is
// dropped on purpose: it can echo the command, and on a write the command
// carries the credential.
func runSecurity(stdin []byte, args ...string) ([]byte, error) {
	cmd := exec.Command(securityPath, args...)
	cmd.Stdin = bytes.NewReader(stdin)
	return cmd.Output()
}

func (s claudeSource) security(stdin []byte, args ...string) ([]byte, error) {
	if s.runSecurity != nil {
		return s.runSecurity(stdin, args...)
	}
	if runtime.GOOS != "darwin" {
		return nil, errors.New("the Keychain exists only on macOS")
	}
	return runSecurity(stdin, args...)
}

// readKeychain returns the item's password: the same JSON document that
// .credentials.json holds on Linux.
func (s claudeSource) readKeychain(service string) ([]byte, error) {
	out, err := s.security(nil, "find-generic-password", "-s", service, "-w")
	if err != nil {
		var exit *exec.ExitError
		if errors.As(err, &exit) && exit.ExitCode() == keychainNotFound {
			return nil, os.ErrNotExist
		}
		return nil, fmt.Errorf("could not read Keychain item %q: %w", service, err)
	}
	return bytes.TrimRight(out, "\n"), nil
}

// writeKeychain replaces the item's password. The password goes in on stdin,
// hex-encoded, through security's interactive mode: never on the command
// line, where any process could read it with ps, and never needing quoting.
func (s claudeSource) writeKeychain(service string, password []byte) error {
	account, err := keychainAccount()
	if err != nil {
		return err
	}
	for _, field := range []string{service, account} {
		if strings.ContainsAny(field, "\"\\\n") {
			return fmt.Errorf("cannot write Keychain item %q: unsupported character in its name", service)
		}
	}
	command := fmt.Sprintf("add-generic-password -U -a \"%s\" -s \"%s\" -X %s\n", account, service, hex.EncodeToString(password))
	if _, err := s.security([]byte(command), "-i"); err != nil {
		return fmt.Errorf("could not write Keychain item %q: %w", service, err)
	}
	// Interactive mode can exit 0 when its command failed. The refresh has
	// already spent the old refresh token, so a lost write logs the account
	// out: read the item back and say so plainly if it did not change.
	stored, err := s.readKeychain(service)
	if err != nil || !bytes.Equal(stored, password) {
		return fmt.Errorf("could not write Keychain item %q: it did not take the new token", service)
	}
	return nil
}

// keychainAccount is the account Claude Code files its item under: the
// login name.
func keychainAccount() (string, error) {
	if name := os.Getenv("USER"); name != "" {
		return name, nil
	}
	current, err := user.Current()
	if err != nil {
		return "", fmt.Errorf("could not find the Keychain account name: %w", err)
	}
	return current.Username, nil
}
