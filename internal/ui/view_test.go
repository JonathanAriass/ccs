package ui

import (
	"errors"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/JonathanAriass/ccs/internal/session"
	"github.com/charmbracelet/lipgloss"
	"github.com/charmbracelet/x/ansi"
	"github.com/muesli/termenv"
)

// TestMain forces a deterministic TrueColor profile for the test binary.
// lipgloss auto-detects the terminal's color profile, and `go test` runs
// with stdout/stderr that are not a TTY, so lipgloss would otherwise strip
// every style's ANSI codes and the dim/selected/error styling this file
// checks for would be indistinguishable from plain text. Matches gsv's
// internal/ui/diff_test.go precedent. Does not affect the compiled ccs
// binary, which still auto-detects the profile of the real terminal it runs
// in.
func TestMain(m *testing.M) {
	lipgloss.SetColorProfile(termenv.TrueColor)
	os.Exit(m.Run())
}

// sizedModel returns a Model pre-populated with n views and sized as if a
// real terminal had already sent its first WindowSizeMsg — View() renders ""
// until that happens, so every rendering test needs a real size.
func sizedModel(n, width, height int) Model {
	m := modelWith(n)
	m.width, m.height = width, height
	return m
}

// visibleText strips the ANSI styling view.go itself emits, leaving the
// characters a terminal would actually place on the grid.
func visibleText(s string) string { return ansi.Strip(s) }

func controlChars(s string) []string {
	var found []string
	for _, r := range s {
		if r == '\n' {
			continue
		}
		if r < 0x20 || r == 0x7f {
			found = append(found, string(r))
		}
	}
	return found
}

// Every transcript-derived field (name via ai-title, directory, last human,
// last assistant) is attacker/accident-controlled content. This is the
// integration half of the sanitize guarantee: sanitize() itself is unit
// tested in sanitize_test.go, but nothing there proves View() actually calls
// it on every field. NOTE: this fixture's long CWD gets truncated by
// elideMiddle, and lipgloss's own pane-width truncation happens to strip some
// of what survives — so on its own this test does NOT reliably pin the CWD
// sanitize call; see TestViewSanitizesShortCWD, which uses an untruncated CWD
// specifically to close that gap. This test still pins name, the row's
// last-message column, and both preview fields.
func TestViewSanitizesAllTranscriptDerivedFields(t *testing.T) {
	m := sizedModel(1, 100, 30)
	m.views[0] = session.View{
		Session: session.Session{
			Name:            "weird\tname\r",
			CWD:             "/tmp/we\rird\tdir",
			Status:          "busy",
			StatusUpdatedAt: 1,
		},
		TTY:           "ttys001",
		LastHuman:     "hi\x1b[2Jthere\r",
		LastAssistant: "ok\tgot\x07it\r",
		HasPreview:    true,
	}
	// The preview body (LastHuman/LastAssistant) only reaches the frame via
	// m.preview, which syncPreview fills — View() alone leaves it empty. See
	// TestUpdateSizesAndFillsTheViewport's doc comment for why that split
	// exists. Without this call the assertions below would still pass, but
	// vacuously: an empty viewport has no control characters either.
	m.syncPreview()

	out := visibleText(m.View())
	if got := controlChars(out); len(got) > 0 {
		t.Errorf("frame still contains %d control characters: %q", len(got), got)
	}
	if strings.Contains(m.View(), "\x1b[2J") {
		t.Error("raw escape sequence from transcript content survived into the frame")
	}
}

// TestViewSanitizesShortCWD exists because TestViewSanitizesAllTranscriptDerivedFields
// has a blind spot for the CWD field specifically: its fixture's CWD is long
// enough that elideMiddle truncates it, and — empirically verified while
// building this test — lipgloss's own pane-level Width()/MaxWidth() rendering
// incidentally strips some raw control bytes when it has to truncate a line to
// fit, which can mask a MISSING sanitize() call on content that was already
// going to be cut. A short CWD that elideMiddle passes through untouched (well
// under its width budget) has no such truncation step to hide behind, so this
// is the fixture that actually pins formatRow's `sanitize(homeAbbrev(...))`
// call on its own.
func TestViewSanitizesShortCWD(t *testing.T) {
	m := sizedModel(1, 100, 30)
	m.views[0] = session.View{
		Session: session.Session{Name: "n", CWD: "/a\x07b", Status: "busy", StatusUpdatedAt: 1},
		TTY:     "ttys001",
	}
	out := visibleText(m.View())
	if got := controlChars(out); len(got) > 0 {
		t.Errorf("frame still contains %d control characters from an untruncated CWD: %q", len(got), got)
	}
}

