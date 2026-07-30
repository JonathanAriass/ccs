// Package iterm focuses iTerm2 tabs by tty.
package iterm

import (
	_ "embed"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
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
	// Pass the tty as argv, never interpolated into the script source.
	out, err := exec.Command("osascript", path, "--", "/dev/"+tty).Output()
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
