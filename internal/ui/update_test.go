package ui

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/JonathanAriass/ccs/internal/session"
	"github.com/charmbracelet/bubbles/textinput"
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
//
// LastHuman is deliberately SHORT (a handful of words) while LastAssistant
// carries the 400-repeat body that does the overflowing: at this fixture's
// dimensions a 400-repeat LastHuman alone runs to ~46 wrapped lines, which
// pushes the "Last assistant" label (and everything after it) well past the
// 16-row viewport at YOffset 0 — invisible without scrolling. Every existing
// scroll/overflow test only ever needs SOME content past the viewport height,
// which the long assistant body still supplies on its own; a short human
// half additionally lets TestPreviewShowsActivityAgeAndSourceLabel assert the
// assistant label's presence without having to scroll to find it first,
// matching how a reader actually encounters it (at the top, unscrolled).
func modelWithOverflowingPreview(t *testing.T, n int) Model {
	t.Helper()
	views := make([]session.View, n)
	for i := range views {
		views[i] = session.View{
			Session:       session.Session{SessionID: fmt.Sprintf("s%d", i)},
			HasPreview:    true,
			LastHuman:     previewHumanMarker(i) + " " + strings.Repeat("word ", 3),
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

	// Resize smaller, with NO sessionsMsg (no poll) in between. Width stays
	// >= stackBreakpoint deliberately, so this resize exercises the same
	// side-by-side geometry it always has — the stacked arm gets its own
	// dedicated coverage (TestViewportHeightMatchesRenderedPreviewInBothLayouts)
	// rather than entangling itself with this test's unrelated concern.
	next, _ = m.Update(tea.WindowSizeMsg{Width: 100, Height: 16})
	m = next.(Model)

	wantHeight := previewBodyHeight(paneInnerHeight(bodyPaneHeight(16)), previewMetadataLines)
	if m.preview.Height != wantHeight {
		t.Errorf("viewport Height = %d after a resize with no intervening poll, want %d (was %d before the resize)",
			m.preview.Height, wantHeight, firstHeight)
	}
	_, wantWidth := paneWidths(100)
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

// TestPreviewMetadataLineCountMatchesTheConstant is, by itself, "the only
// link between the rendered block and the number" previewBodyHeight
// subtracts from the pane height — if the metadata block gains or loses a
// line, the viewport is silently mis-sized: content clipped, or the pane
// overflowing its own border.
//
// BOTH ActivityAt states are counted, and that is not belt-and-suspenders:
// the Activity line is the block's first line whose CONTENT branches on view
// state (compactAge's "-" placeholder vs a real age) while its WRITE does
// not — a fixture that only ever leaves ActivityAt zero (every OTHER fixture
// in this package does) cannot tell that unconditional write apart from a
// mutant that appends the line only in the non-zero state, or only in the
// zero one: whichever state the fixture never visits renders the wrong count
// with the whole suite green. Asserting both closes that gap for both
// mutants at once.
func TestPreviewMetadataLineCountMatchesTheConstant(t *testing.T) {
	m := modelWithOverflowingPreview(t, 1)
	_, previewW := paneWidths(m.width)
	innerW := paneInnerWidth(previewW)

	for _, c := range []struct {
		name       string
		activityAt time.Time
	}{
		{"nothing on disk (renders \"Activity: -\")", time.Time{}},
		{"a real age (renders \"Activity: 44m\")", fixedNow.Add(-44 * time.Minute)},
	} {
		m.views[0].ActivityAt = c.activityAt
		got := strings.Count(m.renderPreviewMetadata(m.selected(), innerW, fixedNow), "\n") + 1
		if got != previewMetadataLines {
			t.Errorf("%s: metadata renders %d lines, previewMetadataLines = %d", c.name, got, previewMetadataLines)
		}
	}

	// THIRD counted state: renaming replaces the title line with the input,
	// it does not add one. A state-branching metadata block needs each state
	// counted on its own — see the M10 lesson.
	m.renaming = true
	m.nameInput = textinput.New()
	if got := strings.Count(m.renderPreviewMetadata(m.selected(), innerW, fixedNow), "\n") + 1; got != previewMetadataLines {
		t.Errorf("renaming state renders %d lines, want %d", got, previewMetadataLines)
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

// --- Small-height layout: the preview pane disappears below previewFits'
// threshold, and focus must not be left pointing at a pane that isn't drawn.

// shrunkBelowThePreviewThreshold returns a 3-session model with the cursor at
// the given index, the preview pane FOCUSED, scrolled away from the top, and
// the terminal then resized below previewFits' threshold (height 13, two rows
// under the boundary at 15) — the exact state the dead-key hazard lives in.
//
// Every step is asserted rather than assumed: if the preview were already
// invisible at the starting size, or focus did not survive the resize, or the
// viewport never scrolled, the tests below would be asserting against a state
// their own mutants also produce.
func shrunkBelowThePreviewThreshold(t *testing.T, cursor int) Model {
	t.Helper()
	m := modelWithOverflowingPreview(t, 3)
	if !m.previewVisible() {
		t.Fatal("fixture: preview must be visible at the starting (tall) size")
	}
	// Scroll through the real key path while the preview is still visible.
	// A viewport at offset 0 makes every "did not scroll" assertion below
	// unfalsifiable in the up direction, since LineUp clamps at 0.
	m = scrolledPreview(t, m, 3)
	m.cursor = cursor
	m.focus = focusPreview

	next, _ := m.Update(tea.WindowSizeMsg{Width: 100, Height: 13}) // below the threshold (15)
	m = next.(Model)
	if m.previewVisible() {
		t.Fatal("fixture: preview must NOT be visible at height 13 — see previewFits")
	}
	if m.focus != focusPreview {
		t.Fatal("fixture: focus must still read focusPreview after the resize — a resize does not touch m.focus")
	}
	if m.preview.YOffset == 0 {
		t.Fatal("fixture: the viewport must still be scrolled after the resize")
	}
	return m
}

// TestJAndKDoNotFreezeAfterShrinkingBelowThePreviewThreshold is the regression
// test for the dead-key hazard: focus the preview pane at a tall terminal,
// shrink below previewFits' threshold (where View stops drawing a preview pane
// at all), and press j or k. Before the previewVisible() guard on Up/Down,
// m.focus stayed focusPreview across the resize (nothing resets it — see
// previewVisible's doc comment for why that is deliberate) and the key called
// m.preview.LineUp/LineDown on a viewport nothing on screen shows — a key that
// visibly does nothing, which reads as a frozen app.
//
// BOTH DIRECTIONS, because they are two separate guards in update.go and only
// one of them was covered: this test sent nothing but 'j' while its comment
// claimed to describe LineUp and LineDown alike, and dropping the
// previewVisible() call from the Up branch left the whole suite green.
//
// Each direction starts from a cursor that CAN move that way — 0 for j, 2 for
// k. Asserting "the cursor moved" from a position already at that boundary
// would hold with the bug fully present, proving nothing.
func TestJAndKDoNotFreezeAfterShrinkingBelowThePreviewThreshold(t *testing.T) {
	cases := []struct {
		name       string
		key        tea.KeyMsg
		start, end int
	}{
		{"j", tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'j'}}, 0, 1},
		{"k", tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'k'}}, 2, 1},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			m := shrunkBelowThePreviewThreshold(t, c.start)

			next, _ := m.Update(c.key)
			m = next.(Model)

			if m.cursor != c.end {
				t.Errorf("cursor = %d after %s with an invisible preview focused, want %d — "+
					"the list must move regardless of m.focus", m.cursor, c.name, c.end)
			}
		})
	}
}

