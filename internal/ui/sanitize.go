package ui

import (
	"strings"

	"github.com/charmbracelet/x/ansi"
)

// tabWidth is the column interval tabs expand to, matching what git itself
// assumes when it renders diffs.
const tabWidth = 8

// sanitize makes arbitrary bytes safe to draw inside a bordered pane.
//
// Transcript text — the last human message, the last assistant reply, an
// ai-title — is all arbitrary file content, and the pane's width math cannot
// see what a terminal will do with control characters in it. Both
// lipgloss.Width and ansi.StringWidth measure "\t" and "\r" as zero columns,
// and ansi.Truncate passes them through untouched — but a terminal expands a
// tab to the next tab stop and sends a carriage return back to column 0. A
// CRLF, tab-indented block of text (a pasted log excerpt, say) therefore
// renders far wider than it measures, and each "\r" makes the pane's trailing
// padding and right border overwrite the start of that row, dropping "│"
// characters at arbitrary columns across the whole frame. Raw escape
// sequences in transcript content would likewise be executed by the terminal.
//
// So tabs are expanded to spaces, carriage returns are dropped, and every
// other C0 control character and DEL becomes a middle dot: what gets measured
// is then exactly what the terminal draws.
//
// For ccs this is defense-in-depth rather than a fix for an observed bug: no
// record accepted by the human-message rule was found to contain ANSI across
// all 4,221 transcripts. It stays because the preview also renders assistant
// output, and transcript text is untrusted.
func sanitize(s string) string {
	if !needsSanitizing(s) {
		return s
	}
	lines := strings.Split(s, "\n")
	for i, ln := range lines {
		lines[i] = sanitizeLine(ln)
	}
	return strings.Join(lines, "\n")
}

// needsSanitizing is a cheap byte scan so the common all-printable case
// avoids rebuilding the string. Newlines are row separators, not content.
func needsSanitizing(s string) bool {
	for i := 0; i < len(s); i++ {
		if c := s[i]; (c < 0x20 && c != '\n') || c == 0x7f {
			return true
		}
	}
	return false
}

// sanitizeLine sanitizes a single row, tracking the visible column as it goes
// so each tab expands to the correct next tab stop.
func sanitizeLine(line string) string {
	var b strings.Builder
	b.Grow(len(line))
	col := 0
	for _, r := range line {
		switch {
		case r == '\t':
			n := tabWidth - col%tabWidth
			b.WriteString(strings.Repeat(" ", n))
			col += n
		case r == '\r':
			// A CRLF file leaves one of these on every line. It carries no
			// content, and drawing it would wreck the row.
		case r < 0x20 || r == 0x7f:
			b.WriteRune('·')
			col++
		case r < 0x80:
			b.WriteRune(r)
			col++
		default:
			b.WriteRune(r)
			col += ansi.StringWidth(string(r))
		}
	}
	return b.String()
}
