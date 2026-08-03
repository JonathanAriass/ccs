package iterm

import (
	"context"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"syscall"
	"testing"
	"time"
)

// focusLockPath serialises every test in this package that MOVES iTerm2's focus,
// across processes.
//
// The hazard is not inside one test run. A single `go test ./...` is green, and
// `-p 1` changes nothing — only this package touches focus, and its tests are
// already serial within a run. The hazard is TWO OVERLAPPING INVOCATIONS: two
// agents on one repo, a file-watcher alongside a manual run, two worktrees.
// TestFocusActuallyMovesFocus focuses a tab and then reads back which tab is
// frontmost, so another process focusing anything inside that window corrupts
// the read. Measured here: with a second invocation issuing focus moves
// continuously, 4 of 8 runs failed — and they failed with "the select/activate
// block is inert", which accuses working production code of being a no-op.
// Whoever chases that message deletes the positive control or "fixes"
// focus.applescript. Hence a lock rather than a retry or a tolerance.
//
// The path must be FIXED and ABSOLUTE. A repo-relative lock would hand every
// worktree and agent checkout its own file and protect nothing. os.TempDir()
// honours $TMPDIR, which on macOS is the per-user DARWIN_USER_TEMP_DIR and is
// byte-identical across separate `go test` invocations (verified). A runner that
// set a per-invocation TMPDIR would silently un-protect these tests; nothing
// does today, and hardcoding /tmp trades that for a world-writable path.
var focusLockPath = filepath.Join(os.TempDir(), "ccs-iterm-focus.lock")

// lockFocus blocks until this process owns the focus lock and releases it when
// the test ends. Call it from any test that moves iTerm2's focus or reads back
// which session is frontmost.
func lockFocus(t *testing.T) {
	t.Helper()
	f := openFocusLock(t)
	// The KERNEL drops an flock when the fd closes, including when the process
	// is SIGKILLed, so a killed test binary cannot strand the lock. Registering
	// the cleanup before acquiring also covers a Fatalf inside acquire.
	t.Cleanup(func() { f.Close() })
	acquireFocusLock(t, f)
}

func openFocusLock(t *testing.T) *os.File {
	t.Helper()
	f, err := os.OpenFile(focusLockPath, os.O_CREATE|os.O_RDWR, 0o600)
	if err != nil {
		t.Fatalf("open focus lock %s: %v", focusLockPath, err)
	}
	return f
}

// acquireFocusLock WAITS for the lock; it never skips on contention. A positive
// control that skips under load is a positive control that has stopped guarding.
//
// It polls with LOCK_NB instead of blocking in the kernel so that a wedge
// surfaces as a named failure that points at the lock file, rather than as a
// silent hang until the go test timeout.
func acquireFocusLock(t *testing.T, f *os.File) {
	t.Helper()
	deadline := time.Now().Add(60 * time.Second)
	for {
		err := syscall.Flock(int(f.Fd()), syscall.LOCK_EX|syscall.LOCK_NB)
		if err == nil {
			return
		}
		if !errors.Is(err, syscall.EWOULDBLOCK) {
			t.Fatalf("flock %s: %v", focusLockPath, err)
		}
		if time.Now().After(deadline) {
			t.Fatalf("timed out after 60s waiting for %s — another ccs focus test is still holding it",
				focusLockPath)
		}
		time.Sleep(25 * time.Millisecond)
	}
}

// releaseFocusLock drops the lock while keeping the fd open, for the one test
// that has to hand it over mid-run.
func releaseFocusLock(t *testing.T, f *os.File) {
	t.Helper()
	if err := syscall.Flock(int(f.Fd()), syscall.LOCK_UN); err != nil {
		t.Fatalf("unlock %s: %v", focusLockPath, err)
	}
}

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