// TestJAndKDoNotScrollAnInvisiblePreview is the other half, and it needs its own
// fixture: a cursor that MOVES triggers syncPreview()+GotoTop(), which parks the
// viewport at 0 whatever a stray LineUp/LineDown did on the way, so the test
// above cannot see a phantom scroll at all.
//
// Starting at the boundary for each direction makes the cursor move a no-op, so
// nothing resets the viewport and a scroll of a pane that is not on screen has
// nowhere to hide.
func TestJAndKDoNotScrollAnInvisiblePreview(t *testing.T) {
	cases := []struct {
		name   string
		key    tea.KeyMsg
		cursor int
	}{
		{"j", tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'j'}}, 2}, // already at the bottom
		{"k", tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'k'}}, 0}, // already at the top
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			m := shrunkBelowThePreviewThreshold(t, c.cursor)
			startOffset := m.preview.YOffset

			next, _ := m.Update(c.key)
			m = next.(Model)

			if m.cursor != c.cursor {
				t.Fatalf("cursor = %d, want unchanged at the boundary %d — the fixture must actually be blocked",
					m.cursor, c.cursor)
			}
			if m.preview.YOffset != startOffset {
				t.Errorf("preview YOffset changed from %d to %d — %s scrolled a pane nothing on screen shows",
					startOffset, m.preview.YOffset, c.name)
			}
		})
	}
}

