package ui

import (
	"strings"
	"testing"
)

// NOTE on deviation from the brief: the brief said to copy gsv's
// sanitize_test.go verbatim. gsv's version integration-tests sanitize()
// through a fully built Model — m.diffRaw, m.renderDiff(), m.View(),
// m.stashes, m.files — none of which exist in ccs's ui package, and can't:
// this file is written at Step 1, before layout.go, model.go, or view.go
// exist at all. A byte-for-byte copy does not compile. These tests instead
// pin sanitize() directly, unit-style, covering the same scenarios gsv's
// suite covered (CRLF+tab content, raw escape sequences, control characters
// in row-shaped text) without requiring a Model. The rendering-pipeline half
// of the guarantee ("every transcript-derived string in View() is sanitized
// before it reaches the frame") is covered separately in view_test.go, once
// View() exists.

func TestSanitizePlainTextUnchanged(t *testing.T) {
	// The fast path: needsSanitizing must short-circuit before sanitizeLine
	// ever runs, so ordinary ASCII text is returned as the exact same value.
	s := "plain ASCII text, nothing to do here"
	if got := sanitize(s); got != s {
		t.Errorf("sanitize(%q) = %q, want unchanged", s, got)
	}
}

func TestSanitizeExpandsTabsToTabStops(t *testing.T) {
	cases := []struct {
		name, in, want string
	}{
		{"tab at column 0 fills to 8", "\tROOT_DIR", strings.Repeat(" ", 8) + "ROOT_DIR"},
		{"tab after one char fills to next stop", "a\tb", "a" + strings.Repeat(" ", 7) + "b"},
		{"two tabs from column 0", "\t\tx", strings.Repeat(" ", 16) + "x"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			if got := sanitize(c.in); got != c.want {
				t.Errorf("sanitize(%q) = %q want %q", c.in, got, c.want)
			}
		})
	}
}

func TestSanitizeDropsCarriageReturns(t *testing.T) {
	in := "line one\r\nline two\r\n"
	got := sanitize(in)
	if strings.Contains(got, "\r") {
		t.Errorf("carriage return survived sanitize: %q", got)
	}
	want := "line one\nline two\n"
	if got != want {
		t.Errorf("sanitize(%q) = %q want %q", in, got, want)
	}
}

func TestSanitizeReplacesControlCharsWithMiddleDot(t *testing.T) {
	in := "a\x01b\x1fc\x7fd"
	want := "a·b·c·d"
	if got := sanitize(in); got != want {
		t.Errorf("sanitize(%q) = %q want %q", in, got, want)
	}
}

// A last-message column is arbitrary transcript content, so it can contain
// raw escape sequences. Those must never survive to reach the terminal.
func TestSanitizeNeutralizesEscapeSequences(t *testing.T) {
	in := "+harmless\n+danger\x1b[2J\x1b[Hcleared\n"
	got := sanitize(in)
	if strings.Contains(got, "\x1b") {
		t.Error("raw ESC survived sanitize")
	}
	if !strings.Contains(got, "danger") || !strings.Contains(got, "cleared") {
		t.Error("neutralizing the escape should not have eaten the surrounding text")
	}
}

func TestSanitizePreservesNewlinesAndResetsTabColumnPerLine(t *testing.T) {
	// "first" is 5 columns wide, so if the tab-stop tracker did NOT reset at
	// the newline, the second line's leading tab would start at column 5 and
	// expand to only 3 spaces (5+3=8) instead of a full 8. Different word
	// lengths on each line make that leak visible in the padding width, not
	// just the (necessarily different) trailing text.
	in := "\tfirst\n\tsecondword"
	got := sanitize(in)
	lines := strings.Split(got, "\n")
	if len(lines) != 2 {
		t.Fatalf("sanitize(%q) = %q, want 2 lines", in, got)
	}
	want := strings.Repeat(" ", 8)
	if !strings.HasPrefix(lines[0], want) || !strings.HasPrefix(lines[1], want) {
		t.Errorf("both lines should start with a full 8-space tab stop (column resets per line): %q", got)
	}
}

// flattenToRow's semantics, stated as cases. The property that matters to the
// list pane — "the result never contains a newline" — is pinned at the row and
// frame level by TestFormatRowIsAlwaysExactlyOneLine and
// TestListPaneDrawsExactlyOneRowPerSession; this pins WHAT the flattened text
// looks like, which those two cannot say much about.
func TestFlattenToRowCollapsesWhitespaceRunsContainingNewlines(t *testing.T) {
	cases := []struct {
		name, in, want string
	}{
		{"no newline is returned untouched", "plain  text\ttab", "plain  text\ttab"},
		{"a newline becomes one space", "first\nsecond", "first second"},
		{
			// The observed message. A blank line is a two-newline run: collapsing
			// each newline separately would leave a two-space gap in a column
			// where every column is scarce.
			"a blank line does not become a run of spaces",
			"Written to `pr.md`.\n\n## Heading",
			"Written to `pr.md`. ## Heading",
		},
		{"indentation around a newline collapses with it", "end of line\n    indented", "end of line indented"},
		{"leading and trailing newlines are trimmed, not turned into spaces", "\n\nbody\n\n", "body"},
		{"a whitespace run with no newline in it is left alone", "two  spaces\nand\ttab", "two  spaces and\ttab"},
		{"newlines only", "\n\n\n", ""},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			got := flattenToRow(c.in)
			if got != c.want {
				t.Errorf("flattenToRow(%q) = %q want %q", c.in, got, c.want)
			}
			if strings.Contains(got, "\n") {
				t.Errorf("flattenToRow(%q) = %q, which still contains a newline", c.in, got)
			}
		})
	}
}

// Multi-byte runes are copied byte by byte, so this pins that the scan does not
// corrupt them — a UTF-8 continuation byte is never mistaken for whitespace.
func TestFlattenToRowPreservesMultiByteRunes(t *testing.T) {
	if got, want := flattenToRow("完了\n完了"), "完了 完了"; got != want {
		t.Errorf("flattenToRow = %q want %q", got, want)
	}
}

func TestNeedsSanitizingFastPath(t *testing.T) {
	cases := []struct {
		in   string
		want bool
	}{
		{"plain text", false},
		{"has\nnewline, still plain", false},
		{"has\ttab", true},
		{"has\rcr", true},
		{"has\x7fdel", true},
		{"has\x1bescape", true},
	}
	for _, c := range cases {
		if got := needsSanitizing(c.in); got != c.want {
			t.Errorf("needsSanitizing(%q) = %v want %v", c.in, got, c.want)
		}
	}
}
