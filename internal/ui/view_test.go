package ui

import (
	"errors"
	"fmt"
	"os"
	"regexp"
	"strings"
	"testing"
	"time"

	"github.com/JonathanAriass/ccs/internal/session"
	tea "github.com/charmbracelet/bubbletea"
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

// titleOf extracts the preview pane's title line directly from
// renderPreviewMetadata, rather than substring-matching the whole joined
// frame. A whole-frame substring search for e.g. "1/" would also match a CWD
// like "/tmp/v1/x" for the wrong reason — extracting the actual title line
// is what makes an assertion against it real evidence.
func titleOf(m Model) string {
	v := m.selected()
	if v == nil {
		return ""
	}
	_, previewW := paneWidths(m.width)
	line, _, _ := strings.Cut(m.renderPreviewMetadata(v, paneInnerWidth(previewW)), "\n")
	return visibleText(line)
}

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
	raw := m.renderList(listW, bodyPaneHeight(m.height), listPane)

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
//
// Built entirely through Update (WindowSizeMsg then sessionsMsg), not by
// hand-assigning m.width/m.height/m.views the way sizedModel does. That
// matters here specifically: sizedModel never calls syncPreview, so
// m.preview stays its zero value — Height 0, no content — and a sweep built
// on it cannot render a page indicator or observe the border swap at all
// (Important #2). Every session below is repeated so BOTH focus states get
// an overflowing preview to sweep.
func busyFrameModel(width, height int, status string) Model {
	m := New()
	next, _ := m.Update(tea.WindowSizeMsg{Width: width, Height: height})
	m = next.(Model)
	next, _ = m.Update(sessionsMsg{views: []session.View{
		{
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
		},
		{
			Session: session.Session{SessionID: "sess-b", Name: "b", CWD: "/tmp", Status: "idle"},
		},
	}})
	m = next.(Model)
	m.status = status
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
		// Sweep BOTH focus states (Important #2): busyFrameModel used to be
		// built by sizedModel, which never calls syncPreview, so this sweep
		// previously rendered a bare "Preview" title at every width — it
		// never exercised the marker, the page indicator, or the border swap
		// that focusPreview brings in, and so could not see Important #1 (the
		// widened title wrapping and clipping the preview pane's own bottom
		// border at widths 40-42). Now built through Update, both states are
		// real.
		for _, focus := range []focusArea{focusList, focusPreview} {
			for w := minTermWidth; w <= 80; w++ {
				m := busyFrameModel(w, 20, status)
				m.focus = focus
				frame := m.View()

				// Guard against the assertion below passing vacuously on a frame
				// that rendered nothing at all.
				lines := strings.Split(frame, "\n")
				if len(lines) < 10 {
					t.Fatalf("width %d focus %v (status %q): frame is only %d lines, nothing to measure:\n%s",
						w, focus, status, len(lines), frame)
				}
				if !strings.Contains(visibleText(frame), "Sessions") {
					t.Fatalf("width %d focus %v (status %q): frame has no session list:\n%s", w, focus, status, frame)
				}

				for i, ln := range lines {
					if got := lipgloss.Width(ln); got > w {
						t.Fatalf("width %d focus %v (status %q): line %d is %d display columns, want <= %d:\n%q",
							w, focus, status, i, got, w, visibleText(ln))
					}
				}
			}
		}
	}
}

// widthSweepModel builds a model at the given terminal width with an
// overflowing preview exchange and a realistic metadata block.
//
// The Version and TTY are not decoration. "Version: 2.1.220" is 16 display
// columns, wider than the preview pane's interior at every width in [40, 48),
// and this fixture deliberately omitted both fields for most of the branch —
// which is exactly why nothing caught the metadata block wrapping and taking
// the pane's bottom border with it there. A fixture that omits the widest
// field a real session carries documents its own blind spot; this one carries
// it.
func widthSweepModel(width, height int) Model {
	m := New()
	next, _ := m.Update(tea.WindowSizeMsg{Width: width, Height: height})
	m = next.(Model)
	next, _ = m.Update(sessionsMsg{views: []session.View{{
		Session: session.Session{
			SessionID: "s0",
			Status:    "waiting",
			Version:   "2.1.220",
		},
		TTY:           "ttys017",
		HasPreview:    true,
		LastHuman:     strings.Repeat("word ", 400),
		LastAssistant: strings.Repeat("word ", 400),
	}}})
	return next.(Model)
}