// TestPreviewShowsActivityAgeAndSourceLabel pins the two new affordances in
// both directions each: age line present and derived from ActivityAt; label
// present exactly when a subagent is live.
func TestPreviewShowsActivityAgeAndSourceLabel(t *testing.T) {
	m := modelWithOverflowingPreview(t, 1)
	m.views[0].ActivityAt = time.Now().Add(-44 * time.Minute)
	m.views[0].LiveAgent = ""
	m.syncPreview()
	frame := visibleText(m.View())
	if !strings.Contains(frame, "Activity: 44m") {
		t.Errorf("no age line for a 44m-old source:\n%s", frame)
	}
	if strings.Contains(frame, "⚙") {
		t.Errorf("source label shown while the MAIN thread is live")
	}

	m.views[0].LiveAgent = "impl-fix2"
	m.syncPreview()
	frame = visibleText(m.View())
	if !strings.Contains(frame, "Last assistant (⚙ impl-fix2):") {
		t.Errorf("assistant half not labeled with the live agent:\n%s", frame)
	}
}

func TestPreviewAgeDashWhenNothingOnDisk(t *testing.T) {
	m := modelWithOverflowingPreview(t, 1)
	m.views[0].ActivityAt = time.Time{}
	m.syncPreview()
	if !strings.Contains(visibleText(m.View()), "Activity: -") {
		t.Error("zero ActivityAt must render as '-' like the TTY field does")
	}
}

// TestTabIsNoOpWhenPreviewNotVisible pins the other half of "focus must not
// point at a pane that isn't drawn": with no second pane to switch TO, Tab
// must not toggle m.focus (and must not schedule a command).
func TestTabIsNoOpWhenPreviewNotVisible(t *testing.T) {
	m := modelWithOverflowingPreview(t, 3)
	next, _ := m.Update(tea.WindowSizeMsg{Width: 100, Height: 13})
	m = next.(Model)
	if m.previewVisible() {
		t.Fatal("fixture: preview must NOT be visible at height 13")
	}

	before := m.focus
	next, cmd := m.Update(tea.KeyMsg{Type: tea.KeyTab})
	m = next.(Model)
	if m.focus != before {
		t.Errorf("Tab changed focus from %v to %v with no preview pane on screen", before, m.focus)
	}
	if cmd != nil {
		t.Error("Tab scheduled a command with no preview pane on screen")
	}
}

