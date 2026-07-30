package transcript

import (
	"bytes"
	"encoding/json"
	"strings"
)

// Summary is what the preview pane needs from a transcript.
type Summary struct {
	LastHuman     string
	LastAssistant string
	AITitle       string
}

type rec struct {
	Type          string          `json:"type"`
	IsMeta        bool            `json:"isMeta"`
	PromptSource  string          `json:"promptSource"`
	AITitle       string          `json:"aiTitle"`
	ToolUseResult json.RawMessage `json:"toolUseResult"`
	Origin        *struct {
		Kind string `json:"kind"`
	} `json:"origin"`
	Message struct {
		ID      string          `json:"id"`
		Content json.RawMessage `json:"content"`
	} `json:"message"`
}

// machineryPrefixes mark `type:"user"` records that the human did not type:
// slash-command expansion, local command output, hook and system injections,
// and interrupt markers. Verified across all 4,221 transcripts on the dev
// machine: 923 records pass the full rule and none contains any of these.
var machineryPrefixes = []string{
	"<command-name>", "<command-message>", "<command-args>",
	"<local-command-stdout>", "<local-command-caveat>",
	"<task-notification>", "<system-reminder>", "<ide_opened_file>",
	"[Request interrupted", "Caveat: The messages below",
	"This session is being continued from a previous conversation",
}

// isHuman reports whether r is a message the user actually typed.
//
// A `last-prompt` record type exists and looks like a shortcut for this. It is
// not: it is a conversation-LEAF pointer, and resolving it agreed with this
// rule only 1 time out of 11 live transcripts — the rest resolved to system,
// assistant, or attachment records. Do not replace this with last-prompt.
func isHuman(r *rec, text string) bool {
	if r.Type != "user" || r.IsMeta || len(r.ToolUseResult) > 0 {
		return false
	}
	human := r.PromptSource == "typed" // older format
	if r.Origin != nil {
		human = r.Origin.Kind == "human" // newer format wins
	}
	if !human {
		return false
	}
	t := strings.TrimSpace(text)
	if t == "" {
		return false
	}
	for _, p := range machineryPrefixes {
		if strings.HasPrefix(t, p) {
			return false
		}
	}
	return true
}

// contentText handles both content shapes and keeps only text blocks.
//
// message.content is a plain string in older records and an array of blocks in
// newer ones. Selecting type=="text" also drops thinking, tool_use, image, and
// the "fallback" block (a model-switch marker carrying no text).
func contentText(raw json.RawMessage) string {
	if len(raw) == 0 {
		return ""
	}
	if raw[0] == '"' {
		var s string
		if json.Unmarshal(raw, &s) == nil {
			return s
		}
		return ""
	}
	// NOTE on the type=="text" filter below: it is currently redundant for thinking
	// blocks specifically, because this struct has no field matching their "thinking"
	// key, so such a block decodes with Text == "" and contributes nothing either way.
	// Proven by running both variants. Keep the filter — it is the only thing that
	// would still exclude a block type that later gains a "text" field, and it states
	// intent. Do not delete it as dead code.
	var blocks []struct {
		Type string `json:"type"`
		Text string `json:"text"`
	}
	if json.Unmarshal(raw, &blocks) != nil {
		return ""
	}
	var sb bytes.Buffer
	for _, b := range blocks {
		if b.Type == "text" {
			if sb.Len() > 0 {
				sb.WriteByte('\n')
			}
			sb.WriteString(b.Text)
		}
	}
	return sb.String()
}

// hasToolResult reports whether the content array carries a tool_result block.
func hasToolResult(raw json.RawMessage) bool {
	if len(raw) == 0 || raw[0] != '[' {
		return false
	}
	var blocks []struct {
		Type string `json:"type"`
	}
	if json.Unmarshal(raw, &blocks) != nil {
		return false
	}
	for _, b := range blocks {
		if b.Type == "tool_result" {
			return true
		}
	}
	return false
}

// Read extracts the last exchange and title from a transcript's tail.
//
// Walking backwards means the common case stops after a handful of records:
// measured lookback for the assistant reply is p50 6, max 54.
func Read(path string) (Summary, error) {
	lines, err := tailLines(path, MaxRecords, MaxBytes)
	if err != nil {
		return Summary{}, err
	}
	var s Summary
	for i := len(lines) - 1; i >= 0; i-- {
		var r rec
		if json.Unmarshal([]byte(lines[i]), &r) != nil {
			continue // truncated or corrupt line; the rest still count
		}
		switch r.Type {
		case "ai-title":
			if s.AITitle == "" && r.AITitle != "" {
				s.AITitle = r.AITitle
			}
		case "assistant":
			if s.LastAssistant == "" {
				s.LastAssistant = contentText(r.Message.Content)
			}
		case "user":
			if s.LastHuman != "" || hasToolResult(r.Message.Content) {
				continue
			}
			if t := contentText(r.Message.Content); isHuman(&r, t) {
				s.LastHuman = strings.TrimSpace(t)
			}
		}
		if s.AITitle != "" && s.LastAssistant != "" && s.LastHuman != "" {
			break
		}
	}
	return s, nil
}