// TestViewErrReplacesListOnly pins the brief's literal requirement: "m.err
// renders in place of the list" — not the whole screen. The preview pane and
// key legend must still be present.
func TestViewErrReplacesListOnly(t *testing.T) {
	m := sizedModel(0, 100, 30)
	m.err = errors.New("registry unreadable")

	out := visibleText(m.View())
	if !strings.Contains(out, "registry unreadable") {
		t.Error("error text must appear in the frame")
	}
	if !strings.Contains(out, "quit") {
		t.Error("the key legend must still render alongside the error")
	}
}

// TestViewNoPreviewWhenHasPreviewFalse pins the brief's requirement literally:
// "When HasPreview is false, the preview pane reads no preview."
func TestViewNoPreviewWhenHasPreviewFalse(t *testing.T) {
	m := sizedModel(1, 100, 30)
	m.views[0] = session.View{
		Session:    session.Session{Name: "x", Status: "idle"},
		HasPreview: false,
	}
	m.syncPreview() // fills m.preview with "no preview" — see TestViewSanitizesAllTranscriptDerivedFields
	out := visibleText(m.View())
	if !strings.Contains(out, "no preview") {
		t.Errorf("expected \"no preview\" in the frame, got:\n%s", out)
	}
}

func TestViewHasPreviewShowsDetail(t *testing.T) {
	m := sizedModel(1, 100, 30)
	m.views[0] = session.View{
		Session:    session.Session{Name: "x", Status: "busy", Version: "1.2.3"},
		TTY:        "ttys004",
		HasPreview: true,
		LastHuman:  "do the thing",
	}
	m.syncPreview() // see TestViewSanitizesAllTranscriptDerivedFields
	out := visibleText(m.View())
	if strings.Contains(out, "no preview") {
		t.Error("a session with HasPreview true must not show the no-preview placeholder")
	}
	if !strings.Contains(out, "1.2.3") {
		t.Error("preview must show the session's Version")
	}
	if !strings.Contains(out, "do the thing") {
		t.Error("preview must show LastHuman")
	}
}

func TestViewEmptyListShowsPlaceholder(t *testing.T) {
	m := sizedModel(0, 100, 30)
	out := visibleText(m.View())
	if !strings.Contains(out, "no live sessions") {
		t.Errorf("expected an empty-list placeholder, got:\n%s", out)
	}
}

// TestViewDimsRowsWithNoTTY pins the "background session, cannot be focused"
// visual cue: a row with an empty TTY must render inside the dim style, not
// the plain one. Direction matters here — a session WITH a tty must NOT be
// dimmed, or the cue is meaningless.
func TestViewDimsRowsWithNoTTY(t *testing.T) {
	// Status is "busy" (glyph color 220) for both rows, deliberately NOT
	// "idle" — idleColor and dimRow both use color 240, and an idle glyph's
	// own ANSI prefix would coincidentally match dimPrefix regardless of
	// whether the row itself got dimmed, producing a false positive.
	m := sizedModel(2, 100, 30)
	m.views[0] = session.View{Session: session.Session{Name: "bg", Status: "busy"}, TTY: ""}
	m.views[1] = session.View{Session: session.Session{Name: "fg", Status: "busy"}, TTY: "ttys002"}
	m.cursor = -1 // neither row is selected, so only the dim/plain guard is exercised

	// Render the LIST pane only (not the full View()): the preview pane's own
	// border also happens to use color 240, same as dimRow, and would
	// otherwise contaminate a substring search over the full joined frame.
	listW, _ := paneWidths(m.width)
	raw := m.renderList(listW, bodyPaneHeight(m.height))

	dimANSI := dimRow.Render("z") // capture this style's actual escape prefix
	dimPrefix := dimANSI[:strings.Index(dimANSI, "z")]

	lines := strings.Split(raw, "\n")
	var bgLine, fgLine string
	for _, ln := range lines {
		switch {
		case strings.Contains(ansi.Strip(ln), "bg"):
			bgLine = ln
		case strings.Contains(ansi.Strip(ln), "fg"):
			fgLine = ln
		}
	}
	if bgLine == "" || fgLine == "" {
		t.Fatalf("could not find both rows in the list pane:\n%s", raw)
	}
	if !strings.Contains(bgLine, dimPrefix) {
		t.Errorf("row with empty TTY must be dimmed: %q", bgLine)
	}
	if strings.Contains(fgLine, dimPrefix) {
		t.Errorf("row WITH a tty must not be dimmed: %q", fgLine)
	}
}

