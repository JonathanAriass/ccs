// Package iterm focuses iTerm2 tabs by tty.
package iterm

import (
	"context"
	_ "embed"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"time"
)

//go:embed focus.applescript
var focusScript string

var (
	// ErrNotFound means no iTerm2 session owns that tty.
	ErrNotFound = errors.New("iterm: no tab owns that tty")
	// ErrNotRunning means iTerm2 is not running, so focusing is impossible.
	ErrNotRunning = errors.New("iterm: iTerm2 is not running")
)

// Running reports whether iTerm2 is up.
//
// This guard is not optional: without it, osascript's `tell application` would
// LAUNCH iTerm2 and block for 120 seconds. lsappinfo answers in ~9ms, versus
// ~161ms for the AppleScript equivalent.
func Running() bool {
	out, err := exec.Command("lsappinfo", "find", "bundleID=com.googlecode.iterm2").Output()
	return err == nil && strings.TrimSpace(string(out)) != ""
}

// focusCmd builds the osascript invocation for one tty.
//
// It exists as its own function so the WaitDelay below can be asserted directly
// and cheaply, by TestFocusCmdBoundsTheWaitAfterTheDeadline — a 0.00s structural
// check that names the field when it changes.
//
// Inlining it back into Focus is still a real regression, and the suite will now
// catch it: TestFocusReturnsWhenAForkedChildHoldsStdout drives the whole guarantee
// through Focus against a fake osascript that forks a child holding the stdout
// pipe, so an inlined call that drops WaitDelay reddens there even though the
// structural check above is untouched.
func focusCmd(ctx context.Context, script, tty string) *exec.Cmd {
	// The tty is passed as argv, never interpolated into the script source.
	cmd := exec.CommandContext(ctx, "osascript", script, "--", "/dev/"+tty)
	// WaitDelay is what makes the caller's context deadline structural rather
	// than assumed. .Output() collects stdout through an os.Pipe, and killing
	// osascript on the deadline only unblocks Wait if nothing else holds that
	// pipe's write end — if osascript ever forks a child that inherited it, Wait
	// blocks on EOF forever and the 5-second context buys nothing. Focus runs
	// SYNCHRONOUSLY inside Update, on bubbletea's single event-loop goroutine, so
	// "forever" here means the TUI never accepts another keystroke. WaitDelay
	// bounds the gap between the process dying and its pipes closing.
	cmd.WaitDelay = time.Second
	return cmd
}

// Focus selects the window, tab, and pane owning tty (e.g. "ttys017").
//
// The caller must resolve tty FRESH immediately before calling. macOS recycles
// tty numbers, so a mapping cached at poll time can point at a completely
// unrelated tab by the time Enter is pressed.
func Focus(tty string) error {
	if tty == "" || tty == "??" {
		return ErrNotFound
	}
	if !Running() {
		return ErrNotRunning
	}
	dir, err := os.MkdirTemp("", "ccs-osa")
	if err != nil {
		return err
	}
	defer os.RemoveAll(dir)
	path := filepath.Join(dir, "focus.applescript")
	if err := os.WriteFile(path, []byte(focusScript), 0o600); err != nil {
		return err
	}
	// The context deadline is defence in depth. focus.applescript already wraps its
	// tell block in `with timeout of 3 seconds`, but that bounds Apple Event replies,
	// not the osascript process itself — and Focus is called SYNCHRONOUSLY from the
	// TUI's Enter handler, so anything unbounded here freezes the UI. Belt and braces
	// on the one call that can block.
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	out, err := focusCmd(ctx, path, tty).Output()
	if err != nil {
		// AppleScript error text is LOCALIZED — this machine returns Spanish.
		// Never branch on English message text; the exit status is the signal.
		return ErrNotFound
	}
	if strings.TrimSpace(string(out)) != "OK" {
		return ErrNotFound
	}
	return nil
}
