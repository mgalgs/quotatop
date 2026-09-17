# Theme fixtures for the reviewer

One file per theme, rendered with colour forced on (TrueColor, what
`CLICOLOR_FORCE=1` does in `--snapshot`), full layout at width 132.

`theme-0.txt` .. `theme-6.txt` are captures of the current code, one per
theme, named by theme index. They are rendered from a deterministic model
(fixed clock, host and readings) rather than a live `--snapshot`, so they
carry no machine-specific data and diff cleanly; view them with a colour
terminal, e.g. `cat -v` shows the SGR sequences, or:

    CLICOLOR_FORCE=1 go test -run TestThemeZeroMatchesPreChangeFixture -v .

The `theme-0-prechange[-help].txt` pair is the regression fixture: it was
captured from the pre-theme code, and
`TestThemeZeroMatchesPreChangeFixture` asserts that rendering theme 0 with
the new code reproduces it byte for byte, except the `t` footer hint and
the `?` help line this round deliberately added.
