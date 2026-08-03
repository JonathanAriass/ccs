package ui

import (
	"fmt"
	"strings"
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

// viewsWithIDs builds a polled list from SessionIDs, in the order given.
func viewsWithIDs(ids ...string) []session.View {
	out := make([]session.View, 0, len(ids))
	for _, id := range ids {
		out = append(out, session.View{Session: session.Session{SessionID: id}})
	}
	return out
}

// modelWithIDs seeds the on-screen list with identified sessions.
func modelWithIDs(ids ...string) Model {
	m := New()
	m.views = viewsWithIDs(ids...)
	return m
}

// --- the cursor must follow the SELECTED SESSION across a re-sorting poll ---

// TestCursorFollowsSelectedSessionAcrossAReorderingPoll is the regression test
// for a selection that drifted onto a neighbouring session every time the list
// re-sorted — and then sent ⏎ to the wrong iTerm2 tab, which is the one action
// this tool exists to perform.
//
// The fixture's ORDER CHANGES, and that is the whole test. A poll that returns
// the same list in the same order asserts a post-state that already held: the
// old index-clamping code passes it trivially. Sessions changing status and
// floating to the top is this tool's entire premise, so the reordering case is
// not an edge case, it is the main one.
func TestCursorFollowsSelectedSessionAcrossAReorderingPoll(t *testing.T) {
	m := modelWithIDs("A", "B", "C")
	m.cursor = 2 // the user has C selected

	// C flips to "waiting" and sortViews floats it to the top; A and B shift down.
	next, _ := m.Update(sessionsMsg{views: viewsWithIDs("C", "A", "B")})
	m = next.(Model)

	if m.selected() == nil {
		t.Fatal("selected() must not be nil after a poll")
	}
	if got := m.selected().SessionID; got != "C" {
		t.Errorf("selection drifted to %q; it must stay on the session the user selected (C)", got)
	}
	if m.cursor != 0 {
		t.Errorf("cursor = %d, want 0 — C's new position", m.cursor)
	}
}

// TestCursorFallsBackToClampWhenSelectedSessionExits covers the other branch:
// the selected session is genuinely gone, so there is no identity to follow and
// the old index-clamp is the right answer. The order still changes, so a
// mutation that matched by position rather than falling through would land
// somewhere else.
func TestCursorFallsBackToClampWhenSelectedSessionExits(t *testing.T) {
	m := modelWithIDs("A", "B", "C")
	m.cursor = 2 // C selected

	next, _ := m.Update(sessionsMsg{views: viewsWithIDs("B", "A")}) // C exited
	m = next.(Model)

	if m.cursor != 1 {
		t.Fatalf("cursor = %d, want 1 (old index 2 clamped into a 2-element list)", m.cursor)
	}
	if got := m.selected().SessionID; got != "A" {
		t.Errorf("selected %q, want A — the clamped index of the new list", got)
	}
}

// TestReconcileDoesNotMatchOnAnEmptySessionID pins both halves of reconcile's
// "" guard directly, which no Update-level fixture can: an empty SessionID is
// the absence of an identity, not an identity that several views share.
//
// Without the guard on the lookup map, every unidentified view would inherit one
// arbitrary other unidentified view's cost; without it on the selection, an
// unidentified selection would "follow" the first unidentified row in the new
// list instead of falling through to the clamp.
func TestReconcileDoesNotMatchOnAnEmptySessionID(t *testing.T) {
	old := []session.View{{Session: session.Session{SessionID: ""}, Tokens: 500, Cost: 9.9}}
	next := viewsWithIDs("real", "")

	got, cursor := reconcile(old, next, 0)

	if got[1].Tokens != 0 || got[1].Cost != 0 {
		t.Errorf("an unidentified view inherited tokens=%d cost=%v from an unrelated unidentified view",
			got[1].Tokens, got[1].Cost)
	}
	if cursor != 0 {
		t.Errorf("cursor = %d, want 0 — an empty SessionID is not an identity to follow, so the clamp applies", cursor)
	}
}

// --- Tokens/Cost must survive a poll ---

// TestPollCarriesTokensAndCostForwardAcrossAReorder pins the fix for the
// preview pane flickering to "$0.00" every two seconds.
//
// session.Collect deliberately never computes cost (it is a full transcript
// scan), so every polled View arrives with Tokens and Cost zeroed — replacing
// the list wholesale threw away whatever the last costMsg had written.
//
// The fixture reorders for a second reason here: carrying the numbers forward
// BY INDEX would also make this pass if the order were stable, and would be
// just as wrong. Reordering is what proves the match is on SessionID.
func TestPollCarriesTokensAndCostForwardAcrossAReorder(t *testing.T) {
	m := modelWithIDs("A", "B")
	m.views[0].Tokens, m.views[0].Cost = 1234, 5.67
	m.views[1].Tokens, m.views[1].Cost = 99, 0.5

	// B floats above A, and "C" is a session seen for the first time.
	next, _ := m.Update(sessionsMsg{views: viewsWithIDs("B", "A", "C")})
	m = next.(Model)

	got := map[string]session.View{}
	for _, v := range m.views {
		got[v.SessionID] = v
	}
	if got["A"].Tokens != 1234 || got["A"].Cost != 5.67 {
		t.Errorf("A: tokens=%d cost=%v, want the carried-forward 1234/5.67", got["A"].Tokens, got["A"].Cost)
	}
	if got["B"].Tokens != 99 || got["B"].Cost != 0.5 {
		t.Errorf("B: tokens=%d cost=%v, want the carried-forward 99/0.5", got["B"].Tokens, got["B"].Cost)
	}
	if got["C"].Tokens != 0 || got["C"].Cost != 0 {
		t.Errorf("C: tokens=%d cost=%v, want 0/0 — a newly seen session has no prior figures to inherit",
			got["C"].Tokens, got["C"].Cost)
	}
}

// --- m.status is transient and must not outlive what it described ---

func TestPollClearsTransientStatus(t *testing.T) {
	// MOVE FIRST: seed a real status so clearing it is an observable change and
	// not the zero value already sitting there.
	m := modelWithIDs("A")
	m.status = "could not focus: iterm: no tab owns that tty"

	next, _ := m.Update(sessionsMsg{views: viewsWithIDs("A")})
	m = next.(Model)

	if m.status != "" {
		t.Errorf("a poll must clear the transient status, got %q", m.status)
	}
}

func TestCursorMovementClearsTransientStatus(t *testing.T) {
	m := modelWithIDs("A", "B")
	m.status = "background session — no tab to focus"

	next, _ := m.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'j'}})
	m = next.(Model)

	if m.cursor != 1 {
		t.Fatalf("cursor = %d, want 1 — the fixture must actually move", m.cursor)
	}
	if m.status != "" {
		t.Errorf("moving the selection must clear a status raised on the previous row, got %q", m.status)
	}
}

