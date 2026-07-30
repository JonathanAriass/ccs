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

	// A negative fixture must be rejected by exactly ONE guard, and must
	// be VALID JSON so it survives the Unmarshal check and actually reaches that
	// guard. An earlier version used invalid JSON for these, which meant the parse
	// error rejected them first and both the pid guard and the suffix filter could
	// be deleted with the suite still green. Where the platform makes a single-guard
	// fixture impossible, that is called out at the fixture instead of pretending
	// otherwise.
	write("notes.txt", `{"pid":999,"sessionId":"wrong-extension","cwd":"/tmp"}`) // only the .json filter rejects this
	write("zeropid.json", `{"pid":0,"sessionId":"zero-pid","cwd":"/tmp"}`)       // only the pid<=0 guard rejects this
	write("nopid.json", `{"sessionId":"absent-pid","cwd":"/tmp"}`)               // missing pid unmarshals to 0
	// A directory named *.json passes the suffix filter, so this fixture reaches
	// IsDir() — but os.ReadFile on a directory also errors on its own, so IsDir()
	// and the ReadFile error jointly reject it. No fixture can pin IsDir() alone.
	if err := os.Mkdir(filepath.Join(dir, "subdir.json"), 0o755); err != nil {
		t.Fatal(err)
	}

	got, err := Load(dir)
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if len(got) != 2 {
		t.Fatalf("want 2 sessions (malformed, wrong-extension, pid<=0 and dir all skipped), got %d", len(got))
	}

	// Name each rejection explicitly. The count above would still pass if one guard
	// over-rejected while another under-rejected, so pin the identities too.
	for _, s := range got {
		if s.PID <= 0 {
			t.Errorf("a session with pid <= 0 leaked through: %+v", s)
		}
		switch s.SessionID {
		case "wrong-extension":
			t.Error("a non-.json file was loaded — the suffix filter is not doing anything")
		case "zero-pid", "absent-pid":
			t.Errorf("pid<=0 session %q was loaded — the pid guard is not doing anything", s.SessionID)
		}
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
