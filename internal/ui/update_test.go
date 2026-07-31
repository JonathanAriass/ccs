package ui

import (
	"testing"

	"github.com/JonathanAriass/ccs/internal/session"
	tea "github.com/charmbracelet/bubbletea"
)

func TestClamp(t *testing.T) {
	cases := []struct {
		name string
		i, n int
		want int
	}{
		{"empty list clamps to 0", 5, 0, 0},
		{"negative index clamps to 0", -1, 3, 0},
		{"index past the end clamps to n-1", 3, 3, 2},
		{"in-range index is untouched", 1, 3, 1},
		{"index exactly at n-1 is untouched", 2, 3, 2},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			if got := clamp(c.i, c.n); got != c.want {
				t.Errorf("clamp(%d,%d) = %d want %d", c.i, c.n, got, c.want)
			}
		})
	}
}

func modelWith(n int) Model {
	m := New()
	for i := 0; i < n; i++ {
		m.views = append(m.views, session.View{})
	}
	return m
}

func TestCursorMovementClamps(t *testing.T) {
	m := modelWith(3)
	down := tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'j'}}
	up := tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'k'}}

	for i := 0; i < 10; i++ {
		next, _ := m.Update(down)
		m = next.(Model)
	}
	if m.cursor != 2 {
		t.Errorf("cursor should clamp at 2, got %d", m.cursor)
	}
	for i := 0; i < 10; i++ {
		next, _ := m.Update(up)
		m = next.(Model)
	}
	if m.cursor != 0 {
		t.Errorf("cursor should clamp at 0, got %d", m.cursor)
	}
}

func TestCursorClampsWhenSessionsDisappear(t *testing.T) {
	// Sessions exit while the TUI is open. A shorter list must not leave the
	// cursor pointing past the end.
	m := modelWith(5)
	m.cursor = 4
	next, _ := m.Update(sessionsMsg{views: []session.View{{}, {}}})
	m = next.(Model)
	if m.cursor != 1 {
		t.Errorf("cursor = %d, want 1", m.cursor)
	}
	if m.selected() == nil {
		t.Error("selected() must not be nil after clamping")
	}
}

func TestSelectedOnEmptyList(t *testing.T) {
	m := modelWith(0)
	if m.selected() != nil {
		t.Error("selected() must be nil for an empty list")
	}
	// Enter on an empty list must be a no-op, not a panic.
	next, _ := m.Update(tea.KeyMsg{Type: tea.KeyEnter})
	if next == nil {
		t.Error("Update returned nil model")
	}
}

func TestQuitKey(t *testing.T) {
	m := modelWith(1)
	_, cmd := m.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'q'}})
	if cmd == nil {
		t.Fatal("q must return a command")
	}
	if _, ok := cmd().(tea.QuitMsg); !ok {
		t.Error("q must return tea.Quit")
	}
}

// --- sessionsMsg.err guard ---

func TestSessionsMsgErrSetsErrAndKeepsOldViews(t *testing.T) {
	// MOVE FIRST: seed real views and a real cursor so the assertions below
	// verify a CHANGE (err set, views untouched), not a coincidental zero
	// value. An error response must not wipe out what's currently on screen.
	m := modelWith(3)
	m.cursor = 2
	wantErr := errBoom
	next, cmd := m.Update(sessionsMsg{err: wantErr})
	m = next.(Model)
	if m.err != wantErr {
		t.Errorf("m.err = %v, want %v", m.err, wantErr)
	}
	if len(m.views) != 3 {
		t.Errorf("an error response must not clear the existing view list, got len %d", len(m.views))
	}
	if cmd != nil {
		t.Error("an error response must not schedule a cost lookup")
	}
}

func TestSessionsMsgSuccessClearsPriorErr(t *testing.T) {
	m := modelWith(0)
	m.err = errBoom // start in the error state so clearing it is a real change
	next, _ := m.Update(sessionsMsg{views: []session.View{{}}})
	m = next.(Model)
	if m.err != nil {
		t.Errorf("a successful poll must clear a previous error, got %v", m.err)
	}
}

// errBoom is a fixed sentinel so tests can compare by identity.
var errBoom = &testErr{"boom"}

type testErr struct{ s string }

func (e *testErr) Error() string { return e.s }

// --- costMsg guard: only apply a result that still matches the selection ---