// TestBlockedCursorMovementKeepsStatus is the reject half: the message describes
// the CURRENT row, so a keypress that does not leave that row must not dismiss
// it. Without this, "clear the status on j/k" would be indistinguishable from
// "clear the status on any keypress".
func TestBlockedCursorMovementKeepsStatus(t *testing.T) {
	m := modelWithIDs("only-one") // already at both boundaries
	const want = "background session — no tab to focus"
	m.status = want

	next, _ := m.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'j'}})
	m = next.(Model)

	if m.status != want {
		t.Errorf("status = %q, want it kept (%q) — the selection never left the row it describes", m.status, want)
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

// previewHumanMarker / previewAssistantMarker are the tokens that identify
// WHOSE exchange the preview pane is showing.
//
// Every session in modelWithOverflowingPreview used to be handed the same
// strings.Repeat("word ", 400) for both fields, which made all of them
// byte-identical — and an identical fixture cannot answer the one question the
// preview pane exists to answer. Both syncPreview() calls on the cursor-move
// paths could be deleted with the whole suite green, while the running program
// showed the newly selected session's metadata (rendered live from
// m.selected()) above the PREVIOUS session's words (still sitting in
// m.preview), for up to a poll interval.
//
// The human marker leads the exchange, so it is on screen at YOffset 0 without
// scrolling. The two markers differ because the assistant's text is what a LIST
// ROW shows (lastMessage), so a single shared token could be matched in the
// list and mistaken for evidence about the preview.
func previewHumanMarker(i int) string     { return fmt.Sprintf("HUMAN-%d-MARKER", i) }
func previewAssistantMarker(i int) string { return fmt.Sprintf("ASSISTANT-%d-MARKER", i) }

// modelWithOverflowingPreview builds a model the way the program does — through
// Update — so the viewport's size and content come from production code rather
// than from the test.
//
// This matters more than it looks. A test that assigns m.preview.Height and
// calls SetContent itself passes whether or not anything in the real message
// flow ever sizes or fills that viewport. Sizing it in View (which has a value
// receiver, so its writes are discarded) leaves the live model at Height 0
// where LineDown is a no-op — and every hand-built test stays green.
//
// Every session's exchange is DISTINCT (see previewHumanMarker) and long enough
// to overflow the pane; several tests depend on both properties.
func modelWithOverflowingPreview(t *testing.T, n int) Model {
	t.Helper()
	views := make([]session.View, n)
	for i := range views {
		views[i] = session.View{
			Session:       session.Session{SessionID: fmt.Sprintf("s%d", i)},
			HasPreview:    true,
			LastHuman:     previewHumanMarker(i) + " " + strings.Repeat("word ", 400),
			LastAssistant: previewAssistantMarker(i) + " " + strings.Repeat("reply ", 400),
		}
	}
	m := New()
	next, _ := m.Update(tea.WindowSizeMsg{Width: 118, Height: 30})
	m = next.(Model)
	next, _ = m.Update(sessionsMsg{views: views})
	m = next.(Model)
	if n > 0 && m.preview.TotalLineCount() <= m.preview.Height {
		t.Fatalf("fixture does not overflow: %d content lines in a %d-row viewport",
			m.preview.TotalLineCount(), m.preview.Height)
	}
	return m
}

// previewShows reports whether the preview pane's scrolling body currently
// renders token. It reads m.preview.View() — the body alone — rather than the
// whole frame, because the list pane renders every session's last assistant
// message too and a whole-frame search would match a row instead.
func previewShows(m Model, token string) bool {
	return strings.Contains(visibleText(m.preview.View()), token)
}

// TestCursorMoveShowsTheNewlySelectedSessionsExchange pins the syncPreview()
// calls on BOTH cursor-move paths (update.go's Up and Down cases).
//
// The metadata block above the exchange is rendered live from m.selected() on
// every frame, but the exchange itself comes from m.preview, which only changes
// when something calls syncPreview(). Without the refill, j leaves the pane
// showing the new session's Status/Version/TTY/Tokens/Cost directly above the
// PREVIOUS session's words — a pane that describes two different sessions at
// once — until the next poll happens to fix it, up to pollInterval later.
//
// Asserting the new session's marker is present is only half of it: the pane
// would also "contain" it if it rendered every session's text. The old
// session's marker must be GONE.
func TestCursorMoveShowsTheNewlySelectedSessionsExchange(t *testing.T) {
	cases := []struct {
		name       string
		key        tea.KeyMsg
		start, end int
	}{
		{"down", tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'j'}}, 0, 1},
		{"up", tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'k'}}, 2, 1},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			m := modelWithOverflowingPreview(t, 3)
			m.cursor = c.start
			m.syncPreview() // the cursor was moved behind Update's back
			if !previewShows(m, previewHumanMarker(c.start)) {
				t.Fatalf("fixture: preview does not show session %d's exchange to begin with:\n%s",
					c.start, visibleText(m.preview.View()))
			}

			next, _ := m.Update(c.key)
			m = next.(Model)

			if m.cursor != c.end {
				t.Fatalf("cursor = %d, want %d — the selection did not actually change", m.cursor, c.end)
			}
			if !previewShows(m, previewHumanMarker(c.end)) {
				t.Errorf("after moving to session %d the preview does not show its exchange:\n%s",
					c.end, visibleText(m.preview.View()))
			}
			if previewShows(m, previewHumanMarker(c.start)) {
				t.Errorf("after moving off session %d the preview still shows its exchange — "+
					"the pane's metadata now describes session %d while its words belong to session %d:\n%s",
					c.start, c.end, c.start, visibleText(m.preview.View()))
			}
		})
	}
}

