// Package session discovers live Claude Code sessions from the registry that
// Claude Code maintains at ~/.claude/sessions/<pid>.json.
//
// This package is strictly a reader. It never writes to, prunes, or locks
// anything under ~/.claude.
package session

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
)

// Session is one entry of the registry.
//
// Note the casing: the registry is camelCase (sessionId, procStart), while
// Claude Code's hook stdin uses snake_case for the same concepts.
type Session struct {
	PID             int    `json:"pid"`
	SessionID       string `json:"sessionId"`
	CWD             string `json:"cwd"`
	Version         string `json:"version"`
	Kind            string `json:"kind"`
	Name            string `json:"name"`
	Status          string `json:"status"`
	WaitingFor      string `json:"waitingFor"`
	ProcStart       string `json:"procStart"`
	StartedAt       int64  `json:"startedAt"`
	UpdatedAt       int64  `json:"updatedAt"`
	StatusUpdatedAt int64  `json:"statusUpdatedAt"`
}

// Load parses every *.json in dir. Files that are unreadable or malformed are
// skipped rather than failing the whole load: sessions exit while we read, and
// one corrupt file must never blank the list. A missing directory is not an
// error — it just means no sessions have ever run.
func Load(dir string) ([]Session, error) {
	entries, err := os.ReadDir(dir)
	if err != nil {
		if os.IsNotExist(err) {
			return nil, nil
		}
		return nil, err
	}
	var out []Session
	for _, e := range entries {
		if e.IsDir() || !strings.HasSuffix(e.Name(), ".json") {
			continue
		}
		b, err := os.ReadFile(filepath.Join(dir, e.Name()))
		if err != nil {
			continue // vanished between ReadDir and ReadFile
		}
		var s Session
		if err := json.Unmarshal(b, &s); err != nil {
			continue
		}
		if s.PID <= 0 {
			continue // guards the signal probe in liveness.go
		}
		out = append(out, s)
	}
	return out, nil
}

// Dir is the default registry location.
func Dir() string {
	home, err := os.UserHomeDir()
	if err != nil {
		return ""
	}
	return filepath.Join(home, ".claude", "sessions")
}
