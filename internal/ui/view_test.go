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
//
// The `now` handed to renderPreviewMetadata is fixed rather than time.Now():
// every caller of this helper asserts on the title's SHAPE, never on the
// Activity line's age text, so a fixed instant keeps the fixture
// deterministic without claiming the value matters.
func titleOf(m Model) string {
	v := m.selected()
	if v == nil {
		return ""
	}
	_, previewW := paneWidths(m.width)
	line, _, _ := strings.Cut(m.renderPreviewMetadata(v, paneInnerWidth(previewW), fixedNow), "\n")
	return visibleText(line)
}

// fixedNow is the `now` every structural (non-age-asserting) renderPreviewMetadata
// test call passes, so those fixtures are deterministic without implying the
// instant itself is meaningful.
var fixedNow = time.Date(2024, 1, 1, 12, 0, 0, 0, time.UTC)

// controlChars lists the C0 control characters and DELs left in s.
//
// NEWLINES ARE SKIPPED, and that skip is not full coverage of newlines — it is
// the absence of it. A rendered FRAME is made of newlines (one per screen row),
// so a helper that flagged them would flag every frame; but that also means the
// sanitize tests below cannot see a stray newline INSIDE a list row, which is a
// real bug that shipped: sanitize deliberately preserves newlines for the
// preview pane, and a row that keeps one draws that session across several
// screen rows. That invariant is pinned separately and at the right altitude by
// TestFormatRowIsAlwaysExactlyOneLine (the row) and
// TestListPaneDrawsExactlyOneRowPerSession (the pane). Do not "strengthen" this
// helper to cover it — assert on the row, not on the frame.
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
// last assistant, and — since Task 2 — the live subagent's name) is
// attacker/accident-controlled content. This is the integration half of the
// sanitize guarantee: sanitize() itself is unit tested in sanitize_test.go,
// but nothing there proves View() actually calls it on every field. NOTE:
// this fixture's long CWD gets truncated by elideMiddle, and lipgloss's own
// pane-width truncation happens to strip some of what survives — so on its
// own this test does NOT reliably pin the CWD sanitize call; see
// TestViewSanitizesShortCWD, which uses an untruncated CWD specifically to
// close that gap. This test still pins name, the row's last-message column,
// and all three preview fields (human, assistant, and the assistant's ⚙
// source label).
//
// LiveAgent is transcript-PATH-derived, not transcript CONTENT — it comes
// from filepath.Base of a subagent's filename, not anything a message body
// carries — but a macOS filename may contain any byte but "/" and NUL, so a
// hostile or malformed one reaches the frame exactly the same way a hostile
// message would. "ev\x1b[2Jil\ragent\x07" is a real value agentDisplayName
// can hand back for such a filename (verified against transcript.go's
// parsing), not a hypothetical string.
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
		LiveAgent:     "ev\x1b[2Jil\ragent\x07",
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