func TestUpdateSizesAndFillsTheViewport(t *testing.T) {
	// The guard for the architectural rule. Drives the model ONLY through
	// Update — assigns nothing — so it fails if sizing or SetContent lives in
	// View, where the writes are discarded.
	m := modelWithOverflowingPreview(t, 1)

	if m.preview.Height <= 0 {
		t.Fatalf("viewport Height = %d after a WindowSizeMsg; sizing never reached the live model",
			m.preview.Height)
	}
	if m.preview.Width <= 0 {
		t.Fatalf("viewport Width = %d after a WindowSizeMsg", m.preview.Width)
	}

	m.focus = focusPreview
	next, _ := m.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'j'}})
	m = next.(Model)
	if m.preview.YOffset == 0 {
		t.Error("j did not scroll a viewport sized and filled entirely through Update")
	}
}

// TestWindowResizeAlonePropagatesToTheViewport isolates the
// tea.WindowSizeMsg case's m.syncPreview() call from the one at the end of
// the sessionsMsg case. modelWithOverflowingPreview (and every other test
// using it) sends WindowSizeMsg then sessionsMsg, so sessionsMsg's own
// syncPreview() call independently re-sizes and re-fills the viewport —
// masking a missing call in the WindowSizeMsg case entirely. Confirmed by
// code review: deleting that call reddens nothing.
//
// A real resize with no poll in between is not a corner case: the 2s poll
// interval means dragging a terminal split, or a Neovim window resize via
// claudecode.nvim, routinely lands well inside that window. Without the
// call, m.preview keeps the OLD size's Height/Width until the next poll,
// which the reviewer observed rendering the preview pane one row past the
// list pane with its own bottom border overwritten by stale-height content.
func TestWindowResizeAlonePropagatesToTheViewport(t *testing.T) {
	m := New()
	next, _ := m.Update(tea.WindowSizeMsg{Width: 118, Height: 30})
	m = next.(Model)
	next, _ = m.Update(sessionsMsg{views: []session.View{{
		Session:    session.Session{SessionID: "s0"},
		HasPreview: true,
		LastHuman:  strings.Repeat("word ", 400),
	}}})
	m = next.(Model)
	firstHeight, firstWidth := m.preview.Height, m.preview.Width

	// Resize smaller, with NO sessionsMsg (no poll) in between.
	next, _ = m.Update(tea.WindowSizeMsg{Width: 80, Height: 16})
	m = next.(Model)

	wantHeight := previewBodyHeight(paneInnerHeight(bodyPaneHeight(16)), previewMetadataLines)
	if m.preview.Height != wantHeight {
		t.Errorf("viewport Height = %d after a resize with no intervening poll, want %d (was %d before the resize)",
			m.preview.Height, wantHeight, firstHeight)
	}
	_, wantWidth := paneWidths(80)
	wantWidth = paneInnerWidth(wantWidth)
	if m.preview.Width != wantWidth {
		t.Errorf("viewport Width = %d after a resize with no intervening poll, want %d (was %d before the resize)",
			m.preview.Width, wantWidth, firstWidth)
	}

	// Cross-check against the actual rendered frame. With a correct size, the
	// list and preview panes' bottom borders align on the same line — the
	// frame's raw text contains exactly two "╯" (bottom-right corners), one
	// per pane. With the stale (too-tall) viewport from before this bug fix,
	// the preview pane's own content overflows past where its border belongs
	// and lipgloss never emits that border row at all within the frame: only
	// the list pane's "╯" appears. Verified by hand against both frames (this
	// bug reproduced, then reverted) before writing this assertion.
	if got := strings.Count(m.View(), "╯"); got != 2 {
		t.Errorf("rendered frame has %d bottom-right pane corners (╯), want 2 — the preview pane's own border is missing:\n%s", got, m.View())
	}
}

