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
	age := compactAge(v.StatusUpdatedAt, now)
	msg := truncateToWidth(sanitize(lastMessage(v)), msgW)

	row := fmt.Sprintf("%s %-*s %-*s %*s %s", glyph, nameW, name, dirW, dir, ageWidth, age, msg)

	// Safety net: truncateToWidth/elideMiddle budget in RUNES, not display
	// columns (that's what layout.go's own tests pin). Transcript text is
	// real user content and can contain double-width runes (CJK, emoji —
	// verified against the live registry: an assistant reply containing "✅"
	// is exactly this), which makes the rune-budgeted row wider on screen
	// than the pane. lipgloss.Style.Width() word-WRAPS an overflowing line
	// rather than clipping it, so a single too-wide row silently becomes two
	// screen lines and shoves every row below it out of place. ansi.Truncate
	// hard-clips by real display width (ANSI-aware, so the glyph's own color
	// codes are measured correctly), guaranteeing every row is exactly one
	// screen line regardless of what a transcript happens to contain.
	return ansi.Truncate(row, width, "")
}

// previewField renders one "Label: value" line in the preview pane.
func previewField(label, value string) string {
	return labelStyle.Render(label+":") + " " + value
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

	listW, previewW := paneWidths(m.width)
	paneH := max(1, m.height-2) // leave room for the status/legend footer

	var list string
	if m.err != nil {
		list = m.renderListError(listW, paneH)
	} else {
		list = m.renderList(listW, paneH)
	}
	preview := m.renderPreview(previewW, paneH)
	body := lipgloss.JoinHorizontal(lipgloss.Top, list, preview)

	var footer strings.Builder
	if m.status != "" {
		footer.WriteString(statusFooter.Render(sanitize(m.status)) + "\n")
	}
	footer.WriteString(help.New().ShortHelpView(m.keys.ShortHelp()))

	return lipgloss.JoinVertical(lipgloss.Left, body, footer.String())
}

// renderListError draws the list pane in its error state: m.err REPLACES the
// session list (not the whole screen — the preview pane and legend still
// render normally around it).
func (m Model) renderListError(width, height int) string {
	var b strings.Builder
	b.WriteString(titleStyle.Render("Sessions"))
	b.WriteString("\n" + errStyle.Render("error: "+sanitize(m.err.Error())))
	return listPane.Width(paneStyleWidth(width)).Height(paneStyleHeight(height)).
		MaxWidth(width).MaxHeight(height).Render(b.String())
}

func (m Model) renderList(width, height int) string {
	inner := max(0, paneInnerWidth(width)-listCursorWidth)
	home, _ := os.UserHomeDir()
	now := time.Now()

	var b strings.Builder
	b.WriteString(titleStyle.Render(fmt.Sprintf("Sessions (%d)", len(m.views))))
	if len(m.views) == 0 {
		b.WriteString("\n  (no live sessions)")
	}
	for i, v := range m.views {
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
	return listPane.Width(paneStyleWidth(width)).Height(paneStyleHeight(height)).
		MaxWidth(width).MaxHeight(height).Render(b.String())
}

func (m Model) renderPreview(width, height int) string {
	inner := paneInnerWidth(width)
	var b strings.Builder
	b.WriteString(titleStyle.Render("Preview"))

	v := m.selected()
	switch {
	case v == nil:
		b.WriteString("\n  (no session selected)")
	case !v.HasPreview:
		b.WriteString("\n  no preview")
	default:
		b.WriteString("\n" + previewField("Status", v.Status))
		b.WriteString("\n" + previewField("Version", v.Version))
		tty := v.TTY
		if tty == "" {
			tty = "-"
		}
		b.WriteString("\n" + previewField("TTY", tty))
		b.WriteString("\n\n" + labelStyle.Render("main thread"))
		b.WriteString("\n" + previewField("Tokens", fmt.Sprintf("%d", v.Tokens)))
		b.WriteString("\n" + previewField("Cost", fmt.Sprintf("$%.2f", v.Cost)))
		b.WriteString("\n\n" + labelStyle.Render("Last human:"))
		b.WriteString("\n" + wrapToWidth(sanitize(v.LastHuman), inner))
		b.WriteString("\n\n" + labelStyle.Render("Last assistant:"))
		b.WriteString("\n" + wrapToWidth(sanitize(v.LastAssistant), inner))
	}

	return previewPane.Width(paneStyleWidth(width)).Height(paneStyleHeight(height)).
		MaxWidth(width).MaxHeight(height).Render(b.String())
}
