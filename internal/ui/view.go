package ui

import (
	"fmt"
	"os"
	"strings"
	"time"

	"github.com/JonathanAriass/ccs/internal/session"
	"github.com/charmbracelet/bubbles/help"
	"github.com/charmbracelet/lipgloss"
	"github.com/charmbracelet/x/ansi"
)

// minTermWidth / minTermHeight are the smallest terminal the two-pane layout
// can be drawn in at all. Below this View shows a short notice instead of a
// frame that would spill past the screen edge.
const (
	minTermWidth  = 40
	minTermHeight = 8
)

// ageWidth is the fixed column budget reserved for the compact-age field in
// a list row (enough for e.g. "999d").
const ageWidth = 4

var (
	paneStyle   = lipgloss.NewStyle().Border(lipgloss.RoundedBorder()).Padding(0, 1)
	listPane    = paneStyle.BorderForeground(lipgloss.Color("62"))
	previewPane = paneStyle.BorderForeground(lipgloss.Color("240"))

	selectedRow  = lipgloss.NewStyle().Foreground(lipgloss.Color("212")).Bold(true)
	dimRow       = lipgloss.NewStyle().Foreground(lipgloss.Color("240"))
	errStyle     = lipgloss.NewStyle().Foreground(lipgloss.Color("196")).Bold(true)
	statusFooter = lipgloss.NewStyle().Foreground(lipgloss.Color("214"))
	labelStyle   = lipgloss.NewStyle().Foreground(lipgloss.Color("240"))
	titleStyle   = lipgloss.NewStyle().Bold(true)

	waitingColor = lipgloss.NewStyle().Foreground(lipgloss.Color("196"))
	busyColor    = lipgloss.NewStyle().Foreground(lipgloss.Color("220"))
	shellColor   = lipgloss.NewStyle().Foreground(lipgloss.Color("39"))
	idleColor    = lipgloss.NewStyle().Foreground(lipgloss.Color("240"))
)

// statusGlyph returns the styled one-character indicator for a session's
// status. Colors: waiting=red (needs the user right now), busy=yellow
// (working), shell=blue (a plain shell, not Claude), idle/unrecognized=dim.
func statusGlyph(status string) string {
	switch status {
	case "waiting":
		return waitingColor.Render("●")
	case "busy":
		return busyColor.Render("◐")
	case "shell":
		return shellColor.Render("◇")
	case "idle":
		return idleColor.Render("○")
	default:
		return idleColor.Render("○")
	}
}

// lastMessage picks which transcript field a row/preview shows as "the last
// message": the assistant's final reply when there is one (it's usually the
// more useful summary of where things stand), falling back to the human's
// message when the assistant hasn't replied yet.
func lastMessage(v session.View) string {
	if v.LastAssistant != "" {
		return v.LastAssistant
	}
	return v.LastHuman
}

