package iterm

import (
	"errors"
	"os/exec"
	"strings"
	"testing"
	"time"
)

func TestFocusRejectsEmptyTTY(t *testing.T) {
	// Background/daemon sessions have no tty. This must fail fast without
	// spawning osascript at all.
	start := time.Now()
	err := Focus("")
	if !errors.Is(err, ErrNotFound) {
		t.Errorf("want ErrNotFound, got %v", err)
	}
	if elapsed := time.Since(start); elapsed > 50*time.Millisecond {
		t.Errorf("empty tty must short-circuit, took %v", elapsed)
	}
}

// TestFocusRejectsPlaceholderTTY covers the OTHER half of the
// `tty == "" || tty == "??"` guard. Without it, deleting just the "??" arm
// leaves the entire suite green — ttyName() returns "??" for any non-pty
// controlling terminal, and while OutermostTTY never surfaces it today (it
// only ever yields a real ttysNNN or ""), Focus is a public function and must
// not be one future caller away from shelling out on a placeholder value.
func TestFocusRejectsPlaceholderTTY(t *testing.T) {
	start := time.Now()
	err := Focus("??")
	if !errors.Is(err, ErrNotFound) {
		t.Errorf("want ErrNotFound, got %v", err)
	}
	if elapsed := time.Since(start); elapsed > 50*time.Millisecond {
		t.Errorf("\"??\" tty must short-circuit, took %v", elapsed)
	}
}

// currentTTYForTest returns a tty iTerm2 itself reports as belonging to a live
// session, or "" if that cannot be determined.
//
// It asks iTerm2 directly rather than using this package's Snapshot /
// OutermostTTY on purpose. A positive control built out of the code under test
// agrees with that code's bugs — the cc-notify plan shipped exactly that mistake,
// where the test's tty walk was a verbatim clone of the script's, so a bug in the
// walk made the gate and the code agree and the test silently followed it. This
// must stay an independent oracle.
//
// Callers guard with Running() first, so the `tell` here cannot launch iTerm2.
func currentTTYForTest(t *testing.T) string {
	t.Helper()
	out, err := exec.Command("osascript", "-e",
		`tell application "iTerm2" to return tty of current session of current window`).Output()
	if err != nil {
		return ""
	}
	// Focus takes a bare name ("ttys017") and prepends /dev/ itself.
	return strings.TrimPrefix(strings.TrimSpace(string(out)), "/dev/")
}

// allTTYsForTest lists every tty iTerm2 currently owns, via an independent oracle.
func allTTYsForTest(t *testing.T) []string {
	t.Helper()
	out, err := exec.Command("osascript", "-e",
		`tell application "iTerm2" to return tty of sessions of tabs of windows`).Output()
	if err != nil {
		return nil
	}
	var ttys []string
	for _, f := range strings.Split(string(out), ",") {
		f = strings.TrimPrefix(strings.TrimSpace(f), "/dev/")
		if f != "" {
			ttys = append(ttys, f)
		}
	}
	return ttys
}

func TestFocusActuallyMovesFocus(t *testing.T) {
	if !Running() {
		t.Skip("iTerm2 not running")
	}
	// THE test for this package. Everything else proves a tty RESOLVES; this proves
	// something MOVES.
	//
	// Measured: deleting the whole `select w / select tab / select session / activate`
	// block from focus.applescript leaves every other test in this file GREEN,
	// because they focus the ALREADY-FOCUSED session — the match branch returns "OK"
	// whether or not any selection happens. A Focus that is a complete no-op returning
	// nil passes them all. That is exactly what a user experiences as "Enter does
	// nothing", which is the one failure this package exists to prevent.
	//
	// So: focus a DIFFERENT tty and assert the frontmost session actually changed.
	before := currentTTYForTest(t)
	if before == "" {
		t.Skip("cannot resolve the current tty")
	}
	var target string
	for _, tty := range allTTYsForTest(t) {
		if tty != before {
			target = tty
			break
		}
	}
	if target == "" {
		t.Skip("only one iTerm2 session — cannot verify movement")
	}

	if err := Focus(target); err != nil {
		t.Fatalf("Focus(%q) = %v, want nil", target, err)
	}
	// Restore the user's original focus regardless of outcome.
	defer func() { _ = Focus(before) }()

	after := currentTTYForTest(t)
	if after != target {
		t.Errorf("focus did not move: before=%q target=%q after=%q — the select/activate block is inert",
			before, target, after)
	}
}

func TestFocusResolvesArgvPastSeparator(t *testing.T) {
	if !Running() {
		t.Skip("iTerm2 not running")
	}
	// POSITIVE CONTROL. osascript does not strip `--`; it arrives as argv item 1.
	// Without this test, a script that reads item 1 directly would set target to
	// "--", answer NOTFOUND for every tty, and Focus would return ErrNotFound
	// always — indistinguishable from "that tab is genuinely gone". Assert that a
	// tty which DOES exist resolves, so ErrNotFound means what it says.
	tty := currentTTYForTest(t) // a tty known to belong to a live iTerm2 session
	if tty == "" {
		t.Skip("no resolvable iTerm2 tty in this environment")
	}
	if err := Focus(tty); err != nil {
		t.Fatalf("Focus(%q) = %v, want nil — argv indexing is likely off by one", tty, err)
	}
}

func TestFocusUnknownTTY(t *testing.T) {
	if !Running() {
		t.Skip("iTerm2 not running")
	}
	// A tty in no tab must report not-found rather than hanging or erroring.
	// The 3-second AppleScript timeout is the backstop; this must be far under.
	start := time.Now()
	err := Focus("ttys999")
	if !errors.Is(err, ErrNotFound) {
		t.Errorf("want ErrNotFound, got %v", err)
	}
	if elapsed := time.Since(start); elapsed > 5*time.Second {
		t.Errorf("must be time-bounded, took %v", elapsed)
	}
}

// TestFocusRunningGuardShortCircuits pins the Running() guard without
// touching the real iTerm2 process, which the task forbids stopping.
//
// Clearing PATH makes BOTH lsappinfo and osascript unresolvable via
// exec.LookPath, so this can't distinguish "guard present" from "guard
// absent" by success/failure alone — Focus errors either way. What it CAN
// distinguish is WHICH error comes back: with the guard in place, Running()
// itself fails to find lsappinfo and Focus returns ErrNotRunning before ever
// reaching osascript. If the guard were deleted or skipped, Focus would fall
// through to the osascript exec, which also fails to resolve, landing on the
// exit-status guard's ErrNotFound instead. The two errors are the signal.
func TestFocusRunningGuardShortCircuits(t *testing.T) {
	t.Setenv("PATH", "")
	start := time.Now()
	err := Focus("ttys017")
	if !errors.Is(err, ErrNotRunning) {
		t.Errorf("want ErrNotRunning (guard should fire before any exec), got %v", err)
	}
	if elapsed := time.Since(start); elapsed > 50*time.Millisecond {
		t.Errorf("must fail fast without spawning osascript, took %v", elapsed)
	}
}
