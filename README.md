# quotatop

Live quota for your AI coding agents, in one terminal window.

```
▌ QUOTATOP  host                                                                                              Wed 9:03:25 AM   ↻ 19s

╭─ CLAUDE ──────────────────────────────────────────────────────╮  ╭─ CODEX ──────────────────────────────────────────────── team ─╮
│ 5-hour                                                    31% │  │ 5-hour                                                    85% │
│ ██████████████████▌░░░░░░░░░░░░░░░░░░░░░░░░░░░░░░░░░░░░░░░░░░ │  │ ███████████████████████████████████████████████████▌░░░░░░░░░ │
│ resets in 36m 34s                                             │  │ resets in 2h 07m                                              │
│                                                               │  │                                                               │
│ Weekly                                                    68% │  │ Weekly                                                    49% │
│ █████████████████████████████████████████▌░░░░░░░░░░░░░░░░░░░ │  │ █████████████████████████████▌░░░░░░░░░░░░░░░░░░░░░░░░░░░░░░░ │
│ in 4d 10h · +1.1%/h avg → full in 1d 4h (3d 6h short)         │  │ in 5d 11h · +1.4%/h avg → full in 1d 13h (3d 21h short)       │
│                                                               │  ╰─ reported 3s ago ───────────────────────────── local · extra ─╯
│ Weekly · Fable                                            28% │                                                                   
│ █████████████████░░░░░░░░░░░░░░░░░░░░░░░░░░░░░░░░░░░░░░░░░░░░ │                                                                   
│ in 4d 10h · +0.5%/h avg → ~77% at reset (2d 2h spare)         │                                                                   
╰─ fetched 31s ago ─────────────────────── account · 10m cache ─╯                                                                   

r refresh · R force-fresh · ? keys · q quit                                                                tightest codex 5-hour 85%
```

(Shown without colour. In a colour terminal each gauge is a green-to-red
gradient, the unfilled part is the same gradient darkened, and a bar creeping
into the red end reads at a glance without looking at the number.)

## Running it

```bash
quotatop
```

Give it its own tmux window and leave it there.

| Key | Does |
|-----|------|
| `r` | refresh all sources now |
| `R` | refresh Claude past its 10-minute cache (a real API call) |
| `l` | cycle layout: full, compact, vertical (the choice is remembered across runs) |
| `t` | cycle colour themes (the choice is remembered across runs) |
| `?` | toggle the key list and data-source notes |
| `q` / `esc` / `ctrl-c` | quit |

| Flag | Does |
|------|------|
| `--interval 20s` | how often to poll all sources |
| `--snapshot` | render one frame to stdout and exit — no TUI |
| `--json` | write one JSON document to stdout and exit — no TUI |
| `--width N` | width for `--snapshot` (0 detects the terminal, falls back to the widest layout) |
| `--height N` | height for `--snapshot` (0 = no height limit) |
| `--layout NAME` | layout for `--snapshot`: `full`, `compact` or `vertical` (default `full`) |
| `--theme N` | theme index for `--snapshot` (0-6); the t key is the interactive control |
| `--fresh` | bypass the Claude cache on the first read |
| `--no-history` | do not read or write the trend file |

`--snapshot` honours `NO_COLOR` and `CLICOLOR_FORCE`, so it is also the way to
check a rendering change without driving a terminal.

The detail line sheds its least important part rather than being cut off
mid-word — the absolute reset time goes first, then the reset-gap parenthetical
described below. The full reading needs roughly 110 columns in the side-by-side
layout; below that you still get the projection, just less of its context.

## What it reads

Directly, in this process — the binary is self-contained and shells out to
nothing:

- **Claude** — the OAuth token from `~/.claude/.credentials.json`, then a GET
  to the usage endpoint (`api.anthropic.com/api/oauth/usage`), cached for 10
  minutes in `~/.cache/quotatop/claude-quota.json`. The panel reports when the
  numbers were *observed* (from the cache stamp), not when it asked, so a
  cached reading cannot look fresher than it is.
