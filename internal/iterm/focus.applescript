-- Focus the iTerm2 window/tab/pane owning a tty.
--
-- The `with timeout` is MANDATORY. Against a non-running app, `tell` both
-- LAUNCHES it and blocks for the default 120-second Apple event timeout, which
-- would freeze the TUI on Enter. Callers must also pre-check that iTerm2 is
-- running so this never triggers a launch.
--
-- Bind the topology with `set` before iterating: assignment preserves the
-- nested window>tab>session list shape, while evaluating the specifier inline
-- lets iTerm2 flatten it.
--
-- Select order is window, then tab, then session. `select window` is required
-- for a cross-window target — tab and session selects alone leave the current
-- window unchanged. A minimized window needs no special handling; `select`
-- de-miniaturizes it.
--
-- Do NOT use `index of tab`: the sdef declares the property but reading it
-- raises -1728. Use the iteration counter.
--
-- MEASURED on macOS 26.5: osascript does NOT strip the `--` separator; it
-- arrives as argv item 1. Reading `item 1` directly would set target to "--",
-- which matches no session, so Enter would silently never focus anything.
-- Drop a leading "--" explicitly; other builds may strip it.
on run argv
    set a to argv
    if (count of a) > 0 and (item 1 of a) is "--" then set a to rest of a
    if (count of a) < 1 then return "NOTFOUND"
    set target to item 1 of a
    with timeout of 3 seconds
        tell application "iTerm2"
            set wl to windows
            repeat with wi from 1 to (count of wl)
                set w to item wi of wl
                set grouped to tty of sessions of tabs of w
                repeat with ti from 1 to (count of grouped)
                    set row to item ti of grouped
                    repeat with si from 1 to (count of row)
                        if (item si of row) is target then
                            select w
                            select (tab ti of w)
                            select (session si of tab ti of w)
                            activate
                            return "OK"
                        end if
                    end repeat
                end repeat
            end repeat
        end tell
    end timeout
    return "NOTFOUND"
end run