func TestTabTogglesFocus(t *testing.T) {
	// Asserts focus CHANGED from a known starting state. Asserting "focus is
	// preview" after Tab would pass even with Tab deleted, whenever focus
	// already happened to be there — the post-state-already-held trap.
	m := modelWithOverflowingPreview(t, 3)
	if m.focus != focusList {
		t.Fatalf("fixture must start on the list, got %v", m.focus)
	}
	tab := tea.KeyMsg{Type: tea.KeyTab}

	next, _ := m.Update(tab)
	m = next.(Model)
	if m.focus != focusPreview {
		t.Fatalf("first Tab: focus = %v, want focusPreview", m.focus)
	}

	next, _ = m.Update(tab)
	m = next.(Model)
	if m.focus != focusList {
		t.Errorf("second Tab: focus = %v, want focusList (Tab must toggle, not set)", m.focus)
	}
}

// scrolledPreview presses j n times with the PREVIEW focused, restores whatever
// focus the caller had, and fails if the viewport did not actually move.
//
// A test that needs "the viewport is somewhere other than the top" has to get
// there through the real key path, and has to verify it got there: an offset of
// 0 silently turns every "did not scroll" assertion below into one that cannot
// fail, since LineUp clamps at 0.
func scrolledPreview(t *testing.T, m Model, n int) Model {
	t.Helper()
	focus := m.focus
	m.focus = focusPreview
	for i := 0; i < n; i++ {
		next, _ := m.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'j'}})
		m = next.(Model)
	}
	m.focus = focus
	if m.preview.YOffset == 0 {
		t.Fatalf("fixture must be scrolled away from the top after %d presses of j; YOffset is still 0", n)
	}
	return m
}

