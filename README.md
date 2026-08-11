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

Two panes: sessions on the left, a scrollable preview of the selected
session on the right.

| Key | Action |
|-----|--------|
| j / k, ↓ / ↑ | move the selection |
| ⇥ / Tab | switch pane focus; j/k then move whichever pane is focused |
| ⏎ | jump iTerm2 to the tab/window/pane owning the selected session |
| r | refresh immediately |
| n | rename |
| q | quit |

⏎, r and q are unaffected by focus — they always act on the selected
session and the app as a whole, regardless of which pane j/k currently move.

**Renaming is ccs's own label, not Claude Code's.** Press `n` to edit the
selected session's name; ⏎ saves, Esc cancels, and saving an empty value
clears the override back to the auto-name. The override replaces the name
shown in the list; the preview pane has no name field of its own (its
metadata is status/version/tty/activity/cost), so the override appears
there only transiently, in the rename input itself, while editing.
Overrides live in ccs's own `$XDG_CONFIG_HOME/ccs/names.json` (falling back
to `~/.config/ccs/names.json` when that variable is unset), keyed by
session ID — they survive restarts and `claude -r`/`--resume`, but are
dropped on `/clear` since that starts a new session ID. A renamed session's
name is also pushed to its real iTerm2 tab title, and re-asserted on every
poll: Claude Code periodically rewrites the tab title on its own, so a
one-shot push would silently revert. A session with no tty (a
background/daemon process — the same case ⏎ reports as "no tab to focus")
is renamed in the list like any other, but never receives a tab-title push,
since there is no tab to write to. Clearing the override stops the
re-assertion but does not itself restore whatever title was showing before
— the tab keeps the last title it was given until something else (Claude
Code included) writes a new one.

**Live-only.** A session only appears once, and only while its process is
running. Sessions from the registry whose PID has died, or been reused by an
unrelated process, are filtered out — dead entries never show up as ghosts.

**The preview follows the newest activity.** The "Last assistant" half shows
whichever source — the main thread or a subagent — changed most recently, and
is labeled `⚙ <agent name>` whenever that source is a subagent that has
produced text; a just-spawned subagent shows a fresh `Activity:` beside the
main thread's last reply instead. The "Last human" half is always the main
thread's, since that's who the user is actually talking to. The `Activity:`
line in the pinned metadata reports how long ago that live source last
changed, reading `-` when nothing is on disk to time (a purged transcript
under a still-running process shows this too, with the preview body reading
`no preview (transcript missing)` instead of the plain `no preview`).

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

**`go test ./...` briefly moves your iTerm2 focus and puts it back.** A single
invocation is safe to run as-is, and `-p 1` is *not* required: only
`internal/iterm` touches focus, and its tests are already serial within a run.
Two *simultaneous* invocations — two agents on one repo, a file-watcher
alongside a manual run, two worktrees — would otherwise corrupt each other's
focus measurements, so they are serialized by an advisory flock at
`$TMPDIR/ccs-iterm-focus.lock`; the second one waits up to 60s rather than
reporting a false failure. `TestFocusActuallyMovesFocus` is a genuine
positive control: it focuses a *different* iTerm2 tab, asserts the frontmost
session actually changed, and restores your original focus. Every other test in
that package focuses the already-focused tab, where a Focus that does nothing at
all still returns "OK" — so this is the only test that proves ⏎ moves anything.
It stays.
