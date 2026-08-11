package ui

import (
	"os"
	"path/filepath"
	"testing"
)

func TestLoadNamesMissingAndCorrupt(t *testing.T) {
	dir := t.TempDir()
	if got := loadNames(filepath.Join(dir, "absent.json")); len(got) != 0 {
		t.Fatalf("missing file: want empty map, got %v", got)
	}
	p := filepath.Join(dir, "corrupt.json")
	os.WriteFile(p, []byte("{not json"), 0o644)
	if got := loadNames(p); len(got) != 0 {
		t.Fatalf("corrupt file: want empty map, got %v", got)
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