// formatRow composes one plain-text session row: glyph, name, directory, age,
// last message. Every plain-text field's width is decided BEFORE any styling
// is applied to the composed line (selectedRow/dimRow wrap the whole thing in
// renderList), so ANSI bytes from a later style never enter the width math —
// the same discipline gsv's formatStashLine documents.
func formatRow(v session.View, home string, now time.Time, width int) string {
	const fixedCols = 5 // glyph(1) + 4 separator spaces between the 5 fields
	budget := width - fixedCols - ageWidth
	if budget < 0 {
		budget = 0
	}
	nameW, dirW, msgW := rowWidths(budget)

	glyph := statusGlyph(v.Status)
	// DisplayName() can resolve to the transcript's ai-title (see its own doc
	// comment), and CWD is drawn inside the same bordered pane as transcript
	// text — both go through sanitize for the same reason lastMessage does.
	name := truncateToWidth(sanitize(v.DisplayName()), nameW)
	dir := elideMiddle(sanitize(homeAbbrev(v.CWD, home)), dirW)
	age := compactAge(v.StatusUpdatedTime(), now)
	msg := truncateToWidth(sanitize(lastMessage(v)), msgW)

	row := fmt.Sprintf("%s %-*s %-*s %*s %s", glyph, nameW, name, dirW, dir, ageWidth, age, msg)

	// Safety net, and it is still load-bearing even though every field above
	// is now clipped to DISPLAY COLUMNS (layout.go's own tests pin that).
	//
	// The reason is the Sprintf above: Go's %-*s verb pads to a width counted
	// in RUNES. So name and dir are clipped by columns and then padded by
	// runes, and a field of double-width runes — CJK, emoji; verified against
	// the live registry, where an assistant reply containing "✅" is exactly
	// this — comes back from %-*s with roughly nameW/2 surplus spaces
	// appended. Measured on a 54-column row without this net: 60 columns for a
	// CJK name, 57 for a CJK CWD, 63 for both.
	//
	// That matters because lipgloss.Style.Width() word-WRAPS an overflowing
	// line rather than clipping it, so a single too-wide row silently becomes
	// two screen lines and shoves every row below it out of place.
	// ansi.Truncate hard-clips by real display width (ANSI-aware, so the
	// glyph's own color codes are measured correctly), guaranteeing every row
	// is exactly one screen line regardless of what a transcript contains.
	//
	// It does NOT make the per-field clips redundant: with the row bounded
	// here, a field that overran its budget costs the fields AFTER it — the
	// age and the message get clipped off the end instead. See
	// TestFormatRowKeepsLaterFieldsOnTheRow, which is what pins those.
	return ansi.Truncate(row, width, "")
}

// listTitle composes the list pane's title line: the session count, and — when
// the pane is too short to draw every session — WHICH rows of the list are on
// screen ("Sessions (5-12/15)").
//
// The windowed form is the affordance for listWindow's clipping, and it has to
// exist: a pane that silently drops nine of fifteen sessions while its title
// still reads "Sessions (15)" tells the reader the opposite of the truth, and
// the sessions it drops are exactly the ones they cannot see to go looking for.
// Reusing the count the title already carries keeps that honest without adding
// a scrollbar or any second piece of chrome to a pane that is short precisely
// because there is no room.
//
// The result is always exactly ONE line, and that is load-bearing for the same
// reason previewTitle's is: lipgloss word-WRAPS an over-wide line inside a
// Style.Width() block rather than clipping it, so a title one column too wide
// becomes two lines and costs the pane its own bottom border via MaxHeight.
// Hence the clamp — the count grows with the session count and the digit widths
// of the window bounds, and nothing else bounds it.
func listTitle(total, first, shown, innerW int) string {
	label := fmt.Sprintf("Sessions (%d)", total)
	if shown < total {
		label = fmt.Sprintf("Sessions (%d-%d/%d)", first+1, first+shown, total)
	}
	if innerW <= 0 {
		return ""
	}
	return ansi.Truncate(label, innerW, "…")
}

// previewField renders one "Label: value" line in the preview pane.
func previewField(label, value string) string {
	return labelStyle.Render(label+":") + " " + value
}