// TestTabAndRenameAreNoOpsJustBelowTheStackedPreviewFloor is
// TestTabIsNoOpWhenPreviewNotVisible and TestRenameIsNoOpWhenPreviewNotVisible's
// missing stacked-mode counterpart.
//
// Both of those fixtures sit at width 100 (side-by-side), so they only ever
// exercise the WIDE arm's PreviewShown threshold. A previewVisible() that
// reverted to the old standalone previewFits(m.height) — ignoring m.layout and
// m.width entirely — would still pass both of them unchanged: at width 100,
// height 13, previewFits and the wide arm agree. It would NOT redden on the
// stacked arm's own, different floor (paneH >= 18, not >= 15) — a forced-stacked
// terminal one row short of that floor would then wrongly report the preview
// visible, and Tab/n would silently act on a pane that is not drawn. This
// pins that boundary directly, in forced-stacked mode specifically.
func TestTabAndRenameAreNoOpsJustBelowTheStackedPreviewFloor(t *testing.T) {
	m := modelWithOverflowingPreview(t, 3)
	m.layout = layoutStacked
	next, _ := m.Update(tea.WindowSizeMsg{Width: 60, Height: 19}) // paneH=17, one below the stacked floor (18)
	m = next.(Model)
	if m.previewVisible() {
		t.Fatal("fixture: preview must NOT be visible at height 19 stacked (paneH=17 < 18)")
	}

	before := m.focus
	next, cmd := m.Update(tea.KeyMsg{Type: tea.KeyTab})
	m = next.(Model)
	if m.focus != before {
		t.Errorf("Tab changed focus from %v to %v with no preview pane on screen (stacked)", before, m.focus)
	}
	if cmd != nil {
		t.Error("Tab scheduled a command with no preview pane on screen (stacked)")
	}

	next, cmd = m.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'n'}})
	m = next.(Model)
	if m.renaming {
		t.Error("n entered rename mode with no preview pane on screen (stacked)")
	}
	if cmd != nil {
		t.Error("n scheduled a command with no preview pane on screen (stacked)")
	}
}

// --- session rename: store override, mode-first dispatch, targeting the
// session captured at n-press rather than the cursor row at save time ---

func TestRenameOverrideShowsAndClears(t *testing.T) {
	m := modelWithOverflowingPreview(t, 2)
	m.names = map[string]string{m.views[0].SessionID: "my-migration-fix"}
	frame := visibleText(m.View())
	if !strings.Contains(frame, "my-migration-fix") {
		t.Errorf("override not shown:\n%s", frame)
	}
	delete(m.names, m.views[0].SessionID)
	if strings.Contains(visibleText(m.View()), "my-migration-fix") {
		t.Error("cleared override still shown")
	}
}

func TestRenameModeRoutesKeysToTheFieldOnly(t *testing.T) {
	m := modelWithOverflowingPreview(t, 3)
	m.names = map[string]string{}
	m.namesFile = filepath.Join(t.TempDir(), "names.json")
	n, _ := m.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'n'}})
	m = n.(Model)
	if !m.renaming {
		t.Fatal("n did not enter rename mode")
	}
	startCursor := m.cursor
	// Every one of these has a bound meaning outside the mode. BOTH halves:
	// the char reaches the field AND the normal action does not fire.
	//
	// NOTE: this loop sends KeyRunes '\t' — a typed TAB CHARACTER — not the
	// Tab KEY. tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'\t'}}.String()
	// is "\t", and the Cycle binding is "tab", which only a
	// tea.KeyMsg{Type: tea.KeyTab} produces. So this rune never could have
	// exercised the Cycle guard below — it is here only to pin what
	// textinput does with a literal tab byte (collapses it to one space; see
	// the Value() assertion). The real Tab KEY is sent separately below.
	for _, r := range "qjr\t" {
		n, _ = m.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{r}})
		m = n.(Model)
	}
	if m.cursor != startCursor {
		t.Errorf("j moved the cursor during rename")
	}
	// textinput sanitizes its input to a single line (san(), textinput.go):
	// tab collapses to one space rather than passing through or being
	// dropped — verified against the vendored v1.0.0 source, not a guess.
	if got := m.nameInput.Value(); got != "qjr " {
		t.Errorf("field did not receive the typed keys: %q", got)
	}

	// The real Tab KEY, and Up/Down — none of these are runes, so the loop
	// above cannot exercise them. All three are bound outside the mode
	// (Cycle, Up, Down) and must be swallowed by the mode exactly like the
	// rune keys above.
	for _, kt := range []tea.KeyType{tea.KeyTab, tea.KeyUp, tea.KeyDown} {
		n, _ = m.Update(tea.KeyMsg{Type: kt})
		m = n.(Model)
	}
	if m.focus != focusList {
		t.Errorf("tab switched panes during rename")
	}
	if m.cursor != startCursor {
		t.Errorf("up/down moved the cursor during rename")
	}

	// And the app did not quit: reaching here at all is the assertion, but pin
	// the mode too.
	if !m.renaming {
		t.Error("mode exited without Enter/Esc")
	}
}

