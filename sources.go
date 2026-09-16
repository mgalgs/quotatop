package main

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"time"
)

// Window is one quota bar: a labelled percentage with a reset deadline.
type Window struct {
	Key      string // stable across refreshes; used as the history key
	Label    string
	Percent  float64
	ResetsAt time.Time
	Length   time.Duration // how long the window lasts, for the sustained burn model
	Note     string        // shown under the bar when something needs saying
	Expired  bool          // the reading is older than the window it describes
}

// windowExpired reports whether a window's own length means the reading no
// longer describes the live window: the observation is old enough that the
// window has reset since it was taken. A window with no known length is
// never expired.
func windowExpired(length time.Duration, observed, now time.Time) bool {
	if length <= 0 {
		return false
	}
	return now.Sub(observed) >= length
}

// Snapshot is everything one source knows right now.
type Snapshot struct {
	Source       string // "claude" or "codex"
	Title        string
	Chip         string // plan or similar, shown in the panel's top-right
	Windows      []Window
	Observed     time.Time // when the data itself was observed, not when we asked
	Verb         string    // "fetched" / "reported"
	Footnote     string
	Detail       string
	Warning      string // panel-level caution, shown once under the windows
	LimitReached string // reason the account is refusing work, "" when not blocked
	Err          error
	At           time.Time // when this snapshot was produced
}

// --- Claude ---------------------------------------------------------------

type claudeLimit struct {
	Kind     string   `json:"kind"`
	Percent  *float64 `json:"percent"`
	ResetsAt string   `json:"resets_at"`
	Scope    *struct {
		Model *struct {
			DisplayName string `json:"display_name"`
		} `json:"model"`
		Surface *string `json:"surface"`
	} `json:"scope"`
}

type claudeBucket struct {
	Utilization *float64 `json:"utilization"`
	ResetsAt    *string  `json:"resets_at"`
}

type claudePayload struct {
	Limits   []claudeLimit `json:"limits"`
	FiveHour *claudeBucket `json:"five_hour"`
	SevenDay *claudeBucket `json:"seven_day"`
}

var claudeLabels = map[string]string{
	"session":       "5-hour",
	"weekly_all":    "Weekly",
	"weekly_scoped": "Weekly",
}

// claudeWindowLengths is the known length of each Claude limit kind. Claude's
// JSON does not report one, so it is mapped here. Any kind not listed has an
// unknown length and gets zero.
var claudeWindowLengths = map[string]time.Duration{
	"session":       5 * time.Hour,
	"weekly_all":    168 * time.Hour,
	"weekly_scoped": 168 * time.Hour,
}

func parseISO(value string) time.Time {
	if value == "" {
		return time.Time{}
	}
	for _, layout := range []string{time.RFC3339Nano, time.RFC3339} {
		if t, err := time.Parse(layout, value); err == nil {
			return t.Local()
		}
	}
	return time.Time{}
}

// claudeCacheTTL is how long a fetched reading stays fresh in the on-disk
// cache. Longer and the panel refetches instead of serving a stale number as
// if it were live.
const claudeCacheTTL = 10 * time.Minute

// claudeSource reads the Claude usage endpoint directly. The HTTP call and
// both paths are injectable so tests never need a socket, the real
// credentials file or the real cache.
type claudeSource struct {
	// doRequest performs the usage request; nil uses http.DefaultClient.Do.
	doRequest       func(*http.Request) (*http.Response, error)
	credentialsPath string
	cachePath       string
}

// defaultClaudeSource wires the real paths: the token lives in
// ~/.claude/.credentials.json, the cache in ~/.cache/quotatop/.
func defaultClaudeSource() claudeSource {
	var source claudeSource
	if home, err := os.UserHomeDir(); err == nil {
		source.credentialsPath = filepath.Join(home, ".claude", ".credentials.json")
		source.cachePath = filepath.Join(home, ".cache", "quotatop", "claude-quota.json")
	}
	// QUOTATOP_CLAUDE_CREDENTIALS overrides the credentials path, e.g. for a
	// macOS user whose token lives in the Keychain and who has exported it
	// to a JSON file of the same shape.
	if override := setting("QUOTATOP_CLAUDE_CREDENTIALS"); override != "" {
		source.credentialsPath = override
	}
	return source
}

