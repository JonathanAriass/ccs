package ui

import (
	"os"
	"path/filepath"
	"strings"
	"syscall"
	"testing"
	"time"
	"unicode/utf8"

	"github.com/JonathanAriass/ccs/internal/session"
	tea "github.com/charmbracelet/bubbletea"
)

func TestTitleSeqSanitizesHostileNames(t *testing.T) {
	got := titleSeq("ev\x1b[2Jil\rx")
	if strings.Contains(got[4:len(got)-1], "\x1b") || strings.Contains(got, "\r") {
		t.Fatalf("control bytes survived into the OSC payload: %q", got)
	}
	if !strings.HasPrefix(got, "\x1b]0;") || !strings.HasSuffix(got, "\a") {
		t.Fatalf("not an OSC 0 sequence: %q", got)
	}
}

// TestTitleSeqClampsToCharLimit pins both directions of the length bound:
// nameInput.CharLimit (model.go) guards only the TYPED path, but a name can
// also arrive via a hand-edited names.json, which has no limit at all. A
// name at exactly the CharLimit bound must still pass through whole; only
// something LONGER than what typing could ever produce gets clamped.
func TestTitleSeqClampsToCharLimit(t *testing.T) {
	exact := strings.Repeat("a", 64)
	if got, want := titleSeq(exact), "\x1b]0;"+exact+"\a"; got != want {
		t.Errorf("a 64-rune name (the CharLimit bound) must pass through intact: got %q want %q", got, want)
	}

	over := strings.Repeat("b", 65)
	got := titleSeq(over)
	payload := got[4 : len(got)-1]
	if n := utf8.RuneCountInString(payload); n != 64 {
		t.Errorf("a 65-rune name must clamp to 64 runes, got %d runes: %q", n, got)
	}
}

// TestTitleSeqScrubsC1ControlsAndInvalidUTF8 closes the gap sanitize()
// deliberately leaves open (sanitize is shared with the preview pane, which
// has different needs — see title.go's own comment). C0 controls and DEL are
// already handled by sanitize; this covers what it does not:
//
//   - C1 controls (U+0080-U+009F) are valid, unremarkable UTF-8 bytes as far
//     as sanitize's C0-only scan is concerned, but some terminals in some
//     modes act on them as control-sequence introducers/terminators exactly
//     like their ESC-prefixed 7-bit equivalents.
//   - Invalid UTF-8 (e.g. a lone lead byte) survives whenever nothing ELSE in
//     the string trips sanitize's needsSanitizing fast path — the bug is
//     input-dependent, not structural, which is what made it easy to miss.
func TestTitleSeqScrubsC1ControlsAndInvalidUTF8(t *testing.T) {
	hostile := map[string]string{
		"C1 OSC introducer (U+009D) and C1 ST (U+009C), both valid UTF-8": "x\u009d0;pwned\u009cy",
		"C1 CSI introducer (U+009B), valid UTF-8":                         "x\u009b2Jy",
		"lone lead byte, nothing else in the string trips sanitizing":     "caf\xc3",
	}
	for name, input := range hostile {
		got := titleSeq(input)
		payload := got[4 : len(got)-1]
		for _, r := range payload {
			if r == utf8.RuneError || (r >= 0x80 && r <= 0x9f) {
				t.Errorf("%s: a C1 control or invalid UTF-8 rune survived into the OSC payload: %q", name, got)
				break
			}
		}
	}

	// Both directions: this is a scrub of the C1 range and decode errors, not
	// a blanket non-ASCII filter — ordinary multi-byte Unicode must survive.
	if got, want := titleSeq("café ☕"), "\x1b]0;café ☕\a"; got != want {
		t.Errorf("ordinary Unicode must pass through unscrubbed: got %q want %q", got, want)
	}
}

func TestValidTTYBothDirections(t *testing.T) {
	for tty, want := range map[string]bool{
		"ttys017": true, "ttys0": true,
		"console": false, "ttys": false, "ttys01a": false, "ttys0; rm": false, "": false,
	} {
		if got := validTTY(tty); got != want {
			t.Errorf("validTTY(%q) = %v want %v", tty, got, want)
		}
	}
}

