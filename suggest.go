package main

import (
	"encoding/json"
	"fmt"
	"os"
	"sort"
	"strings"
	"time"
)

// This file generalizes, to every source defaultSources() knows about, the
// selection policy fork-sandbox-headroom-quotatop (an external consumer
// script) applies to Claude credentials alone via `quotatop --json | jq`:
// drop whatever cannot take work, then rank what is left by weekly headroom.
// That script stays external and unchanged; --suggest brings the same
// answer in-repo, where the per-family window mapping already lives.

// sessionState names why a ranked source's session-role window reads the way
// it does, distinct enough from a plain percentage that both render modes
// need to branch on it rather than just print a number.
type sessionState string

const (
	sessionOK           sessionState = "ok"            // a live, unexpired percentage
	sessionResetPending sessionState = "reset-pending" // expired: the window has reset since this reading
	sessionAbsent       sessionState = "absent"        // the source's family has no session-role window
)

// rankedSource is one routable source's resolved policy inputs, shared by
// the text and JSON renderers so the two can never disagree. It intentionally
// carries more than the JSON schema exposes -- weeklyProjectionValid only
// distinguishes "holds-to-reset" from "no-projection" in the text word, and
// has no JSON field of its own, since weeklyExhaustsBeforeReset alone is
// enough for the JSON consumer's ranking logic.
type rankedSource struct {
	source          string
	account         string
	credentialsPath string

	weeklyPercent             float64
	weeklyWindowKey           string
	weeklyProjectionValid     bool
	weeklyExhaustsBeforeReset bool

	sessionState   sessionState
	sessionPercent float64 // meaningful only when sessionState == sessionOK or sessionResetPending (0 there)

	observedAgeSeconds int64
}

// excludedSource is one source the policy dropped, with the reason a human
// or a consumer can act on -- never silent.
type excludedSource struct {
	source  string
	account string
	reason  string
}

// suggestion is the full policy outcome for one fetch round: the routable
// sources in rank order, and everything else with why it was dropped.
type suggestion struct {
	ranked   []rankedSource
	excluded []excludedSource
}

// weeklyRole and sessionRole answer "which window is the account-wide weekly
// meter, and which is the short session window" for one source family.
// Claude's weekly_scoped is deliberately left unmapped here -- it meters one
// model family, not the account, and a routing decision concerns the
// account-wide meter. A source outside both known families maps to no role
// at all, so buildSuggestion excludes it for lacking a weekly window rather
// than guessing.
func weeklyRole(source string) string {
	switch source {
	case "claude":
		return "weekly_all"
	case "codex":
		return "secondary"
	default:
		return ""
	}
}

func sessionRole(source string) string {
	switch source {
	case "claude":
		return "session"
	case "codex":
		return "primary"
	default:
		return ""
	}
}

func findWindow(windows []Window, key string) (Window, bool) {
	if key == "" {
		return Window{}, false
	}
	for _, window := range windows {
		if window.Key == key {
			return window, true
		}
	}
	return Window{}, false
}

// buildSuggestion applies the routing policy to one fetch round's snapshots,
// in defaultSources() order -- the operator's configured preference order,
// and the tiebreak of last resort once weekly headroom is equal. It takes a
// *History and now the same way encodeJSON does: the History it is handed is
// just data already read into memory, so this stays pure and needs no
// fetching to test.
func buildSuggestion(snaps []Snapshot, history *History, now time.Time) suggestion {
	var result suggestion
	candidates := make([]rankedSource, 0, len(snaps))
	for _, snap := range snaps {
		ranked, reason, ok := evaluateSource(snap, history, now)
		if !ok {
			result.excluded = append(result.excluded, excludedSource{source: snap.Source, account: snap.Account, reason: reason})
			continue
		}
		candidates = append(candidates, ranked)
	}

	// A projection that does not exhaust before reset always beats one that
	// does, then lower weekly percent wins, and SliceStable leaves ties in
	// defaultSources() order rather than reshuffling them.
	sort.SliceStable(candidates, func(i, j int) bool {
		a, b := candidates[i], candidates[j]
		if a.weeklyExhaustsBeforeReset != b.weeklyExhaustsBeforeReset {
			return !a.weeklyExhaustsBeforeReset
		}
		return a.weeklyPercent < b.weeklyPercent
	})
	result.ranked = candidates
	return result
}

