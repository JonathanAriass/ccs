package ui

import (
	"encoding/json"
	"os"
	"path/filepath"
)

// namesPath is where session-rename overrides live. ccs's own file — the
// registry under ~/.claude belongs to Claude Code and is NEVER written.
func namesPath() string {
	base := os.Getenv("XDG_CONFIG_HOME")
	if base == "" {
		home, _ := os.UserHomeDir()
		base = filepath.Join(home, ".config")
	}
	return filepath.Join(base, "ccs", "names.json")
}

// loadNames treats a missing or corrupt file as empty: a broken names file
// must never prevent ccs from starting.
func loadNames(path string) map[string]string {
	m := map[string]string{}
	b, err := os.ReadFile(path)
	if err != nil {
		return m
	}
	if json.Unmarshal(b, &m) != nil {
		return map[string]string{}
	}
	return m
}

// saveNames writes atomically: temp file in the SAME directory, then rename.
// A crash mid-save leaves the previous file intact.
func saveNames(path string, m map[string]string) error {
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return err
	}
	b, err := json.MarshalIndent(m, "", "  ")
	if err != nil {
		return err
	}
	tmp, err := os.CreateTemp(filepath.Dir(path), ".names-*")
	if err != nil {
		return err
	}
	if _, err := tmp.Write(b); err != nil {
		tmp.Close()
		os.Remove(tmp.Name())
		return err
	}
	if err := tmp.Close(); err != nil {
		os.Remove(tmp.Name())
		return err
	}
	return os.Rename(tmp.Name(), path)
}
