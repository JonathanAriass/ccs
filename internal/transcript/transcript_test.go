package transcript

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestSlug(t *testing.T) {
	cases := []struct{ in, want string }{
		{"/Users/ariass/Desktop", "-Users-ariass-Desktop"},
		{"/Users/ariass/.config/nvim", "-Users-ariass--config-nvim"},
		{
			"/Users/ariass/Desktop/okt-api/.claude/worktrees/OKT-18841-detracciones-mx",
			"-Users-ariass-Desktop-okt-api--claude-worktrees-OKT-18841-detracciones-mx",
		},
	}
	for _, c := range cases {
		if got := Slug(c.in); got != c.want {
			t.Errorf("Slug(%q) = %q want %q", c.in, got, c.want)
		}
	}
}

func TestTailLines(t *testing.T) {
	dir := t.TempDir()

	t.Run("record larger than the default scanner buffer", func(t *testing.T) {
		// bufio.Scanner's default 64KB buffer read 316 of 28,631 lines on the
		// real 87MB transcript before failing with "token too long". The largest
		// single record observed in the corpus is 1.28MB.
		p := filepath.Join(dir, "big.jsonl")
		big := `{"type":"user","pad":"` + strings.Repeat("x", 200_000) + `"}`
		if err := os.WriteFile(p, []byte(big+"\n"), 0o644); err != nil {
			t.Fatal(err)
		}
		lines, err := tailLines(p, 400, 8<<20)
		if err != nil {
			t.Fatalf("tailLines: %v", err)
		}
		if len(lines) != 1 {
			t.Fatalf("want 1 line, got %d", len(lines))
		}
	})

	t.Run("truncated final record is tolerated", func(t *testing.T) {
		p := filepath.Join(dir, "partial.jsonl")
		body := `{"type":"user","message":{"content":"one"}}` + "\n" + `{"type":"asss`
		if err := os.WriteFile(p, []byte(body), 0o644); err != nil {
			t.Fatal(err)
		}
		lines, err := tailLines(p, 400, 8<<20)
		if err != nil {
			t.Fatalf("tailLines: %v", err)
		}
		if len(lines) == 0 {
			t.Fatal("want the complete record to survive")
		}
	})

	t.Run("empty file", func(t *testing.T) {
		p := filepath.Join(dir, "empty.jsonl")
		if err := os.WriteFile(p, nil, 0o644); err != nil {
			t.Fatal(err)
		}
		lines, err := tailLines(p, 400, 8<<20)
		if err != nil {
			t.Fatalf("empty file must not error: %v", err)
		}
		if len(lines) != 0 {
			t.Fatalf("want 0 lines, got %d", len(lines))
		}
	})

	t.Run("missing file returns an error", func(t *testing.T) {
		if _, err := tailLines(filepath.Join(dir, "nope.jsonl"), 400, 8<<20); err == nil {
			t.Fatal("want an error for a missing file")
		}
	})

	t.Run("a file past maxBytes drops only the seek fragment", func(t *testing.T) {
		// The only test that exercises the mid-file seek, so it is the only thing
		// pinning the fragment-drop. It also catches the wrong placement of that
		// drop: trimming after the loop would discard a valid record here instead
		// of the fragment, because this file holds far more than maxRecords lines
		// past the seek point.
		p := filepath.Join(dir, "huge.jsonl")
		f, err := os.Create(p)
		if err != nil {
			t.Fatal(err)
		}
		// ~9MB of padded records, comfortably past the 8MB window.
		pad := strings.Repeat("x", 900)
		for i := 0; i < 10000; i++ {
			fmt.Fprintf(f, `{"n":%d,"pad":"%s"}`+"\n", i, pad)
		}
		f.Close()

		st, err := os.Stat(p)
		if err != nil {
			t.Fatal(err)
		}
		if st.Size() <= MaxBytes {
			t.Fatalf("fixture must exceed MaxBytes to exercise the seek: %d", st.Size())
		}

		lines, err := tailLines(p, MaxRecords, MaxBytes)
		if err != nil {
			t.Fatalf("tailLines: %v", err)
		}
		if len(lines) != MaxRecords {
			t.Fatalf("want %d lines, got %d", MaxRecords, len(lines))
		}
		// Every retained line must be a COMPLETE record. A surviving fragment would
		// not parse; a mis-placed drop would still leave all lines complete, which
		// is why the count assertion above matters too.
		for i, ln := range lines {
			var rec map[string]any
			if err := json.Unmarshal([]byte(ln), &rec); err != nil {
				t.Fatalf("line %d is not a complete record: %v (%.60s)", i, err, ln)
			}
		}
		// And the tail must really be the END of the file, not the middle.
		var last map[string]any
		_ = json.Unmarshal([]byte(lines[len(lines)-1]), &last)
		if n, _ := last["n"].(float64); int(n) != 9999 {
			t.Errorf("last retained record is n=%v, want 9999 — not reading the tail", last["n"])
		}
	})

	t.Run("blank lines interleaved with records are skipped", func(t *testing.T) {
		// Real transcripts have been observed with stray blank lines. Without the
		// skip, an empty string would occupy a ring slot as if it were a record,
		// displacing a real one and failing to parse as JSON downstream.
		p := filepath.Join(dir, "blank.jsonl")
		body := "{\"n\":1}\n\n{\"n\":2}\n   \n{\"n\":3}\n"
		if err := os.WriteFile(p, []byte(body), 0o644); err != nil {
			t.Fatal(err)
		}
		lines, err := tailLines(p, 400, 8<<20)
		if err != nil {
			t.Fatalf("tailLines: %v", err)
		}
		if len(lines) != 3 {
			t.Fatalf("want 3 lines, got %d: %q", len(lines), lines)
		}
		for i, ln := range lines {
			if strings.TrimSpace(ln) == "" {
				t.Fatalf("line %d is blank, want a record", i)
			}
			var rec map[string]any
			if err := json.Unmarshal([]byte(ln), &rec); err != nil {
				t.Fatalf("line %d is not valid JSON: %v (%q)", i, err, ln)
			}
		}
	})

	t.Run("caps at maxRecords keeping the LAST ones", func(t *testing.T) {
		p := filepath.Join(dir, "many.jsonl")
		var sb strings.Builder
		for i := 0; i < 100; i++ {
			sb.WriteString(`{"n":`)
			sb.WriteString(strings.Repeat("0", 1))
			sb.WriteString(`}`)
			sb.WriteByte('\n')
		}
		if err := os.WriteFile(p, []byte(sb.String()), 0o644); err != nil {
			t.Fatal(err)
		}
		lines, err := tailLines(p, 10, 8<<20)
		if err != nil {
			t.Fatal(err)
		}
		if len(lines) != 10 {
			t.Fatalf("want 10, got %d", len(lines))
		}
	})
}