// previewTitle composes the preview pane's title line out of three pieces —
// the word "Preview", the focus marker, and the page indicator — degrading to
// whatever fits in innerW display columns.
//
// indicator is the page indicator for the CURRENT offset ("3/12") and widest is
// the widest one this content can ever produce ("12/12"); both are "" when the
// exchange fits. Every candidate is RENDERED with indicator but MEASURED with
// widest, so which pieces survive cannot depend on where the reader has
// scrolled to — if the title fits on page 1 it fits on every page. Deciding per
// page is what made the indicator show through page 9 and then vanish from page
// 10 at widths 43-45, which reads as "there is no more content" at the exact
// moment there still is. (The number itself still gains a column when it gains
// a digit; the title is left-aligned and nothing is laid out against its
// trailing edge.)
//
// The literal word "Preview" is dropped BEFORE either affordance, because the
// pane is already identified by its border and its position while the marker
// says what j/k will do and the indicator says there is more to read. That
// ordering is what keeps both affordances alive at minTermWidth, where the
// pane's interior is 12 columns: "Preview ▸ 12/12" is 15 and does not fit,
// "▸ 12/12" is 7 and fits comfortably.
//
// The marker is text, not colour: the focused state has to stay legible on a
// terminal whose colour rendering we cannot verify.
//
// The result is always exactly ONE line, and that is load-bearing: lipgloss
// word-WRAPS a Style.Width() block that overflows rather than clipping it, so a
// title one column too wide becomes two lines, previewMetadataLines goes stale,
// the viewport is mis-sized against it, and the pane's own MaxHeight clips its
// bottom border off the frame.
//
// Last resort, reachable only with a page count wide enough to fill the pane on
// its own (four digits at minTermWidth): the marker alone. Nothing shorter than
// that carries any information, so below it the title goes empty rather than
// printing a fragment of a page number.
func previewTitle(focused bool, indicator, widest string, innerW int) string {
	marker := ""
	if focused {
		marker = "▸"
	}
	compose := func(word, ind string) string {
		var parts []string
		for _, p := range []string{word, marker, ind} {
			if p != "" {
				parts = append(parts, p)
			}
		}
		return strings.Join(parts, " ")
	}
	// Most informative first.
	for _, c := range []struct{ show, measure string }{
		{compose("Preview", indicator), compose("Preview", widest)},
		{compose("", indicator), compose("", widest)},
		{compose("", ""), compose("", "")},
	} {
		if c.show == "" {
			continue // this rendering carries nothing; try the next one
		}
		if lipgloss.Width(c.measure) <= innerW {
			return c.show
		}
	}
	return ""
}

func (m Model) View() string {
	if m.width == 0 || m.height == 0 {
		// Not sized yet — bubbletea sends a WindowSizeMsg before the first
		// real render, so this is only ever visible for a single frame.
		return ""
	}
	if m.width < minTermWidth || m.height < minTermHeight {
		return lipgloss.Place(m.width, m.height, lipgloss.Center, lipgloss.Center,
			fmt.Sprintf("Terminal too small\n(need %d×%d)", minTermWidth, minTermHeight))
	}

	paneH := bodyPaneHeight(m.height) // leave room for the status/legend footer

	var body string
	if !m.previewVisible() {
		// Below previewFits' threshold the preview pane could show its pinned
		// metadata but ZERO lines of the actual exchange — which is the whole
		// point of the pane — while its one phantom viewport line (see
		// previewFits) also breaks the frame's own border. A pane that renders
		// nothing useful AND corrupts the frame is strictly worse than no pane
		// at all, so draw the list alone at the full terminal width: it still
		// does the tool's primary job (see sessions, press Enter to jump), and
		// the list itself stays usable well below this threshold — 3 rows at
		// minTermHeight rising to 8 at height 13, always including the selected
		// one, with the title saying which of them are on screen (see listWindow
		// and listTitle). This is also the mode that needs those two most: the
		// terminal is at its shortest here, so the list is at its most clipped.
		//
		// The list is always drawn with the "focused" border here: it is the
		// only interactive pane on screen, so there is nothing for the border
		// color to distinguish it from. m.focus itself is left untouched — see
		// previewVisible's doc comment for why handleKey, not this branch, is
		// what keeps j/k working regardless of what m.focus says.
		if m.err != nil {
			body = m.renderListError(m.width, paneH, listPane)
		} else {
			body = m.renderList(m.width, paneH, listPane)
		}
	} else {
		listW, previewW := paneWidths(m.width)

		lp, pp := listPane, previewPane
		if m.focus == focusPreview {
			// The brighter border follows focus.
			lp, pp = previewPane, listPane
		}

		var list string
		if m.err != nil {
			list = m.renderListError(listW, paneH, lp)
		} else {
			list = m.renderList(listW, paneH, lp)
		}
		preview := m.renderPreview(previewW, paneH, pp)
		body = lipgloss.JoinHorizontal(lipgloss.Top, list, preview)
	}

	// Both footer lines are hard-clipped to the terminal width.
	//
	// The key legend is 52 display columns at full length, but minTermWidth is
	// 40 — so at every width in [40, 52) an unclamped legend is WIDER than the
	// screen. lipgloss.JoinVertical below pads every block to the widest one, so
	// a 52-column footer does not just overflow itself: it stretches EVERY body
	// row to 52 columns too, each of which the terminal then wraps onto a second
	// line, doubling the frame's height and garbling the layout. Measured over
	// widths 40..80: 12 of 41 overflowed, all of them by up to 12 columns, and
	// all of them in [40, 52). That range is not hypothetical — it is what a
	// vertical iTerm2 split or a Neovim :terminal in a side window gives you.
	//
	// h.Width tells bubbles to elide the legend gracefully (with "…") instead of
	// dropping bindings, but it is NOT sufficient on its own: its truncation is
	// approximate and still returned 52 columns at Width = 45. ansi.Truncate,
	// which clips by real display width, is what actually guarantees the bound.
	var footer strings.Builder
	if m.status != "" {
		footer.WriteString(ansi.Truncate(statusFooter.Render(sanitize(m.status)), m.width, "") + "\n")
	}
	h := help.New()
	h.Width = m.width
	footer.WriteString(ansi.Truncate(h.ShortHelpView(m.keys.shortHelpFor(m.previewVisible())), m.width, ""))

	return lipgloss.JoinVertical(lipgloss.Left, body, footer.String())
}