func TestJKRouteToTheFocusedPaneOnly(t *testing.T) {
	// Each case asserts BOTH halves: the focused pane moved AND the other did
	// not. Asserting only the first half passes for a `j` that moves neither.
	down := tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'j'}}
	up := tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'k'}}

	t.Run("list focused: cursor moves, viewport does not scroll", func(t *testing.T) {
		m := modelWithOverflowingPreview(t, 3)
		m.focus = focusList
		startOffset := m.preview.YOffset

		next, _ := m.Update(down)
		m = next.(Model)

		if m.cursor != 1 {
			t.Errorf("cursor = %d, want 1", m.cursor)
		}
		if m.preview.YOffset != startOffset {
			t.Errorf("viewport scrolled to %d while the list had focus", m.preview.YOffset)
		}
	})

	// list-focused-at-the-boundary: the subtest above is blind to an
	// unconditional m.preview.LineDown(1) run alongside the cursor move —
	// the move changes the selection, which triggers syncPreview()+GotoTop(),
	// and GotoTop resets YOffset to 0 regardless of what LineDown just did,
	// leaving the final YOffset equal to startOffset (also 0) either way.
	// Starting the cursor already at the boundary makes the selection change
	// a no-op, so nothing resets the viewport afterward — a stray LineDown
	// here has nowhere to hide.
	t.Run("list focused at the boundary (down): viewport still does not scroll", func(t *testing.T) {
		m := modelWithOverflowingPreview(t, 3)
		m.focus = focusList
		m.cursor = len(m.views) - 1 // already at the bottom; j must not move it
		startOffset := m.preview.YOffset

		next, _ := m.Update(down)
		m = next.(Model)

		if m.cursor != len(m.views)-1 {
			t.Fatalf("cursor = %d, want unchanged at the boundary %d — fixture must actually be blocked", m.cursor, len(m.views)-1)
		}
		if m.preview.YOffset != startOffset {
			t.Errorf("viewport scrolled to %d while the list had focus (blocked cursor)", m.preview.YOffset)
		}
	})

	// The k analog of the FIRST subtest, and it inherits that subtest's blind
	// spot rather than pretending otherwise: a real selection change calls
	// GotoTop(), so the viewport lands at 0 whatever a stray LineUp did on the
	// way. What this can still pin is that the reset happens at all, which it
	// does from a NONZERO starting offset — asserting "unchanged" from an
	// offset of 0, as this subtest used to, is a post-state that already held
	// and cannot fail for any reason. The stray-LineUp mutant is caught by the
	// boundary subtest below.
	t.Run("list focused: cursor moves (up), and the viewport is reset", func(t *testing.T) {
		m := modelWithOverflowingPreview(t, 3)
		m.cursor = 1 // nonzero, so k has somewhere to move to
		m = scrolledPreview(t, m, 3)
		m.focus = focusList

		next, _ := m.Update(up)
		m = next.(Model)

		if m.cursor != 0 {
			t.Errorf("cursor = %d, want 0", m.cursor)
		}
		if m.preview.YOffset != 0 {
			t.Errorf("YOffset = %d after k changed the selection, want 0", m.preview.YOffset)
		}
	})

	// Up-direction analog of the boundary subtest above — and the direction
	// that needs more care than a paste. LineDown can move a viewport off
	// offset 0, so the down version catches a stray scroll starting from a
	// fresh fixture; LineUp CLAMPS at 0, so the same fixture makes the
	// assertion unfalsifiable. Only a nonzero starting offset gives a stray
	// LineUp somewhere to be seen.
	t.Run("list focused at the boundary (up): viewport still does not scroll", func(t *testing.T) {
		m := modelWithOverflowingPreview(t, 3)
		m.cursor = 0 // already at the top; k must not move it
		m = scrolledPreview(t, m, 3)
		m.focus = focusList
		startOffset := m.preview.YOffset

		next, _ := m.Update(up)
		m = next.(Model)

		if m.cursor != 0 {
			t.Fatalf("cursor = %d, want unchanged at the boundary 0 — fixture must actually be blocked", m.cursor)
		}
		if m.preview.YOffset != startOffset {
			t.Errorf("viewport scrolled from %d to %d while the list had focus (blocked cursor)",
				startOffset, m.preview.YOffset)
		}
	})

	t.Run("preview focused: viewport scrolls, cursor does not move", func(t *testing.T) {
		m := modelWithOverflowingPreview(t, 3)
		m.focus = focusPreview
		startCursor := m.cursor

		next, _ := m.Update(down)
		m = next.(Model)

		if m.preview.YOffset == 0 {
			t.Error("viewport did not scroll while the preview had focus")
		}
		if m.cursor != startCursor {
			t.Errorf("cursor moved to %d while the preview had focus", m.cursor)
		}
	})

	// The k/Up analog of the subtest above. TestCursorMovementClamps already
	// sends 'k', but only with the list focused (the default). Previously
	// nothing in the suite ever sent 'k' or tea.KeyUp with the PREVIEW
	// focused, so the Up case's LineUp short-circuit could be deleted with
	// the suite staying green — confirmed by running that mutation against
	// the pre-this-fix test suite.
	t.Run("preview focused: k scrolls up, cursor does not move", func(t *testing.T) {
		m := modelWithOverflowingPreview(t, 3)
		m.focus = focusPreview
		m.cursor = 1 // nonzero: an accidental cursor move (mutant fallthrough) is observable
		startCursor := m.cursor

		// Scroll down first so there is somewhere for k (LineUp) to move from.
		for i := 0; i < 3; i++ {
			next, _ := m.Update(down)
			m = next.(Model)
		}
		startOffset := m.preview.YOffset
		if startOffset == 0 {
			t.Fatal("fixture must be scrolled down before k is meaningful")
		}

		next, _ := m.Update(up)
		m = next.(Model)

		if m.preview.YOffset >= startOffset {
			t.Errorf("viewport YOffset = %d, want less than %d after k with the preview focused", m.preview.YOffset, startOffset)
		}
		if m.cursor != startCursor {
			t.Errorf("cursor moved to %d while the preview had focus", m.cursor)
		}
	})
}

