package session

import (
	"os"
	"path/filepath"
	"testing"
	"time"
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
	// The fixture has always carried a real "statusUpdatedAt" but nothing read it
	// back, so the json tag could be renamed or dropped and this suite stayed
	// green while every row lost its age. Pin the raw millisecond value here; its
	// UNIT is pinned by TestStatusUpdatedTimeDecodesRegistryMilliseconds, which
	// is a genuinely different failure mode — neither mutation reddens the other
	// test.
	if s := byPID[90880]; s.StatusUpdatedAt != registryStatusUpdatedAt {
		t.Errorf("statusUpdatedAt = %d, want %d — the json tag is not reaching the field",
			s.StatusUpdatedAt, registryStatusUpdatedAt)
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

// registryStatusUpdatedAt is one "statusUpdatedAt" value copied VERBATIM out of a
// live ~/.claude/sessions/<pid>.json entry — the same literal TestLoad's fixture
// carries, and the same one internal/ui/layout_test.go names registryMs. Live
// entries are 13-digit millisecond epochs without exception (verified across the
// whole registry: 1781533969571, 1781781776210, 1783407915578, ...), never
// seconds. The duplicated literal is deliberate: a real registry value cannot be
// recomputed in the wrong unit the way a time.Now()-derived one can.
const registryStatusUpdatedAt int64 = 1785322956268

// TestStatusUpdatedTimeDecodesRegistryMilliseconds pins the UNIT of the
// registry's statusUpdatedAt in the package that owns both the json tag and the
// conversion. It was previously pinned only from internal/ui, through golden
// frames of compactAge/formatRow/View — which inverts the dependency, since
// internal/ui is the volatile layer and those frames get rewritten regularly.
// Anyone editing this package and running the prescribed
// `go test -count=1 ./internal/session/` got a green suite with the unit wrong.
//
// The expected instant is written out LONGHAND rather than derived from
// time.UnixMilli, because a fixture built with the same call under test agrees
// with any mutation of it. Do not "simplify" it back. Read as seconds, this
// literal is the year 58544; every duration computed from it goes negative, the
// caller's clock-skew clamp swallows the negative, and the age column renders
// "now" for every row — the bug this test exists to keep dead, which shipped once
// already.
//
// Deliberately NOT added alongside: a zero-value test. It is green under every
// mutation of StatusUpdatedTime, so it would be a dead assertion.
func TestStatusUpdatedTimeDecodesRegistryMilliseconds(t *testing.T) {
	got := Session{StatusUpdatedAt: registryStatusUpdatedAt}.StatusUpdatedTime().UTC()
	want := time.Date(2026, 7, 29, 11, 2, 36, 268_000_000, time.UTC)
	// Equal compares instants, so this is timezone-independent. A rewrite to ==
	// or to comparing .String() would reintroduce a TZ dependency.
	if !got.Equal(want) {
		t.Errorf("StatusUpdatedTime(%d) = %s, want %s — statusUpdatedAt is MILLISECONDS",
			registryStatusUpdatedAt, got.Format(time.RFC3339Nano), want.Format(time.RFC3339Nano))
	}
}