// scrollPreviewToEnd drives j through Update, with the preview temporarily
// focused, until the viewport stops moving — calling visit at every offset it
// passes through, INCLUDING the starting one. Focus is restored afterwards so a
// caller sweeping focus states can still scroll.
//
// Stepping one line at a time is the point: the defect this covers (a title
// that fits at "9/42" and not at "10/42") appears at one specific offset and is
// invisible to a test that only samples the first and last.
func scrollPreviewToEnd(t *testing.T, m Model, visit func(m Model)) Model {
	t.Helper()
	down := tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'j'}}
	for i := 0; ; i++ {
		visit(m)
		if i > 10000 {
			t.Fatal("viewport never stopped scrolling")
		}
		before := m.preview.YOffset
		focus := m.focus
		m.focus = focusPreview
		next, _ := m.Update(down)
		m = next.(Model)
		m.focus = focus
		if m.preview.YOffset == before {
			return m
		}
	}
}

// pageIndicatorRE matches the "N/M" page indicator anywhere in a title.
var pageIndicatorRE = regexp.MustCompile(`\d+/\d+`)

// TestPreviewTitleKeepsBothAffordancesAtEveryWidth pins the spec's two title
// requirements — "the focus indicator must not depend on colour alone" and
// "overflow must be visible" — across the whole supported width band and at
// every scroll position, in both directions (present when it should be, absent
// when it should not).
//
// The old code appended each piece only if the WHOLE title still fit, which
// failed at both ends of that band: at widths 40-42 "Preview ▸" alone consumed
// the 12-column interior and the indicator never appeared at all, and at widths
// 43-45 "Preview ▸ 9/42" fit while "Preview ▸ 10/42" did not — so the indicator
// showed for nine pages and then VANISHED, which reads as "there is no more
// content" at the exact moment there still is.
//
// TestPreviewTitleShowsFocus and TestPreviewTitleShowsOverflowOnlyWhenItOverflows
// cover the same two affordances but only ever at width 118, where everything
// fits and no degradation happens.
func TestPreviewTitleKeepsBothAffordancesAtEveryWidth(t *testing.T) {
	for _, focus := range []focusArea{focusList, focusPreview} {
		for w := minTermWidth; w <= 80; w++ {
			m := widthSweepModel(w, 20)
			m.focus = focus
			_, previewW := paneWidths(w)
			innerW := paneInnerWidth(previewW)

			if m.preview.TotalLineCount() <= m.preview.Height {
				t.Fatalf("width %d: fixture does not overflow (%d lines in %d rows) — there is no indicator to assert on",
					w, m.preview.TotalLineCount(), m.preview.Height)
			}

			shape := ""
			scrollPreviewToEnd(t, m, func(m Model) {
				title := titleOf(m)
				where := fmt.Sprintf("width %d focus %v offset %d", w, focus, m.preview.YOffset)

				if !pageIndicatorRE.MatchString(title) {
					t.Fatalf("%s: title %q has no page indicator, but the exchange overflows (%d lines in %d rows)",
						where, title, m.preview.TotalLineCount(), m.preview.Height)
				}
				if got := strings.Contains(title, "▸"); got != (focus == focusPreview) {
					t.Fatalf("%s: title %q carries the focus marker = %v, want %v",
						where, title, got, focus == focusPreview)
				}
				if got := lipgloss.Width(title); got > innerW {
					t.Fatalf("%s: title %q is %d display columns, want <= %d — a wider title wraps onto a second line",
						where, title, got, innerW)
				}
				// The whole block must stay exactly previewMetadataLines rows,
				// which requires the title to be exactly one of them.
				block := m.renderPreviewMetadata(m.selected(), innerW)
				if got := strings.Count(block, "\n") + 1; got != previewMetadataLines {
					t.Fatalf("%s: metadata block is %d lines, want previewMetadataLines = %d:\n%s",
						where, got, previewMetadataLines, visibleText(block))
				}
				// STABILITY, and this is the assertion that pins "decide what
				// fits against the WIDEST rendering this content can produce".
				// With the page number normalised out, the title's SHAPE —
				// which pieces are present and in what order — must be
				// identical at every offset. A fit decision made against the
				// current page instead reddens here the moment the page number
				// gains a digit and pushes the title one column over, which is
				// what dropped the indicator from page 10 on at widths 43-45.
				// (The rendered width itself does grow by that digit; nothing
				// is laid out against the title's trailing edge, and the sweep
				// above already bounds it by innerW at every page.)
				if got := pageIndicatorRE.ReplaceAllString(title, "N/M"); shape == "" {
					shape = got
				} else if got != shape {
					t.Fatalf("%s: title reads %q, but it was shaped %q at the top — "+
						"the title must not gain or lose pieces as the reader scrolls",
						where, got, shape)
				}
			})
		}
	}
}

