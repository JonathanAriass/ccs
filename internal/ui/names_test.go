package ui

import (
	"os"
	"path/filepath"
	"testing"
)

// wantEmptyNonNil fails unless got is a non-nil, zero-length map.
//
// `len(got) != 0` alone cannot tell a nil map from an empty one — len(nil)
// is 0 — and json.Unmarshal([]byte("null"), &m) sets m to nil while
// returning NO error, so a names.json containing the literal "null" used to
// sail through this exact check while leaving loadNames' caller holding a
// nil map, which panics on its first write (m.names[id] = name). Both
// halves matter: nil must fail this just as loudly as a populated map would.
func wantEmptyNonNil(t *testing.T, label string, got map[string]string) {
	t.Helper()
	if got == nil {
		t.Fatalf("%s: want a non-nil empty map, got nil", label)
	}
	if len(got) != 0 {
		t.Fatalf("%s: want empty map, got %v", label, got)
	}
}

func TestLoadNamesMissingAndCorrupt(t *testing.T) {
	dir := t.TempDir()
	wantEmptyNonNil(t, "missing file", loadNames(filepath.Join(dir, "absent.json")))

	p := filepath.Join(dir, "corrupt.json")
	os.WriteFile(p, []byte("{not json"), 0o644)
	wantEmptyNonNil(t, "corrupt file", loadNames(p))

	// The JSON literal "null" is the one shape that is NOT a decode error —
	// Unmarshal("null", &m) succeeds and sets m to nil — so it needs its own
	// fixture distinct from "corrupt.json" above, which fails to decode at
	// all and would pass this test even without the nil-guard in loadNames.
	nullPath := filepath.Join(dir, "null.json")
	os.WriteFile(nullPath, []byte("null"), 0o644)
	wantEmptyNonNil(t, "null-literal file", loadNames(nullPath))
}

// TestNewWiresTheRealNamesFile pins the final review's I3 finding: every
// rename test in the package injects m.names/m.namesFile by hand, and
// TestMain points XDG_CONFIG_HOME at an empty temp dir, so no test ever
// observed New() actually reading (or targeting) a real path. Two
// independent mutations at the New() call site — loadNames(namesPath()) →
// an empty map, and namesFile: namesPath() → "" — both left the whole
// suite green: the first silently breaks the feature's headline promise
// (overrides never survive a restart) and the second breaks every save
// (`rename won't persist` on every rename, nothing ever written). This
// test seeds a real file under a temp XDG_CONFIG_HOME, builds a model the
// way main() does — through New(), not a hand-built struct literal — and
// checks both halves of the wiring at once.
func TestNewWiresTheRealNamesFile(t *testing.T) {
	dir := t.TempDir()
	t.Setenv("XDG_CONFIG_HOME", dir)
	want := filepath.Join(dir, "ccs", "names.json")
	if err := os.MkdirAll(filepath.Dir(want), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(want, []byte(`{"seeded-session":"seeded-name"}`), 0o644); err != nil {
		t.Fatal(err)
	}

	m := New()

	if got := m.names["seeded-session"]; got != "seeded-name" {
		t.Errorf("New() did not load the on-disk override: m.names[%q] = %q, want %q", "seeded-session", got, "seeded-name")
	}
	if m.namesFile != want {
		t.Errorf("m.namesFile = %q, want %q", m.namesFile, want)
	}
}

func TestSaveNamesRoundTripsAndIsAtomic(t *testing.T) {
	p := filepath.Join(t.TempDir(), "ccs", "names.json")
	want := map[string]string{"sess-1": "my migration"}
	if err := saveNames(p, want); err != nil {
		t.Fatal(err)
	}
	if got := loadNames(p); got["sess-1"] != "my migration" {
		t.Fatalf("round trip: %v", got)
	}
	// Atomicity: a save that cannot complete must leave the old content intact.
	// Make the DIRECTORY read-only so the temp file cannot be created.
	os.Chmod(filepath.Dir(p), 0o555)
	defer os.Chmod(filepath.Dir(p), 0o755)
	if err := saveNames(p, map[string]string{"sess-1": "clobbered"}); err == nil {
		t.Fatal("want an error from an unwritable dir")
	}
	os.Chmod(filepath.Dir(p), 0o755)
	if got := loadNames(p); got["sess-1"] != "my migration" {
		t.Fatalf("failed save must not touch the old file, got %v", got)
	}
}