func TestPushTitlesWritesRenamedSessionsOnly(t *testing.T) {
	old := devDir
	devDir = t.TempDir()
	defer func() { devDir = old }()
	// fake devices as plain files. "console" is created too, REAL, so that a
	// call-site bypass of validTTY (e.g. dropping "|| !validTTY(v.TTY)" from
	// pushTitlesCmd's filter) leaves evidence instead of being masked by
	// os.OpenFile's ENOENT on a path that was never created.
	os.WriteFile(filepath.Join(devDir, "ttys001"), nil, 0o644)
	os.WriteFile(filepath.Join(devDir, "ttys002"), nil, 0o644)
	os.WriteFile(filepath.Join(devDir, "console"), nil, 0o644)
	views := []session.View{
		{Session: session.Session{SessionID: "a"}, TTY: "ttys001"},
		{Session: session.Session{SessionID: "b"}, TTY: "ttys002"},
		{Session: session.Session{SessionID: "c"}, TTY: "console"}, // renamed but invalid tty
	}
	names := map[string]string{"a": "renamed-a", "c": "never-written"}
	cmd := pushTitlesCmd(views, names)
	if cmd == nil {
		t.Fatal("nil cmd")
	}
	cmd() // run the write synchronously in the test
	b1, _ := os.ReadFile(filepath.Join(devDir, "ttys001"))
	if !strings.Contains(string(b1), "renamed-a") {
		t.Errorf("renamed session's title not written: %q", b1)
	}
	// BOTH directions: un-renamed session untouched, invalid tty untouched
	if b2, _ := os.ReadFile(filepath.Join(devDir, "ttys002")); len(b2) != 0 {
		t.Errorf("un-renamed session was written: %q", b2)
	}
	if b3, _ := os.ReadFile(filepath.Join(devDir, "console")); len(b3) != 0 {
		t.Errorf("invalid tty WAS written: %q", b3)
	}
	if entries, _ := os.ReadDir(devDir); len(entries) != 3 {
		t.Errorf("a write created a file outside the three fake devices: %v", entries)
	}
}

// TestSaveAndPollBothPushTitles pins both call sites: the Enter-save path in
// handleKey's renaming branch, and the sessionsMsg (poll) case.
//
// The poll half needs care. sessionsMsg's cmd is tea.Batch(costCmdForSelected(),
// pushTitlesCmd(...)) — two non-nil cmds — and bubbletea's own Batch (see
// compactCmds in the vendored source) does NOT run its members when called: it
// returns a tea.BatchMsg, a []tea.Cmd, and it is the real bubbletea runtime
// that later invokes each member concurrently. Calling the outer cmd once and
// asserting non-nil would therefore pass even if pushTitlesCmd were dropped
// from the batch entirely (costCmdForSelected alone is also non-nil) — the
// exact vacuous shape the brief warns against. So this test type-asserts the
// tea.BatchMsg and runs its members itself, the same thing the runtime would
// do, and checks the write those members actually produce.
func TestSaveAndPollBothPushTitles(t *testing.T) {
	old := devDir
	devDir = t.TempDir()
	defer func() { devDir = old }()
	os.WriteFile(filepath.Join(devDir, "ttys001"), nil, 0o644)
	m := modelWithOverflowingPreview(t, 1)
	m.views[0].TTY = "ttys001"
	m.namesFile = filepath.Join(t.TempDir(), "names.json")
	m.names = map[string]string{}
	// save path
	n, _ := m.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'n'}})
	m = n.(Model)
	m.nameInput.SetValue("pushed-on-save")
	n, cmd := m.Update(tea.KeyMsg{Type: tea.KeyEnter})
	m = n.(Model)
	if cmd == nil {
		t.Fatal("save returned no cmd")
	}
	cmd()
	b, _ := os.ReadFile(filepath.Join(devDir, "ttys001"))
	if !strings.Contains(string(b), "pushed-on-save") {
		t.Fatalf("save did not push: %q", b)
	}

	// poll path re-asserts
	os.WriteFile(filepath.Join(devDir, "ttys001"), nil, 0o644) // simulate Claude Code overwriting
	n, cmd = m.Update(sessionsMsg{views: m.views})
	m = n.(Model)
	if cmd == nil {
		t.Fatal("poll returned no cmd")
	}
	msg := cmd()
	batch, ok := msg.(tea.BatchMsg)
	if !ok {
		t.Fatalf("poll cmd is not a tea.Batch of the cost lookup and title push: %T (%v)", msg, msg)
	}
	for _, sub := range batch {
		if sub != nil {
			sub()
		}
	}
	b, _ = os.ReadFile(filepath.Join(devDir, "ttys001"))
	if !strings.Contains(string(b), "pushed-on-save") {
		t.Fatalf("poll did not re-assert: %q", b)
	}
}