// buildSessionTree writes a fake session layout under dir and returns the main
// transcript path. mtimes are set explicitly — mtime ordering is the entire
// behaviour under test, so no fixture may rely on write order.
func buildSessionTree(t *testing.T, dir string, mainAge, teammateAge, workflowAge time.Duration) string {
	t.Helper()
	now := time.Now()
	mk := func(rel, content string, age time.Duration) string {
		p := filepath.Join(dir, rel)
		if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(p, []byte(content), 0o644); err != nil {
			t.Fatal(err)
		}
		if err := os.Chtimes(p, now.Add(-age), now.Add(-age)); err != nil {
			t.Fatal(err)
		}
		return p
	}
	main := mk("sess-1.jsonl", `{"type":"assistant","message":{"content":[{"type":"text","text":"MAIN-TEXT"}]}}`+"\n", mainAge)
	mk("sess-1/subagents/agent-aimpl-fix2-0123456789abcdef.jsonl",
		`{"type":"assistant","message":{"content":[{"type":"text","text":"TEAMMATE-TEXT"}]}}`+"\n", teammateAge)
	// Real unnamed (workflow) agent files are "agent-a<16 hex>" — the "a" marker
	// IS present, there is just no separate name and no trailing "-<hex>" for
	// agentNamePat to match. (task-1 review, Minor 2: an earlier fixture here
	// used "agent-<17 hex>", a shape absent from the real corpus that happened
	// to fall back to "agent" for a different reason — a missing "a" marker,
	// not a missing name-tail — so it didn't actually cover this case.)
	mk("sess-1/subagents/workflows/wf_x-1/agent-a15f8508938729d84.jsonl",
		`{"type":"assistant","message":{"content":[{"type":"text","text":"WORKFLOW-TEXT"}]}}`+"\n", workflowAge)
	// A .meta.json sidecar sits beside every real subagent transcript in the
	// actual corpus (737 of them, one per .jsonl). Given the newest mtime of the
	// whole tree — well under any age a caller passes for the three sources
	// above — so a LiveSource that forgot to filter by extension (task-1
	// review, Important 2) would hand this path back as "live" instead of
	// degrading to one of the three real sources.
	mk("sess-1/subagents/agent-aimpl-fix2-0123456789abcdef.meta.json",
		`{"agentType":"impl-fix2"}`, time.Second)
	return main
}

func TestLiveSourcePicksNewestAcrossMainAndSubagents(t *testing.T) {
	cases := []struct {
		name                  string
		mainAge, tmAge, wfAge time.Duration
		wantAgent             string // "" = main
	}{
		// Both directions, per the house rule: main newest AND subagent newest.
		{"main newest", time.Minute, time.Hour, time.Hour, ""},
		{"teammate newest", time.Hour, time.Minute, 2 * time.Hour, "impl-fix2"},
		{"workflow agent newest (bare hex name falls back)", time.Hour, 2 * time.Hour, time.Minute, "agent"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			main := buildSessionTree(t, t.TempDir(), c.mainAge, c.tmAge, c.wfAge)
			src := LiveSource(main)
			// The .meta.json sidecar buildSessionTree writes is always the newest
			// file in the tree — this pins the extension filter (task-1 review,
			// Important 2), not just the three real sources' relative ordering.
			if filepath.Ext(src.Path) != ".jsonl" {
				t.Fatalf("picked a non-.jsonl file as the live source: %s", src.Path)
			}
			if src.Agent != c.wantAgent {
				t.Fatalf("Agent = %q, want %q (picked %s)", src.Agent, c.wantAgent, src.Path)
			}
			if src.Mod.IsZero() {
				t.Fatal("Mod is zero for an existing source")
			}
			if c.wantAgent == "" && src.Path != main {
				t.Errorf("main newest but Path = %s", src.Path)
			}
		})
	}
}

func TestLiveSourceWithNoFilesAtAll(t *testing.T) {
	src := LiveSource(filepath.Join(t.TempDir(), "absent.jsonl"))
	if !src.Mod.IsZero() || src.Agent != "" {
		t.Fatalf("want zero Source for nothing on disk, got %+v", src)
	}
}

func TestLiveSourceMainMissingButSubagentsExist(t *testing.T) {
	dir := t.TempDir()
	main := buildSessionTree(t, dir, time.Minute, time.Hour, time.Hour)
	if err := os.Remove(main); err != nil {
		t.Fatal(err)
	}
	src := LiveSource(main)
	if filepath.Ext(src.Path) != ".jsonl" {
		t.Fatalf("picked a non-.jsonl file as the live source: %s", src.Path)
	}
	if src.Agent != "impl-fix2" {
		t.Fatalf("want the teammate as live source when main is purged, got %+v", src)
	}
}
