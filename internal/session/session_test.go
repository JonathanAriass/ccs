package session

import (
	"os"
	"path/filepath"
	"testing"
)

func TestLoad(t *testing.T) {
	dir := t.TempDir()
	write := func(name, body string) {
		if err := os.WriteFile(filepath.Join(dir, name), []byte(body), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	write("90880.json", `{"pid":90880,"sessionId":"af78c52c","cwd":"/Users/x/Desktop",
	 "procStart":"Wed Jul 29 09:04:10 2026","version":"2.1.220","kind":"interactive",
	 "name":"desktop-b4","status":"waiting","statusUpdatedAt":1785322956268}`)
	write("1601.json", `{"pid":1601,"sessionId":"beef","cwd":"/Users/x","procStart":"Tue Jul  7 07:05:15 2026","status":"busy"}`)
	write("broken.json", `{not json`)
	write("notes.txt", `ignored`)

	got, err := Load(dir)
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if len(got) != 2 {
		t.Fatalf("want 2 sessions (malformed and non-json skipped), got %d", len(got))
	}

	byPID := map[int]Session{}
	for _, s := range got {
		byPID[s.PID] = s
	}
	if s := byPID[90880]; s.Name != "desktop-b4" || s.Status != "waiting" || s.CWD != "/Users/x/Desktop" {
		t.Errorf("90880 parsed wrong: %+v", s)
	}
	// Single-digit day keeps its two spaces — the liveness check depends on it.
	if s := byPID[1601]; s.ProcStart != "Tue Jul  7 07:05:15 2026" {
		t.Errorf("procStart mangled: %q", s.ProcStart)
	}
}

func TestLoadMissingDir(t *testing.T) {
	got, err := Load(filepath.Join(t.TempDir(), "nope"))
	if err != nil {
		t.Fatalf("missing dir must not error, got %v", err)
	}
	if len(got) != 0 {
		t.Fatalf("want 0 sessions, got %d", len(got))
	}
}
