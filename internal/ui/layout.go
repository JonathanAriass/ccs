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

// listTitleLines is how many rows renderList spends on its title before the
// first session row. Same contract as previewMetadataLines: the number is
// pinned to the real output by TestListTitleIsExactlyOneLine, and the rendered
// output is the authority.
const listTitleLines = 1

// listCapacity is how many session ROWS the list pane can draw inside a pane of
// the given outer height: its interior, minus the title.
//
// Drawing more than this does not merely overflow harmlessly. lipgloss renders
// the pane's content and then MaxHeight-clips the whole box to its outer
// height, and the bottom border is the LAST row of that box — so every row past
// the interior costs the pane its bottom border, not the row itself. The list
// then has no bottom edge at all and the frame's own structure is gone. With a
// realistic fifteen live sessions that was every terminal height from
// minTermHeight through 19; see listWindow for what is drawn instead.
func listCapacity(paneOuterH int) int {
	return max(0, paneInnerHeight(paneOuterH)-listTitleLines)
}

// listWindow returns the half-open range [first, first+n) of session rows the
// list pane should draw, given how many there are, where the cursor is, and how
// many rows fit (listCapacity).
//
// The window ALWAYS contains the cursor, and that is the load-bearing property:
// a window that clipped the selected row would leave the user pressing ⏎ on a
// session they cannot see, which is the one action this tool exists to perform.
//
// It is a pure function of (total, cursor, capacity) rather than a scroll
// offset stored on the Model, deliberately. An offset would have to be
// maintained by every path that moves the cursor (j, k, a re-sorting poll that
// relocates the selection, a resize that changes the capacity) and by nothing
// else — four places that must agree, i.e. four places that can drift. Deriving
// the window means View and any future consumer cannot disagree about it.
//
// The cursor is kept mid-pane where possible (rather than only scrolling when
// it would fall off an edge, which no stateless function can do) so the
// selected row sits in a stable screen position while the list moves under it.
// Out-of-range cursors — reconcile can leave -1 on an empty list, and tests set
// it deliberately — clamp to the nearest valid window rather than panicking.
func listWindow(total, cursor, capacity int) (first, n int) {
	if capacity <= 0 || total <= 0 {
		return 0, 0
	}
	if total <= capacity {
		return 0, total
	}
	first = cursor - capacity/2
	if first > total-capacity {
		first = total - capacity
	}
	if first < 0 {
		first = 0
	}
	return first, capacity
}

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

// truncateToWidth clips s to w DISPLAY COLUMNS, marking the cut with an
// ellipsis.
//
// Columns, not runes — the same distinction wrapToWidth's doc comment makes
// at length. Both of formatRow's call sites sit behind its own ansi.Truncate
// safety net, which is the only reason a rune budget here was ever harmless;
// that stops being true the moment anything calls this without such a net, so
// it budgets in columns directly rather than relying on a caller to paper over
// the gap a second time. Note the net bounds the ROW, not this function's
// result: a rune budget here would still cost the fields drawn after it (see
// formatRow's own comment).
func truncateToWidth(s string, w int) string {
	if w <= 0 {
		return ""
	}
	return ansi.Truncate(s, w, "…")
}