// fetchClaude reads the live quota. fresh bypasses the cache for reading, but
// the new reading still lands in it.
func fetchClaude(fresh bool) Snapshot { return defaultClaudeSource().fetch(fresh) }

// accessToken pulls the Claude token out of the credentials file. Missing,
// unreadable or tokenless all mean "not signed in"; the file's contents never
// enter the error.
func (s claudeSource) accessToken() (string, error) {
	raw, err := os.ReadFile(s.credentialsPath)
	if err != nil {
		return "", errors.New("not signed in to Claude Code (no credentials file)")
	}
	var doc struct {
		ClaudeAiOauth struct {
			AccessToken string `json:"accessToken"`
		} `json:"claudeAiOauth"`
	}
	if err := json.Unmarshal(raw, &doc); err != nil || doc.ClaudeAiOauth.AccessToken == "" {
		return "", errors.New("not signed in to Claude Code (credentials have no Claude token)")
	}
	return doc.ClaudeAiOauth.AccessToken, nil
}

// claudeCache is the on-disk reading. fetched_at is a unix seconds number so
// the stamp is portable and trivially comparable.
type claudeCache struct {
	FetchedAt float64       `json:"fetched_at"`
	Payload   claudePayload `json:"payload"`
}

// readCache returns a usable reading. Any failure -- missing, corrupt, wrong
// shape -- is a miss rather than an error: a bad cache costs one extra
// request, not a red panel.
func (s claudeSource) readCache() (claudePayload, time.Time, bool) {
	raw, err := os.ReadFile(s.cachePath)
	if err != nil {
		return claudePayload{}, time.Time{}, false
	}
	var cache claudeCache
	if err := json.Unmarshal(raw, &cache); err != nil || cache.FetchedAt <= 0 {
		return claudePayload{}, time.Time{}, false
	}
	return cache.Payload, time.Unix(int64(cache.FetchedAt), 0), true
}

// writeCache records a reading for the next poll. It goes to a temp file in
// the same directory and is renamed into place, so a crash cannot leave a
// truncated cache behind. Failing to write is not an error: the reading was
// still served.
func (s claudeSource) writeCache(payload claudePayload, fetchedAt time.Time) {
	raw, err := json.Marshal(claudeCache{FetchedAt: float64(fetchedAt.Unix()), Payload: payload})
	if err != nil {
		return
	}
	dir := filepath.Dir(s.cachePath)
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return
	}
	tmp, err := os.CreateTemp(dir, ".claude-quota-*.tmp")
	if err != nil {
		return
	}
	name := tmp.Name()
	defer os.Remove(name)
	if _, err := tmp.Write(raw); err != nil {
		tmp.Close()
		return
	}
	if err := tmp.Close(); err != nil {
		return
	}
	if err := os.Chmod(name, 0o600); err != nil {
		return
	}
	if err := os.Rename(name, s.cachePath); err != nil {
		os.Remove(name)
	}
}

func (s claudeSource) fetch(fresh bool) Snapshot {
	snap := Snapshot{Source: "claude", Title: "CLAUDE", Verb: "fetched", At: time.Now(),
		Footnote: "account · 10m cache"}
	now := time.Now()
	if !fresh {
		if cached, fetchedAt, ok := s.readCache(); ok && now.Sub(fetchedAt) < claudeCacheTTL {
			cand := snap
			// The stamp, not the current time: a cached reading must never
			// look fresher than it is.
			cand.Observed = fetchedAt
			cand = s.withPayload(cand, cached)
			if cand.Err == nil {
				return cand
			}
			// A young cache with no usable limits costs one extra request,
			// not a red panel for the rest of the TTL: fall through and
			// refetch.
		}
	}
	payload, err := s.requestUsage()
	if err != nil {
		snap.Err = err
		return snap
	}
	// Whole seconds: the same value the cache stamp carries, so Observed and
	// the stamp agree exactly.
	snap.Observed = time.Unix(now.Unix(), 0)
	snap = s.withPayload(snap, payload)
	// Cache only a reading the panel will accept: a 200 with no usable
	// limits must not pin a red panel for the whole TTL.
	if snap.Err == nil {
		s.writeCache(payload, now)
	}
	return snap
}

