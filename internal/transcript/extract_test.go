package transcript

import (
	"encoding/json"
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

func TestReadRejectsEveryMachineryPrefix(t *testing.T) {
	// One fixture per prefix, so deleting ANY single entry from machineryPrefixes
	// reddens the suite. Without this, only three of the eleven were exercised and
	// the rest could be removed silently — and the visible consequence is the preview
	// column showing raw command output or a hook injection instead of the human's
	// question, which reads as "this feature is bad" rather than "this is a bug".
	//
	// Each fixture is a single-guard case: origin IS human, not meta, no toolUseResult,
	// no tool_result block, non-empty text — so the prefix filter is the only thing
	// that can reject it. The control record below proves the file is readable at all.
	//
	// This is a literal copy of machineryPrefixes, NOT a range over the production
	// variable. Ranging over machineryPrefixes itself was tried first and does not
	// work: deleting an entry from the production list also deletes that entry's own
	// t.Run case, so the subtest simply stops existing instead of failing, and the
	// suite reports PASS with one fewer subtest. Verified by removing
	// "<local-command-stdout>", "<task-notification>", and "<ide_opened_file>" from
	// machineryPrefixes in turn with a range-based version of this test still in
	// place: each time, `go test -run TestReadRejectsEveryMachineryPrefix -v` showed
	// 10 RUN lines instead of 11 and PASS, with no failure anywhere.
	wantPrefixes := []string{
		"<command-name>", "<command-message>", "<command-args>",
		"<local-command-stdout>", "<local-command-caveat>",
		"<task-notification>", "<system-reminder>", "<ide_opened_file>",
		"[Request interrupted", "Caveat: The messages below",
		"This session is being continued from a previous conversation",
	}
	// Catches the opposite drift: a prefix added to machineryPrefixes with no
	// matching test case here would otherwise go uncovered silently.
	if len(wantPrefixes) != len(machineryPrefixes) {
		t.Fatalf("machineryPrefixes has %d entries, this test covers %d — add the new one here",
			len(machineryPrefixes), len(wantPrefixes))
	}

	for _, prefix := range wantPrefixes {
		t.Run(prefix, func(t *testing.T) {
			p := writeJSONL(t,
				`{"type":"user","origin":{"kind":"human"},"message":{"content":"the real question"}}`,
				`{"type":"user","origin":{"kind":"human"},"message":{"content":`+
					mustJSON(prefix+" trailing content")+`}}`,
			)
			got, err := Read(p)
			if err != nil {
				t.Fatal(err)
			}
			if got.LastHuman != "the real question" {
				t.Errorf("prefix %q not rejected: LastHuman = %q", prefix, got.LastHuman)
			}
		})
	}
}

// mustJSON encodes s as a JSON string literal, so prefixes containing quotes or
// brackets (e.g. "[Request interrupted") survive embedding in the fixture.
func mustJSON(s string) string {
	b, err := json.Marshal(s)
	if err != nil {
		panic(err)
	}
	return string(b)
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

	t.Run("older format REJECTS records that are not typed", func(t *testing.T) {
		// The reject direction of `human := r.PromptSource == "typed"`, which the
		// accept-direction test above cannot cover.
		//
		// Every other negative fixture in this file carries a non-nil `origin`, so the
		// `if r.Origin != nil` branch overrides the promptSource result and the bare
		// comparison never decides anything. Measured: mutating the line to
		// `human := true` — default-accept for old-format records, the live regression
		// shape — leaves the ENTIRE suite green without this case.
		//
		// Both records below lack `origin` entirely, so promptSource is the only thing
		// that can reject them. The control record proves the file reads at all.
		p := writeJSONL(t,
			`{"type":"user","promptSource":"typed","message":{"content":"the real question"}}`,
			`{"type":"user","promptSource":"suggested","message":{"content":"a suggestion, not typed"}}`,
			`{"type":"user","message":{"content":"no promptSource at all"}}`,
		)
		got, _ := Read(p)
		if got.LastHuman != "the real question" {
			t.Errorf("LastHuman = %q — an old-format non-typed record leaked through", got.LastHuman)
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