func TestRenameSaveClearAndCancel(t *testing.T) {
	m := modelWithOverflowingPreview(t, 2)
	m.names = map[string]string{}
	m.namesFile = filepath.Join(t.TempDir(), "names.json")
	sid := m.views[0].SessionID

	type step struct {
		typed string
		key   tea.KeyType
		want  string // expected override after the step ("" = none)
	}
	// save, then clear via empty save, then cancel must not resurrect
	for _, s := range []step{
		{"fix-db", tea.KeyEnter, "fix-db"},
		{"", tea.KeyEnter, ""},
		{"ghost", tea.KeyEsc, ""},
	} {
		n, _ := m.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'n'}})
		m = n.(Model)
		m.nameInput.SetValue(s.typed)
		n, _ = m.Update(tea.KeyMsg{Type: s.key})
		m = n.(Model)
		if got := m.names[sid]; got != s.want {
			t.Fatalf("after %q+%v: override=%q want %q", s.typed, s.key, got, s.want)
		}
		if m.renaming {
			t.Fatal("mode did not exit")
		}
	}
	// persistence: a fresh load sees the state the LAST SAVE left (cleared)
	if got := loadNames(m.namesFile); len(got) != 0 {
		t.Fatalf("cleared override persisted: %v", got)
	}

	// Hostile input: a typed name is transcript-adjacent content the SAME as
	// every other field this package draws, and formatRow's own callers all
	// go through sanitize+flattenToRow. The Enter path must too, or a raw
	// escape/control byte lands in m.names and (via formatRow, unsanitized)
	// on screen every time the row renders — not just once, like a transcript
	// message would.
	n, _ := m.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'n'}})
	m = n.(Model)
	m.nameInput.SetValue("ev\x1b[2Jil\rx")
	n, _ = m.Update(tea.KeyMsg{Type: tea.KeyEnter})
	m = n.(Model)
	if got := controlChars(m.names[sid]); len(got) > 0 {
		t.Errorf("stored override contains control bytes: %q in %q", got, m.names[sid])
	}
}

// TestRenameTargetsTheSessionCapturedAtNPress delivers the mid-edit re-sort
// as a REAL sessionsMsg through Update, not a hand-swap of m.views — the
// final review's I4 finding. A hand-swap only pins that renameFor (captured
// at n-press) survives some slice mutation; it never actually exercises the
// path a real 2-second poll takes while a rename is open — reconcile's
// cursor-tracking, syncPreview, and (since C1) the shared
// exitRenameIfInputCannotBeDrawn guard all run on the way there, and any of
// them could regress this without a hand-swapped test ever noticing.
func TestRenameTargetsTheSessionCapturedAtNPress(t *testing.T) {
	m := modelWithOverflowingPreview(t, 2)
	m.names = map[string]string{}
	m.namesFile = filepath.Join(t.TempDir(), "names.json")
	sidA := m.views[0].SessionID
	sidB := m.views[1].SessionID
	n, _ := m.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'n'}})
	m = n.(Model)

	// A poll re-sorts the list mid-edit: B now sits where A was. This is the
	// same shape sortViews (session package) produces — a status change
	// floats one session to the top — delivered exactly as Update receives
	// it from a real poll: msg.views already in the new order.
	resorted := []session.View{m.views[1], m.views[0]}
	n, _ = m.Update(sessionsMsg{views: resorted})
	m = n.(Model)
	if !m.renaming {
		t.Fatal("the re-sort exited rename mode — fixture expects the preview to stay visible and a session to stay selected")
	}

	m.nameInput.SetValue("belongs-to-A")
	n, _ = m.Update(tea.KeyMsg{Type: tea.KeyEnter})
	m = n.(Model)
	if m.names[sidA] != "belongs-to-A" {
		t.Errorf("rename followed the ROW, not the session: %v", m.names)
	}
	if _, ok := m.names[sidB]; ok {
		t.Errorf("rename leaked onto session B, which merely inherited A's old row: %v", m.names)
	}
}