// requestUsage asks the usage endpoint for the current numbers. A failed
// request reports its status code and nothing else: the body and the token
// never enter the message.
func (s claudeSource) requestUsage() (claudePayload, error) {
	if s.doRequest == nil {
		s.doRequest = http.DefaultClient.Do
	}
	token, err := s.accessToken()
	if err != nil {
		return claudePayload{}, err
	}
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	req, err := http.NewRequestWithContext(ctx, http.MethodGet,
		"https://api.anthropic.com/api/oauth/usage", nil)
	if err != nil {
		return claudePayload{}, err
	}
	req.Header.Set("Authorization", "Bearer "+token)
	req.Header.Set("anthropic-beta", "oauth-2025-04-20")
	req.Header.Set("Content-Type", "application/json")
	resp, err := s.doRequest(req)
	if err != nil {
		return claudePayload{}, err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return claudePayload{}, fmt.Errorf("Claude usage request failed with HTTP %d", resp.StatusCode)
	}
	var payload claudePayload
	if err := json.NewDecoder(resp.Body).Decode(&payload); err != nil {
		return claudePayload{}, errors.New("Claude usage response is not readable JSON")
	}
	return payload, nil
}

// withPayload turns a payload into the snapshot's windows, mapping each limit
// kind to its label and length the way the panel always has.
func (s claudeSource) withPayload(snap Snapshot, payload claudePayload) Snapshot {
	for _, limit := range payload.Limits {
		if limit.Percent == nil {
			continue
		}
		label, ok := claudeLabels[limit.Kind]
		if !ok {
			label = limit.Kind
		}
		if limit.Scope != nil {
			if limit.Scope.Model != nil && limit.Scope.Model.DisplayName != "" {
				label += " · " + limit.Scope.Model.DisplayName
			} else if limit.Kind == "weekly_scoped" {
				label += " · scoped"
			}
			if limit.Scope.Surface != nil && *limit.Scope.Surface != "" {
				label += " · " + *limit.Scope.Surface
			}
		}
		length := claudeWindowLengths[limit.Kind]
		snap.Windows = append(snap.Windows, Window{
			Key: limit.Kind, Label: label, Percent: *limit.Percent,
			ResetsAt: parseISO(limit.ResetsAt), Length: length,
			Expired: windowExpired(length, snap.Observed, snap.At),
		})
	}
	// Older payloads carried only the two named buckets, with no limits[].
	if len(snap.Windows) == 0 {
		for _, bucket := range []struct {
			key, label string
			data       *claudeBucket
		}{{"session", "5-hour", payload.FiveHour}, {"weekly_all", "Weekly", payload.SevenDay}} {
			if bucket.data == nil || bucket.data.Utilization == nil {
				continue
			}
			window := Window{Key: bucket.key, Label: bucket.label, Percent: *bucket.data.Utilization,
				Length: claudeWindowLengths[bucket.key]}
			window.Expired = windowExpired(window.Length, snap.Observed, snap.At)

			if bucket.data.ResetsAt != nil {
				window.ResetsAt = parseISO(*bucket.data.ResetsAt)
			}
			snap.Windows = append(snap.Windows, window)
		}
	}
	if len(snap.Windows) == 0 {
		snap.Err = errors.New("Claude usage response contained no usage limits")
		return snap
	}
	return snap
}

// --- Codex ----------------------------------------------------------------

// codexRLWindow is one of the two rate-limit windows a usage row carries.
type codexRLWindow struct {
	UsedPercent   *float64 `json:"used_percent"`
	WindowMinutes *float64 `json:"window_minutes"`
	ResetsAt      *float64 `json:"resets_at"` // unix epoch seconds
}

type codexRateLimits struct {
	LimitID              *string        `json:"limit_id"`
	PlanType             *string        `json:"plan_type"`
	Primary              *codexRLWindow `json:"primary"`
	Secondary            *codexRLWindow `json:"secondary"`
	RateLimitReachedType *string        `json:"rate_limit_reached_type"`
}

