package ui

import (
	"testing"
	"time"
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

func TestCompactAge(t *testing.T) {
	now := time.Date(2026, 7, 31, 12, 0, 0, 0, time.UTC)
	cases := []struct {
		name string
		secs int64
		want string
	}{
		{"zero timestamp renders empty", 0, ""},
		{"30 seconds ago is now", now.Add(-30 * time.Second).Unix(), "now"},
		{"5 minutes ago", now.Add(-5 * time.Minute).Unix(), "5m"},
		{"3 hours ago", now.Add(-3 * time.Hour).Unix(), "3h"},
		{"2 days ago", now.Add(-50 * time.Hour).Unix(), "2d"},
		{"future timestamp clamps to now, not negative", now.Add(1 * time.Hour).Unix(), "now"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			if got := compactAge(c.secs, now); got != c.want {
				t.Errorf("compactAge(%d) = %q want %q", c.secs, got, c.want)
			}
		})
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