- **Codex** — the local session logs, walked recursively from `$CODEX_HOME`
  (or `~/.codex`) plus `/sessions`, for the newest recorded rate limits. There
  is no server to ask: Codex only knows its limits when it reports them, which
  is why that panel can be minutes or hours stale and says so.

> **Both of these are undocumented and can change without notice.** Neither the
> OAuth usage endpoint nor the shape of the Codex session log is a public
> interface, and neither vendor owes this tool stability. If a panel starts
> reporting an error after an update, that is the most likely reason.

A source that fails turns into a red panel with the error in it; the other
panel keeps running.

## Configuration

Every setting is an environment variable, and the config file supplies defaults
for the same names — so there is one list to learn, and anything added later
works in both places. The environment always wins over the file.

The file is read from the first of:

1. `$QUOTATOP_CONFIG`
2. `$XDG_CONFIG_HOME/quotatop/config`
3. `~/.config/quotatop/config`

Format is `KEY=VALUE`, one per line. `#` starts a comment, blank lines are
skipped, a value may contain `=`, and a value wrapped in matching quotes is
unquoted. A leading `~/` expands to your home directory. Nothing else is
interpreted — no escapes, no variable expansion, no command substitution. A
missing or malformed file is not an error: a monitor has to start.

```ini
# ~/.config/quotatop/config
QUOTATOP_CODEX_ROOTS = ~/work/agent-runs/*/codex-sessions:~/.codex-archive
```

| Setting | Does |
|---------|------|
| `QUOTATOP_CODEX_ROOTS` | extra Codex session roots (see below) |
| `QUOTATOP_CLAUDE_CREDENTIALS` | override the path to `.credentials.json` |
| `QUOTATOP_CLAUDE_ACCOUNT_<label>` | add a Claude panel that reads `<label>`'s own credentials file (see below) |
| `QUOTATOP_CODEX_ACCOUNT_<label>` | add a Codex panel that scans `<label>`'s own session roots (see below) |
| `QUOTATOP_HISTORY` | override the trend file's location |
| `QUOTATOP_STATE` | override the persisted-preferences file's location (environment only — the config file does not supply it) |
| `QUOTATOP_CONFIG` | override the config file's location |

The theme and layout picked with `t` and `l` are remembered across runs in
`$XDG_STATE_HOME/quotatop/state.json` (or
`~/.local/state/quotatop/state.json` when `XDG_STATE_HOME` is unset),
overridden by `QUOTATOP_STATE`. An explicit `--theme` or `--layout` flag
beats the remembered value for that one `--snapshot` frame without changing
what is remembered.

`QUOTATOP_CODEX_ROOTS` is a `:`-separated list of paths, each expanded as a
shell-style glob, each match walked like the default root. It exists so
sessions that only live outside `~/.codex` are still found — containerised or
sandboxed runs, or an archive of older logs. A leading `~/` is expanded in each
element, not just the first, so a home-relative second root works. A pattern
matching nothing is ignored.

### Multiple accounts

Declaring one or more `QUOTATOP_CLAUDE_ACCOUNT_<label>` variables (config file
or environment, `<label>` is any name you pick) replaces the single unnamed
Claude panel with one panel per label, sorted by label in ascending byte
order; `QUOTATOP_CLAUDE_CREDENTIALS` is not consulted once any are declared.
Each value is the path to that account's own `.credentials.json`.

```ini
QUOTATOP_CLAUDE_ACCOUNT_work = ~/work/.claude/.credentials.json
QUOTATOP_CLAUDE_ACCOUNT_personal = ~/.claude/.credentials.json
```

