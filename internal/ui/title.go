package ui

import (
	"os"
	"path/filepath"

	"github.com/JonathanAriass/ccs/internal/session"
	tea "github.com/charmbracelet/bubbletea"
)

// devDir exists so tests can aim title writes at a temp dir instead of /dev.
var devDir = "/dev"

// titleSeq builds the OSC 0 (icon+window title) sequence for a name. The name
// is sanitized first: a title is transcript-adjacent user text headed for a
// terminal, exactly the injection surface the notification work closed —
// sanitize strips the ESC and BEL bytes that could terminate or nest sequences.
func titleSeq(name string) string {
	return "\x1b]0;" + flattenToRow(sanitize(name)) + "\a"
}

// validTTY accepts ttys+digits only — the containment rule shared with
// cc-notify's -execute guard. Anything else is never opened.
func validTTY(name string) bool {
	if len(name) <= 4 || name[:4] != "ttys" {
		return false
	}
	for _, r := range name[4:] {
		if r < '0' || r > '9' {
			return false
		}
	}
	return true
}

// pushTitlesCmd re-asserts custom titles on every renamed session's tab.
// Claude Code periodically rewrites terminal titles, so a one-shot push would
// silently revert — this runs on each poll, and clearing a name stops it.
// Writes are best-effort: a vanished or unwritable tty must never surface as
// an error, matching how tab colors behave.
func pushTitlesCmd(views []session.View, names map[string]string) tea.Cmd {
	type target struct{ dev, seq string }
	var ts []target
	for _, v := range views {
		name := names[v.SessionID]
		if name == "" || !validTTY(v.TTY) {
			continue
		}
		ts = append(ts, target{filepath.Join(devDir, v.TTY), titleSeq(name)})
	}
	if len(ts) == 0 {
		return nil
	}
	return func() tea.Msg {
		for _, t := range ts {
			f, err := os.OpenFile(t.dev, os.O_WRONLY|os.O_APPEND, 0)
			if err != nil {
				continue
			}
			f.WriteString(t.seq)
			f.Close()
		}
		return nil
	}
}