// renderListError draws the list pane in its error state: m.err REPLACES the
// session list (not the whole screen — the preview pane and legend still
// render normally around it).
// The error text is WRAPPED to the pane's interior and then clamped to the rows
// left under the title, for the reason renderList's own rows are (see
// listCapacity): lipgloss word-wraps an over-wide line rather than clipping it,
// and every line past the interior costs the pane its bottom border rather than
// costing the line. m.err carries a filesystem path — the real one is "open
// /Users/x/.claude/projects/<long-project-dir>/session-registry.json:
// permission denied", 124 columns — so this is not a hypothetical: at
// minTermWidth it wrapped to five lines and broke the frame at heights 8 and 9.
// The registry being unreadable is exactly when the user needs the frame to
// still make sense.
func (m Model) renderListError(width, height int, pane lipgloss.Style) string {
	var b strings.Builder
	b.WriteString(titleStyle.Render("Sessions"))
	lines := strings.Split(wrapToWidth("error: "+sanitize(m.err.Error()), paneInnerWidth(width)), "\n")
	for _, ln := range lines[:min(len(lines), listCapacity(height))] {
		b.WriteString("\n" + errStyle.Render(ln))
	}
	return pane.Width(paneStyleWidth(width)).Height(paneStyleHeight(height)).
		MaxWidth(width).MaxHeight(height).Render(b.String())
}

func (m Model) renderList(width, height int, pane lipgloss.Style) string {
	inner := max(0, paneInnerWidth(width)-listCursorWidth)
	home, _ := os.UserHomeDir()
	now := time.Now()

	// Only the rows the pane's interior can actually hold — see listCapacity for
	// what drawing more costs, and listWindow for why the selected row is always
	// among them.
	first, shown := listWindow(len(m.views), m.cursor, listCapacity(height))

	var b strings.Builder
	b.WriteString(titleStyle.Render(listTitle(len(m.views), first, shown, paneInnerWidth(width))))
	if len(m.views) == 0 {
		b.WriteString("\n  (no live sessions)")
	}
	for i := first; i < first+shown; i++ {
		v := m.views[i]
		line := formatRow(v, home, now, inner)
		switch {
		case i == m.cursor:
			b.WriteString("\n" + selectedRow.Render("› "+line))
		case v.TTY == "":
			// A background/daemon session has no tab to focus. Dimmed so it
			// still reads clearly, but visually deprioritized versus rows
			// Enter can actually act on.
			b.WriteString("\n" + dimRow.Render("  "+line))
		default:
			b.WriteString("\n  " + line)
		}
	}
	return pane.Width(paneStyleWidth(width)).Height(paneStyleHeight(height)).
		MaxWidth(width).MaxHeight(height).Render(b.String())
}