// TestFocusCmdBoundsTheWaitAfterTheDeadline pins the WaitDelay.
//
// The context deadline alone does not bound Focus. .Output() reads stdout
// through an os.Pipe; if osascript forked a child that inherited the write end,
// killing osascript on the deadline would leave cmd.Wait blocked on EOF from a
// pipe nobody closes. Focus is called synchronously from the TUI's Update, so
// that is a permanent freeze of the whole UI rather than a slow keypress.
//
// That scenario cannot be provoked from a test, so this asserts the field
// instead: the guarantee has to be structural, and a structural guarantee that
// nothing checks is one refactor away from being deleted as noise.
func TestFocusCmdBoundsTheWaitAfterTheDeadline(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	cmd := focusCmd(ctx, "/nonexistent/focus.applescript", "ttys017")

	if cmd.WaitDelay <= 0 {
		t.Errorf("cmd.WaitDelay = %v; a context deadline does not bound Wait when a forked child holds the stdout pipe", cmd.WaitDelay)
	}
	// Guard the extraction itself: the argv shape is what
	// TestFocusResolvesArgvPastSeparator depends on.
	want := []string{"osascript", "/nonexistent/focus.applescript", "--", "/dev/ttys017"}
	if len(cmd.Args) != len(want) {
		t.Fatalf("cmd.Args = %q, want %q", cmd.Args, want)
	}
	for i := range want {
		if cmd.Args[i] != want[i] {
			t.Errorf("cmd.Args[%d] = %q, want %q", i, cmd.Args[i], want[i])
		}
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
	//
	// That read-back is only meaningful if nothing else is moving focus, so take
	// the cross-process lock BEFORE sampling `before` — sampling it outside the
	// lock would let a concurrent run invalidate the baseline this test compares
	// against. See focusLockPath.
	lockFocus(t)
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
	//
	// This does not MOVE focus, but it reads the current tty and then focuses
	// it, so it must not interleave with another invocation's move — its Focus
	// would otherwise land on a stale tty and drag focus out from under the
	// other run's read-back.
	lockFocus(t)
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

// TestFocusMoveWaitsForTheCrossProcessLock pins that TestFocusActuallyMovesFocus
// actually takes the lock, by DETERMINISTICALLY holding it and requiring the
// other process to wait.
//
// It is deliberately not "spawn two runs and assert both pass". That shape was
// measured: two simultaneous full-suite runs redden only about 1 pair in 8 by
// chance, and two runs of just TestFocusActuallyMovesFocus reddened 0 of 8 — so
// a race-and-hope test would sit green through a completely deleted lock most of
// the time. Here the parent OWNS the contended resource and asserts the defining
// property: a focus-moving test cannot proceed while another process holds the
// lock. The 5s hold is >3x the child's measured 1.0-1.5s solo runtime, so "still
// running" is a statement about the lock, not about scheduler luck.
//
// Two anti-vacuity guards, both load-bearing. The environment guards below
// mirror the child's own, so this test SKIPS rather than passing when the child
// could only skip. And the final assertion demands the literal
// "--- PASS: TestFocusActuallyMovesFocus" in the child's -test.v output: a skip
// prints "--- SKIP" and reddens here, which is what stops "child exited 0" from
// being satisfied by a child that did nothing. It also catches a future rename
// of TestFocusActuallyMovesFocus silently turning the child into a no-op run.
func TestFocusMoveWaitsForTheCrossProcessLock(t *testing.T) {
	if !Running() {
		t.Skip("iTerm2 not running")
	}
	if len(allTTYsForTest(t)) < 2 {
		t.Skip("only one iTerm2 session — the child would skip and prove nothing")
	}

	f := openFocusLock(t)
	defer f.Close()
	acquireFocusLock(t, f)

	var out strings.Builder
	// os.Args[0] is this test binary. The ^...$ anchor is what keeps the child
	// from re-entering this test.
	child := exec.Command(os.Args[0], "-test.run", "^TestFocusActuallyMovesFocus$", "-test.v")
	child.Stdout, child.Stderr = &out, &out
	if err := child.Start(); err != nil {
		t.Fatalf("start child: %v", err)
	}
	done := make(chan error, 1)
	go func() { done <- child.Wait() }()

	const hold = 5 * time.Second
	start := time.Now()
	select {
	case err := <-done:
		releaseFocusLock(t, f)
		t.Fatalf("child finished in %v, inside the %v this process held %s (err=%v) — "+
			"TestFocusActuallyMovesFocus is not taking the cross-process focus lock.\n%s",
			time.Since(start), hold, focusLockPath, err, out.String())
	case <-time.After(hold):
	}
	releaseFocusLock(t, f)

	select {
	case err := <-done:
		if err != nil {
			t.Fatalf("child failed after the lock was released: %v\n%s", err, out.String())
		}
	case <-time.After(60 * time.Second):
		child.Process.Kill()
		<-done // let the output-copying goroutines finish before reading out
		t.Fatalf("child never finished after the lock was released\n%s", out.String())
	}
	if !strings.Contains(out.String(), "--- PASS: TestFocusActuallyMovesFocus") {
		t.Fatalf("child did not actually RUN the focus move (skipped?):\n%s", out.String())
	}
}
