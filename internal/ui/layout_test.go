package ui

import (
	"strings"
	"testing"
	"time"

	"github.com/JonathanAriass/ccs/internal/session"
	// lipgloss is deliberately absent from layout.go itself (the pane math must
	// not depend on the rendering library), but the width assertions below
	// measure wrapToWidth's output the way its CONSUMER does — and its consumer
	// is a viewport rendered through lipgloss. Asserting in any other unit would
	// be asserting against the wrong authority.
	"github.com/charmbracelet/lipgloss"
)

func TestPaneWidths(t *testing.T) {
	cases := []struct{ total, wantList, wantPreview int }{
		{120, 72, 48},
		{80, 48, 32},
		{40, 24, 16},
	}
	for _, c := range cases {
		l, p := paneWidths(c.total)
		if l != c.wantList || p != c.wantPreview {
			t.Errorf("paneWidths(%d) = (%d,%d) want (%d,%d)", c.total, l, p, c.wantList, c.wantPreview)
		}
		if l+p != c.total {
			t.Errorf("paneWidths(%d) does not partition the width: %d+%d", c.total, l, p)
		}
	}
}

// TestTruncateToWidth pins DISPLAY-COLUMN behaviour (lipgloss.Width, the same
// measure wrapToWidth's own tests use — see the import comment above), not
// rune count. The double-width cases are what a rune budget gets wrong: a
// budget of runes lets a CJK/emoji string through at up to twice its declared
// column width, which is the defect wrapToWidth's doc comment describes at
// length for the wrap path and this function shared until now for the
// truncate path.
func TestTruncateToWidth(t *testing.T) {
	cases := []struct {
		in   string
		w    int
		want string
	}{
		{"hello", 10, "hello"},
		{"hello world", 8, "hello w…"},
		{"hi", 1, "…"},
		{"", 5, ""},
		{"hello", 0, ""},
		// All double-width. A rune budget of 6 would keep 5 runes (10 columns)
		// plus an ellipsis — nearly double the declared width. The real
		// (column) budget can't fit a 6th double-width rune AND the ellipsis
		// in the last column, so it stops one rune early rather than split
		// one: exactly the "rounds under, never over" behaviour ansi.Truncate
		// already gives formatRow's Version/TTY fields.
		{strings.Repeat("完", 5), 6, "完完…"},
		// Exact fit: width equals the budget precisely, so nothing is cut.
		{strings.Repeat("完", 5), 10, "完完完完完"},
		// Mixed ascii + emoji, the shape real transcript text takes.
		{"ascii" + strings.Repeat("✅", 5), 8, "ascii✅…"},
	}
	for _, c := range cases {
		got := truncateToWidth(c.in, c.w)
		if got != c.want {
			t.Errorf("truncateToWidth(%q,%d) = %q want %q", c.in, c.w, got, c.want)
		}
		if w := lipgloss.Width(got); w > c.w {
			t.Errorf("truncateToWidth(%q,%d) = %q, %d display columns, want <= %d", c.in, c.w, got, w, c.w)
		}
	}
}