// TestFormatRowNeverExceedsItsWidthBudgetInDisplayColumns is a regression
// test for a bug found by running View() against real (read-only) session
// registry data: a transcript reply containing a double-width rune ("✅")
// pushed a real row's DISPLAY width past its budget even though
// truncateToWidth's RUNE-count budget looked correct — layout.go's
// truncateToWidth/elideMiddle (by design, per the brief) budget in runes, not
// display columns. lipgloss.Style.Width() word-wraps a line that overflows
// its declared width rather than clipping it, so that one too-wide row
// silently became two screen lines in the real render and shoved every row
// below it out of place (confirmed live: fixed, then re-ran against the same
// registry data and the corruption was gone). formatRow's ansi.Truncate
// safety net is what's supposed to prevent this — this test pins that
// contract directly, in display columns, independent of how many rows lipgloss
// happens to need before it decides to wrap in a full render.
func TestFormatRowNeverExceedsItsWidthBudgetInDisplayColumns(t *testing.T) {
	v := session.View{
		Session: session.Session{Name: "n", CWD: "/tmp/proj", Status: "busy"},
		// Entirely double-width runes: a rune-count budget of N runes
		// measures as ~2N display columns, so any budget shortfall shows up
		// clearly regardless of exactly where truncateToWidth's cut falls.
		LastAssistant: strings.Repeat("完", 80),
	}
	const width = 54
	row := formatRow(v, "", time.Now(), width)
	if w := lipgloss.Width(row); w > width {
		t.Errorf("formatRow returned a row %d columns wide, want <= %d: %q", w, width, row)
	}
}

func TestViewTerminalTooSmall(t *testing.T) {
	m := sizedModel(1, minTermWidth-1, minTermHeight)
	out := visibleText(m.View())
	if !strings.Contains(out, "Terminal too small") {
		t.Errorf("expected the too-small notice, got:\n%s", out)
	}
}

// busyFrameModel builds a model whose every field is long enough to compete for
// the width budget, so a width sweep exercises real truncation rather than a
// frame that happens to be narrow.
func busyFrameModel(width, height int, status string) Model {
	m := sizedModel(2, width, height)
	m.status = status
	m.views[0] = session.View{
		Session: session.Session{
			SessionID:       "sess-a",
			Name:            "a-fairly-long-session-name",
			CWD:             "/Users/x/Desktop/okt-api/.claude/worktrees/OKT-18841-detracciones-mx",
			Status:          "waiting",
			Version:         "2.1.220",
			StatusUpdatedAt: 1785322956268,
		},
		TTY: "ttys017",
		// Double-width runes: the row budget is counted in runes, so CJK text
		// is what turns a "correct" budget into an overflowing line.
		LastAssistant: strings.Repeat("完了", 40),
		LastHuman:     strings.Repeat("please do the thing ", 8),
		HasPreview:    true,
	}
	m.views[1] = session.View{
		Session: session.Session{SessionID: "sess-b", Name: "b", CWD: "/tmp", Status: "idle"},
	}
	return m
}

// TestViewNeverExceedsTerminalWidth sweeps every width the two-pane layout
// accepts, with and without a status line.
//
// DIRECTION: this pins the ACCEPT side, which nothing did before.
// TestViewTerminalTooSmall pins only the REJECT side — that a width below
// minTermWidth shows the notice. "We refuse to draw below 40" says nothing
// about whether what we DO draw at 40 fits on screen, and it did not: the key
// legend is 52 display columns at full length, so every width in [40, 52)
// rendered a frame wider than the terminal. lipgloss.JoinVertical pads every
// block to the widest one, so the damage was not confined to the footer — all
// eleven body rows were stretched to 52 columns, the terminal wrapped each onto
// a second line, and an 11-line frame became 22. [40, 52) is exactly what a
// vertical iTerm2 split or a Neovim :terminal in a side window gives you.
func TestViewNeverExceedsTerminalWidth(t *testing.T) {
	for _, status := range []string{"", "could not focus: iterm: no tab owns that tty"} {
		for w := minTermWidth; w <= 80; w++ {
			m := busyFrameModel(w, 20, status)
			frame := m.View()

			// Guard against the assertion below passing vacuously on a frame
			// that rendered nothing at all.
			lines := strings.Split(frame, "\n")
			if len(lines) < 10 {
				t.Fatalf("width %d (status %q): frame is only %d lines, nothing to measure:\n%s",
					w, status, len(lines), frame)
			}
			if !strings.Contains(visibleText(frame), "Sessions") {
				t.Fatalf("width %d (status %q): frame has no session list:\n%s", w, status, frame)
			}

			for i, ln := range lines {
				if got := lipgloss.Width(ln); got > w {
					t.Fatalf("width %d (status %q): line %d is %d display columns, want <= %d:\n%q",
						w, status, i, got, w, visibleText(ln))
				}
			}
		}
	}
}