// TestPreviewTitleOmitsTheIndicatorAtEveryWidthWhenItFits is the reject half of
// the sweep above: no overflow, no indicator — at every width, not just the one
// wide width TestPreviewTitleShowsOverflowOnlyWhenItOverflows uses. Without it,
// "always show an indicator" would satisfy the accept half completely.
func TestPreviewTitleOmitsTheIndicatorAtEveryWidthWhenItFits(t *testing.T) {
	for _, focus := range []focusArea{focusList, focusPreview} {
		for w := minTermWidth; w <= 80; w++ {
			m := New()
			next, _ := m.Update(tea.WindowSizeMsg{Width: w, Height: 20})
			m = next.(Model)
			next, _ = m.Update(sessionsMsg{views: []session.View{{
				Session:    session.Session{SessionID: "s0", Status: "idle", Version: "2.1.220"},
				TTY:        "ttys017",
				HasPreview: true,
				LastHuman:  "hi",
			}}})
			m = next.(Model)
			m.focus = focus
			if m.preview.TotalLineCount() > m.preview.Height {
				t.Fatalf("width %d: fixture must FIT (%d lines in %d rows)",
					w, m.preview.TotalLineCount(), m.preview.Height)
			}

			title := titleOf(m)
			if pageIndicatorRE.MatchString(title) {
				t.Errorf("width %d focus %v: title %q shows a page indicator for an exchange that fits",
					w, focus, title)
			}
			if got := strings.Contains(title, "▸"); got != (focus == focusPreview) {
				t.Errorf("width %d focus %v: title %q carries the focus marker = %v, want %v",
					w, focus, title, got, focus == focusPreview)
			}
		}
	}
}

// TestDoubleWidthExchangeIsFullyReachableAndHonestlyIndicated is the end-to-end
// half of the wrapToWidth display-width fix (layout.go).
//
// With a rune-budgeted wrap, a CJK exchange produced logical lines twice the
// pane's width; lipgloss re-wrapped each onto two screen rows and
// viewport.View()'s MaxHeight clipped the surplus, while TotalLineCount — which
// counts LOGICAL lines — reported the content as fitting. Two distinct
// failures, both reproduced before this fix at 100x30: with 100 repeats only 6
// of 10 lines rendered, no indicator appeared and j was a silent no-op; with
// 300 repeats the title reached "2/2" — "you are at the end" — at maximum
// scroll while the end of the message had never been on screen.
//
// So this asserts BOTH: the end is reachable, and the title never claims
// completeness while it is not.
func TestDoubleWidthExchangeIsFullyReachableAndHonestlyIndicated(t *testing.T) {
	const endMarker = "ENDMARKER"
	for _, repeats := range []int{100, 300} {
		m := New()
		next, _ := m.Update(tea.WindowSizeMsg{Width: 100, Height: 30})
		m = next.(Model)
		next, _ = m.Update(sessionsMsg{views: []session.View{{
			Session:       session.Session{SessionID: "s0"},
			HasPreview:    true,
			LastHuman:     "hi",
			LastAssistant: strings.Repeat("完了", repeats) + endMarker,
		}}})
		m = next.(Model)
		m.focus = focusPreview

		// "M/M" — the indicator's value at the last page. "" when the exchange
		// fits, in which case the title claims completeness by saying nothing.
		lastPage := widestScrollIndicator(m.preview.TotalLineCount(), m.preview.Height)

		m = scrollPreviewToEnd(t, m, func(m Model) {
			title := titleOf(m)
			complete := lastPage == "" || strings.Contains(title, lastPage)
			if complete && !strings.Contains(visibleText(m.preview.View()), endMarker) {
				t.Fatalf("repeats %d, offset %d: title %q reports the exchange as complete, "+
					"but its last characters (%q) are not on screen:\n%s",
					repeats, m.preview.YOffset, title, endMarker, visibleText(m.preview.View()))
			}
		})

		if body := visibleText(m.preview.View()); !strings.Contains(body, endMarker) {
			t.Errorf("repeats %d: scrolled to the bottom (offset %d of %d lines in %d rows) and the end of the message (%q) is still unreachable:\n%s",
				repeats, m.preview.YOffset, m.preview.TotalLineCount(), m.preview.Height, endMarker, body)
		}
	}
}