`QUOTATOP_CODEX_ACCOUNT_<label>` does the same for Codex: each value is a
`QUOTATOP_CODEX_ROOTS`-style glob list naming that account's session roots.
Declaring any `QUOTATOP_CODEX_ACCOUNT_` replaces the single unnamed Codex
panel entirely — the default `~/.codex`/`$CODEX_HOME` root and
`QUOTATOP_CODEX_ROOTS` are both dropped, so a value here has to name
everywhere that account's sessions live, or the panel finds nothing.

A labelled panel's title gains `· <label>`, and its `--json` entry gains an
`account` field carrying the label — see below.

Setting a label's environment variable to the empty string disables it even
when the config file declares it — useful for turning an account off on one
machine without editing the shared config file. This is unlike plain settings
such as `QUOTATOP_CODEX_ROOTS`, where an empty environment variable counts as
unset and falls back to the config file instead of disabling anything.

## Trend and burn rate

Every percentage change is appended to `~/.cache/quotatop/history.jsonl`
(override with `QUOTATOP_HISTORY`, disable with `--no-history`). That file is
what makes the sparkline and the burn rate work on the first frame after a
restart instead of an hour later.

The rate comes from one of two models. Long windows (24h or more) are projected
from their own elapsed pace — the percentage accrued since an anchor, divided
by the hours since that anchor — because a weekly window's elapsed time
already contains the nights and days away from the keyboard, and extrapolating
a working-hours slope across mostly-sleep time overstates the burn; these are
labelled `avg`. The anchor is the first sample after history last saw the
window at zero, so the days before it don't get charged against the
post-anchor slope; it falls back to the window's own open (zero, by
definition) when history does not reach back that far, and also when the
window has been open 12h or more but that first sample is too recent to trust
on its own. Shorter windows keep the live slope, fitted over the samples since
the window last reset — a reset is a sharp drop, and averaging across one
would report a meaningless negative burn.

Either way the result is `→ ~38% at reset`, or a red `→ full in 1d 7h` when the
window will not survive the pace. Where there is a reset deadline to measure
against, the forecast also carries the gap between the two:

- `→ full in 2d 6h (2d 14h short)` — you run out two and a half days *before*
  the window resets.
- `→ ~72% at reset (1d 2h spare)` — the pace would only reach 100% a day after
  the reset arrives, so it never gets there.

`short` therefore only ever appears on a red line and `spare` only on a
survivable one; both are computed from the same crossing time as the headline
and cannot disagree with it. The parenthetical is dropped when the gap is
bigger than the window's own length — several more windows would have to pass
before the pace came in with room to spare, which is not a useful reading.
In practice this only ever suppresses `spare`: a `short` gap is bounded by how
much of the window remains, which is always within one window length, so it
always shows. On a weekly window a `→ ~11% at reset` with no `spare` alongside
it just means the gap was that large.

Rates are a prompt to look, not a forecast.

## JSON output

`--json` writes one document to stdout and exits, for status lines, hooks and
anything else that wants the numbers without a terminal:

```bash
quotatop --json | jq '.sources[] | select(.id=="claude") | .windows[]
                      | select(.key=="weekly_all") | .percent'
```

The document is versioned (`"schema": 1`) and stable. Three rules for consumers:

- **`sources` can hold more than one entry with the same `source`** — one
  Claude or Codex account, per `QUOTATOP_CLAUDE_ACCOUNT_<label>` /
  `QUOTATOP_CODEX_ACCOUNT_<label>` (see Configuration above); `select(.source==
  "claude")` then matches all of them at once. Disambiguate with `account`
  (the label; the key is omitted entirely when unconfigured, not an empty
  string) or match `id` instead, which is `source` alone with no account
  configured and `source/account` once one is — `claude`, or `claude/work`
  and `claude/personal`.
- **Iterate windows and match on `key`; never index by position.** A source can
  gain or lose a window — `weekly_scoped` only exists while a model-scoped
  weekly bar is active.
- **`projection` is always an object, never null.** Check `valid` before
  reading the rest of it.

