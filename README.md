# ccs — Claude Code session viewer

A keyboard-driven terminal UI listing every live Claude Code session on the
machine, with a preview of where each one is at.

## Install

    go install github.com/JonathanAriass/ccs@latest

Or build locally:

    go build -o ccs . && cp ccs ~/.local/bin/

## Use

Run `ccs` anywhere. It reads the session registry at `~/.claude/sessions`,
filters it down to sessions whose process is still actually alive, and lists
them sorted by urgency: `waiting` first, then `busy`, then `shell`, then
`idle`. The list re-polls every 2 seconds.

Two panes: sessions on the left, a preview of the selected session on the
right.

| Key | Action |
|-----|--------|
| j / k, ↓ / ↑ | move the selection |
| ⏎ | jump iTerm2 to the tab/window/pane owning the selected session |
| r | refresh immediately |
| q | quit |

**Live-only.** A session only appears once, and only while its process is
running. Sessions from the registry whose PID has died, or been reused by an
unrelated process, are filtered out — dead entries never show up as ghosts.

**Cost is main-thread-only.** The Tokens/Cost figures in the preview pane
cover the session's main conversation thread — they do not include any
subagents it may have spawned. Cost is computed lazily, only for whichever
row is currently selected: a full transcript scan is too expensive (hundreds
of milliseconds on a large transcript) to run for every session on every
2-second poll, so it is deliberately left out of the list view and only ever
paid for the one row you're looking at.

**Focus requires iTerm2.** Pressing ⏎ shells out to iTerm2 via AppleScript to
select the window/tab/pane whose tty matches the session and bring it to the
front. On any other terminal app, or if iTerm2 isn't running, this fails
silently with a status message ("could not focus" / "background session — no
tab to focus") rather than doing nothing.

## Tests

    go build ./... && go vet ./... && gofmt -l . && go test ./... -race -count=1

**`go test ./...` briefly steals your window focus, so it is not safe to run
unattended alongside other work.** `TestFocusActuallyMovesFocus` is a genuine
positive control: it focuses a *different* iTerm2 tab, asserts the frontmost
session actually changed, and restores your original focus. Every other test in
that package focuses the already-focused tab, where a Focus that does nothing at
all still returns "OK" — so this is the only test that proves ⏎ moves anything.
It stays.