// TestPreviewPaneBorderSurvivesEveryWidth is the regression net for the
// preview pane's bottom border, which nothing above the pane can afford to
// push off the frame.
//
// lipgloss word-WRAPS any line wider than the pane rather than clipping it, so
// one over-wide line in the metadata block pushes the block past
// previewMetadataLines and the pane's MaxHeight then clips its own bottom-right
// corner out of the frame. Two separate lines could do it, and this test now
// covers both:
//
//   - THE TITLE, which grows by up to 6 columns for the focus marker and the
//     page indicator. Reddens at widths 40-42 with focus=focusPreview against a
//     title with no width budget at all.
//
//   - THE METADATA FIELDS, of which "Version: 2.1.220" is the widest a real
//     session produces at 16 columns — wider than the pane's interior at every
//     width in [40, 48). This fixture used to omit Version deliberately, on the
//     grounds that it was a separate pre-existing defect; measurement says
//     otherwise. With a short exchange, base 46896e1 renders 2 pane corners at
//     every width in [40, 55] while e367bce renders 1 at [40, 47]: padding the
//     viewport to its full Height regardless of content removed the vertical
//     slack that used to absorb the wrapped line, turning an intermittent
//     pre-existing wrap into an always-on missing border across the supported
//     band. Both halves are fixed and both are swept here.
func TestPreviewPaneBorderSurvivesEveryWidth(t *testing.T) {
	// Both exchange lengths: the viewport pads to its full Height either way,
	// so the metadata block has the same room in both — but the SHORT exchange
	// is the fixture the regression was measured with, and the overflowing one
	// is the only one that renders a page indicator in the title at all.
	fixtures := map[string]func(w, h int) Model{
		"overflowing exchange": widthSweepModel,
		"short exchange": func(w, h int) Model {
			m := New()
			next, _ := m.Update(tea.WindowSizeMsg{Width: w, Height: h})
			m = next.(Model)
			next, _ = m.Update(sessionsMsg{views: []session.View{{
				// Every value here is deliberately long enough to reach the
				// pane's inner width at narrow terminals, so all of
				// renderPreviewMetadata's clamps are actually exercised. With
				// Tokens: 0 / Cost: $0.00 and a 7-column TTY these lines were
				// only 9-12 columns wide, so the TTY, Tokens and "main thread"
				// clamps could each be deleted — or every field over-clamped by
				// three columns — with the whole suite still green.
				Session:    session.Session{SessionID: "s0", Status: "waiting", Version: "2.1.220"},
				TTY:        "ttys017-nested-inner",
				Tokens:     123456789,
				Cost:       12345.678,
				HasPreview: true,
				LastHuman:  "hi",
			}}})
			return next.(Model)
		},
	}
	for name, build := range fixtures {
		for _, focus := range []focusArea{focusList, focusPreview} {
			for w := minTermWidth; w <= 80; w++ {
				m := build(w, 20)
				m.focus = focus
				frame := m.View()
				if got := strings.Count(frame, "╯"); got != 2 {
					t.Fatalf("%s, width %d focus %v: frame has %d bottom-right pane corners (╯), want 2 — a pane's bottom border is missing:\n%s",
						name, w, focus, got, frame)
				}
			}
		}
	}
}