// evaluateSource resolves one snapshot against the policy: either the
// rankedSource it should contribute, or the reason it was dropped. The
// checks run in the order the policy is specified in, so the reason
// reported is always the first one that applies.
func evaluateSource(snap Snapshot, history *History, now time.Time) (rankedSource, string, bool) {
	if snap.Err != nil {
		return rankedSource{}, fmt.Sprintf("error: %s", snap.Err), false
	}
	if snap.LimitReached != "" {
		return rankedSource{}, fmt.Sprintf("blocked: %s", snap.LimitReached), false
	}
	if len(snap.Windows) == 0 {
		return rankedSource{}, "no windows", false
	}
	weeklyWindow, ok := findWindow(snap.Windows, weeklyRole(snap.Source))
	if !ok {
		return rankedSource{}, "no weekly window", false
	}
	// An expired reading predates the window's own reset, so its stale
	// percent must not block or rank the source -- it is scored as if the
	// window just opened at 0%, the same way an expired window's projection
	// is withheld below rather than trusted.
	weeklyPercent := weeklyWindow.Percent
	if weeklyWindow.Expired {
		weeklyPercent = 0
	}
	if weeklyPercent >= 100 {
		return rankedSource{}, fmt.Sprintf("weekly at %.0f%%", weeklyPercent), false
	}

	state, sessionPercent := sessionAbsent, 0.0
	if sessionWindow, ok := findWindow(snap.Windows, sessionRole(snap.Source)); ok {
		if sessionWindow.Expired {
			state = sessionResetPending
		} else {
			state, sessionPercent = sessionOK, sessionWindow.Percent
			if sessionPercent >= 100 {
				return rankedSource{}, fmt.Sprintf("session at %.0f%%", sessionPercent), false
			}
		}
	}

	projectionValid, exhausts := false, false
	if !weeklyWindow.Expired {
		projection := history.Project(snap.Identity(), weeklyWindow, now)
		projectionValid = projection.Valid
		exhausts = projection.Valid && !projection.ExhaustAt.IsZero()
	}

	age := int64(now.Sub(snap.Observed) / time.Second)
	if snap.Observed.IsZero() || age < 0 {
		age = 0
	}

	return rankedSource{
		source:                    snap.Source,
		account:                   snap.Account,
		credentialsPath:           snap.CredentialsPath,
		weeklyPercent:             weeklyPercent,
		weeklyWindowKey:           weeklyWindow.Key,
		weeklyProjectionValid:     projectionValid,
		weeklyExhaustsBeforeReset: exhausts,
		sessionState:              state,
		sessionPercent:            sessionPercent,
		observedAgeSeconds:        age,
	}, "", true
}

// suggestName is the "<source>" or "<source>/<account>" label both render
// modes use to name a source.
func suggestName(source, account string) string {
	if account == "" {
		return source
	}
	return source + "/" + account
}

func weeklyProjectionWord(valid, exhausts bool) string {
	switch {
	case !valid:
		return "no-projection"
	case exhausts:
		return "exhausts-before-reset"
	default:
		return "holds-to-reset"
	}
}

func sessionField(r rankedSource) string {
	switch r.sessionState {
	case sessionOK:
		return fmt.Sprintf("session=%.0f%%", r.sessionPercent)
	case sessionResetPending:
		return "session=reset-pending"
	default:
		return "session=-"
	}
}

// ageSuffix appends " [reading Nh old]" once a reading is stale enough to be
// worth flagging -- an hour or more -- so an idle Codex log's aging reading
// stays visible when it is used to route work.
func ageSuffix(seconds int64) string {
	hours := seconds / 3600
	if hours < 1 {
		return ""
	}
	return fmt.Sprintf(" [reading %dh old]", hours)
}

// renderSuggestText is the frozen text format: one line per ranked source,
// "pick:" for the first and "  N. " for the rest, then one "excluded:" line
// per dropped source, in the order buildSuggestion returned them.
func renderSuggestText(sugg suggestion) string {
	var b strings.Builder
	for i, r := range sugg.ranked {
		if i == 0 {
			b.WriteString("pick: ")
		} else {
			fmt.Fprintf(&b, "  %d. ", i+1)
		}
		fmt.Fprintf(&b, "%s weekly=%.0f%% %s %s%s\n",
			suggestName(r.source, r.account), r.weeklyPercent,
			weeklyProjectionWord(r.weeklyProjectionValid, r.weeklyExhaustsBeforeReset),
			sessionField(r), ageSuffix(r.observedAgeSeconds))
	}
	for _, e := range sugg.excluded {
		fmt.Fprintf(&b, "excluded: %s — %s\n", suggestName(e.source, e.account), e.reason)
	}
	return b.String()
}