// TestViewLegendElidesGracefullyWhenItDoesNotFit pins help.Model.Width
// specifically, which the sweep above cannot: ansi.Truncate alone would satisfy
// every width assertion there by hard-clipping mid-word. The difference the two
// produce is visible in the last line — bubbles drops whole bindings and marks
// the cut with "…", a bare ansi.Truncate ends on whatever character fell on the
// boundary ("… r refre").
func TestViewLegendElidesGracefullyWhenItDoesNotFit(t *testing.T) {
	m := busyFrameModel(minTermWidth, 20, "")
	lines := strings.Split(visibleText(m.View()), "\n")
	legend := strings.TrimRight(lines[len(lines)-1], " ")

	if !strings.HasSuffix(legend, "…") {
		t.Errorf("at width %d the legend must elide at a binding boundary with an ellipsis, got %q",
			minTermWidth, legend)
	}
	if !strings.Contains(legend, "up") {
		t.Errorf("the elided legend must still show the first bindings, got %q", legend)
	}
}

// TestViewShowsMetadataEvenWithoutAPreview pins the corrected scope of
// HasPreview: it governs the last EXCHANGE, not the whole pane. Gating the
// whole pane on it blanked Status, Version, TTY, Tokens and Cost for 4 of 14
// live sessions — precisely the ones where the tty and the cost are the only
// information available about them.
func TestViewShowsMetadataEvenWithoutAPreview(t *testing.T) {
	m := sizedModel(1, 100, 30)
	m.views[0] = session.View{
		Session:    session.Session{Name: "x", Status: "busy", Version: "9.9.9"},
		TTY:        "ttys042",
		Tokens:     4242,
		Cost:       12.34,
		HasPreview: false,
	}
	m.syncPreview() // see TestViewSanitizesAllTranscriptDerivedFields
	out := visibleText(m.View())
	for _, want := range []string{"no preview", "busy", "9.9.9", "ttys042", "4242", "$12.34"} {
		if !strings.Contains(out, want) {
			t.Errorf("preview pane for a session with no exchange must still show %q, got:\n%s", want, out)
		}
	}
}

// TestViewRowShowsAgeFromRegistryMilliseconds pins the CALL SITE of compactAge:
// formatRow must hand it session.StatusUpdatedTime(), the conversion that knows
// the registry's unit. Reading the raw int64 as seconds instead lands in the
// year 58544, every duration underflows, the clock-skew clamp swallows the
// negative and the column renders "now" for every row on screen.
func TestViewRowShowsAgeFromRegistryMilliseconds(t *testing.T) {
	m := sizedModel(1, 100, 30)
	m.views[0] = session.View{
		Session: session.Session{
			Name:            "aged",
			Status:          "idle",
			StatusUpdatedAt: time.Now().Add(-3 * time.Hour).UnixMilli(),
		},
		TTY: "ttys001",
	}
	out := visibleText(m.View())
	if !strings.Contains(out, "3h") {
		t.Errorf("the row's age column must render \"3h\" for a session last updated 3 hours ago, got:\n%s", out)
	}
	if strings.Contains(out, "now") {
		t.Errorf("a 3-hour-old session must not render as \"now\" — the age is being read in the wrong unit:\n%s", out)
	}
}

func TestViewNotYetSizedRendersEmpty(t *testing.T) {
	m := New() // width/height still zero — no WindowSizeMsg received yet
	if got := m.View(); got != "" {
		t.Errorf("View() before sizing = %q, want empty", got)
	}
}