A window's `expired` (bool, omitted when false) means the window has certainly
reset since this percentage was observed — either the reading outlived the
window's own length, or the window's reported reset time has already passed.
`percent` still carries the last known number for reference, but it describes
a window that is gone; `projection.valid` is always `false` when `expired` is
`true`, since a forecast derived from a discarded reading is worse than no
forecast. A consumer that cares about the *current* state should skip a
window's `percent` when `expired` is set rather than treat it as live.

This matters most for Codex, which has no server to poll: a reading is only as
fresh as the newest session log that recorded one, so five idle hours are
enough to make the 5-hour window's number describe a window that no longer
exists. It is per-window, not per-source — one observation time is shared by
every window of a source, but they have different lengths, so a ten-hour-old
Codex reading expires the 5-hour window while the weekly one stays valid.

A source's `limit_reached` (string, omitted when empty) carries the reason
the account is refusing work — for example
`workspace_member_usage_limit_reached` — when the source itself has reported
a block, independent of any single window's percentage.

A Claude source's `credentials_path` (string, always present, never
omitted) is the absolute path this source reads its OAuth token from — the
matching `QUOTATOP_CLAUDE_ACCOUNT_<label>` value, `QUOTATOP_CLAUDE_CREDENTIALS`,
or the `~/.claude/.credentials.json` default, whichever applied. It is set
regardless of whether the fetch succeeded, so an errored source can still be
identified by which account's file it tried to read. Codex has no single
credentials file, so its `credentials_path` is always `""`; an unlabeled
Claude source falls back to `""` too, in the one case none of the above can
resolve it — no `QUOTATOP_CLAUDE_ACCOUNT_<label>` or `QUOTATOP_CLAUDE_CREDENTIALS`
override, and the home directory itself can't be determined. A consumer that
manages several Claude accounts can use this to route a separate action —
launching a process, say — at the same credentials file this reading came
from.

Window keys are `session`, `weekly_all` and `weekly_scoped` for Claude, and
`primary` and `secondary` for Codex. Reset times come with a precomputed
`resets_in_seconds`, and readings with an `observed_age_seconds`, so a shell
consumer never has to parse a timestamp. A failing source reports its `error`
in-band and the other source is still present, so the document is always
complete.

The projection carries `full_at` (when the pace reaches 100%, reset or no
reset), `gap_seconds` (negative when the window empties before it resets — the
`short` case above, positive for `spare`) and `exhausts_before_reset`.

`--json` records a trend sample like the TUI does, so a status line refreshing
on a timer keeps the history useful. Writers serialize on a lock file beside
the history, and a compaction always re-reads under that lock, so several
processes writing at once cannot drop each other's samples. Pass `--no-history`
to read and write nothing.

## Platform support

**Linux is what this is tested on.** The Claude reader expects the OAuth token
in `~/.claude/.credentials.json`.

**macOS is not supported yet.** Claude Code stores that credential in the
Keychain there, not in a file, so the Claude panel will report an error. Two
ways forward, and PRs are welcome for either:

- Export the token to a JSON file of the same shape and point
  `QUOTATOP_CLAUDE_CREDENTIALS` at it. That path exists precisely for this.
- Add a proper Keychain reader. This has deliberately *not* been shipped
  untested — there is no Mac here to verify it on, and a credential path that
  silently reads the wrong thing is worse than one that is plainly absent.

The Codex reader is filesystem-only and works anywhere Go does.

## Development

```bash
go test ./...        # layout invariants, history round-trip, projection maths
go build -o quotatop .
```

Dependencies are vendored, so both work with no network and no module cache.
After changing a dependency, re-run `go mod vendor` and commit `vendor/`.

The tests assert that every rendered panel is an exact rectangle of the width it
was asked for — misaligned borders are the classic TUI regression, and they are
easy to introduce with a glyph whose display width is not one cell.

## Licence

See [LICENSE](LICENSE).