// The missing-transcript reason: HasPreview false + zero ActivityAt means
// nothing exists on disk (Claude Code's ~30-day cleanup purges transcripts
// under live processes). Both directions: reason shown only in that state.
func TestNoPreviewSaysWhyWhenTranscriptIsMissing(t *testing.T) {
	m := sizedModel(1, 100, 30)
	m.views[0] = session.View{Session: session.Session{SessionID: "s0", Status: "idle"}}
	m.views[0].ActivityAt = time.Time{}
	m.syncPreview()
	if !strings.Contains(visibleText(m.View()), "no preview (transcript missing)") {
		t.Error("purged transcript must say so")
	}
	m.views[0].ActivityAt = time.Now()
	m.syncPreview()
	out := visibleText(m.View())
	if strings.Contains(out, "transcript missing") || !strings.Contains(out, "no preview") {
		t.Error("a session with a transcript but no exchange keeps the plain 'no preview'")
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

// rowMsgMarker leads every fixture's last-message field below, so a test can
// tell whether the message column survived at all — as opposed to being shoved
// off the end of the row by a field that overran its own budget.
const rowMsgMarker = "MSGSTART"

// formatRowFixtures are the field combinations formatRow has to survive. All
// carry an age (3 hours) and a message, both of which a correct row keeps
// visible at the widths swept below.
//
// WHICH FIELD holds the double-width text is the whole point, and getting that
// wrong is what disarmed this file's own safety net for a whole commit. Until
// now every fixture here put its wide runes in LastAssistant — the LAST field,
// with no padding after it — so once truncateToWidth began clipping by DISPLAY
// COLUMNS (commit abc3fab) nothing in the suite could reach formatRow's
// ansi.Truncate net any more, and deleting that net left the whole suite green
// while a real row still overflowed.
//
// Name and CWD are the fields that still reach it, for a reason worth stating:
// Go's %-*s verb pads by RUNES while those two are now clipped by COLUMNS, so a
// column-clamped CJK field comes back with roughly nameW/2 surplus spaces
// appended. Measured on a 54-column row without the net: 60 columns for a CJK
// name, 57 for a CJK CWD, 63 for both, 60 for an emoji name.
func formatRowFixtures() map[string]session.View {
	base := func(name, cwd, msg string) session.View {
		return session.View{
			Session: session.Session{
				Name:            name,
				CWD:             cwd,
				Status:          "busy",
				StatusUpdatedAt: time.Now().Add(-3 * time.Hour).UnixMilli(),
			},
			LastAssistant: rowMsgMarker + " " + msg,
		}
	}
	// Entirely double-width runes: a rune-count budget of N measures as ~2N
	// display columns, so any shortfall shows up clearly regardless of exactly
	// where the cut falls. The paths are long enough that elideMiddle has
	// something to elide — a CWD already inside its budget is passed through
	// untouched and cannot tell a correct elideMiddle from a missing one.
	const wideName = "完了完了完了完了完了完了完了完了"
	const widePath = "/仕事/プロジェクト/深い/階層/作業/木/現在"
	const longPath = "/Users/x/Desktop/okt-api/.claude/worktrees/OKT-18841-detracciones-mx"
	return map[string]session.View{
		"ascii":                     base("session-name", longPath, strings.Repeat("word ", 40)),
		"double-width message":      base("session-name", longPath, strings.Repeat("完", 80)),
		"double-width name":         base(wideName, longPath, strings.Repeat("word ", 40)),
		"double-width cwd":          base("session-name", widePath, strings.Repeat("word ", 40)),
		"double-width name and cwd": base(wideName, widePath, strings.Repeat("word ", 40)),
		"emoji name":                base(strings.Repeat("✅", 16), longPath, strings.Repeat("word ", 40)),
	}
}

// TestFormatRowNeverExceedsItsWidthBudgetInDisplayColumns is a regression
// test for a bug found by running View() against real (read-only) session
// registry data: a transcript reply containing a double-width rune ("✅")
// pushed a real row's DISPLAY width past its budget. lipgloss.Style.Width()
// word-wraps a line that overflows its declared width rather than clipping it,
// so that one too-wide row silently became two screen lines in the real render
// and shoved every row below it out of place (confirmed live: fixed, then
// re-ran against the same registry data and the corruption was gone).
// formatRow's ansi.Truncate safety net is what prevents this — this test pins
// that contract directly, in display columns, independent of how many rows
// lipgloss happens to need before it decides to wrap in a full render.
//
// Swept over every width the layout can hand a row rather than the single
// width 54 it used to check, starting at 0: a degenerate budget is what a
// narrow pane produces, and it is where an off-by-one in rowWidths would land.
func TestFormatRowNeverExceedsItsWidthBudgetInDisplayColumns(t *testing.T) {
	now := time.Now()
	for name, v := range formatRowFixtures() {
		for width := 0; width <= 120; width++ {
			row := formatRow(v, "", now, width)
			if w := lipgloss.Width(row); w > width {
				t.Errorf("%s: formatRow at width %d returned a row %d columns wide: %q",
					name, width, w, visibleText(row))
				break
			}
		}
	}
}

// TestFormatRowKeepsLaterFieldsOnTheRow pins the OTHER half of the row's
// contract, which the width sweep above cannot reach: no field may spend
// another field's budget.
//
// The width sweep is satisfied by any row that fits, including one where an
// unclamped Name ran to 80 columns and the safety net then clipped the age and
// the message off the end entirely. That is precisely what happens with
// truncateToWidth or elideMiddle removed from formatRow — the row still "fits",
// and every width assertion in this file stays green, while the age column (the
// one that says whether a session is stale) and the message have silently
// vanished from the screen. So this asserts they are still there.
func TestFormatRowKeepsLaterFieldsOnTheRow(t *testing.T) {
	const width = 54
	now := time.Now()
	for name, v := range formatRowFixtures() {
		row := visibleText(formatRow(v, "", now, width))
		if !strings.Contains(row, "3h") {
			t.Errorf("%s: the age column is gone from %q — an earlier field overran its budget",
				name, row)
		}
		if !strings.Contains(row, rowMsgMarker) {
			t.Errorf("%s: the last-message column is gone from %q — an earlier field overran its budget",
				name, row)
		}
	}
}

// multiLineFixture is one session whose fields carry newlines, plus the text
// that FOLLOWS each newline. tails is what keeps the single-line assertions
// below from passing vacuously: a row that clipped the field before its newline
// has no newline either, and would satisfy them while showing the user less
// than it should.
type multiLineFixture struct {
	view session.View
	// flat is the same session with every field already written as the single
	// line it should flatten to — the row a reader should see.
	flat  session.View
	tails []string
}

// multiLineRowFixtures covers the three text fields a row draws, ONE FIELD AT A
// TIME and then all together.
//
// Per-field matters: the observed bug came in through the last message, but
// DisplayName() resolves to a transcript ai-title and CWD comes from the
// registry, so all three are arbitrary content and all three can carry a
// newline. A fixture that only ever puts one in the message cannot tell a fix
// applied to all three from a fix applied to the message alone.
//
// The message is the real one that produced the three-row session observed at
// 118x30 against the live registry: a sentence, a BLANK line, then a markdown
// heading.
func multiLineRowFixtures() map[string]multiLineFixture {
	base := func(name, cwd, msg string) session.View {
		return session.View{
			Session: session.Session{
				Name:            name,
				CWD:             cwd,
				Status:          "idle",
				StatusUpdatedAt: time.Now().Add(-3 * time.Hour).UnixMilli(),
			},
			LastAssistant: msg,
		}
	}
	const (
		plainName = "session-name"
		plainCWD  = "/x/okt-api"
		plainMsg  = "all done here"

		nlName   = "Configure CSV\nexport"
		flatName = "Configure CSV export"
		nlCWD    = "/x/okt\n-api"
		flatCWD  = "/x/okt -api"
		nlMsg    = "Written to `pr.md`.\n\n## Heading\nmore text"
		// One space for the blank-line run, not two or three.
		flatMsg = "Written to `pr.md`. ## Heading more text"
	)
	return map[string]multiLineFixture{
		"newline in name": {
			base(nlName, plainCWD, plainMsg),
			base(flatName, plainCWD, plainMsg),
			[]string{"export"},
		},
		"newline in cwd": {
			base(plainName, nlCWD, plainMsg),
			base(plainName, flatCWD, plainMsg),
			[]string{"-api"},
		},
		"newline in message": {
			base(plainName, plainCWD, nlMsg),
			base(plainName, plainCWD, flatMsg),
			[]string{"## Heading", "more text"},
		},
		"newline in all three": {
			base(nlName, nlCWD, nlMsg),
			base(flatName, flatCWD, flatMsg),
			[]string{"export", "-api", "## Heading", "more text"},
		},
	}
}

// TestFormatRowDrawsANewlineFieldExactlyLikeTheSingleLineItMeans states the
// whole contract as one equality, at every width: a field carrying newlines
// must render byte for byte like the single line it flattens to.
//
// It is the ORDER of flatten and clamp that this catches and nothing else in
// this file does. Clamping first is not a cosmetic difference: truncateToWidth
// and elideMiddle budget in DISPLAY COLUMNS and a newline measures as zero of
// them, so a clamp applied first keeps text from both sides of the newline —
// and flattening afterwards turns that zero-width byte into a real space, so
// the field comes back one column WIDER than the budget it was just clamped to.
// The row then overruns and formatRow's ansi.Truncate net takes the difference
// out of the last field on the row, the message. Verified: with the two swapped,
// every other test in this package stays green.
//
// The comparison is against a hand-written flat fixture rather than against
// formatRow's own output, so it says what the reader should SEE ("a newline
// reads as a space, a blank line as one space too") instead of restating the
// implementation.
func TestFormatRowDrawsANewlineFieldExactlyLikeTheSingleLineItMeans(t *testing.T) {
	now := time.Now()
	for name, f := range multiLineRowFixtures() {
		for width := 0; width <= 120; width++ {
			got := formatRow(f.view, "", now, width)
			want := formatRow(f.flat, "", now, width)
			if got != want {
				t.Errorf("%s: at width %d the newline fixture renders as\n\t%q\nbut the same text written on one line renders as\n\t%q",
					name, width, visibleText(got), visibleText(want))
				break
			}
		}
	}
}

// TestFormatRowIsAlwaysExactlyOneLine is the regression test for a bug found by
// running the real binary against the real session registry: a session whose
// last message contained newlines rendered across THREE screen rows — the
// second blank, the third carrying no glyph, no name and no age — and pushed
// every session below it down and off the pane.
//
// sanitize() deliberately preserves newlines (it splits on them, sanitizes each
// line and rejoins), which is correct for the preview pane and wrong for a
// single-line row: nothing between sanitize and the screen removes them.
// formatRow's ansi.Truncate net does not, either — it measures a newline as
// zero columns and passes it through, so a row can be "within budget" and still
// be several rows tall.
//
// The width sweep starts at 0 for the same reason the display-column sweep
// above does, but the wide end is what carries the test: a narrow row clips its
// fields before their newlines. That is exactly the vacuity the tails check
// closes.
func TestFormatRowIsAlwaysExactlyOneLine(t *testing.T) {
	// 118 columns is the terminal the bug was observed in, and it is wide enough
	// that every fixture field below reaches the row untruncated.
	const observedWidth = 118
	now := time.Now()
	for name, f := range multiLineRowFixtures() {
		for width := 0; width <= 120; width++ {
			row := formatRow(f.view, "", now, width)
			if n := strings.Count(row, "\n"); n > 0 {
				t.Errorf("%s: formatRow at width %d contains %d newline(s), so this session draws across %d screen rows: %q",
					name, width, n, n+1, visibleText(row))
				break
			}
		}
		row := visibleText(formatRow(f.view, "", now, observedWidth))
		for _, tail := range f.tails {
			if !strings.Contains(row, tail) {
				t.Errorf("%s: %q — the text after a newline — is missing from %q; flattening must JOIN the lines, not keep only the first (and the sweep above passes vacuously on a field clipped before its newline)",
					name, tail, row)
			}
		}
	}
}

// listPaneContentLines returns the non-blank text lines inside a rendered
// pane's border: its title, then one line per row it drew.
//
// Border rows are dropped by content, not by index, so a pane that LOST its
// bottom border (lipgloss MaxHeight-clips the box, and the border is its last
// row — see listCapacity) still parses instead of throwing the count off by
// one. Blank lines are dropped because the pane pads its interior to its full
// Height, and because a "\n\n" in a message renders as a genuinely empty row
// that no line count can distinguish from that padding — every fixture below
// therefore puts visible text after each newline.
func listPaneContentLines(pane string) []string {
	var out []string
	for _, ln := range strings.Split(visibleText(pane), "\n") {
		s := strings.Trim(ln, "│ ")
		if s == "" || strings.Trim(s, "─╭╮╰╯") == "" {
			continue
		}
		out = append(out, s)
	}
	return out
}

// multiLineRowName / multiLineRowMsg are the i-th fixture session's name and
// last message for the list-pane test below. Each carries a marker AFTER its
// newline, so a test can ask which session a given screen line belongs to and
// whether the text past the newline survived. The "-end" suffix keeps "NAME1"
// from matching "NAME11" should the fixture ever grow.
func multiLineRowName(i int) string { return fmt.Sprintf("s%d\nNAME%d-end", i, i) }
func multiLineRowMsg(i int) string  { return fmt.Sprintf("m%d\n\n## MSG%d-end", i, i) }

// TestListPaneDrawsExactlyOneRowPerSession is the frame-level half of the
// invariant TestFormatRowIsAlwaysExactlyOneLine pins on the row itself: what
// the user actually saw was not "a string with a newline in it", it was a list
// where one session occupied three rows and the sessions below it fell off the
// pane.
//
// FOUR sessions, so a row that spans two lines is not confusable with an
// off-by-one, and every field of every one of them carries a newline.
func TestListPaneDrawsExactlyOneRowPerSession(t *testing.T) {
	const n, w, h = 4, 118, 30
	views := make([]session.View, n)
	for i := range views {
		views[i] = session.View{
			Session: session.Session{
				SessionID:       fmt.Sprintf("s%d", i),
				Name:            multiLineRowName(i),
				CWD:             "/a\n/b",
				Status:          "idle",
				StatusUpdatedAt: 1785322956268,
			},
			TTY:           "ttys017",
			LastAssistant: multiLineRowMsg(i),
		}
	}
	m := New()
	next, _ := m.Update(tea.WindowSizeMsg{Width: w, Height: h})
	m = next.(Model)
	next, _ = m.Update(sessionsMsg{views: views})
	m = next.(Model)

	listW, _ := paneWidths(w)
	paneH := bodyPaneHeight(h)
	// The pane must have room to DRAW the extra lines, or the bug would be
	// hidden by the same MaxHeight clipping that would otherwise cost it its
	// bottom border, and the count below would come out right for the wrong
	// reason.
	if room, need := paneInnerHeight(paneH), 1+4*n; room < need {
		t.Fatalf("pane interior is %d rows; a multi-line render needs up to %d to be visible at all", room, need)
	}
	if got := listCapacity(paneH); got < n {
		t.Fatalf("list capacity is %d rows, fewer than the %d sessions — some would be windowed out", got, n)
	}

	pane := m.renderList(listW, paneH, listPane)
	lines := listPaneContentLines(pane)
	if len(lines) != n+1 {
		t.Fatalf("list pane drew %d content lines, want %d (one title + one row per session):\n%s",
			len(lines), n+1, pane)
	}
	if !strings.Contains(lines[0], "Sessions") {
		t.Fatalf("expected the pane title first, got %q:\n%s", lines[0], pane)
	}
	// A count alone is satisfied by n rows that all show the same session, so
	// pin WHICH session each row is — and that the text after each field's
	// newline came with it.
	for i := 0; i < n; i++ {
		nameTail, msgTail := fmt.Sprintf("NAME%d-end", i), fmt.Sprintf("MSG%d-end", i)
		var found string
		hits := 0
		for _, ln := range lines[1:] {
			if strings.Contains(ln, nameTail) {
				hits++
				found = ln
			}
		}
		if hits != 1 {
			t.Errorf("session %d's name appears on %d rows, want exactly 1:\n%s", i, hits, pane)
			continue
		}
		if !strings.Contains(found, msgTail) {
			t.Errorf("session %d's row %q lost the text after its message's newline (%q):\n%s",
				i, found, msgTail, pane)
		}
		if !strings.Contains(found, "/a /b") {
			t.Errorf("session %d's row %q does not show its CWD's two lines joined by a space:\n%s",
				i, found, pane)
		}
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
			// Double-width runes: fields are clipped by display column but
			// padded by rune (see formatRow's safety net), so CJK text is what
			// turns a "correct" budget into an overflowing line.
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
				block := m.renderPreviewMetadata(m.selected(), innerW, fixedNow)
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
	now := fixedNow
	v := session.View{
		Session:    session.Session{SessionID: "s0", Status: "waiting", Version: "2.1.220"},
		TTY:        "ttys017-nested-inner",
		Tokens:     123456789,
		Cost:       12345.678,
		ActivityAt: now.Add(-3 * time.Hour),
	}
	// EVERY line renderPreviewMetadata puts through `fit`, with the content it
	// would carry if nothing clamped it.
	//
	// "main thread" is not a previewField and enumerating only the five that are
	// is how this list came to have, on its first outing, exactly the
	// one-directional gap it was written to close: over-clamping that one line
	// by three columns survived the whole suite, while the identical mutation on
	// the Status line was killed. Anything `fit` touches belongs here.
	unclamped := []string{
		previewField("Status", v.Status),
		previewField("Version", v.Version),
		previewField("TTY", v.TTY),
		previewField("Activity", compactAge(v.ActivityAt, now)),
		labelStyle.Render("main thread"),
		previewField("Tokens", fmt.Sprintf("%d", v.Tokens)),
		previewField("Cost", fmt.Sprintf("$%.2f", v.Cost)),
	}

	for innerW := 1; innerW <= 60; innerW++ {
		m := New()
		m.views = []session.View{v}
		rendered := m.renderPreviewMetadata(&m.views[0], innerW, now)

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

// TestLegendAdvertisesSwitchPaneOnlyWhenThereIsAPaneToSwitchTo pins the
// legend against the layout actually on screen, in both directions.
//
// Below previewFits' threshold Tab is a deliberate no-op (see
// TestTabIsNoOpWhenPreviewNotVisible), so a legend still offering "switch pane"
// there names a key that does nothing. The reject half alone would be satisfied
// by dropping the binding altogether, which is why the accept half is here too.
//
// The legend is read at a width where nothing is elided: at minTermWidth
// bubbles drops trailing bindings for room, and "switch pane" missing because
// the line was too short is a different fact from it being disabled.
func TestLegendAdvertisesSwitchPaneOnlyWhenThereIsAPaneToSwitchTo(t *testing.T) {
	for _, c := range []struct {
		height int
		want   bool
	}{
		{20, true},  // two-pane: Tab switches
		{15, true},  // the threshold itself (moved from 14 — see previewFits)
		{14, false}, // one row below it: list-only, Tab is a no-op
		{minTermHeight, false},
	} {
		m := heightSweepModel(liveSessionCount, 100, c.height)
		if got := m.previewVisible(); got != c.want {
			t.Fatalf("height %d: fixture's previewVisible() = %v, want %v", c.height, got, c.want)
		}
		lines := strings.Split(visibleText(m.View()), "\n")
		legend := lines[len(lines)-1]
		if got := strings.Contains(legend, "switch pane"); got != c.want {
			t.Errorf("height %d: legend advertises \"switch pane\" = %v, want %v (preview visible = %v): %q",
				c.height, got, c.want, c.want, strings.TrimRight(legend, " "))
		}
		// The rest of the legend must survive either way — disabling one
		// binding must not take the others with it.
		for _, still := range []string{"up", "down", "focus tab", "refresh", "quit"} {
			if !strings.Contains(legend, still) {
				t.Errorf("height %d: legend lost %q: %q", c.height, still, strings.TrimRight(legend, " "))
			}
		}
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

// liveSessionCount is how many sessions the height sweeps below treat as the
// normal case.
//
// It is not a round number picked for looks: it is what the author actually has
// running. A one-session fixture is what let the list pane overflow its own
// bottom border at EVERY height from minTermHeight through 19 without a single
// test noticing — with one session the list content is two lines and fits
// anywhere, so the sweep's structural invariant held for a reason that had
// nothing to do with the layout being sound.
const liveSessionCount = 15

// heightSweepModel builds a model with n realistic, overflowing sessions at the
// given terminal size. Version, TTY, and nonzero Tokens/Cost are all set
// deliberately: widthSweepModel's own doc comment documents how an omitted
// Version specifically hid a real border bug for most of a branch, and the
// same reasoning applies on the height axis — a fixture that omits fields a
// live session actually carries proves less than one that doesn't.
//
// Every session's Name is distinct so a test can tell WHICH rows a short pane
// chose to draw, not merely how many.
func heightSweepModel(n, width, height int) Model {
	views := make([]session.View, n)
	for i := range views {
		views[i] = session.View{
			Session: session.Session{
				SessionID:       fmt.Sprintf("s%d", i),
				Name:            sessionRowName(i),
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
		}
	}
	m := New()
	next, _ := m.Update(tea.WindowSizeMsg{Width: width, Height: height})
	m = next.(Model)
	next, _ = m.Update(sessionsMsg{views: views})
	return next.(Model)
}

// sessionRowName is the Name of heightSweepModel's i-th session, in a form a
// row-presence check can match without matching its neighbours: "name-1" is a
// substring of "name-13", so the trailing marker is what keeps
// "is session 1 on screen?" from being answered by session 13.
func sessionRowName(i int) string { return fmt.Sprintf("sess-%d-end", i) }

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
//
// SESSION COUNT IS PART OF THE FIXTURE, and for most of this branch it was the
// part that made the sweep vacuous. With ONE session the list holds two lines
// and fits at every height, so the invariant above was satisfied by the fixture
// rather than by the layout: the list pane in fact lost its bottom border
// whenever len(views)+1 exceeded its interior — 6 sessions broke heights 8-10,
// and liveSessionCount broke every height from minTermHeight through 19, which
// is this tool's normal case in any window shorter than a half screen. Both
// counts are swept now; the small one is kept because it is the only one that
// exercises a list SHORTER than its pane, where listWindow must not clip.
func TestFrameStructurallySoundAcrossHeightSweep(t *testing.T) {
	for _, n := range []int{1, liveSessionCount} {
		for _, w := range []int{minTermWidth, 60, 100} {
			for h := minTermHeight; h <= 30; h++ {
				for _, focus := range []focusArea{focusList, focusPreview} {
					m := heightSweepModel(n, w, h)
					m.focus = focus
					frame := m.View()

					open := strings.Count(frame, "╭")
					closed := strings.Count(frame, "╯")
					if open != closed {
						t.Fatalf("%d sessions, width %d height %d focus %v: %d panes opened (╭) but %d closed (╯) — a pane's bottom border is broken:\n%s",
							n, w, h, focus, open, closed, frame)
					}
					if open == 0 {
						t.Fatalf("%d sessions, width %d height %d focus %v: frame has no panes at all — nothing to measure:\n%s",
							n, w, h, focus, frame)
					}

					lines := strings.Split(frame, "\n")
					if len(lines) > h {
						t.Fatalf("%d sessions, width %d height %d focus %v: frame is %d lines, want <= %d (the terminal height):\n%s",
							n, w, h, focus, len(lines), h, frame)
					}
					for i, ln := range lines {
						if got := lipgloss.Width(ln); got > w {
							t.Fatalf("%d sessions, width %d height %d focus %v: line %d is %d display columns, want <= %d:\n%q",
								n, w, h, focus, i, got, w, visibleText(ln))
						}
					}
				}
			}
		}
	}
}

// realRegistryError is the shape of the error the list pane actually has to
// render: os.Open's message, carrying a full project path. 124 columns — three
// lines of wrap at minTermWidth, five once the pane's padding is taken off.
var realRegistryError = errors.New(
	"open /Users/x/.claude/projects/-Users-x-Desktop-okt-api--claude-worktrees-OKT-18841/session-registry.json: permission denied")

// TestErrorPaneStaysInsideItsBorder sweeps the list pane's ERROR state over the
// same heights and widths as the sweep above.
//
// Same failure mode, same pane, and it needed the same fix: the error line was
// written unwrapped and unclamped, so a real registry path wrapped to five
// lines inside a four-row interior and lipgloss's MaxHeight took the pane's
// bottom border instead. Measured on the previous commit at minTermWidth,
// heights 8 and 9.
//
// The short error is swept alongside because it is the one that must NOT be
// clipped — a clamp that renders nothing at all would satisfy the border
// assertion perfectly.
func TestErrorPaneStaysInsideItsBorder(t *testing.T) {
	for name, err := range map[string]error{
		"short": errors.New("registry unreadable"),
		"real":  realRegistryError,
	} {
		for _, w := range []int{minTermWidth, 60, 100} {
			for h := minTermHeight; h <= 30; h++ {
				m := heightSweepModel(liveSessionCount, w, h)
				m.err = err
				frame := m.View()

				open := strings.Count(frame, "╭")
				if closed := strings.Count(frame, "╯"); open != closed || open == 0 {
					t.Fatalf("%s error, width %d height %d: %d panes opened (╭), %d closed (╯):\n%s",
						name, w, h, open, closed, frame)
				}
				if lines := strings.Split(frame, "\n"); len(lines) > h {
					t.Fatalf("%s error, width %d height %d: frame is %d lines, want <= %d:\n%s",
						name, w, h, len(lines), h, frame)
				}
				// The error must still be legible, not clamped out of existence:
				// its first word survives at every size.
				if !strings.Contains(visibleText(frame), "error:") {
					t.Fatalf("%s error, width %d height %d: the error is not rendered at all:\n%s",
						name, w, h, visibleText(frame))
				}
			}
		}
	}
}

// TestSelectedRowIsAlwaysOnScreen is the other half of the clamp: a list pane
// that draws only what fits must never drop the SELECTED row.
//
// Without this, "stop drawing outside the box" is satisfied completely by
// rendering the first k sessions and letting the cursor walk off the bottom —
// which is strictly worse than the overflow it replaces, because the user then
// presses ⏎ on a session they cannot see. So this walks the cursor over every
// index at every height and demands the selected row is on screen at each one.
//
// The row is identified by its own Name rather than by the "›" cursor glyph:
// the glyph would still be rendered by a mutant that drew the cursor marker on
// some other row, and the question here is which SESSION is visible.
func TestSelectedRowIsAlwaysOnScreen(t *testing.T) {
	const w = 100 // wide enough that a row's Name survives formatRow's clamp
	for h := minTermHeight; h <= 30; h++ {
		for cursor := 0; cursor < liveSessionCount; cursor++ {
			m := heightSweepModel(liveSessionCount, w, h)
			m.cursor = cursor
			out := visibleText(m.View())

			want := sessionRowName(cursor)
			if !strings.Contains(out, want) {
				t.Fatalf("height %d cursor %d: the selected session %q is not on screen:\n%s",
					h, cursor, want, out)
			}
			// Guard against the assertion above holding because every row is on
			// screen at every height — at which point it could not fail. At the
			// short heights this test exists for, something must be clipped.
			if h < 20 {
				visible := 0
				for i := 0; i < liveSessionCount; i++ {
					if strings.Contains(out, sessionRowName(i)) {
						visible++
					}
				}
				if visible == liveSessionCount {
					t.Fatalf("height %d: all %d rows fit, so this height proves nothing about clipping",
						h, liveSessionCount)
				}
			}
		}
	}
}

// listTitleLineOf extracts the list pane's title from a rendered pane. It is
// the SECOND line — the first is the pane's own top border — and taking the
// first instead yields a run of "─" that contains none of the tokens below, so
// the mistake shows up as a failure rather than as a silently weaker
// assertion. Asserted here so it stays that way.
func listTitleLineOf(t *testing.T, pane string) string {
	t.Helper()
	lines := strings.Split(visibleText(pane), "\n")
	if len(lines) < 2 {
		t.Fatalf("rendered list pane has only %d lines, no title to read:\n%s", len(lines), pane)
	}
	if !strings.Contains(lines[1], "Sessions") {
		t.Fatalf("expected the list title on line 1, got %q:\n%s", lines[1], pane)
	}
	return lines[1]
}

// TestListTitleReportsClippedRows pins the affordance in BOTH directions: a
// windowed list must say so, and a complete one must not.
//
// The direction that matters is the first — a pane silently showing 6 of 15
// sessions under a title reading "Sessions (15)" states the opposite of the
// truth. But pinning only that is satisfied by a title that always shows a
// range, which would then read "Sessions (1-15/15)" on a full list and invent a
// clipping that is not happening.
func TestListTitleReportsClippedRows(t *testing.T) {
	const w = 100
	for h := minTermHeight; h <= 30; h++ {
		m := heightSweepModel(liveSessionCount, w, h)
		m.cursor = 0
		title := listTitleLineOf(t, m.renderList(w, bodyPaneHeight(h), listPane))

		capacity := listCapacity(bodyPaneHeight(h))
		_, shown := listWindow(liveSessionCount, 0, capacity)
		if shown < liveSessionCount {
			want := fmt.Sprintf("1-%d/%d", shown, liveSessionCount)
			if !strings.Contains(title, want) {
				t.Errorf("height %d: %d of %d rows are drawn but the title %q does not say so (want %q in it)",
					h, shown, liveSessionCount, strings.TrimRight(title, " │"), want)
			}
		} else {
			if strings.Contains(title, "/") {
				t.Errorf("height %d: every row fits, but the title %q advertises a clipped window",
					h, strings.TrimRight(title, " │"))
			}
			if !strings.Contains(title, fmt.Sprintf("(%d)", liveSessionCount)) {
				t.Errorf("height %d: title %q must carry the plain session count",
					h, strings.TrimRight(title, " │"))
			}
		}
	}
}

// TestListTitleIsExactlyOneLine pins listTitleLines to the real output, the way
// TestPreviewMetadataLineCountMatchesTheConstant pins previewMetadataLines. The
// title's own width is what can break it: lipgloss WRAPS an over-wide line
// rather than clipping it, so a two-line title silently costs the list pane a
// row of capacity and then its bottom border.
//
// Swept over session counts whose rendered title is far wider than the narrowest
// pane the layout accepts, so the clamp is genuinely exercised rather than
// being satisfied by short numbers.
func TestListTitleIsExactlyOneLine(t *testing.T) {
	for _, total := range []int{0, 1, 15, 1000, 1000000} {
		for innerW := 1; innerW <= 60; innerW++ {
			for _, shown := range []int{0, 1, total / 2, total} {
				first := max(0, min(total-shown, total/3))
				got := listTitle(total, first, shown, innerW)
				if lines := strings.Count(got, "\n") + 1; lines != listTitleLines {
					t.Fatalf("total %d first %d shown %d innerW %d: title %q is %d lines, want listTitleLines = %d",
						total, first, shown, innerW, got, lines, listTitleLines)
				}
				if w := lipgloss.Width(got); w > innerW {
					t.Fatalf("total %d first %d shown %d innerW %d: title %q is %d display columns, want <= %d",
						total, first, shown, innerW, got, w, innerW)
				}
			}
		}
	}
}

// TestPreviewPaneRendersAtHeight15ButNotHeight14 pins previewFits' threshold
// end to end through the real View() path, in BOTH directions — the
// companion to layout_test.go's TestPreviewFitsThresholdIsExactlyHeight15,
// which pins the same boundary at the pure-function level. Pinning only one
// side leaves the other free to drift.
//
// The boundary is 15/14, not 14/13: the Activity line grew
// previewMetadataLines from 9 to 10, and previewFits reads that constant
// directly, so the pane now needs one more row of terminal height to clear
// the same paneInnerHeight(bodyPaneHeight(termH)) >= previewMetadataLines+1
// threshold — paneInnerHeight(bodyPaneHeight(15)) = 11 = previewMetadataLines
// (10) + 1.
func TestPreviewPaneRendersAtHeight15ButNotHeight14(t *testing.T) {
	cases := []struct {
		height    int
		wantPanes int
	}{
		{15, 2}, // the boundary itself: the preview pane IS rendered
		{14, 1}, // one row shorter: list-only
	}
	for _, c := range cases {
		m := heightSweepModel(liveSessionCount, 100, c.height)
		frame := m.View()
		if got := strings.Count(frame, "╭"); got != c.wantPanes {
			t.Errorf("height %d: %d panes opened (╭), want %d:\n%s", c.height, got, c.wantPanes, visibleText(frame))
		}
		if got := strings.Count(frame, "╯"); got != c.wantPanes {
			t.Errorf("height %d: %d panes closed (╯), want %d:\n%s", c.height, got, c.wantPanes, visibleText(frame))
		}
	}
}
