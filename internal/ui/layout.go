package ui

import (
	"fmt"
	"strings"
	"time"

	"github.com/charmbracelet/x/ansi"
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

	// footerLines is the rows View reserves below the panes for the status line
	// and the key legend.
	footerLines = 2
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

// paneInnerHeight returns the rows available to text inside a pane of the given
// outer height. There is no vertical-padding term because paneStyle is
// Padding(0, 1) — it pads horizontally only, which is why panePadW exists and
// panePadH does not. If that padding ever gains a vertical component, this
// function needs the matching term.
func paneInnerHeight(outerH int) int { return max(0, outerH-paneBorderH) }

// bodyPaneHeight is the height the two panes get: the terminal minus the
// status/legend footer.
//
// View and syncPreview must agree on this exactly. If they drift, the viewport
// is sized for a pane of one height while being rendered into a pane of
// another — so both call this rather than each computing m.height-footerLines.
func bodyPaneHeight(termH int) int { return max(1, termH-footerLines) }

// previewBodyHeight is how many rows the scrollable exchange gets: whatever the
// preview pane's interior has left after the pinned metadata block. Never
// negative — not because SetContent would panic (it does not; verified
// against bubbles v1.0.0), but because a negative Height inflates
// maxYOffset() (len(lines) - Height + frame size), letting the viewport
// scroll past the end of its own content.
func previewBodyHeight(paneInnerH, metadataLines int) int {
	h := paneInnerH - metadataLines
	if h < 0 {
		return 0
	}
	return h
}

// previewFits reports whether the preview pane has enough interior room, at
// terminal height termH, to show at least one row of the actual exchange —
// not just its pinned metadata block.
//
// viewport.View() at Height 0 still emits ONE line (lipgloss treats Height(0)
// as unset — see previewBodyHeight), so the pane's interior needs room for
// previewMetadataLines PLUS one more row, or the viewport's single emitted
// line overruns the pane and MaxHeight clips the pane's own bottom border off
// the frame — a pane that shows zero lines of the exchange (the entire point
// of the pane) while ALSO breaking the frame around it. Below that threshold,
// rendering no preview pane at all is strictly better: see
// (Model).previewVisible, which both View and handleKey consult so they
// cannot disagree about whether the pane is on screen.
//
// previewMetadataLines is defined in view.go, not here — this stays a pure
// function of termH so it can be pinned directly (TestPreviewFitsThreshold)
// without building a styled frame.
func previewFits(termH int) bool {
	return paneInnerHeight(bodyPaneHeight(termH)) >= previewMetadataLines+1
}

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

// wrapToWidth hard-wraps s into lines of at most w DISPLAY COLUMNS each, so
// long transcript text does not overflow the preview pane's border. w<=0 is
// treated as "no limit" (the caller has nothing sane to wrap to).
//
// Columns, not runes, and the difference is not cosmetic. This wraps the
// preview's exchange, which is then handed to a viewport whose Width and whose
// lipgloss rendering both budget in display columns. A rune budget makes a
// CJK/emoji line up to twice the pane's width; lipgloss re-wraps each such
// logical line onto two screen rows, and viewport.View()'s MaxHeight then
// hard-clips the surplus rows off the bottom. Because TotalLineCount counts
// LOGICAL lines, the viewport believes the content fits: scrollIndicator sees
// total <= height and prints nothing, maxYOffset is 0, and j is a silent no-op
// — the end of the message is unreachable AND advertised as complete, which is
// the exact defect the scrolling preview exists to remove. (view.go's formatRow
// documents the same rune-vs-column hazard for list rows and fixes it there
// with ansi.Truncate.)
//
// ansi.Hardwrap is the GRAPHEME-based variant, deliberately, not HardwrapWc:
// lipgloss v1.1.0 measures with ansi.StringWidth, which is grapheme-based, and
// viewport.View() sizes its content with lipgloss Style.Width/MaxWidth. Using
// the wide-character/rune variant would budget by a different measure than the
// code that consumes the result, and would break grapheme clusters (a ZWJ emoji
// sequence) across lines. ansi is not lipgloss, so this keeps layout.go free of
// the lipgloss import.
//
// One inherent limit: a single grapheme wider than w cannot be made to fit, so
// its line is w+1 columns. Unreachable in this program — the preview pane's
// interior is at least 12 columns at minTermWidth — but worth knowing before
// this is reused somewhere narrower.
func wrapToWidth(s string, w int) string {
	if w <= 0 {
		return s
	}
	return ansi.Hardwrap(s, w, true)
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

// compactAge renders a status-transition instant as a short relative age for
// the list row: "now", "5m", "3h", "2d".
//
// It takes a time.Time, NOT a raw registry integer: the registry stores
// statusUpdatedAt in milliseconds, and an earlier version of this function took
// the int64 and read it as seconds, which put every timestamp in the year 58544
// and made every row render "now". The unit conversion belongs next to the json
// tag (session.Session.StatusUpdatedTime), so this function cannot be handed a
// number in the wrong unit at all.
//
// A zero timestamp (never set — an older/malformed registry entry) renders as ""
// rather than a nonsensical multi-decade age. A timestamp in the future (clock
// skew between the hook that wrote it and this machine) clamps to "now" instead
// of going negative.
func compactAge(t, now time.Time) string {
	if t.UnixMilli() <= 0 {
		return ""
	}
	d := now.Sub(t)
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

// scrollPages is how many page-sized chunks the content occupies, or 0 when it
// fits and there is nothing to indicate.
func scrollPages(total, height int) int {
	if height <= 0 || total <= height {
		return 0
	}
	return (total + height - 1) / height
}

// widestScrollIndicator is the WIDEST string scrollIndicator can return for
// this content, whatever the offset: the same "N/M" with N as wide as M.
//
// The caller decides whether the page indicator fits in the pane title, and
// that decision must not depend on which page the reader happens to be on. A
// per-page decision made the indicator fit through "9/12" and stop fitting at
// "10/12", so at widths 43-45 it showed for nine pages and then vanished —
// and an indicator that disappears as you scroll reads as "there is no more
// content", inverting the very thing it is there to say.
//
// The current page is always <= pages (scrollIndicator's own at-bottom branch
// caps it there), so this really is the widest form.
func widestScrollIndicator(total, height int) string {
	pages := scrollPages(total, height)
	if pages == 0 {
		return ""
	}
	return fmt.Sprintf("%d/%d", pages, pages)
}

// scrollIndicator renders "2/3" when content overflows its pane, and "" when it
// fits.
//
// Without it a clipped message looks identical to a complete one — which is the
// defect this whole feature exists to remove, merely relocated from the pane
// body to the pane title.
func scrollIndicator(total, height, offset int) string {
	pages := scrollPages(total, height)
	if pages == 0 {
		return ""
	}
	page := offset/height + 1
	// page reports which page-sized CHUNK the top visible line falls in. The
	// last screenful straddles two chunks whenever height does not evenly
	// divide total, so the top line never enters the final chunk and page
	// would sit at pages-1 with nothing left to read — inverting the whole
	// point of this indicator (a complete message reading as clipped).
	//
	// offset >= total-height is "the viewport is scrolled as far as it can
	// go": total-height is bubbles' own maxYOffset PROVIDED the viewport's
	// Style carries no vertical frame size, which is true everywhere in this
	// codebase (m.preview.Style is never set — see
	// TestPreviewViewportStyleHasNoFrameSize). At that offset the reader is
	// on the last line available, so the last page is exactly what they're
	// on.
	if offset >= total-height {
		page = pages
	}
	return fmt.Sprintf("%d/%d", page, pages)
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