// TestPreviewMetadataIsClampedButNotOverClamped pins renderPreviewMetadata's
// clamp from BOTH sides.
//
// The border sweep above only proves the clamp is not too LOOSE — every line
// fits, so no line pushes the pane past its border. It says nothing about the
// clamp being too TIGHT: subtracting three columns inside `fit` truncates every
// metadata value three characters early and the whole suite stays green,
// because nothing asserts that a field which FITS is rendered in full. That is
// this project's recurring shape — a guard whose only test constrains one
// direction — so this test pins the other one.
func TestPreviewMetadataIsClampedButNotOverClamped(t *testing.T) {
	v := session.View{
		Session: session.Session{SessionID: "s0", Status: "waiting", Version: "2.1.220"},
		TTY:     "ttys017-nested-inner",
		Tokens:  123456789,
		Cost:    12345.678,
	}
	// Every line renderPreviewMetadata emits from a field, with the value it
	// would carry if nothing clamped it.
	unclamped := []string{
		previewField("Status", v.Status),
		previewField("Version", v.Version),
		previewField("TTY", v.TTY),
		previewField("Tokens", fmt.Sprintf("%d", v.Tokens)),
		previewField("Cost", fmt.Sprintf("$%.2f", v.Cost)),
	}

	for innerW := 1; innerW <= 60; innerW++ {
		m := New()
		m.views = []session.View{v}
		rendered := m.renderPreviewMetadata(&m.views[0], innerW)

		for _, line := range strings.Split(rendered, "\n") {
			if got := ansi.StringWidth(line); got > innerW {
				t.Fatalf("innerW=%d: metadata line %q is %d display columns, want <= %d — clamp too loose",
					innerW, visibleText(line), got, innerW)
			}
		}
		// The other direction: any field that fits must appear untouched.
		for _, full := range unclamped {
			if ansi.StringWidth(full) > innerW {
				continue // legitimately clamped at this width
			}
			if !strings.Contains(rendered, full) {
				t.Fatalf("innerW=%d: %q is only %d columns and fits, but is not rendered intact — clamp too tight:\n%s",
					innerW, visibleText(full), ansi.StringWidth(full), visibleText(rendered))
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

func TestPreviewTitleShowsFocus(t *testing.T) {
	// Asserts on the TEXT of the title, not on a style. The terminal is
	// dark-ansi and colour cannot be verified through the pty harness, so the
	// focused state must be legible without it.
	m := modelWithOverflowingPreview(t, 2)

	m.focus = focusList
	if s := m.View(); strings.Contains(s, "Preview ▸") {
		t.Error("preview title shows the focus marker while the list has focus")
	}

	m.focus = focusPreview
	if s := m.View(); !strings.Contains(s, "Preview ▸") {
		t.Error("preview title lacks the focus marker while the preview has focus")
	}
}

func TestPreviewTitleShowsOverflowOnlyWhenItOverflows(t *testing.T) {
	// Both directions again, driven through Update so the viewport's real size
	// and content decide the answer.
	over := modelWithOverflowingPreview(t, 1)
	if !strings.Contains(over.View(), "1/") {
		t.Error("overflowing exchange shows no page indicator")
	}

	short := New()
	next, _ := short.Update(tea.WindowSizeMsg{Width: 118, Height: 30})
	short = next.(Model)
	next, _ = short.Update(sessionsMsg{views: []session.View{
		{Session: session.Session{SessionID: "s0"}, HasPreview: true, LastHuman: "hi", LastAssistant: "hello"},
	}})
	short = next.(Model)
	if short.preview.TotalLineCount() > short.preview.Height {
		t.Fatalf("fixture must fit: %d lines in %d rows",
			short.preview.TotalLineCount(), short.preview.Height)
	}
	if strings.Contains(short.View(), "1/") {
		t.Error("short exchange shows a page indicator it should not")
	}
}

// borderSeq returns s's border-color escape prefix. Unlike dimRow (a plain
// foreground style, where TestViewDimsRowsWithNoTTY's "render a placeholder
// character and take everything before it" trick works directly), listPane
// and previewPane are bordered BOX styles — rendering a placeholder character
// through one produces a whole multi-line box, not a simple prefix. Instead,
// render an empty box and take the escape sequence ahead of its top-left
// corner ("╭"), which is exactly the color code the real frame's border rows
// carry.
func borderSeq(s lipgloss.Style) string {
	top := strings.SplitN(s.Render(""), "\n", 2)[0]
	before, _, _ := strings.Cut(top, "╭")
	return before
}

// TestFocusedBorderFollowsFocus pins Step 5's border swap (view.go's `lp, pp
// := listPane, previewPane` / swap-on-focusPreview block), which nothing
// previously touched: deleting the swap left the whole suite green. Colour
// cannot be checked through the pty harness, but TestMain pins a real
// TrueColor profile for this test binary, so styles emit real escape codes
// here — the same technique TestViewDimsRowsWithNoTTY already relies on for
// dimRow.
func TestFocusedBorderFollowsFocus(t *testing.T) {
	brightSeq := borderSeq(listPane) // the style Step 5 moves TO the focused pane
	dimSeq := borderSeq(previewPane) // the style Step 5 moves TO the unfocused pane
	if brightSeq == dimSeq {
		t.Fatal("listPane and previewPane render identical border escapes; this fixture cannot distinguish focus")
	}

	m := modelWithOverflowingPreview(t, 2)
	// The top border line is "╭...╮╭...╮" — the list pane's corner, then the
	// preview pane's. Splitting on the first "╮" separates the two halves.
	halves := func(mm Model) (listHalf, previewHalf string) {
		top := strings.Split(mm.View(), "\n")[0]
		before, after, ok := strings.Cut(top, "╮")
		if !ok {
			t.Fatalf("could not find the list pane's top-right corner in %q", top)
		}
		return before, after
	}

	m.focus = focusList
	listHalf, previewHalf := halves(m)
	if !strings.Contains(listHalf, brightSeq) {
		t.Errorf("list focused: list pane's top border does not carry the bright color, got %q", listHalf)
	}
	if !strings.Contains(previewHalf, dimSeq) {
		t.Errorf("list focused: preview pane's top border does not carry the dim color, got %q", previewHalf)
	}

	m.focus = focusPreview
	listHalf, previewHalf = halves(m)
	if !strings.Contains(listHalf, dimSeq) {
		t.Errorf("preview focused: list pane's top border does not carry the dim color, got %q", listHalf)
	}
	if !strings.Contains(previewHalf, brightSeq) {
		t.Errorf("preview focused: preview pane's top border does not carry the bright color, got %q", previewHalf)
	}
}

// TestPageIndicatorAdvancesWithScroll pins the one half of "the page number
// advances as you scroll" that nothing previously covered: replacing
// m.preview.YOffset with the literal 0 inside scrollIndicator's call site
// left the entire suite green, because both TestPreviewTitleShowsFocus and
// TestPreviewTitleShowsOverflowOnlyWhenItOverflows only ever match the
// substring "1/" — exactly the frozen-at-page-1 state.
func TestPageIndicatorAdvancesWithScroll(t *testing.T) {
	m := modelWithOverflowingPreview(t, 1)
	m.focus = focusPreview
	start := titleOf(m)

	down := tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'j'}}
	for i := 0; i < 20; i++ {
		next, _ := m.Update(down)
		m = next.(Model)
	}
	if m.preview.YOffset == 0 {
		t.Fatal("fixture must actually scroll — YOffset is still 0")
	}
	if got := titleOf(m); got == start {
		t.Errorf("title still %q at YOffset %d — the page number never advanced", got, m.preview.YOffset)
	}
}

// TestPageIndicatorReachesTheLastPageAtTheBottom is the integration half of
// Important #5: scrolled all the way down, the title must read the TRUE last
// page ("N/N"), not "N-1/N" — a complete message must not look clipped, which
// is exactly the defect this whole feature exists to remove (see
// scrollIndicator's doc comment). The fixture is asserted to NOT be an exact
// multiple of the viewport height: at an exact multiple the pre-fix formula
// already lands on the last page by coincidence, which would make this test
// pass for the wrong reason.
func TestPageIndicatorReachesTheLastPageAtTheBottom(t *testing.T) {
	m := modelWithOverflowingPreview(t, 1)
	m.focus = focusPreview

	down := tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'j'}}
	// Scroll well past the end; LineDown clamps at the viewport's own
	// maxYOffset, so any generous upper bound is safe here.
	for i := 0; i < m.preview.TotalLineCount(); i++ {
		next, _ := m.Update(down)
		m = next.(Model)
	}

	total, height := m.preview.TotalLineCount(), m.preview.Height
	if total%height == 0 {
		t.Fatalf("fixture's content (%d lines) is an exact multiple of the pane height (%d) — "+
			"the pre-fix bug and the fix render identically here, so this proves nothing",
			total, height)
	}
	pages := (total + height - 1) / height
	want := fmt.Sprintf("%d/%d", pages, pages)
	if got := titleOf(m); !strings.Contains(got, want) {
		t.Errorf("title at the bottom = %q, want it to contain %q (the true last page)", got, want)
	}
}

