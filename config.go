package main

import (
	"os"
	"path/filepath"
	"strings"
)

// configValues holds the QUOTATOP_* defaults loaded from the config file
// early in main(). It is the same set of settings as the environment
// variables, persisted: a variable present on the command line always wins
// over the file. Nil means no config file was loaded, which is the state
// tests and any code path that runs without main() see.
var configValues map[string]string

// loadConfig reads KEY=VALUE settings from path. Every failure yields an
// empty map rather than an error: a missing or broken config file must never
// stop the monitor from starting.
//
// Parsing: a line whose first non-space character is '#' is a comment, and
// blank lines are skipped. A line is split on the first '=' only, so a value
// may contain '='. Key and value are trimmed of surrounding whitespace; a
// value wrapped in matching single or double quotes is unquoted. Nothing
// else about quoting is interpreted -- no escapes, no variable expansion, no
// command substitution. A leading ~/ in a value expands to the home
// directory; a bare ~ or a ~user form is left alone. A line with no '=' is
// skipped. A later line for the same key wins.
func loadConfig(path string) map[string]string {
	values := map[string]string{}
	data, err := os.ReadFile(path)
	if err != nil {
		return values
	}
	for _, line := range strings.Split(string(data), "\n") {
		line = strings.TrimSpace(line)
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}
		eq := strings.IndexByte(line, '=')
		if eq < 0 {
			continue
		}
		key := strings.TrimSpace(line[:eq])
		if key == "" {
			continue
		}
		value := strings.TrimSpace(line[eq+1:])
		if n := len(value); n >= 2 &&
			(value[0] == '"' && value[n-1] == '"' || value[0] == '\'' && value[n-1] == '\'') {
			value = value[1 : n-1]
		}
		values[key] = expandTilde(value)
	}
	return values
}

// configPath works out which file to read: $QUOTATOP_CONFIG if set, otherwise
// $XDG_CONFIG_HOME/quotatop/config, otherwise ~/.config/quotatop/config.
func configPath() string {
	if path := os.Getenv("QUOTATOP_CONFIG"); path != "" {
		return path
	}
	if xdg := os.Getenv("XDG_CONFIG_HOME"); xdg != "" {
		return filepath.Join(xdg, "quotatop", "config")
	}
	if home, err := os.UserHomeDir(); err == nil {
		return filepath.Join(home, ".config", "quotatop", "config")
	}
	return ""
}

// setting returns the effective value of a QUOTATOP_* setting: the
// environment wins, then the config file, then "". An empty variable counts
// as unset, so a cleared variable falls back to the file. Works correctly
// when configValues is nil -- the environment alone.
func setting(key string) string {
	if value := os.Getenv(key); value != "" {
		return value
	}
	return configValues[key]
}

// settingsWithPrefixEnvKeys, when non-nil, restricts settingsWithPrefix to
// exactly these environment variable names instead of the full ambient
// os.Environ() -- a test seam. Production code never sets it, so the real
// environment is used unmodified. Tests set it so a QUOTATOP_*_ACCOUNT_*
// value a developer has exported on their own machine for their own use can
// never leak into a test that enumerates a specific, known set of account
// labels (or asserts that none are configured at all).
var settingsWithPrefixEnvKeys map[string]bool

// settingsWithPrefix returns every setting whose key starts with prefix,
// keyed by the remainder of the key after the prefix -- the account label for
// a QUOTATOP_CLAUDE_ACCOUNT_<label> or QUOTATOP_CODEX_ACCOUNT_<label> key.
// configValues supplies the starting set (already expandTilde-treated by
// loadConfig); the environment is then overlaid on top, key by key. An empty
// value -- from either source -- counts as unset and removes the entry, so a
// cleared environment variable can disable an account the config file
// declares -- unlike setting(), where an empty environment variable falls
// back to the config file instead of deleting the value. A
// suffix that is empty after trimming names no account and is skipped. Works
// when configValues is nil.
func settingsWithPrefix(prefix string) map[string]string {
	result := map[string]string{}
	apply := func(key, value string) {
		if !strings.HasPrefix(key, prefix) {
			return
		}
		suffix := strings.TrimSpace(key[len(prefix):])
		if suffix == "" {
			return
		}
		if value == "" {
			delete(result, suffix)
			return
		}
		result[suffix] = value
	}
	for key, value := range configValues {
		apply(key, value)
	}
	for _, entry := range os.Environ() {
		key, value, ok := strings.Cut(entry, "=")
		if !ok {
			continue
		}
		if settingsWithPrefixEnvKeys != nil && !settingsWithPrefixEnvKeys[key] {
			continue
		}
		apply(key, expandTilde(value))
	}
	return result
}

// expandTilde replaces a leading ~/ in path with the home directory, the
// way a shell does. Nothing else is interpreted: a bare ~ or a ~user form is
// left alone, and a tilde anywhere but at the start is not touched.
func expandTilde(path string) string {
	if strings.HasPrefix(path, "~/") {
		if home, err := os.UserHomeDir(); err == nil {
			return filepath.Join(home, path[2:])
		}
	}
	return path
}