func TestElideMiddle(t *testing.T) {
	// Directory paths elide in the MIDDLE — the tail identifies the worktree
	// and is what distinguishes two sessions in the same repo.
	cases := []struct {
		in   string
		w    int
		want string
	}{
		{"~/Desktop/okt-api", 30, "~/Desktop/okt-api"},
		// The brief's own worked example for this fixture ("~/Desktop/ok…-detracciones-mx")
		// does not match what its own reference elideMiddle implementation actually
		// produces for these inputs (verified: head=14, tail=15 runes for w=30, giving
		// the value below, which is exactly 30 runes and keeps both ends as intended —
		// only the brief's hand-written "want" was off). Corrected to the true output.
		{"~/Desktop/okt-api/.claude/worktrees/OKT-18841-detracciones-mx", 30, "~/Desktop/okt-…detracciones-mx"},
		// All double-width, budget 10. A RUNE budget (the old implementation)
		// keeps 4 head runes (8 columns) + 5 tail runes (10 columns) + the
		// ellipsis = 19 display columns for a "10-wide" result — verified
		// against the old implementation directly, restored only for this
		// comment. The column-aware version keeps the same shape but measures
		// in the unit its own contract promises.
		{strings.Repeat("完", 20), 10, "完完…完完"},
		// Mixed ascii + CJK: the head and tail land on ascii, so this also
		// pins that a boundary landing exactly between an ascii and a
		// double-width run does not miscount either side.
		{"prefix-" + strings.Repeat("完", 10) + "-suffix", 14, "prefix…-suffix"},
	}
	for _, c := range cases {
		got := elideMiddle(c.in, c.w)
		if w := lipgloss.Width(got); w > c.w {
			t.Errorf("elideMiddle(%q,%d) = %q, %d display columns, want <= %d", c.in, c.w, got, w, c.w)
		}
		if c.want != "" && got != c.want {
			t.Errorf("elideMiddle(%q,%d) = %q want %q", c.in, c.w, got, c.want)
		}
	}
}

// --- Guards added beyond the brief for view.go's row/preview composition ---

func TestPaneWidthsTooNarrowGivesAllToList(t *testing.T) {
	// Strictly below minPaneWidth*2 (20) there isn't enough room for two panes
	// at all; the preview pane collapses to 0 rather than going negative.
	for _, total := range []int{0, 1, 19} {
		l, p := paneWidths(total)
		if p != 0 {
			t.Errorf("paneWidths(%d) preview = %d, want 0 below the two-pane floor", total, p)
		}
		if l != total {
			t.Errorf("paneWidths(%d) list = %d, want the whole width (%d)", total, l, total)
		}
	}
}

// TestPaneWidthsBoundary pins the "<" in "total < minPaneWidth*2" exactly at
// the boundary: 19 must take the too-narrow branch (proven above), 20 must
// NOT — a "<=" mutation would wrongly force 20 through the narrow branch too.
func TestPaneWidthsBoundary(t *testing.T) {
	l, p := paneWidths(minPaneWidth * 2)
	if p == 0 {
		t.Errorf("paneWidths(%d) = (%d,%d), want the normal split at exactly the floor, not the too-narrow branch", minPaneWidth*2, l, p)
	}
}

func TestElideMiddleShortInputUnchanged(t *testing.T) {
	in := "short"
	if got := elideMiddle(in, 30); got != in {
		t.Errorf("elideMiddle(%q,30) = %q, want unchanged", in, got)
	}
}

func TestElideMiddleTinyWidth(t *testing.T) {
	// w<=3 has no room for head+…+tail, so the whole budget goes to dots.
	if got := elideMiddle("this is a long path", 2); got != "……" {
		t.Errorf("elideMiddle at w=2 = %q, want %q", got, "……")
	}
}

func TestRowWidthsPartitionsExactly(t *testing.T) {
	for _, total := range []int{0, 1, 7, 30, 61, 100} {
		name, dir, msg := rowWidths(total)
		if name < 0 || dir < 0 || msg < 0 {
			t.Errorf("rowWidths(%d) = (%d,%d,%d), negative field", total, name, dir, msg)
		}
		if got := name + dir + msg; got != total {
			t.Errorf("rowWidths(%d) sums to %d, want %d", total, got, total)
		}
	}
}

