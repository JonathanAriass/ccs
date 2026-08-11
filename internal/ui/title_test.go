package ui

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

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
	// fake devices as plain files
	os.WriteFile(filepath.Join(devDir, "ttys001"), nil, 0o644)
	os.WriteFile(filepath.Join(devDir, "ttys002"), nil, 0o644)
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
	if entries, _ := os.ReadDir(devDir); len(entries) != 2 {
		t.Errorf("a write created a file outside the two fake ttys: %v", entries)
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