// codexRow is one line of a session log. Only token_count rows carrying rate
// limits are usage reports; everything else in the stream is ignored.
type codexRow struct {
	Timestamp string `json:"timestamp"`
	Payload   *struct {
		Type       string           `json:"type"`
		RateLimits *codexRateLimits `json:"rate_limits"`
	} `json:"payload"`
}

// codexSource walks Codex session logs. The roots are injectable so tests
// point at their own temp directories and never the real ~/.codex.
type codexSource struct {
	defaultRoot string        // $CODEX_HOME (or ~/.codex) plus /sessions; kind "interactive"
	extraRoots  []string      // QUOTATOP_CODEX_ROOTS glob matches; kind "extra"
	timeout     time.Duration // bounds the walk; zero uses codexScanTimeout
	blockScan   func()        // test hook: run at the top of the scan, may block
}

// codexScanTimeout bounds the walk. The scan runs in-process now, so an
// unresponsive mount under a root would otherwise hold the panel on
// "refreshing" forever; the timeout trades a hung scan for an error panel
// that recovers on the next tick, the way the helper subprocess used to. The
// bound is the 25s the helper subprocess ran under, not the 5s of the Claude
// side: that one covers a single HTTP request, whereas this one covers a
// recursive walk that parses every line of every session log, and a heavy
// user's tree can exceed a fifth of that in normal operation. Tightening the
// bound would not fix the case it is meant for -- a hung mount still leaks a
// blocked goroutine per timeout -- and would only convert slow-but-fine scans
// into total failures that drop the last good reading.
const codexScanTimeout = 25 * time.Second

// defaultCodexSource works out the roots from the environment.
func defaultCodexSource() codexSource {
	var source codexSource
	home := os.Getenv("CODEX_HOME")
	if home == "" {
		if dir, err := os.UserHomeDir(); err == nil {
			home = filepath.Join(dir, ".codex")
		}
	}
	if home != "" {
		source.defaultRoot = filepath.Join(home, "sessions")
	}
	// QUOTATOP_CODEX_ROOTS is a ':'-separated list of glob patterns, each
	// match being another session root to walk like the default. A pattern
	// matching nothing is ignored: sandbox run directories are routinely
	// gone before we look at them.
	//
	// A leading ~/ in each element is expanded before globbing, per element
	// rather than once for the whole value. A POSIX shell already does that
	// after a ':' in an assignment (which is how PATH=~/bin:~/.local/bin
	// works), so an environment value with a home-relative second root
	// already works today; the config file has no shell, so without this a
	// value like /var/tmp/...:~/.claude/codex-quota would hand a literal
	// ~ to filepath.Glob, which matches nothing and is silently ignored --
	// a degraded reading with no error to explain it.
	if raw := setting("QUOTATOP_CODEX_ROOTS"); raw != "" {
		for _, pattern := range strings.Split(raw, ":") {
			if pattern == "" {
				continue
			}
			pattern = expandTilde(pattern)
			matches, err := filepath.Glob(pattern)
			if err != nil {
				continue
			}
			source.extraRoots = append(source.extraRoots, matches...)
		}
	}
	return source
}

// fetchCodex reads the local session logs directly. There is no server to
// ask: Codex only records its limits when it reports them.
func fetchCodex() Snapshot { return defaultCodexSource().fetch() }

// codexReport is the newest usage row seen so far and where it came from.
type codexReport struct {
	timestamp   time.Time
	path        string
	root        string // "interactive" or "extra"; goes into the footnote
	planType    string
	primary     *codexRLWindow
	secondary   *codexRLWindow
	scanErr     error     // the first file with a genuine read error (I/O, permissions), if any
	reachedType string    // newest non-empty rate_limit_reached_type seen, "" if none
	reachedAt   time.Time // timestamp of the row reachedType came from
}