// TestRenameNoOpsOnIdentitylessSession pins the final review's I5 finding:
// m.renameFor = v.SessionID used to accept "". A View with an empty
// SessionID has no identity to key an override on — reconcile already
// refuses to alias identity-less views to each other for exactly this
// reason (see its own doc comment) — but the Rename case adopted no such
// discipline, so renaming one identity-less session silently renamed EVERY
// identity-less session in the list (same map key) and pushed the same
// title to every one of their real ttys, crossing session boundaries onto
// terminals the user never selected.
//
// Both directions live in one test: entering the mode must no-op (nothing
// to pin on the OTHER side — a no-op entry means Enter/save/push never run
// at all), and the tty writes that WOULD have happened if entry had
// silently succeeded must not have happened either.
func TestRenameNoOpsOnIdentitylessSession(t *testing.T) {
	old := devDir
	devDir = t.TempDir()
	defer func() { devDir = old }()
	os.WriteFile(filepath.Join(devDir, "ttys001"), nil, 0o644)
	os.WriteFile(filepath.Join(devDir, "ttys002"), nil, 0o644)

	m := New()
	next, _ := m.Update(tea.WindowSizeMsg{Width: 118, Height: 30})
	m = next.(Model)
	next, _ = m.Update(sessionsMsg{views: []session.View{
		{Session: session.Session{SessionID: ""}, TTY: "ttys001"},
		{Session: session.Session{SessionID: ""}, TTY: "ttys002"},
	}})
	m = next.(Model)
	if !m.previewVisible() || m.selected() == nil {
		t.Fatal("fixture: preview must be visible with a session selected")
	}
	m.namesFile = filepath.Join(t.TempDir(), "names.json")
	m.names = map[string]string{}

	n, cmd := m.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'n'}})
	m = n.(Model)
	if m.renaming {
		t.Error("n entered rename mode for an identity-less session")
	}
	if cmd != nil {
		t.Error("n scheduled a command for an identity-less session")
	}
	if len(m.names) != 0 {
		t.Errorf("an override was stored for an identity-less session: %v", m.names)
	}
	if b, _ := os.ReadFile(filepath.Join(devDir, "ttys001")); len(b) != 0 {
		t.Errorf("ttys001 was written despite the guard: %q", b)
	}
	if b, _ := os.ReadFile(filepath.Join(devDir, "ttys002")); len(b) != 0 {
		t.Errorf("ttys002 was written despite the guard: %q", b)
	}
}

func TestRenamePersistFailureDegradesWithStatus(t *testing.T) {
	m := modelWithOverflowingPreview(t, 1)
	m.names = map[string]string{}
	dir := t.TempDir()
	os.Chmod(dir, 0o555)
	defer os.Chmod(dir, 0o755)
	m.namesFile = filepath.Join(dir, "sub", "names.json") // MkdirAll will fail
	n, _ := m.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'n'}})
	m = n.(Model)
	m.nameInput.SetValue("ephemeral")
	n, _ = m.Update(tea.KeyMsg{Type: tea.KeyEnter})
	m = n.(Model)
	if m.names[m.views[0].SessionID] != "ephemeral" {
		t.Error("in-memory rename must survive a failed persist")
	}
	if !strings.Contains(m.status, "rename won't persist") {
		t.Errorf("no degradation warning, status=%q", m.status)
	}
}