func TestSelectionChangeResetsScrollToTop(t *testing.T) {
	// Resets from a SCROLLED position. Resetting a viewport already at zero
	// would pass with GotoTop deleted.
	m := modelWithOverflowingPreview(t, 3)
	m.focus = focusPreview
	for i := 0; i < 3; i++ {
		next, _ := m.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'j'}})
		m = next.(Model)
	}
	if m.preview.YOffset == 0 {
		t.Fatal("fixture must be scrolled before the reset is meaningful")
	}

	m.focus = focusList
	next, _ := m.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'j'}})
	m = next.(Model)

	if m.cursor != 1 {
		t.Fatalf("cursor = %d — the selection did not actually change", m.cursor)
	}
	if m.preview.YOffset != 0 {
		t.Errorf("YOffset = %d after a selection change, want 0", m.preview.YOffset)
	}
}

// TestSelectionChangeResetsScrollToTopOnUp is the k/Up analog of
// TestSelectionChangeResetsScrollToTop above. Without it, deleting the Up
// case's m.preview.GotoTop() call left the whole suite green — nothing
// exercised a selection change made via k rather than j.
func TestSelectionChangeResetsScrollToTopOnUp(t *testing.T) {
	m := modelWithOverflowingPreview(t, 3)
	m.cursor = 2 // start on the last row so k below has somewhere to move UP to
	m.focus = focusPreview
	for i := 0; i < 3; i++ {
		next, _ := m.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'j'}})
		m = next.(Model)
	}
	if m.preview.YOffset == 0 {
		t.Fatal("fixture must be scrolled before the reset is meaningful")
	}

	m.focus = focusList
	next, _ := m.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'k'}})
	m = next.(Model)

	if m.cursor != 1 {
		t.Fatalf("cursor = %d — the selection did not actually change", m.cursor)
	}
	if m.preview.YOffset != 0 {
		t.Errorf("YOffset = %d after a selection change via k, want 0", m.preview.YOffset)
	}
}