// consider upgrades the running best when the row is a usage report and is at
// least as new as what we have. Ties go to the later row encountered.
func (r *codexReport) consider(row codexRow, path, root string) {
	if row.Payload == nil || row.Payload.Type != "token_count" {
		return
	}
	rl := row.Payload.RateLimits
	if rl == nil {
		return
	}
	// A blocked signal is captured before the limit_id filter below rejects
	// the row: the reached-type row is routinely a "premium" limit_id row
	// with both windows null, which must still be rejected for percentage
	// purposes but which is exactly the row that says the account is
	// blocked.
	if rl.RateLimitReachedType != nil && *rl.RateLimitReachedType != "" {
		if timestamp, err := time.Parse(time.RFC3339, row.Timestamp); err == nil {
			if !timestamp.Before(r.reachedAt) {
				r.reachedType = *rl.RateLimitReachedType
				r.reachedAt = timestamp
			}
		}
	}
	if rl.LimitID != nil && *rl.LimitID != "codex" {
		return
	}
	if (rl.Primary == nil || rl.Primary.UsedPercent == nil) &&
		(rl.Secondary == nil || rl.Secondary.UsedPercent == nil) {
		return
	}
	timestamp, err := time.Parse(time.RFC3339, row.Timestamp)
	if err != nil {
		return // a row whose timestamp does not parse is skipped: with a zero
		// time it would beat every older dated row and its reading would be
		// reported as "just now" no matter how old it actually is
	}
	if r.path == "" || !timestamp.Before(r.timestamp) {
		var planType string
		if rl.PlanType != nil {
			planType = *rl.PlanType
		}
		scanErr := r.scanErr
		reachedType, reachedAt := r.reachedType, r.reachedAt
		*r = codexReport{timestamp: timestamp, path: path, root: root,
			planType: planType, primary: rl.Primary, secondary: rl.Secondary}
		r.scanErr = scanErr
		r.reachedType, r.reachedAt = reachedType, reachedAt
	}
}

// scan runs the root walk and fills snap with the result.
func (s codexSource) scan() (snap Snapshot) {
	snap = Snapshot{Source: "codex", Title: "CODEX", Verb: "reported", At: time.Now(),
		Footnote: "local"}
	if s.blockScan != nil {
		s.blockScan()
	}
	var report codexReport
	if s.defaultRoot != "" {
		s.walkRoot(s.defaultRoot, "interactive", &report)
	}
	for _, root := range s.extraRoots {
		s.walkRoot(root, "extra", &report)
	}
	if report.path == "" {
		if report.scanErr != nil {
			snap.Err = fmt.Errorf("no Codex usage reports found; %s", report.scanErr)
		} else {
			snap.Err = errors.New("no Codex usage reports found")
		}
		return snap
	}
	snap.Observed = report.timestamp
	snap.Chip = report.planType
	snap.Footnote = "local · " + report.root
	// The cut-off warning is about the whole scan, so it rides on the
	// panel-level Warning, rendered once under the windows. Window.Note was
	// the wrong channel: it renders under every bar, and the "reset time has
	// passed" note -- which fires exactly when the newest readable row is an
	// old one, i.e. when a cut-off log is most likely -- would swallow it.
	if report.scanErr != nil {
		snap.Warning = "a log is cut off; the reading may be stale"
	}
	snap.Detail = report.path
	// A blocked signal only counts if it is at least as new as the reading:
	// an hour-old block followed by a fresh normal row means the block is
	// over.
	if report.reachedType != "" && !report.reachedAt.Before(report.timestamp) {
		snap.LimitReached = report.reachedType
	}
	now := time.Now()
	for _, entry := range []struct {
		key, label string
		window     *codexRLWindow
	}{
		{"primary", "Primary", report.primary},
		{"secondary", "Secondary", report.secondary},
	} {
		if entry.window == nil || entry.window.UsedPercent == nil {
			continue
		}
		window := Window{Key: entry.key, Percent: *entry.window.UsedPercent}
		switch {
		case entry.window.WindowMinutes == nil:
			window.Label = entry.label
		case *entry.window.WindowMinutes == 300:
			window.Label = "5-hour"
		case *entry.window.WindowMinutes == 10080:
			window.Label = "Weekly"
		default:
			window.Label = fmt.Sprintf("%.0f-minute", *entry.window.WindowMinutes)
		}
		if entry.window.WindowMinutes != nil && *entry.window.WindowMinutes > 0 {
			window.Length = time.Duration(*entry.window.WindowMinutes) * time.Minute
		}
		window.Expired = windowExpired(window.Length, snap.Observed, snap.At)
		if entry.window.ResetsAt != nil {
			window.ResetsAt = time.Unix(int64(*entry.window.ResetsAt), 0)
			if !window.ResetsAt.After(now) {
				window.Note = "reset time has passed; awaiting a new report"
			}
		}
		snap.Windows = append(snap.Windows, window)
	}
	return snap
}