// previewMetadataLines is how many rows renderPreviewMetadata occupies. It is
// pinned to the real output by TestPreviewMetadataLineCountMatchesTheConstant —
// if that test reports a different number, change this constant to match the
// test's number and say so in your report. Do not adjust the test to match the
// constant; the rendered output is the authority.
const previewMetadataLines = 9

// renderPreviewMetadata is the pinned part of the preview: everything above the
// scrolling exchange, ending with the blank separator line.
//
// It stays pinned because Status/Version/TTY/Tokens/Cost are reference the
// reader wants WHILE reading a long message. Scrolled away, the reader loses
// track of which session they are even looking at.
//
// innerW is the pane's content width in display columns. EVERY line below must
// fit inside it: lipgloss word-wraps a Style.Width() block that overflows
// rather than clipping it, so a too-wide line becomes two, previewMetadataLines
// silently goes stale, and the pane's own MaxHeight then clips its bottom
// border off the screen (see TestPreviewPaneBorderSurvivesEveryWidth's ╯
// count).
//
// The title degrades token by token (previewTitle). The metadata fields cannot
// — a value is one token — so they are hard-clipped instead, with an ellipsis
// marking the cut. A realistic "Version: 2.1.220" is 16 columns and wrapped the
// pane at every width in [40, 48): harmless-looking, but it costs the pane its
// entire bottom border, and the scrolling preview made it permanent by padding
// the viewport to its full Height regardless of content, removing the vertical
// slack that used to absorb the extra line.
func (m Model) renderPreviewMetadata(v *session.View, innerW int) string {
	// fit clamps one line to the pane's interior. innerW <= 0 means there is no
	// interior at all, so nothing can be drawn in it.
	fit := func(s string) string {
		if innerW <= 0 {
			return ""
		}
		return ansi.Truncate(s, innerW, "…")
	}

	var b strings.Builder
	total, height, offset := m.preview.TotalLineCount(), m.preview.Height, m.preview.YOffset
	b.WriteString(titleStyle.Render(previewTitle(
		m.focus == focusPreview,
		scrollIndicator(total, height, offset),
		widestScrollIndicator(total, height),
		innerW,
	)))
	b.WriteString("\n" + fit(previewField("Status", v.Status)))
	b.WriteString("\n" + fit(previewField("Version", v.Version)))
	tty := v.TTY
	if tty == "" {
		tty = "-"
	}
	b.WriteString("\n" + fit(previewField("TTY", tty)))
	b.WriteString("\n\n" + fit(labelStyle.Render("main thread")))
	b.WriteString("\n" + fit(previewField("Tokens", fmt.Sprintf("%d", v.Tokens))))
	b.WriteString("\n" + fit(previewField("Cost", fmt.Sprintf("$%.2f", v.Cost))))
	b.WriteString("\n")
	return b.String()
}

func (m Model) renderPreview(width, height int, pane lipgloss.Style) string {
	var b strings.Builder
	if v := m.selected(); v == nil {
		b.WriteString(titleStyle.Render("Preview"))
		b.WriteString("\n  (no session selected)")
	} else {
		b.WriteString(m.renderPreviewMetadata(v, paneInnerWidth(width)))
		// Sizing and content come from syncPreview in Update. Writing them here
		// would be discarded — View has a value receiver.
		b.WriteString("\n" + m.preview.View())
	}
	return pane.Width(paneStyleWidth(width)).Height(paneStyleHeight(height)).
		MaxWidth(width).MaxHeight(height).Render(b.String())
}