// TestPushTitlesSkipsAWedgedTTYWithoutBlockingOthers pins the fix for a
// blocking open/write on bubbletea's cmd goroutine: bubbletea spawns one
// goroutine per Cmd and explicitly never reclaims it (see the "we'll have to
// leak the goroutine until Cmd returns" comment in its own tea.go), so a
// device that never accepts the write leaks one goroutine and one open fd
// every poll — and, worse, blocks every OTHER renamed session's title behind
// it, since pushTitlesCmd writes its targets in a single loop on one
// goroutine.
//
// This is reachable with a single keystroke: ^S in a renamed session's
// iTerm2 tab engages IXON in the kernel line discipline, so writes to that
// slave block once its output queue (typically 1-8KB) fills. A real pty
// always has a reader (the terminal emulator), so the faithful reproduction
// here is not "nobody opened the read end" — it's "someone opened it and
// stopped draining": a FIFO in t.TempDir(), opened for reading and then
// filled past capacity, so the write behind it blocks/EAGAINs exactly like a
// stopped tty would. No /dev path is ever touched.
func TestPushTitlesSkipsAWedgedTTYWithoutBlockingOthers(t *testing.T) {
	old := devDir
	devDir = t.TempDir()
	defer func() { devDir = old }()

	wedged := filepath.Join(devDir, "ttys001")
	if err := syscall.Mkfifo(wedged, 0o600); err != nil {
		t.Fatalf("Mkfifo: %v", err)
	}
	reader, err := os.OpenFile(wedged, os.O_RDONLY|syscall.O_NONBLOCK, 0)
	if err != nil {
		t.Fatalf("open FIFO reader: %v", err)
	}
	defer reader.Close()
	// Fill the pipe to capacity so the NEXT write (pushTitlesCmd's) lands on
	// an already-full queue, the closer analogue of a stopped tty — the
	// reader above stays open (matching a real pty's master) but never
	// drains.
	filler, err := os.OpenFile(wedged, os.O_WRONLY|syscall.O_NONBLOCK, 0)
	if err != nil {
		t.Fatalf("open FIFO filler writer: %v", err)
	}
	buf := make([]byte, 4096)
	for {
		if _, err := filler.Write(buf); err != nil {
			break // EAGAIN: the pipe is now full
		}
	}
	filler.Close()

	os.WriteFile(filepath.Join(devDir, "ttys002"), nil, 0o644)
	views := []session.View{
		{Session: session.Session{SessionID: "wedged"}, TTY: "ttys001"},
		{Session: session.Session{SessionID: "healthy"}, TTY: "ttys002"},
	}
	names := map[string]string{"wedged": "never-delivered", "healthy": "reaches-its-tty"}

	cmd := pushTitlesCmd(views, names)
	if cmd == nil {
		t.Fatal("nil cmd")
	}

	done := make(chan struct{})
	go func() {
		cmd()
		close(done)
	}()
	select {
	case <-done:
	case <-time.After(2 * time.Second):
		t.Fatal("cmd blocked on the wedged tty instead of returning — O_NONBLOCK missing or ineffective")
	}

	b, _ := os.ReadFile(filepath.Join(devDir, "ttys002"))
	if !strings.Contains(string(b), "reaches-its-tty") {
		t.Errorf("HEAD-OF-LINE BLOCK: the healthy tty behind the wedged one was never written: %q", b)
	}
}
