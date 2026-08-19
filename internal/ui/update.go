package ui

import (
	"strings"

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

// reconcile merges a freshly polled list into the one currently on screen,
// returning the list to display and where the cursor belongs in it.
//
// Everything here keys off SessionID rather than list position, because a poll
// RE-SORTS: sortViews puts "waiting" on top, so a session that starts waiting
// jumps to index 0 and shifts every session below it down one. Two things have
// to survive that.
//
//   - THE SELECTION. Re-anchoring the cursor by index means the highlight slides
//     onto whatever session took over that slot. Pressing ⏎ then focuses the
//     wrong iTerm2 tab — the single action this tool exists to perform. And it
//     is not a rare race: sessions changing status and floating to the top is
//     the tool's whole premise, so the bug fires on exactly the events the user
//     is watching for.
//
//   - TOKENS AND COST. Collect deliberately does not compute cost (it is a full
//     transcript scan), so every polled View arrives with Tokens/Cost zeroed.
//     Assigning the new slice wholesale therefore threw away whatever the last
//     costMsg had written, and the preview pane flickered to "$0.00" every two
//     seconds until the next lookup came back.
//
// When the selected session is no longer in the new list — it exited — the
// cursor falls back to the old index, clamped into range.
//
// A View with an empty SessionID has no identity to match on, so it is skipped
// by both halves rather than silently aliasing every other identity-less view.
// Load does not require the field, so a malformed registry entry can produce
// one; test fixtures produce them routinely.
func reconcile(old, next []session.View, cursor int) ([]session.View, int) {
	prev := make(map[string]session.View, len(old))
	for _, v := range old {
		if v.SessionID != "" {
			prev[v.SessionID] = v
		}
	}

	selID := ""
	if cursor >= 0 && cursor < len(old) {
		selID = old[cursor].SessionID
	}

	found := -1
	for i := range next {
		if p, ok := prev[next[i].SessionID]; ok {
			next[i].Tokens, next[i].Cost = p.Tokens, p.Cost
		}
		if selID != "" && next[i].SessionID == selID {
			found = i
		}
	}
	if found < 0 {
		found = clamp(cursor, len(next))
	}
	return next, found
}

// syncPreview resizes and refills the preview viewport from the current
// selection and terminal size.
//
// This lives in Update, not View, and that is not a style preference: View has
// a value receiver, so anything it writes to m.preview is discarded when the
// frame returns. The live model would keep Height 0 and no content, and
// LineDown on a zero-height empty viewport returns without moving.
func (m *Model) syncPreview() {
	g := layoutGeom(m.layout, len(m.views), m.width, m.height)
	inner := paneInnerWidth(g.PrevW)
	m.preview.Width = inner
	m.preview.Height = previewBodyHeight(paneInnerHeight(g.PrevH), previewMetadataLines)

	v := m.selected()
	switch {
	case v == nil:
		m.preview.SetContent("")
	case !v.HasPreview:
		if v.ActivityAt.IsZero() {
			m.preview.SetContent("  no preview (transcript missing)")
		} else {
			m.preview.SetContent("  no preview")
		}
	default:
		var body strings.Builder
		body.WriteString(labelStyle.Render("Last human:") + "\n")
		body.WriteString(wrapToWidth(sanitize(v.LastHuman), inner))
		asstLabel := "Last assistant:"
		if v.LiveAgent != "" {
			// The live text is a subagent's, not the main thread's — say so, or
			// a stale main answer and a live agent answer are indistinguishable.
			// LiveAgent is transcript-PATH-derived (filepath.Base of a subagent
			// file), not transcript CONTENT — but a macOS filename can carry any
			// byte but "/" and NUL, so it goes through sanitize() same as every
			// other transcript-derived string in the frame.
			asstLabel = "Last assistant (⚙ " + sanitize(v.LiveAgent) + "):"
		}
		body.WriteString("\n\n" + labelStyle.Render(asstLabel) + "\n")
		body.WriteString(wrapToWidth(sanitize(v.LastAssistant), inner))
		m.preview.SetContent(body.String())
	}
}

func (m Model) Update(msg tea.Msg) (tea.Model, tea.Cmd) {
	switch msg := msg.(type) {
	case tea.WindowSizeMsg:
		m.width, m.height = msg.Width, msg.Height
		m.syncPreview()
		m.exitRenameIfInputCannotBeDrawn()
		return m, nil

	case tickMsg:
		return m, tea.Batch(pollCmd(), tickCmd())

	case sessionsMsg:
		if msg.err != nil {
			m.err = msg.err
			return m, nil
		}
		m.err = nil
		// A transient message (a failed focus) must not outlive the state it
		// described. Left uncleared it sits there indefinitely AND changes the
		// frame height for as long as it does.
		m.status = ""
		prevSelID := ""
		if sel := m.selected(); sel != nil {
			prevSelID = sel.SessionID
		}
		// Carry the selection and the cost figures across the re-sort, and clamp
		// the cursor when its session has exited. See reconcile.
		m.views, m.cursor = reconcile(m.views, msg.views, m.cursor)
		// The selected session's last message can change between polls; refill
		// so the pane shows the current exchange rather than the one that was
		// current when the cursor last moved.
		m.syncPreview()
		// Reset scroll ONLY when the poll changed WHICH session is selected —
		// e.g. the previously selected session exited and reconcile (see above)
		// landed the cursor on a different one. syncPreview's SetContent clamps
		// YOffset downward only, so without this the reader would be left
		// mid-scroll in a session they never chose to look at, unaware they
		// missed its start.
		//
		// This must stay conditional: the common poll is the SAME selected,
		// often-busy session's own content growing under a reading user.
		// Resetting unconditionally would yank that reader back to the top
		// every ~2s (pollInterval) — worse than the bug this fixes.
		if sel := m.selected(); sel != nil && sel.SessionID != prevSelID {
			m.preview.GotoTop()
		}
		m.exitRenameIfInputCannotBeDrawn()
		// Refresh the selected row's cost every poll too, not just on cursor
		// move: a busy session's transcript keeps growing, so its cost should
		// keep climbing on screen without the user having to nudge the cursor.
		return m, tea.Batch(m.costCmdForSelected(), pushTitlesCmd(m.views, m.names))

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
	// MUST be first: q is a bound key (Quit) outside this mode, and while
	// renaming it has to reach the input field instead of quitting ccs.
	if m.renaming {
		switch msg.Type {
		case tea.KeyCtrlC:
			// ctrl+c is the universal terminal interrupt, not a letter the
			// field needs (unlike q — see the comment above). A rename in
			// progress does not outrank it: quit exactly as it would outside
			// the mode, rather than swallowing it as a dead key.
			return m, tea.Quit
		case tea.KeyEsc:
			m.renaming = false
			m.nameInput.Blur()
			return m, nil
		case tea.KeyEnter:
			m.renaming = false
			m.nameInput.Blur()
			if m.names == nil {
				// Defensive: loadNames now guards its own nil-map return
				// (see names.go), but this keeps the Enter path safe even if
				// a future caller ever hands Model a nil names map directly.
				m.names = map[string]string{}
			}
			name := strings.TrimSpace(flattenToRow(sanitize(m.nameInput.Value())))
			if name == "" {
				delete(m.names, m.renameFor)
			} else {
				m.names[m.renameFor] = name
			}
			if err := saveNames(m.namesFile, m.names); err != nil {
				m.status = "rename won't persist: " + err.Error()
			}
			return m, pushTitlesCmd(m.views, m.names)
		}
		var cmd tea.Cmd
		m.nameInput, cmd = m.nameInput.Update(msg)
		return m, cmd
	}

	switch {
	case key.Matches(msg, m.keys.Quit):
		return m, tea.Quit

	case key.Matches(msg, m.keys.Cycle):
		if !m.previewVisible() {
			// No second pane to switch to — see previewVisible's doc comment.
			// Deliberately a no-op rather than, say, forcing m.focus to
			// focusList: leaving m.focus untouched means a later resize back
			// to a tall enough terminal restores the SAME pane the user had
			// focused before it disappeared, rather than always resetting to
			// the list.
			return m, nil
		}
		if m.focus == focusList {
			m.focus = focusPreview
		} else {
			m.focus = focusList
		}
		return m, nil

	case key.Matches(msg, m.keys.Up):
		if m.focus == focusPreview && m.previewVisible() {
			m.preview.LineUp(1)
			return m, nil
		}
		prev := m.cursor
		m.cursor = clamp(m.cursor-1, len(m.views))
		if m.cursor != prev {
			// The status line describes the row it was raised on ("background
			// session — no tab to focus"). Moving off that row makes it wrong,
			// so it goes with the selection.
			m.status = ""
			m.syncPreview()
			// New session, new content: start at the top rather than part-way
			// into a message whose beginning the reader has not seen.
			m.preview.GotoTop()
			return m, m.costCmdForSelected()
		}
		return m, nil

	case key.Matches(msg, m.keys.Down):
		if m.focus == focusPreview && m.previewVisible() {
			m.preview.LineDown(1)
			return m, nil
		}
		prev := m.cursor
		m.cursor = clamp(m.cursor+1, len(m.views))
		if m.cursor != prev {
			m.status = ""
			m.syncPreview()
			m.preview.GotoTop()
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

	case key.Matches(msg, m.keys.Rename):
		if !m.previewVisible() {
			// The rename input is drawn inside renderPreviewMetadata, which
			// View never calls at this height (see previewVisible's doc
			// comment) — entering the mode here would be an invisible modal:
			// nothing on screen shows it, yet it steals q and captures
			// keystrokes into a field nobody can see. Deliberately a no-op,
			// same discipline as Cycle just above.
			return m, nil
		}
		v := m.selected()
		// A View with an empty SessionID has no identity to key an override
		// on. reconcile (above) already refuses to alias identity-less views
		// to each other for exactly this reason; the rename path needs the
		// same discipline, or worse: m.names[""] would alias EVERY
		// identity-less row to the same override AND push the same title to
		// every one of their real ttys — a rename crossing session
		// boundaries onto other people's terminal tabs, not just a cosmetic
		// mix-up in the list.
		if v == nil || v.SessionID == "" {
			return m, nil
		}
		m.renaming = true
		m.renameFor = v.SessionID
		m.nameInput.SetValue(m.names[v.SessionID]) // prefill the OVERRIDE, not the auto-name: Enter-on-empty clears
		m.nameInput.Focus()
		return m, nil
	}
	return m, nil
}
