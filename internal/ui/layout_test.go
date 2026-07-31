package ui

import (
	"testing"
	"time"

	"github.com/JonathanAriass/ccs/internal/session"
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
	}
	for _, c := range cases {
		if got := truncateToWidth(c.in, c.w); got != c.want {
			t.Errorf("truncateToWidth(%q,%d) = %q want %q", c.in, c.w, got, c.want)
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
	}
	for _, c := range cases {
		got := elideMiddle(c.in, c.w)
		if len([]rune(got)) > c.w {
			t.Errorf("elideMiddle(%q,%d) = %q, too wide (%d)", c.in, c.w, got, len([]rune(got)))
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
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			if got := wrapToWidth(c.in, c.w); got != c.want {
				t.Errorf("wrapToWidth(%q,%d) = %q want %q", c.in, c.w, got, c.want)
			}
		})
	}
}
