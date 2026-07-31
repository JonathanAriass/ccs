package ui

import (
	"github.com/JonathanAriass/ccs/internal/iterm"
	"github.com/JonathanAriass/ccs/internal/session"
	"github.com/charmbracelet/bubbles/key"
	tea "github.com/charmbracelet/bubbletea"
)

// clamp keeps i inside [0, n).
func clamp(i, n int) int {
	if n <= 0 {
		return 0
	}
	if i < 0 {
		return 0
	}
	if i >= n {
		return n - 1
	}
	return i
}

func (m Model) Update(msg tea.Msg) (tea.Model, tea.Cmd) {
	switch msg := msg.(type) {
	case tea.WindowSizeMsg:
		m.width, m.height = msg.Width, msg.Height
		return m, nil

	case tickMsg:
		return m, tea.Batch(pollCmd(), tickCmd())

	case sessionsMsg:
		if msg.err != nil {
			m.err = msg.err
			return m, nil
		}
		m.err = nil
		m.views = msg.views
		// Sessions exit while the TUI is open, so the cursor can end up past
		// the end of a shorter list. Clamp rather than panic.
		m.cursor = clamp(m.cursor, len(m.views))
		// Refresh the selected row's cost every poll too, not just on cursor
		// move: a busy session's transcript keeps growing, so its cost should
		// keep climbing on screen without the user having to nudge the cursor.
		return m, m.costCmdForSelected()

	case costMsg:
		// The selection can move on between the request going out and the
		// reply coming back (the request is an async tea.Cmd; the user is
		// free to press j/k while it is in flight). Only apply a result that
		// still matches what's currently selected — otherwise a slow lookup
		// for a row the user has already left could overwrite the NEW
		// selection's numbers with a stale value for the old one.
		if sel := m.selected(); sel != nil && sel.SessionID == msg.sessionID {
			m.views[m.cursor].Tokens = msg.tokens
			m.views[m.cursor].Cost = msg.cost
		}
		return m, nil

	case tea.KeyMsg:
		return m.handleKey(msg)
	}
	return m, nil
}

func (m Model) handleKey(msg tea.KeyMsg) (tea.Model, tea.Cmd) {
	switch {
	case key.Matches(msg, m.keys.Quit):
		return m, tea.Quit

	case key.Matches(msg, m.keys.Up):
		prev := m.cursor
		m.cursor = clamp(m.cursor-1, len(m.views))
		if m.cursor != prev {
			return m, m.costCmdForSelected()
		}
		return m, nil

	case key.Matches(msg, m.keys.Down):
		prev := m.cursor
		m.cursor = clamp(m.cursor+1, len(m.views))
		if m.cursor != prev {
			return m, m.costCmdForSelected()
		}
		return m, nil

	case key.Matches(msg, m.keys.Refresh):
		m.status = ""
		return m, pollCmd()

	case key.Matches(msg, m.keys.Focus):
		v := m.selected()
		if v == nil {
			return m, nil
		}
		// Re-resolve the tty NOW rather than trusting the polled value: macOS
		// recycles tty numbers, so a mapping captured seconds ago can point at
		// a completely unrelated tab.
		procs, err := session.Snapshot()
		if err != nil {
			m.status = "could not read process table"
			return m, nil
		}
		tty := session.OutermostTTY(v.PID, procs)
		if tty == "" {
			m.status = "background session — no tab to focus"
			return m, nil
		}
		if err := iterm.Focus(tty); err != nil {
			m.status = "could not focus: " + err.Error()
			return m, nil
		}
		m.status = ""
		return m, nil
	}
	return m, nil
}