// TestCompactAge drives every case through session.Session's own
// StatusUpdatedTime conversion, in MILLISECONDS — the exact path production
// takes — rather than hand-building a time.Time here.
//
// The previous version of this table computed every fixture with .Unix()
// (seconds). It was fully green while the age column was completely dead:
// production supplies milliseconds, which read as seconds land in the year
// 58544, so every duration underflowed, the clock-skew clamp swallowed the
// negative, and every row on screen rendered "now" — including sessions idle
// for weeks. A fixture in a unit production never supplies proves nothing about
// production.
func TestCompactAge(t *testing.T) {
	now := time.Date(2026, 7, 31, 12, 0, 0, 0, time.UTC)
	at := func(d time.Duration) int64 { return now.Add(d).UnixMilli() }
	cases := []struct {
		name string
		ms   int64
		want string
	}{
		{"zero timestamp renders empty", 0, ""},
		{"30 seconds ago is now", at(-30 * time.Second), "now"},
		{"5 minutes ago", at(-5 * time.Minute), "5m"},
		{"3 hours ago", at(-3 * time.Hour), "3h"},
		{"2 days ago", at(-50 * time.Hour), "2d"},
		{"future timestamp clamps to now, not negative", at(time.Hour), "now"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			s := session.Session{StatusUpdatedAt: c.ms}
			if got := compactAge(s.StatusUpdatedTime(), now); got != c.want {
				t.Errorf("compactAge(%d ms) = %q want %q", c.ms, got, c.want)
			}
		})
	}
}

// TestCompactAgeOnARealRegistryTimestamp is the regression test for the
// milliseconds-read-as-seconds bug, and it uses a value copied VERBATIM from a
// live ~/.claude/sessions entry (the same literal internal/session's TestLoad
// carries) rather than one derived from time.Now().
//
// That is the whole point: any fixture this test computes for itself can be
// computed in the wrong unit, and then the test agrees with the bug. A real
// registry value cannot be — it is the input production actually gets.
func TestCompactAgeOnARealRegistryTimestamp(t *testing.T) {
	const registryMs int64 = 1785322956268 // "statusUpdatedAt" straight out of a live session file
	s := session.Session{StatusUpdatedAt: registryMs}

	// Pin the decoded instant first. Read as SECONDS this same number is the
	// year 58544, so this assertion fails on the exact mutation that caused the
	// bug — before any duration arithmetic can hide it behind the skew clamp.
	if y := s.StatusUpdatedTime().UTC().Year(); y != 2026 {
		t.Fatalf("a real registry timestamp decoded to year %d, want 2026 — it is being read in the wrong unit", y)
	}

	now := s.StatusUpdatedTime().Add(43 * 24 * time.Hour)
	if got := compactAge(s.StatusUpdatedTime(), now); got != "43d" {
		t.Errorf("compactAge(real registry value, 43 days later) = %q, want %q", got, "43d")
	}
}

func TestHomeAbbrev(t *testing.T) {
	cases := []struct {
		name, path, home, want string
	}{
		{"exact home", "/Users/x", "/Users/x", "~"},
		{"under home", "/Users/x/Desktop/proj", "/Users/x", "~/Desktop/proj"},
		{"outside home unchanged", "/tmp/proj", "/Users/x", "/tmp/proj"},
		{"lookalike sibling is NOT abbreviated", "/Users/xavier/proj", "/Users/x", "/Users/xavier/proj"},
		{"empty home leaves path unchanged", "/Users/x/proj", "", "/Users/x/proj"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			if got := homeAbbrev(c.path, c.home); got != c.want {
				t.Errorf("homeAbbrev(%q,%q) = %q want %q", c.path, c.home, got, c.want)
			}
		})
	}
}

func TestPreviewBodyHeight(t *testing.T) {
	cases := []struct {
		name                 string
		paneInnerH, metadata int
		want                 int
	}{
		{"normal", 20, 9, 11},
		{"metadata fills the pane", 9, 9, 0},
		{"metadata exceeds the pane", 6, 9, 0},
		{"degenerate pane", 0, 9, 0},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			if got := previewBodyHeight(c.paneInnerH, c.metadata); got != c.want {
				t.Errorf("previewBodyHeight(%d, %d) = %d, want %d",
					c.paneInnerH, c.metadata, got, c.want)
			}
		})
	}
}