// TestSessionsMsgResetsScrollWhenSelectionChanges and
// TestSessionsMsgPreservesScrollWhenSelectionUnchanged pin the sessionsMsg
// case's reset to be conditional on the SessionID under the cursor actually
// changing across a poll, not on a poll merely happening.
//
// The two prior tests (TestSelectionChangeResetsScrollToTop and its Up
// analog) only ever change the selection via j/k, which already calls
// GotoTop() directly at the call site — they cannot exercise the sessionsMsg
// case's own reset at all. A poll can ALSO change the selection: if the
// selected session exits, reconcile (internal/ui/update.go's reconcile)
// lands the cursor on a different session while the viewport still has the
// old session's YOffset, since SetContent only clamps downward.
func TestSessionsMsgResetsScrollWhenSelectionChanges(t *testing.T) {
	m := New()
	next, _ := m.Update(tea.WindowSizeMsg{Width: 118, Height: 30})
	m = next.(Model)
	long := strings.Repeat("word ", 400)
	next, _ = m.Update(sessionsMsg{views: []session.View{
		{Session: session.Session{SessionID: "s0"}, HasPreview: true, LastAssistant: long},
		{Session: session.Session{SessionID: "s1"}, HasPreview: true, LastAssistant: long},
	}})
	m = next.(Model)
	if sel := m.selected(); sel == nil || sel.SessionID != "s0" {
		t.Fatalf("fixture must start with s0 selected, got %v", sel)
	}

	m.focus = focusPreview
	for i := 0; i < 3; i++ {
		next, _ := m.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'j'}})
		m = next.(Model)
	}
	if m.preview.YOffset == 0 {
		t.Fatal("fixture must be scrolled before the reset is meaningful")
	}

	// s0 exits; only s1 remains. reconcile clamps the cursor into range,
	// landing it on s1 — a genuinely different session, not a refresh of s0.
	next, _ = m.Update(sessionsMsg{views: []session.View{
		{Session: session.Session{SessionID: "s1"}, HasPreview: true, LastAssistant: long},
	}})
	m = next.(Model)

	if sel := m.selected(); sel == nil || sel.SessionID != "s1" {
		t.Fatalf("selected session = %v, want s1 (s0 must have exited and s1 taken the cursor)", sel)
	}
	if m.preview.YOffset != 0 {
		t.Errorf("YOffset = %d after the selected session exited and the cursor landed on a different one, want 0", m.preview.YOffset)
	}
}