// elideMiddle shortens a path from the middle, keeping both ends, to w
// DISPLAY COLUMNS.
//
// The tail is what distinguishes two sessions in the same repo (a worktree
// name), so truncating from the right would make them indistinguishable.
//
// A nonpositive w means there is no room for ANY of the string, matching
// truncateToWidth. It used to mean the opposite here — the whole string came
// back unclipped, precisely when the caller had said there was no space — and
// two sibling helpers documented identically must not disagree about that.
// Unreachable today: sweeping every terminal size the program renders a frame in
// bottoms w out at 2, and below minTermWidth the "Terminal too small" notice
// replaces the row entirely. So this is contract hygiene, not a layout
// guarantee — if dirW can ever legitimately reach 0, the fix belongs in
// rowWidths (give dir a floor, or drop the column and reallocate its budget),
// not in the elision helper.
func elideMiddle(s string, w int) string {
	// Its own statement, deliberately, rather than folded into the w <= 3 branch
	// below: strings.Repeat PANICS on a negative count, so letting that branch
	// absorb this case would be correct for w == 0 and a panic for w < 0.
	if w <= 0 {
		return ""
	}
	sw := ansi.StringWidth(s)
	if sw <= w {
		return s
	}
	if w <= 3 {
		return strings.Repeat("…", w)
	}
	keep := w - 1
	headW := keep / 2
	tailW := keep - headW

	// ansi.Truncate rounds DOWN when a grapheme straddles the cut point (drops
	// it rather than split it), so the head can never come back wider than
	// headW.
	head := ansi.Truncate(s, headW, "")

	// ansi.Cut's left boundary is not symmetric with that: when IT lands mid-
	// grapheme it rounds OUTWARD, keeping the straddling cluster whole rather
	// than dropping it — so Cut(s, sw-tailW, sw) can hand back MORE than
	// tailW columns (verified: a 20-rune, all-double-width fixture at w=10
	// came back 11 columns wide, one column over budget). Walking the left
	// boundary rightward one column at a time until the slice actually fits
	// corrects that: each step only drops whichever cluster straddled the
	// previous boundary, and the loop is bounded (tailLeft can rise at most to
	// sw, where the slice is empty and trivially fits).
	tailLeft := sw - tailW
	tail := ansi.Cut(s, tailLeft, sw)
	for ansi.StringWidth(tail) > tailW && tailLeft < sw {
		tailLeft++
		tail = ansi.Cut(s, tailLeft, sw)
	}

	return head + "…" + tail
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
// One inherent limit: a single grapheme wider than w cannot be made to fit.
// Measured: wrapToWidth("完", 1) returns "\n完" — ansi.Hardwrap sees curWidth 0
// + width 2 > limit 1 and starts a new line before placing the grapheme even
// though curWidth was already 0, so the result is a leading BLANK line
// followed by the over-wide grapheme's own (2-column) line. That is an extra
// LINE, not merely a line of w+1 columns. Unreachable in this program — the
// preview pane's interior is at least 12 columns at minTermWidth — but worth
// knowing before this is reused somewhere narrower.
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

// compactAge renders an instant as a short relative age: "now", "5m", "3h",
// "2d" — a status-transition instant for the list row, or (since Task 2) a
// live-source mtime for the preview pane's Activity line. Same format either
// way; the caller decides what instant it means.
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

// layoutMode is the user's layout preference. The zero value is auto.
type layoutMode int

const (
	layoutAuto layoutMode = iota
	layoutStacked
	layoutWide
)

// stackBreakpoint is the auto-mode width below which the panes stack. At 90,
// side-by-side yields ~32 preview interior columns — comfortable; below it the
// side-by-side preview shrinks toward its degradation floors while the terminal
// is usually TALL (a vertical iTerm2 split, a Neovim :terminal side window),
// which stacking spends and side-by-side wastes.
const stackBreakpoint = 90

// paneGeom is where every pane dimension comes from. View renders from it,
// syncPreview sizes the viewport from it, previewVisible reads it — ONE
// resolver, so the renderer and the key router can never disagree about what
// is on screen. (The live-preview review caught exactly that drift once: a
// viewport sized for one pane height rendered into another.)
type paneGeom struct {
	ListW, ListH int
	PrevW, PrevH int
	Stacked      bool
	PreviewShown bool
}

// stackedPreviewFloor is the smallest OUTER height a stacked preview pane is
// worth drawing at: previewMetadataLines interior rows + 1 exchange line +
// the border. Below it the pane shows metadata and zero exchange — worse than
// no pane (see the list-only rationale in View). "1 exchange line" is a
// literal line of viewport, not a literal line of MESSAGE TEXT: at this exact
// floor the viewport's one body row is spent on the "Last human:" label
// itself, so a stacked preview AT its floor still shows zero characters of
// the actual exchange. Same behaviour the pre-stacking wide arm always had at
// its own threshold (previewMetadataLines+1) — inherited, not introduced
// here. stackedPreviewWant exists precisely to keep auto out of this floor.
const stackedPreviewFloor = previewMetadataLines + 1 + paneBorderH // 13

// stackedPreviewWant is the preview's TARGET outer height when stacked, not
// merely its floor: stackedPreviewFloor's one body row is spent entirely on
// the "Last human:" label (see its comment) — this adds four more rows so a
// comfortably-sized stacked preview shows some actual exchange text, not just
// metadata. layoutGeom's allocation gives the preview this many rows before
// the list grows past its own floor, and lets the preview absorb any surplus
// once the list has reached its natural size.
const stackedPreviewWant = stackedPreviewFloor + 4 // 17

// stackedListFloor keeps the list useful when stacked: 2 rows + title + border.
const stackedListFloor = 5

// stackedComfortHeight is the smallest paneH at which AUTO mode is willing to
// stack: enough for the list's floor AND the preview's TARGET (not just its
// floor), so auto never renders a stacked frame that is worse than side-by-
// side would have been at the same size. See the amended design doc
// (2026-08-19 amendment) for the regression this closes: the original
// formula gave the preview priority up to its floor from the WIDTH budget's
// upper bound, which squeezed the list to its floor even with 15 sessions —
// at 40×20 that meant 2 list rows and ZERO exchange lines, worse than the
// pre-feature side-by-side render of the same frame (15 rows, 5 lines). Below
// this height auto falls through to the side-by-side rules unconditionally —
// cramped but exactly today's behaviour, never a new regression. FORCED
// stacked (the user explicitly asked for it via `v`) still works all the way
// down to stackedListFloor+stackedPreviewFloor, same as before.
const stackedComfortHeight = stackedListFloor + stackedPreviewWant // 22

// layoutGeom resolves the pane geometry for a frame. A forced mode is a
// preference, not a command: geometry constraints (the stacked floor, the
// side-by-side metadata rule) still decide PreviewShown, and a hidden preview
// degrades to the same list-only layout in every mode.
//
// This absorbs previewFits' old arithmetic into the wide arm's PreviewShown
// line below (see TestLayoutGeomWidePreviewShownThresholdIsExactlyHeight15
// for the boundary that standalone function used to pin directly).
// viewport.View() at Height 0 still emits ONE line (lipgloss treats Height(0)
// as unset — see previewBodyHeight), so a shown preview's interior needs room
// for previewMetadataLines PLUS one more row, or the viewport's single
// emitted line overruns the pane and MaxHeight clips the pane's own bottom
// border off the frame. Below that threshold, rendering no preview pane at
// all is strictly better: see (Model).previewVisible, which both View and
// handleKey consult so they cannot disagree about whether the pane is on
// screen.
func layoutGeom(mode layoutMode, nSessions, width, height int) paneGeom {
	paneH := bodyPaneHeight(height)
	stacked := mode == layoutStacked ||
		(mode == layoutAuto && width < stackBreakpoint && paneH >= stackedComfortHeight)
	if stacked {
		g := paneGeom{Stacked: true, ListW: width, PrevW: width}
		if paneH < stackedListFloor+stackedPreviewFloor {
			g.ListH = paneH // list-only
			g.PrevH = paneH // undrawn; mirrors the wide list-only arm below so
			// syncPreview sizes the (hidden) viewport consistently in both modes
			return g
		}
		g.PreviewShown = true
		// The preview meets its TARGET first (stackedPreviewWant): that is
		// the room left for the list once the preview has taken what it
		// wants. The list then grows to its natural size (rows + title +
		// border) out of that room, clamped so it never drops below its own
		// floor even when natural size is smaller (few sessions) and never
		// grows past it when there's more room than the list needs — any
		// room beyond both floors/wants goes to the preview, which is what
		// lets it absorb surplus rather than the list soaking it up.
		natural := nSessions + 1 + paneBorderH
		listCap := max(natural, stackedListFloor) // never let the cap fall below the floor
		g.ListH = min(max(paneH-stackedPreviewWant, stackedListFloor), listCap)
		g.PrevH = paneH - g.ListH
		return g
	}
	g := paneGeom{ListH: paneH, PrevH: paneH}
	g.ListW, g.PrevW = paneWidths(width)
	g.PreviewShown = paneInnerHeight(paneH) >= previewMetadataLines+1
	if !g.PreviewShown {
		g.ListW = width // list-only draws at the full terminal width
	}
	return g
}
