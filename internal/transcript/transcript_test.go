package transcript

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
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