func TestCostMsgAppliesWhenSessionIDMatchesSelection(t *testing.T) {
	m := modelWith(1)
	m.views[0].SessionID = "sess-a"
	next, _ := m.Update(costMsg{sessionID: "sess-a", tokens: 42, cost: 1.5})
	m = next.(Model)
	if m.views[0].Tokens != 42 || m.views[0].Cost != 1.5 {
		t.Errorf("matching costMsg was not applied: got tokens=%d cost=%v", m.views[0].Tokens, m.views[0].Cost)
	}
}

func TestCostMsgIgnoredWhenSessionIDIsStale(t *testing.T) {
	// The cursor has moved on to a different session since the lookup was
	// requested (e.g. the user pressed j while a slow first-time scan for the
	// PREVIOUS row was still in flight). The late reply must not clobber the
	// new selection's numbers.
	m := modelWith(1)
	m.views[0].SessionID = "sess-current"
	m.views[0].Tokens, m.views[0].Cost = 7, 0.7 // pre-existing real values
	next, _ := m.Update(costMsg{sessionID: "sess-stale", tokens: 999, cost: 99.9})
	m = next.(Model)
	if m.views[0].Tokens != 7 || m.views[0].Cost != 0.7 {
		t.Errorf("stale costMsg was applied: got tokens=%d cost=%v, want the untouched 7,0.7", m.views[0].Tokens, m.views[0].Cost)
	}
}

func TestCostMsgOnEmptyListDoesNotPanic(t *testing.T) {
	m := modelWith(0)
	next, _ := m.Update(costMsg{sessionID: "whatever"})
	if next == nil {
		t.Error("Update returned nil model")
	}
}

// --- cursor-changed guard on Up/Down: only fire a cost lookup on a real move ---

func TestUpDownOnlyFiresCostCmdWhenCursorActuallyMoves(t *testing.T) {
	m := modelWith(1) // a single row: cursor is already at both boundaries
	m.views[0].SessionID = "only-one"

	_, downCmd := m.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'j'}})
	if downCmd != nil {
		t.Error("pressing down at the bottom boundary (no movement) must not schedule a cost lookup")
	}
	_, upCmd := m.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'k'}})
	if upCmd != nil {
		t.Error("pressing up at the top boundary (no movement) must not schedule a cost lookup")
	}
}

func TestDownFiresCostCmdWhenCursorMoves(t *testing.T) {
	m := modelWith(2)
	m.views[0].SessionID = "row-0"
	m.views[1].SessionID = "row-1"

	next, cmd := m.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'j'}})
	m = next.(Model)
	if m.cursor != 1 {
		t.Fatalf("cursor = %d, want 1", m.cursor)
	}
	if cmd == nil {
		t.Fatal("moving the cursor to a new row must schedule a cost lookup")
	}
	msg, ok := cmd().(costMsg)
	if !ok {
		t.Fatalf("cmd() = %T, want costMsg", msg)
	}
	if msg.sessionID != "row-1" {
		t.Errorf("cost lookup was requested for %q, want the NEWLY selected row-1", msg.sessionID)
	}
}

// --- Focus key: re-resolves the tty rather than trusting the polled value ---

func TestFocusOnEmptyListIsNoop(t *testing.T) {
	m := modelWith(0)
	next, cmd := m.Update(tea.KeyMsg{Type: tea.KeyEnter})
	m = next.(Model)
	if cmd != nil {
		t.Error("Enter on an empty list must not schedule a command")
	}
	if m.status != "" {
		t.Errorf("Enter on an empty list must not set a status, got %q", m.status)
	}
}

func TestFocusIgnoresStalePolledTTYForAnUnknownPID(t *testing.T) {
	// v.TTY is a stale/bogus value from a previous poll ("ttys999" was never
	// resolved for this PID — it's deliberately wrong). PID 999999999 will
	// not appear in a real process snapshot. handleKey's contract is to
	// re-resolve via session.Snapshot()+OutermostTTY, NEVER v.TTY directly —
	// so with the fix, resolution correctly finds no live ancestor and
	// reports "background session". A mutant that used v.TTY directly would
	// instead attempt iterm.Focus("ttys999") and report a DIFFERENT status
	// ("could not focus: ..."), which is exactly what distinguishes the two:
	// verified by temporarily changing handleKey to use v.TTY and observing
	// this test fail with the wrong status text.
	m := modelWith(1)
	m.views[0].PID = 999999999
	m.views[0].TTY = "ttys999"

	next, _ := m.Update(tea.KeyMsg{Type: tea.KeyEnter})
	m = next.(Model)
	want := "background session — no tab to focus"
	if m.status != want {
		t.Errorf("status = %q, want %q (stale v.TTY must not have been trusted)", m.status, want)
	}
}