// TestPreviewFitsThresholdIsExactlyHeight14 pins previewFits' boundary in
// BOTH directions, at the pure-function level, independent of any rendering.
// Pinning only one side (e.g. "13 must not fit") leaves the other free to
// drift — a mutant that changed ">=" to ">" would still fail 13 but wrongly
// pass 14 through as well, and this table catches that.
//
// The exact numbers: bodyPaneHeight(14) = 12, paneInnerHeight(12) = 10, and
// previewMetadataLines (9) + 1 = 10 — so 14 lands EXACTLY on the boundary,
// not comfortably inside it. That is deliberate coverage, not an arbitrary
// choice of fixture: it is the value view.go's own TestPreviewPaneRendersAtHeight14ButNotHeight13
// exercises end to end, so this test and that one must agree.
func TestPreviewFitsThresholdIsExactlyHeight14(t *testing.T) {
	cases := []struct {
		termH int
		want  bool
	}{
		{8, false},
		{10, false},
		{13, false}, // one below the boundary
		{14, true},  // the boundary itself
		{15, true},
		{30, true},
	}
	for _, c := range cases {
		if got := previewFits(c.termH); got != c.want {
			t.Errorf("previewFits(%d) = %v, want %v", c.termH, got, c.want)
		}
	}
}

func TestBodyPaneHeightNeverDropsBelowOne(t *testing.T) {
	// lipgloss Height(0) and negative heights both misrender. View already
	// clamps; bodyPaneHeight is now the single place that does it.
	for _, termH := range []int{0, 1, 2, 3, 30} {
		if got := bodyPaneHeight(termH); got < 1 {
			t.Errorf("bodyPaneHeight(%d) = %d, want >= 1", termH, got)
		}
	}
	if got := bodyPaneHeight(30); got != 28 {
		t.Errorf("bodyPaneHeight(30) = %d, want 28", got)
	}
}

func TestScrollIndicator(t *testing.T) {
	// Pins BOTH directions. Testing only "long content shows a marker" leaves
	// the short-content case free to start showing one; testing only the empty
	// case leaves the marker free to vanish entirely.
	cases := []struct {
		name                  string
		total, height, offset int
		want                  string
	}{
		{"fits exactly — no marker", 10, 10, 0, ""},
		{"shorter than the pane — no marker", 3, 10, 0, ""},
		{"overflows, at the top", 30, 10, 0, "1/3"},
		{"overflows, second page", 30, 10, 10, "2/3"},
		{"overflows, last page (height divides total)", 30, 10, 20, "3/3"},
		{"partial last page rounds up", 25, 10, 20, "3/3"},
		// At-bottom rows for Important #5: the LAST screenful straddles two
		// page-sized chunks whenever height does not divide total, so the top
		// visible line's own chunk (what `page` naively reports) sits one
		// short of `pages` at exactly the offset a scrolled-to-the-end
		// viewport holds. Each of the two below is red on the pre-fix
		// formula (page/height+1 alone gives "2/3" and "5/6" respectively,
		// not the true last page) and green with the at-bottom branch.
		{"at the bottom, partial last page", 25, 10, 15, "3/3"},
		{"at the bottom, real fixture size", 95, 17, 78, "6/6"},
		// Boundary check: one row short of the bottom must NOT be treated as
		// the last page — pins that the at-bottom branch fires exactly at
		// total-height, not one early.
		{"one line above the bottom is NOT the last page", 25, 10, 14, "2/3"},
		{"degenerate height", 30, 0, 0, ""},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			if got := scrollIndicator(c.total, c.height, c.offset); got != c.want {
				t.Errorf("scrollIndicator(%d,%d,%d) = %q, want %q",
					c.total, c.height, c.offset, got, c.want)
			}
		})
	}
}

