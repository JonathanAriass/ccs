package transcript

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func writeJSONL(t *testing.T, lines ...string) string {
	t.Helper()
	p := filepath.Join(t.TempDir(), "t.jsonl")
	if err := os.WriteFile(p, []byte(strings.Join(lines, "\n")+"\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	return p
}

func TestReadRejectsMachinery(t *testing.T) {
	// Every one of these is a `type:"user"` record that is NOT something the
	// human typed. The naive "last user record" answer is one of these far more
	// often than it is a real prompt.
	//
	// Each negative record must be rejected by EXACTLY ONE guard, so that deleting
	// that guard turns this test red. Read walks backwards and returns the first
	// match, so a record which stops being rejected immediately becomes LastHuman.
	// An earlier version combined toolUseResult, a tool_result content block, and a
	// missing human origin into ONE record — three guards covering each other, two
	// of which could then be deleted with the suite still green.
	p := writeJSONL(t,
		`{"type":"user","origin":{"kind":"human"},"message":{"content":"the real question"}}`,
		// toolUseResult present, but content is plain text and origin IS human —
		// so only the toolUseResult check can reject it.
		`{"type":"user","origin":{"kind":"human"},"toolUseResult":{"ok":true},"message":{"content":"tool result payload"}}`,
		// A tool_result content block, no toolUseResult field, origin IS human, AND a
		// text block so contentText returns something — otherwise isHuman's empty-text
		// check would reject it anyway and hasToolResult would still be untested.
		// With hasToolResult deleted this record becomes LastHuman and the test reddens.
		`{"type":"user","origin":{"kind":"human"},"message":{"content":[{"type":"tool_result","content":"x"},{"type":"text","text":"looks like a prompt but is not"}]}}`,
		`{"type":"user","isMeta":true,"origin":{"kind":"human"},"message":{"content":"meta injection"}}`,
		`{"type":"user","origin":{"kind":"human"},"message":{"content":"<system-reminder>noise</system-reminder>"}}`,
		`{"type":"user","origin":{"kind":"human"},"message":{"content":"<command-name>/effort</command-name>"}}`,
		`{"type":"user","origin":{"kind":"human"},"message":{"content":"[Request interrupted by user]"}}`,
		`{"type":"user","origin":{"kind":"task-notification"},"message":{"content":"agent done"}}`,
	)
	got, err := Read(p)
	if err != nil {
		t.Fatal(err)
	}
	if got.LastHuman != "the real question" {
		t.Errorf("LastHuman = %q, want %q", got.LastHuman, "the real question")
	}
}

func TestReadContentShapes(t *testing.T) {
	t.Run("string content", func(t *testing.T) {
		p := writeJSONL(t, `{"type":"user","origin":{"kind":"human"},"message":{"content":"plain string"}}`)
		got, _ := Read(p)
		if got.LastHuman != "plain string" {
			t.Errorf("got %q", got.LastHuman)
		}
	})

	t.Run("array content joins text blocks and drops the rest", func(t *testing.T) {
		p := writeJSONL(t, `{"type":"user","origin":{"kind":"human"},"message":{"content":[`+
			`{"type":"text","text":"hello"},{"type":"image","source":{}},{"type":"text","text":"world"}]}}`)
		got, _ := Read(p)
		if got.LastHuman != "hello\nworld" {
			t.Errorf("got %q", got.LastHuman)
		}
	})

	t.Run("older format uses promptSource instead of origin", func(t *testing.T) {
		p := writeJSONL(t, `{"type":"user","promptSource":"typed","message":{"content":"old format"}}`)
		got, _ := Read(p)
		if got.LastHuman != "old format" {
			t.Errorf("got %q", got.LastHuman)
		}
	})
}

func TestReadAssistant(t *testing.T) {
	// Thinking blocks must not leak into the preview.
	p := writeJSONL(t, `{"type":"assistant","message":{"id":"m1","content":[`+
		`{"type":"thinking","thinking":"secret reasoning"},`+
		`{"type":"tool_use","name":"Bash"},`+
		`{"type":"text","text":"the reply"}]}}`)
	got, _ := Read(p)
	if got.LastAssistant != "the reply" {
		t.Errorf("LastAssistant = %q want %q", got.LastAssistant, "the reply")
	}
	if strings.Contains(got.LastAssistant, "secret") {
		t.Error("thinking block leaked into the preview")
	}
}

func TestReadAITitle(t *testing.T) {
	p := writeJSONL(t,
		`{"type":"ai-title","aiTitle":"first title","sessionId":"s"}`,
		`{"type":"ai-title","aiTitle":"latest title","sessionId":"s"}`,
	)
	got, _ := Read(p)
	if got.AITitle != "latest title" {
		t.Errorf("AITitle = %q want %q", got.AITitle, "latest title")
	}
}

func TestReadNoConversation(t *testing.T) {
	// A real live session on the dev machine has a transcript containing ONLY
	// these two record types. It parses fine and yields nothing to preview —
	// a different failure path from the file being missing.
	p := writeJSONL(t,
		`{"type":"ai-title","aiTitle":"titled but empty","sessionId":"s"}`,
		`{"type":"agent-name","name":"x"}`,
	)
	got, err := Read(p)
	if err != nil {
		t.Fatalf("must not error: %v", err)
	}
	if got.LastHuman != "" || got.LastAssistant != "" {
		t.Error("want empty preview")
	}
	if got.AITitle != "titled but empty" {
		t.Errorf("title should still be recovered, got %q", got.AITitle)
	}
}

func TestReadUnparseableLines(t *testing.T) {
	p := writeJSONL(t,
		`{"type":"user","origin":{"kind":"human"},"message":{"content":"survivor"}}`,
		`{not json at all`,
	)
	got, err := Read(p)
	if err != nil {
		t.Fatalf("must not error: %v", err)
	}
	if got.LastHuman != "survivor" {
		t.Errorf("got %q", got.LastHuman)
	}
}