// TestRenameIsNoOpWhenPreviewNotVisible is the C2 fix from the task-1 review:
// the rename input is drawn inside renderPreviewMetadata (view.go), which
// View never calls at all below previewFits' threshold — so entering the
// mode there used to be an INVISIBLE modal. Measured before the fix: at
// height 13 pressing n set m.renaming=true with nothing on screen to show
// it, q typed into the field instead of quitting, and Enter silently
// renamed the session to "q".
//
// Mirrors TestTabIsNoOpWhenPreviewNotVisible's fixture and both-halves
// shape: n must not enter the mode below the threshold, and (by not
// entering it) q must still quit normally there — the same session that,
// above the threshold, n DOES enter (every other TestRename* test in this
// file proves that half, built on modelWithOverflowingPreview's tall
// fixture).
func TestRenameIsNoOpWhenPreviewNotVisible(t *testing.T) {
	m := modelWithOverflowingPreview(t, 3)
	next, _ := m.Update(tea.WindowSizeMsg{Width: 100, Height: 13})
	m = next.(Model)
	if m.previewVisible() {
		t.Fatal("fixture: preview must NOT be visible at height 13")
	}

	next, cmd := m.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'n'}})
	m = next.(Model)
	if m.renaming {
		t.Error("n entered rename mode with no preview pane on screen")
	}
	if cmd != nil {
		t.Error("n scheduled a command with no preview pane on screen")
	}

	// With the mode never entered, q must still be Quit — not swallowed by a
	// field nothing on screen shows.
	_, quitCmd := m.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'q'}})
	if quitCmd == nil {
		t.Fatal("q must return a command")
	}
	if _, ok := quitCmd().(tea.QuitMsg); !ok {
		t.Error("q must still quit when the (never-entered) rename mode is not active")
	}
}

// TestCtrlCQuitsDuringRename pins I3 from the task-1 review: ctrl+c is the
// universal terminal interrupt, not a letter the field needs the way q is
// (see handleKey's own comment on that). Before the fix it fell through to
// m.nameInput.Update, landed in textinput's default branch, and
// insertRunesFromUserInput(nil) did nothing — renaming stayed true, nothing
// quit, nothing typed: a dead key, and (compounded with C2) a mode with no
// escape hatch at all when the preview pane is not on screen.
func TestCtrlCQuitsDuringRename(t *testing.T) {
	m := modelWithOverflowingPreview(t, 1)
	n, _ := m.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'n'}})
	m = n.(Model)
	if !m.renaming {
		t.Fatal("fixture: n did not enter rename mode")
	}

	_, cmd := m.Update(tea.KeyMsg{Type: tea.KeyCtrlC})
	if cmd == nil {
		t.Fatal("ctrl+c during rename must return a command")
	}
	if _, ok := cmd().(tea.QuitMsg); !ok {
		t.Error("ctrl+c during rename must quit, not be swallowed by the input field")
	}
}

// TestRenameEnterSurvivesANilNamesMap pins the defensive guard in the Enter
// path (update.go): loadNames itself is now nil-guarded (see names.go and
// C1 in the task-1 review), but this is belt-and-suspenders for any future
// caller that hands Model a nil names map directly — e.g. a test fixture, or
// a struct literal that never calls New(). Without the guard,
// `m.names[m.renameFor] = name` panics on assignment to a nil map.
func TestRenameEnterSurvivesANilNamesMap(t *testing.T) {
	m := modelWithOverflowingPreview(t, 1)
	m.names = nil
	m.namesFile = filepath.Join(t.TempDir(), "names.json")
	sid := m.views[0].SessionID

	n, _ := m.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'n'}})
	m = n.(Model)
	m.nameInput.SetValue("survived")
	n, _ = m.Update(tea.KeyMsg{Type: tea.KeyEnter}) // must not panic
	m = n.(Model)

	if m.names[sid] != "survived" {
		t.Errorf("rename lost on a nil starting map: %v", m.names)
	}
}