func TestViewNotYetSizedRendersEmpty(t *testing.T) {
	m := New() // width/height still zero — no WindowSizeMsg received yet
	if got := m.View(); got != "" {
		t.Errorf("View() before sizing = %q, want empty", got)
	}
}

// --- Small-height layout: below previewFits' threshold, only the list renders ---

// heightSweepModel builds a model with one realistic, overflowing session at
// the given terminal size. Version, TTY, and nonzero Tokens/Cost are all set
// deliberately: widthSweepModel's own doc comment documents how an omitted
// Version specifically hid a real border bug for most of a branch, and the
// same reasoning applies on the height axis — a fixture that omits fields a
// live session actually carries proves less than one that doesn't.
func heightSweepModel(width, height int) Model {
	m := New()
	next, _ := m.Update(tea.WindowSizeMsg{Width: width, Height: height})
	m = next.(Model)
	next, _ = m.Update(sessionsMsg{views: []session.View{{
		Session: session.Session{
			SessionID:       "s0",
			Name:            "session-name",
			CWD:             "/Users/x/Desktop/project",
			Status:          "waiting",
			Version:         "2.1.220",
			StatusUpdatedAt: 1785322956268,
		},
		TTY:           "ttys017",
		HasPreview:    true,
		LastHuman:     strings.Repeat("word ", 400),
		LastAssistant: strings.Repeat("word ", 400),
		Tokens:        4242,
		Cost:          12.34,
	}}})
	return next.(Model)
}