// jsonSuggestDoc is the published --suggest --json document: one pick (or
// null when nothing is routable), the full ranked list it came from, and
// what was excluded and why.
type jsonSuggestDoc struct {
	Schema      int                   `json:"schema"`
	GeneratedAt string                `json:"generated_at"`
	Pick        *jsonSuggestSource    `json:"pick"`
	Ranked      []jsonSuggestSource   `json:"ranked"`
	Excluded    []jsonSuggestExcluded `json:"excluded"`
}

type jsonSuggestSource struct {
	Rank                      int      `json:"rank"`
	Source                    string   `json:"source"`
	Account                   string   `json:"account,omitempty"`
	CredentialsPath           string   `json:"credentials_path,omitempty"`
	WeeklyPercent             float64  `json:"weekly_percent"`
	WeeklyExhaustsBeforeReset bool     `json:"weekly_exhausts_before_reset"`
	WeeklyWindowKey           string   `json:"weekly_window_key"`
	SessionPercent            *float64 `json:"session_percent"`
	SessionState              string   `json:"session_state"`
	ObservedAgeSeconds        int64    `json:"observed_age_seconds"`
}

type jsonSuggestExcluded struct {
	Source  string `json:"source"`
	Account string `json:"account,omitempty"`
	Reason  string `json:"reason"`
}

// encodeSuggestJSON is the schema-1 --suggest --json document, pure like
// encodeJSON: now is passed in so tests need no clock.
func encodeSuggestJSON(sugg suggestion, now time.Time) jsonSuggestDoc {
	doc := jsonSuggestDoc{
		Schema:      1,
		GeneratedAt: jsonTime(now),
		Ranked:      make([]jsonSuggestSource, 0, len(sugg.ranked)),
		Excluded:    make([]jsonSuggestExcluded, 0, len(sugg.excluded)),
	}
	for i, r := range sugg.ranked {
		doc.Ranked = append(doc.Ranked, encodeSuggestSource(i+1, r))
	}
	if len(doc.Ranked) > 0 {
		pick := doc.Ranked[0]
		doc.Pick = &pick
	}
	for _, e := range sugg.excluded {
		doc.Excluded = append(doc.Excluded, jsonSuggestExcluded{Source: e.source, Account: e.account, Reason: e.reason})
	}
	return doc
}

func encodeSuggestSource(rank int, r rankedSource) jsonSuggestSource {
	encoded := jsonSuggestSource{
		Rank:                      rank,
		Source:                    r.source,
		Account:                   r.account,
		CredentialsPath:           r.credentialsPath,
		WeeklyPercent:             r.weeklyPercent,
		WeeklyExhaustsBeforeReset: r.weeklyExhaustsBeforeReset,
		WeeklyWindowKey:           r.weeklyWindowKey,
		SessionState:              string(r.sessionState),
		ObservedAgeSeconds:        r.observedAgeSeconds,
	}
	if r.sessionState != sessionAbsent {
		pct := r.sessionPercent
		encoded.SessionPercent = &pct
	}
	return encoded
}

// suggestExitCode is the exit code policy for --suggest: 0 once a
// suggestion was printed, 2 when nothing was routable -- matching the
// repo's existing exit-2 style for a flag-usage error, since either way
// there is nothing for a decision-grade caller to act on.
func suggestExitCode(sugg suggestion) int {
	if len(sugg.ranked) == 0 {
		return 2
	}
	return 0
}

// runSuggest fetches every source from the same list --json uses, and
// concurrently for the same reason renderJSON does: a status-line caller
// should not wait for one source before reading the other. It records
// history exactly like --json (recordHistory is !*noHistory), since a
// --suggest run is itself a fetch round the trend and future projections
// should see.
func runSuggest(history *History, fresh, recordHistory, jsonOutput bool) int {
	snaps := fetchAllSources(defaultSources(), fresh)
	recordJSONSnapshots(history, snaps, recordHistory)

	now := time.Now()
	sugg := buildSuggestion(snaps, history, now)

	if jsonOutput {
		encoder := json.NewEncoder(os.Stdout)
		encoder.SetIndent("", "  ")
		if err := encoder.Encode(encodeSuggestJSON(sugg, now)); err != nil {
			fmt.Fprintln(os.Stderr, "quotatop:", err)
			return 1
		}
	} else {
		fmt.Print(renderSuggestText(sugg))
	}
	return suggestExitCode(sugg)
}