// TestRenameInputIsDrawnWhileEditing pins the final review's I2 finding: the
// C2 fix's render half (renderPreviewMetadata drawing "Rename: <input>" in
// place of the title line while m.renaming) had zero regression protection.
// previewMetadataLines stays unchanged whether the title or the input is
// drawn, so the only test that touches m.renaming in the renderer
// (TestPreviewMetadataLineCountMatchesTheConstant) cannot tell the two
// apart — the input could be silently dropped from the frame entirely with
// the whole package suite staying green. Both directions: absent while not
// renaming, present (with the typed value) while renaming.
func TestRenameInputIsDrawnWhileEditing(t *testing.T) {
	m := modelWithOverflowingPreview(t, 1)
	m.names = map[string]string{}
	m.namesFile = filepath.Join(t.TempDir(), "names.json")

	if frame := visibleText(m.View()); strings.Contains(frame, "Rename:") {
		t.Fatalf("fixture: \"Rename:\" shown before renaming even started:\n%s", frame)
	}

	n, _ := m.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'n'}})
	m = n.(Model)
	for _, r := range "new-name" {
		n, _ = m.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{r}})
		m = n.(Model)
	}

	frame := visibleText(m.View())
	if !strings.Contains(frame, "Rename:") {
		t.Errorf("rename input not drawn while editing:\n%s", frame)
	}
	if !strings.Contains(frame, "new-name") {
		t.Errorf("typed value not visible in the frame:\n%s", frame)
	}
}

// --- C1 (final review): m.renaming must not survive whatever invalidates
// the surface its input is drawn on. handleKey's Rename case only guards the
// ENTRY door (TestRenameIsNoOpWhenPreviewNotVisible); these two pin the
// exitRenameIfInputCannotBeDrawn doors the same way, following that test's
// own "resize below the threshold, then assert q still quits" shape. ---

// TestRenameExitsWhenResizedBelowThreshold: a rename opened above the
// preview threshold must close the moment a resize drops below it — before
// the fix, m.renaming survived untouched, reproducing C2's invisible modal
// (nothing on screen shows the field, the footer legend correctly stops
// advertising "n rename" and is now lying about it, and q types into a
// field nobody can see instead of quitting).
func TestRenameExitsWhenResizedBelowThreshold(t *testing.T) {
	m := modelWithOverflowingPreview(t, 1)
	n, _ := m.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'n'}})
	m = n.(Model)
	if !m.renaming {
		t.Fatal("fixture: n did not enter rename mode")
	}

	next, _ := m.Update(tea.WindowSizeMsg{Width: 100, Height: 13})
	m = next.(Model)
	if m.previewVisible() {
		t.Fatal("fixture: preview must NOT be visible at height 13")
	}
	if m.renaming {
		t.Error("rename mode survived a resize below the preview threshold — the invisible modal is back")
	}

	_, quitCmd := m.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'q'}})
	if quitCmd == nil {
		t.Fatal("q must return a command")
	}
	if _, ok := quitCmd().(tea.QuitMsg); !ok {
		t.Error("q must quit once the resize has closed the no-longer-visible rename mode")
	}
}

// TestRenameExitsWhenAllSessionsExitMidEdit is C1's other door (also I1):
// every session the poll knows about can exit while one is mid-rename,
// landing on renderPreview's v == nil branch — same invisible-modal defect,
// reached without ever touching previewVisible().
func TestRenameExitsWhenAllSessionsExitMidEdit(t *testing.T) {
	m := modelWithOverflowingPreview(t, 1)
	n, _ := m.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'n'}})
	m = n.(Model)
	if !m.renaming {
		t.Fatal("fixture: n did not enter rename mode")
	}

	next, _ := m.Update(sessionsMsg{views: nil})
	m = next.(Model)
	if m.selected() != nil {
		t.Fatal("fixture: draining every session must leave nothing selected")
	}
	if m.renaming {
		t.Error("rename mode survived every session exiting mid-edit — same invisible-modal class as the resize door")
	}

	_, quitCmd := m.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'q'}})
	if quitCmd == nil {
		t.Fatal("q must return a command")
	}
	if _, ok := quitCmd().(tea.QuitMsg); !ok {
		t.Error("q must quit once every session has exited and closed the rename mode")
	}
}