// TestFrameStructurallySoundAcrossHeightSweep is the height-axis analog of
// TestViewNeverExceedsTerminalWidth, and it is the test that would have
// caught the original missing-bottom-border bug before it shipped: every
// existing border/height assertion in this file pins terminal height 20
// only, so nothing exercised heights 8-13 at all.
//
// The invariant is chosen to hold in BOTH layout modes this package now has
// (two-pane above previewFits' threshold, list-only below it): every pane
// OPENED is a pane CLOSED. Asserting a fixed corner count (the shape of the
// pre-existing width sweep's own assertion) would need editing the moment
// list-only mode shipped — and a test that has to change in lockstep with
// the code it guards is not really pinning anything independent.
func TestFrameStructurallySoundAcrossHeightSweep(t *testing.T) {
	for _, w := range []int{minTermWidth, 60, 100} {
		for h := minTermHeight; h <= 30; h++ {
			for _, focus := range []focusArea{focusList, focusPreview} {
				m := heightSweepModel(w, h)
				m.focus = focus
				frame := m.View()

				open := strings.Count(frame, "╭")
				closed := strings.Count(frame, "╯")
				if open != closed {
					t.Fatalf("width %d height %d focus %v: %d panes opened (╭) but %d closed (╯) — a pane's bottom border is broken:\n%s",
						w, h, focus, open, closed, frame)
				}
				if open == 0 {
					t.Fatalf("width %d height %d focus %v: frame has no panes at all — nothing to measure:\n%s",
						w, h, focus, frame)
				}

				lines := strings.Split(frame, "\n")
				if len(lines) > h {
					t.Fatalf("width %d height %d focus %v: frame is %d lines, want <= %d (the terminal height):\n%s",
						w, h, focus, len(lines), h, frame)
				}
				for i, ln := range lines {
					if got := lipgloss.Width(ln); got > w {
						t.Fatalf("width %d height %d focus %v: line %d is %d display columns, want <= %d:\n%q",
							w, h, focus, i, got, w, visibleText(ln))
					}
				}
			}
		}
	}
}

// TestPreviewPaneRendersAtHeight14ButNotHeight13 pins previewFits' threshold
// end to end through the real View() path, in BOTH directions — the
// companion to layout_test.go's TestPreviewFitsThresholdIsExactlyHeight14,
// which pins the same boundary at the pure-function level. Pinning only one
// side leaves the other free to drift.
func TestPreviewPaneRendersAtHeight14ButNotHeight13(t *testing.T) {
	cases := []struct {
		height    int
		wantPanes int
	}{
		{14, 2}, // the boundary itself: the preview pane IS rendered
		{13, 1}, // one row shorter: list-only
	}
	for _, c := range cases {
		m := heightSweepModel(100, c.height)
		frame := m.View()
		if got := strings.Count(frame, "╭"); got != c.wantPanes {
			t.Errorf("height %d: %d panes opened (╭), want %d:\n%s", c.height, got, c.wantPanes, visibleText(frame))
		}
		if got := strings.Count(frame, "╯"); got != c.wantPanes {
			t.Errorf("height %d: %d panes closed (╯), want %d:\n%s", c.height, got, c.wantPanes, visibleText(frame))
		}
	}
}