func TestWrapToWidth(t *testing.T) {
	cases := []struct {
		name string
		in   string
		w    int
		want string
	}{
		{"short line unchanged", "hello", 10, "hello"},
		{"exact width unchanged", "hello", 5, "hello"},
		{"wraps at width", "hello world", 5, "hello\n worl\nd"},
		{"zero width returns input unchanged", "hello", 0, "hello"},
		// The budget is DISPLAY COLUMNS, not runes. Three double-width runes
		// are 6 columns, so a 6-column budget takes exactly three of them per
		// line — a rune budget would take six and produce a 12-column line.
		{"double-width runes wrap by column, not by rune", strings.Repeat("完", 6), 6, "完完完\n完完完"},
		// An odd budget cannot be filled exactly by double-width runes: the
		// line stops one column short rather than spilling one over.
		{"double-width runes never straddle the budget", strings.Repeat("完", 4), 5, "完完\n完完"},
		// Mixed width, wrapping mid-run.
		{"mixed narrow and wide", "ab完cd", 4, "ab完\ncd"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			if got := wrapToWidth(c.in, c.w); got != c.want {
				t.Errorf("wrapToWidth(%q,%d) = %q want %q", c.in, c.w, got, c.want)
			}
		})
	}
}

// TestWrapToWidthNeverExceedsItsBudgetInDisplayColumns is the property the
// table above spells out by example, asserted the way the CONSUMER measures.
//
// wrapToWidth's output goes straight into a viewport whose Width and whose
// lipgloss rendering both budget in display columns (ansi.StringWidth, which is
// what lipgloss.Width calls). A rune-budgeted wrap satisfies every "does it
// look right" table above with ASCII and still hands the viewport lines twice
// the pane's width the moment a transcript contains CJK or emoji — lipgloss
// re-wraps each onto two screen rows and MaxHeight clips the surplus, while
// TotalLineCount (logical lines) reports the content as fitting. Measuring in
// the consumer's own unit is what makes that impossible rather than merely
// unlikely.
//
// The ZWJ-cluster input below is also the one thing in this file that pins
// ansi.Hardwrap over ansi.HardwrapWc specifically. Every OTHER input here
// measures identically under both — GraphemeWidth and WcWidth agree on plain
// CJK and on "✅" — so swapping wrapToWidth to HardwrapWc left this whole test
// green until this case was added. A pride-flag sequence
// (U+1F3F3 U+FE0F U+200D U+1F308) is where the two measures actually disagree:
// GraphemeWidth (what lipgloss.Width and this test both use) scores it 2
// columns, WcWidth scores it 1. Verified directly: swapping the call inside
// wrapToWidth to ansi.HardwrapWc makes THIS input overflow its budget at every
// width below, while every other input in this table stays unaffected.
func TestWrapToWidthNeverExceedsItsBudgetInDisplayColumns(t *testing.T) {
	inputs := []string{
		strings.Repeat("完了", 100),          // all double-width
		strings.Repeat("word ", 40),        // all single-width
		"ascii " + strings.Repeat("✅", 30), // mixed, emoji
		strings.Repeat("完了 mixed ", 20),    // interleaved
		"already\nhas\nlines " + strings.Repeat("完", 40),
		// ZWJ emoji cluster (pride-flag sequence: white flag U+1F3F3 + VS16
		// U+FE0F + ZWJ U+200D + rainbow U+1F308), spelled out as explicit
		// escapes rather than pasted invisible bytes so the sequence stays
		// legible in source. The only input in this table that distinguishes
		// Hardwrap from HardwrapWc — see the doc comment above.
		strings.Repeat("\U0001F3F3\uFE0F\u200D\U0001F308 word ", 30),
	}
	for _, w := range []int{4, 12, 36, 80} {
		for _, in := range inputs {
			for i, line := range strings.Split(wrapToWidth(in, w), "\n") {
				if got := lipgloss.Width(line); got > w {
					t.Errorf("wrapToWidth(_, %d): line %d is %d display columns: %q",
						w, i, got, line)
				}
			}
		}
	}
}