func TestSessionsMsgPreservesScrollWhenSelectionUnchanged(t *testing.T) {
	m := New()
	next, _ := m.Update(tea.WindowSizeMsg{Width: 118, Height: 30})
	m = next.(Model)
	long := strings.Repeat("word ", 400)
	next, _ = m.Update(sessionsMsg{views: []session.View{
		{Session: session.Session{SessionID: "s0"}, HasPreview: true, LastAssistant: long},
		{Session: session.Session{SessionID: "s1"}, HasPreview: true, LastAssistant: long},
	}})
	m = next.(Model)
	if sel := m.selected(); sel == nil || sel.SessionID != "s0" {
		t.Fatalf("fixture must start with s0 selected, got %v", sel)
	}

	m.focus = focusPreview
	for i := 0; i < 3; i++ {
		next, _ := m.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'j'}})
		m = next.(Model)
	}
	startOffset := m.preview.YOffset
	if startOffset == 0 {
		t.Fatal("fixture must be scrolled before the preservation is meaningful")
	}

	// s0 is STILL selected — its own LastAssistant just grew, the ordinary
	// busy-session poll. This must NOT reset the scroll: resetting here would
	// yank a reading user back to the top on every ~2s poll.
	longer := long + strings.Repeat("more ", 200)
	next, _ = m.Update(sessionsMsg{views: []session.View{
		{Session: session.Session{SessionID: "s0"}, HasPreview: true, LastAssistant: longer},
		{Session: session.Session{SessionID: "s1"}, HasPreview: true, LastAssistant: long},
	}})
	m = next.(Model)

	if sel := m.selected(); sel == nil || sel.SessionID != "s0" {
		t.Fatalf("selected session = %v, want s0 (same session must still be selected)", sel)
	}
	if m.preview.YOffset != startOffset {
		t.Errorf("YOffset = %d after a same-session poll, want unchanged at %d", m.preview.YOffset, startOffset)
	}
}

func TestEnterJumpsRegardlessOfFocus(t *testing.T) {
	// Enter must not change meaning with focus. Both cases select a session
	// with no PID, so Focus fails predictably and sets a status — which proves
	// the Enter branch RAN, rather than proving nothing by doing nothing.
	for _, f := range []focusArea{focusList, focusPreview} {
		m := modelWithOverflowingPreview(t, 1)
		m.focus = f
		next, _ := m.Update(tea.KeyMsg{Type: tea.KeyEnter})
		m = next.(Model)
		if m.status == "" {
			t.Errorf("focus %v: Enter produced no status — the Focus branch did not run", f)
		}
	}
}

func TestPreviewMetadataLineCountMatchesTheConstant(t *testing.T) {
	// previewBodyHeight subtracts a CONSTANT from the pane height. If the
	// metadata block gains or loses a line, the viewport is silently mis-sized
	// — content clipped, or the pane overflowing its own border. This test is
	// the only link between the rendered block and the number.
	m := modelWithOverflowingPreview(t, 1)
	_, previewW := paneWidths(m.width)
	got := strings.Count(m.renderPreviewMetadata(m.selected(), paneInnerWidth(previewW)), "\n") + 1
	if got != previewMetadataLines {
		t.Errorf("metadata renders %d lines, previewMetadataLines = %d", got, previewMetadataLines)
	}
}

// TestPreviewViewportStyleHasNoFrameSize pins an assumption scrollIndicator's
// at-bottom branch (layout.go) depends on: offset >= total-height is only
// "the viewport is scrolled as far as it can go" when the viewport's Style
// carries no vertical frame size (bubbles' maxYOffset is
// len(lines)-Height-Style.GetVerticalFrameSize()). m.preview.Style is never
// set anywhere in this codebase, so that term is 0 — but nothing else says
// so. If a future change ever gives m.preview a bordered or padded style,
// this test reddens before the at-bottom branch starts firing one row early.
func TestPreviewViewportStyleHasNoFrameSize(t *testing.T) {
	m := modelWithOverflowingPreview(t, 1)
	if got := m.preview.Style.GetVerticalFrameSize(); got != 0 {
		t.Errorf("m.preview.Style.GetVerticalFrameSize() = %d, want 0 — "+
			"scrollIndicator's at-bottom branch assumes offset>=total-height IS the viewport's maxYOffset", got)
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
