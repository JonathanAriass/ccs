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
// must never prevent ccs from starting. This includes the JSON literal
// "null" specifically: json.Unmarshal("null", &m) sets m to nil and returns
// NO error, so the corrupt-file branch below never fires for it — a plain
// `len(got) == 0` check on the result cannot tell nil from an empty map, and
// a nil map panics on the very first `m.names[id] = name` write (a names.json
// containing "null" — e.g. from an empty `{}` mis-typed, or hand-edited — used
// to crash ccs on the first rename). Any other malformed JSON (an array, a
// number, a truncated object) already fails Unmarshal and hits the error
// branch; "null" is the one shape that succeeds while still meaning "nothing
// here".
func loadNames(path string) map[string]string {
	m := map[string]string{}
	b, err := os.ReadFile(path)
	if err != nil {
		return m
	}
	if err := json.Unmarshal(b, &m); err != nil {
		return map[string]string{}
	}
	if m == nil {
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
