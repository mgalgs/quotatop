# Compact forecast-overlay captures for the reviewer

The compact layout squeezes each window onto one line and loses the detail
line's burn forecast; this round prints the forecast's headline inside the
gauge bar instead. These captures show how that looks, in colour.

One file per layout and width — `compact` and `compact-vertical` at 80 and
132 columns — rendered with colour forced on (TrueColor, what
`CLICOLOR_FORCE=1` does in `--snapshot`). As with `testdata/themes/`, they
are rendered from a deterministic model (fixed clock, host and readings, one
Claude account with two windows that carry projections, one failing codex
account) rather than a live `--snapshot`, so they carry no
machine-specific data and diff cleanly. View them in a colour terminal:
`cat -v` shows the SGR sequences, or:

    go test -run TestCompactForecastFixtures -v .

re-renders the same frame; regenerate the captures with

    QUOTATOP_UPDATE_COMPACT_FIXTURES=1 go test -run TestCompactForecastFixtures .

In `compact-80.txt` the "Weekly" bar carries `full in 2d 11h` over its
right-hand end: the characters over the dark track are light, and on
`compact-132.txt` the wider bars show the same text with more untouched bar
left of it. The "5-hour" window (no projection) and the "Weekly · Fable"
bar at 80 columns (forecast would swallow the bar) stay plain.