func (s codexSource) fetch() Snapshot {
	// The walk runs on its own goroutine under a deadline: if it blocks
	// (a hung network or FUSE mount under a root), the deadline returns an
	// error snapshot and the next tick retries, instead of freezing the
	// panel on "refreshing". The blocked goroutine then leaks until the OS
	// unblocks the call; that is the price of a bound Go cannot cancel.
	timeout := s.timeout
	if timeout <= 0 {
		timeout = codexScanTimeout
	}
	ctx, cancel := context.WithTimeout(context.Background(), timeout)
	defer cancel()
	done := make(chan Snapshot, 1)
	go func() { done <- s.scan() }()
	select {
	case snap := <-done:
		return snap
	case <-ctx.Done():
		return Snapshot{Source: "codex", Title: "CODEX", At: time.Now(),
			Err: errors.New("Codex log scan timed out")}
	}
}

// walkRoot scans every .jsonl file under root. A file or directory that
// vanishes or is unreadable is skipped, not an error: sandbox run directories
// are deleted while this scans.
func (s codexSource) walkRoot(root, kind string, report *codexReport) {
	filepath.WalkDir(root, func(path string, entry fs.DirEntry, err error) error {
		if err != nil || entry.IsDir() || !strings.HasSuffix(path, ".jsonl") {
			return nil
		}
		s.scanFile(path, kind, report)
		return nil
	})
}

// codexMaxLineBytes bounds the memory a single line can hold. A real usage
// row is a small JSON object of roughly a kilobyte; a line that long is a
// huge message payload. Exceeding the bound does not stop the scan: the line
// is drained and skipped so the rest of the file is still read.
const codexMaxLineBytes = 1024 * 1024

// readLine returns the next line from r without its trailing newline. A line
// longer than max is discarded and reported with skipped=true and no data:
// the rest of that line is drained so reading continues with the line after
// it, and no more than max bytes are ever held. err is io.EOF at the end of
// the file, in which case any final line without a trailing newline is still
// returned.
func readLine(r *bufio.Reader, max int) (line []byte, skipped bool, err error) {
	var buf []byte
	for {
		chunk, err := r.ReadSlice('\n')
		if !skipped {
			if len(buf)+len(chunk) <= max {
				buf = append(buf, chunk...) // ReadSlice's slice is valid only until the next read
			} else {
				skipped = true
				buf = nil
			}
		}
		// ErrBufferFull means the buffer filled before a newline: the same
		// line continues, not an error.
		if err == bufio.ErrBufferFull {
			continue
		}
		if skipped {
			return nil, true, err
		}
		if len(buf) > 0 && buf[len(buf)-1] == '\n' {
			buf = buf[:len(buf)-1]
		}
		return buf, false, err
	}
}

// scanFile reads one session log. A line that does not parse is skipped
// silently: files are appended to live and the last line is routinely
// half-written. A line longer than codexMaxLineBytes is drained and skipped
// too: it is a huge message payload, never a usage report, and the rows
// after it are still read.
func (s codexSource) scanFile(path, kind string, report *codexReport) {
	file, err := os.Open(path)
	if err != nil {
		return
	}
	defer file.Close()
	reader := bufio.NewReaderSize(file, 64*1024)
	for {
		line, skipped, err := readLine(reader, codexMaxLineBytes)
		// A skipped oversized line is not an error: it is discarded and the
		// rest of the file is still read, so the reading is the newest
		// available and needs no staleness caution.
		if !skipped && len(line) > 0 {
			var row codexRow
			if json.Unmarshal(line, &row) == nil {
				report.consider(row, path, kind)
			}
		}
		// A final line without a trailing newline arrives with io.EOF, so it
		// must be considered before the error ends the scan.
		if err != nil {
			if err != io.EOF && report.scanErr == nil {
				report.scanErr = fmt.Errorf("%s: %v", filepath.Base(path), err)
			}
			break
		}
	}
}
