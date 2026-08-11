package ui

import (
	"os"
	"path/filepath"
	"syscall"
	"unicode/utf8"

	"github.com/JonathanAriass/ccs/internal/session"
	tea "github.com/charmbracelet/bubbletea"
)

// devDir exists so tests can aim title writes at a temp dir instead of /dev.
var devDir = "/dev"

// titleNameLimit matches nameInput.CharLimit (model.go): the typed path is
// already bounded there, so clamping here to the same number changes no
// legitimate behaviour — it only stops the two paths from disagreeing. The
// typed path is not the only way a name reaches here: names.json is
// hand-editable and loadNames applies no limit of its own, so an unbounded
// titleSeq would turn a hand-edited entry (or a future bug upstream) into an
// OSC sequence with no ceiling, repeated every poll. Runes, not bytes: a
// byte clamp could split a multi-byte character and hand an invalid tail to
// the terminal.
const titleNameLimit = 64

// titleSeq builds the OSC 0 (icon+window title) sequence for a name.
//
// The name goes through three passes before it reaches the payload:
//
//   - sanitize + flattenToRow, shared with the preview pane: strips C0
//     controls and DEL, collapses newlines. A title is transcript-adjacent
//     user text headed for a terminal, exactly the injection surface the
//     notification work closed — sanitize strips the ESC and BEL bytes that
//     could terminate or nest sequences.
//   - scrubC1AndInvalidUTF8, NOT shared with sanitize (the preview pane's
//     needs are different and sanitize.go says so explicitly): C1 controls
//     (U+0080-U+009F) are valid UTF-8 that sanitize's C0-only scan never
//     looks at, and some terminals in some modes still act on them as
//     control-sequence introducers/terminators. Invalid UTF-8 (a lone lead
//     byte, say) survives sanitize whenever nothing ELSE in the string trips
//     its needsSanitizing fast path — this runs unconditionally instead, so
//     the result no longer depends on what else happens to be in the name.
//   - a titleNameLimit-rune clamp, see its own doc comment.
func titleSeq(name string) string {
	s := scrubC1AndInvalidUTF8(flattenToRow(sanitize(name)))
	if n := utf8.RuneCountInString(s); n > titleNameLimit {
		r := []rune(s)
		s = string(r[:titleNameLimit])
	}
	return "\x1b]0;" + s + "\a"
}

// scrubC1AndInvalidUTF8 replaces every C1 control (U+0080-U+009F) and every
// invalid UTF-8 byte with a middle dot, leaving ordinary multi-byte Unicode
// untouched. Ranging over a string decodes it as UTF-8 and yields
// utf8.RuneError for anything that doesn't parse — that is what catches the
// invalid-encoding case; the C1 range is a plain value check on top.
func scrubC1AndInvalidUTF8(s string) string {
	var b []rune
	for _, r := range s {
		if r == utf8.RuneError || (r >= 0x80 && r <= 0x9f) {
			r = '·'
		}
		b = append(b, r)
	}
	return string(b)
}

// validTTY accepts ttys+digits only — the containment rule shared with
// cc-notify's -execute guard. Anything else is never opened.
func validTTY(name string) bool {
	if len(name) <= 4 || name[:4] != "ttys" {
		return false
	}
	for _, r := range name[4:] {
		if r < '0' || r > '9' {
			return false
		}
	}
	return true
}

// pushTitlesCmd re-asserts custom titles on every renamed session's tab.
// Claude Code periodically rewrites terminal titles, so a one-shot push would
// silently revert — this runs on each poll, and clearing a name stops it.
// Writes are best-effort: a vanished or unwritable tty must never surface as
// an error, matching how tab colors behave.
func pushTitlesCmd(views []session.View, names map[string]string) tea.Cmd {
	type target struct{ dev, seq string }
	var ts []target
	for _, v := range views {
		name := names[v.SessionID]
		if name == "" || !validTTY(v.TTY) {
			continue
		}
		ts = append(ts, target{filepath.Join(devDir, v.TTY), titleSeq(name)})
	}
	if len(ts) == 0 {
		return nil
	}
	return func() tea.Msg {
		for _, t := range ts {
			pushOne(t.dev, t.seq)
		}
		return nil
	}
}

// pushOne writes seq to dev, best-effort and non-blocking.
//
// bubbletea spawns one goroutine per Cmd and explicitly never reclaims it
// ("we'll have to leak the goroutine until Cmd returns" — tea.go). A tty
// whose output queue is full blocks a plain O_WRONLY open (if nothing has it
// open for reading) or the write itself (if something does but isn't
// draining it, which is the realistic case: a pty always has a reader, the
// terminal emulator). ^S in a renamed session's tab is enough to reach this —
// it engages IXON in the kernel line discipline, so writes to that slave
// block once the queue (typically 1-8KB) fills. Without O_NONBLOCK, one
// wedged tty leaks a goroutine and an fd every poll AND head-of-line-blocks
// every OTHER renamed session's title behind it in this same loop.
//
// O_NONBLOCK alone would just trade "blocks forever" for "silently drops
// part of the sequence" on a nearly-full queue: titleSeq is bounded (see
// titleNameLimit) so a successful write is always the WHOLE sequence in one
// atomic call, but a partial write is still possible against a queue that
// doesn't have titleNameLimit's worth of room left. Leaving a partial,
// unterminated OSC sequence on the tty is worse than skipping the update —
// the terminal keeps consuming bytes as part of that one control sequence
// until it finds a terminator, silently swallowing whatever the tty writes
// next. So a short write gets a best-effort terminating BEL rather than
// being left dangling; if even that can't land, there is nothing further to
// do and the write is abandoned exactly as a full failure would be.
func pushOne(dev, seq string) {
	f, err := os.OpenFile(dev, os.O_WRONLY|os.O_APPEND|syscall.O_NONBLOCK, 0)
	if err != nil {
		return
	}
	defer f.Close()
	n, err := f.WriteString(seq)
	if err == nil {
		return
	}
	if n > 0 && n < len(seq) {
		f.WriteString("\a")
	}
}
