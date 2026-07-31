package ui

import (
	"fmt"
	"strings"
	"time"
)

// Geometry constants.
const (
	listFraction = 0.6 // list takes 60% of the width, preview the rest
	minPaneWidth = 10
)

// Geometry constants for a bordered, horizontally padded pane.
//
// lipgloss sizes a style's box *inside* its border: Style.Width(n) makes the
// content+padding block n columns wide and then draws the border around it,
// so the pane actually occupies n+paneBorderW columns on screen. The same
// holds vertically. Everything below is expressed in *outer* dimensions (what
// the pane really costs on screen) and converted at the point of rendering.
const (
	paneBorderW = 2 // left + right border columns
	paneBorderH = 2 // top + bottom border rows
	panePadW    = 2 // Padding(0, 1): left + right

	// listCursorWidth is the visible width consumed by the "› " / "  " prefix
	// renderList prepends to every row.
	listCursorWidth = 2
)

// paneWidths partitions the terminal width between list and preview.
func paneWidths(total int) (list, preview int) {
	if total < minPaneWidth*2 {
		return total, 0
	}
	list = int(float64(total) * listFraction)
	return list, total - list
}

// paneStyleWidth / paneStyleHeight convert an outer pane size into the value
// handed to lipgloss Style.Width / Style.Height.
func paneStyleWidth(outerW int) int  { return max(0, outerW-paneBorderW) }
func paneStyleHeight(outerH int) int { return max(0, outerH-paneBorderH) }

// paneInnerWidth returns the columns available to text inside a pane of the
// given outer width, after both its border and its horizontal padding.
func paneInnerWidth(outerW int) int { return max(0, outerW-paneBorderW-panePadW) }

// truncateToWidth clips s to w columns, marking the cut with an ellipsis.
func truncateToWidth(s string, w int) string {
	if w <= 0 {
		return ""
	}
	r := []rune(s)
	if len(r) <= w {
		return s
	}
	if w == 1 {
		return "…"
	}
	return string(r[:w-1]) + "…"
}

// elideMiddle shortens a path from the middle, keeping both ends.
//
// The tail is what distinguishes two sessions in the same repo (a worktree
// name), so truncating from the right would make them indistinguishable.
func elideMiddle(s string, w int) string {
	r := []rune(s)
	if len(r) <= w || w <= 0 {
		return s
	}
	if w <= 3 {
		return strings.Repeat("…", w)
	}
	keep := w - 1
	head := keep / 2
	tail := keep - head
	return string(r[:head]) + "…" + string(r[len(r)-tail:])
}

// wrapToWidth hard-wraps s into lines of at most w runes each, so long
// transcript text does not overflow the preview pane's border. It is
// rune-aware so multibyte UTF-8 text is never split mid-rune. w<=0 is treated
// as "no limit" (the caller has nothing sane to wrap to).
func wrapToWidth(s string, w int) string {
	if w <= 0 {
		return s
	}
	r := []rune(s)
	var b strings.Builder
	for len(r) > w {
		b.WriteString(string(r[:w]) + "\n")
		r = r[w:]
	}
	b.WriteString(string(r))
	return b.String()
}

// rowWidths splits a session row's text budget between the name, directory,
// and last-message columns. msg absorbs the remainder so the three always sum
// to exactly total (no rounding gap, the way three-way integer division can
// leave one).
func rowWidths(total int) (name, dir, msg int) {
	if total <= 0 {
		return 0, 0, 0
	}
	name = total * 3 / 10
	dir = total * 3 / 10
	msg = total - name - dir
	return
}

// compactAge renders a Session's StatusUpdatedAt (unix seconds) as a short
// relative age for the list row: "now", "5m", "3h", "2d". A zero timestamp
// (never set — an older/malformed registry entry) renders as "" rather than
// a nonsensical multi-decade age. A timestamp in the future (clock skew
// between the hook that wrote it and this machine) clamps to "now" instead of
// going negative.
func compactAge(unixSec int64, now time.Time) string {
	if unixSec <= 0 {
		return ""
	}
	d := now.Sub(time.Unix(unixSec, 0))
	if d < 0 {
		d = 0
	}
	switch {
	case d < time.Minute:
		return "now"
	case d < time.Hour:
		return fmt.Sprintf("%dm", int(d.Minutes()))
	case d < 24*time.Hour:
		return fmt.Sprintf("%dh", int(d.Hours()))
	default:
		return fmt.Sprintf("%dd", int(d.Hours()/24))
	}
}

// homeAbbrev replaces a leading home-directory prefix with "~", the way a
// shell prompt does. It only matches at a path separator boundary — home
// "/Users/x" must not abbreviate "/Users/xavier/proj", a different user's
// directory that merely starts with the same characters.
func homeAbbrev(path, home string) string {
	if home == "" {
		return path
	}
	if path == home {
		return "~"
	}
	if strings.HasPrefix(path, home+"/") {
		return "~" + path[len(home):]
	}
	return path
}
