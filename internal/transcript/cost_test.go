package transcript

import (
	"fmt"
	"math"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// Divergence from statusline.sh, measured on a real transcript
// (~/.claude/projects/-Users-ariass-Desktop/af78c52c-b5ee-4132-ba91-bacf02d72a27.jsonl,
// frozen to a snapshot so both implementations saw identical input):
//
//	ccs (this package): 99,006,716 tokens   $68.32
//	statusline.sh:      194,856,317 tokens  $397.30
//
// That's 1.97x the tokens and 5.82x the cost — statusline.sh double-counts
// duplicate message.ids (roughly half of real assistant records are repeats)
// and prices with retired Claude 3 Opus rates whose case arms match
// *"Sonnet"* before *"Opus"*. If a future change makes these two agree, that
// is a regression, not a fix — investigate before "fixing" it back into
// alignment. (The transcript is a live, actively-growing session transcript,
// so its absolute totals climb on repeat measurement; the ~5.7x-6.7x ratio is
// the invariant to check, not the dollar figure.)

func TestPriceFor(t *testing.T) {
	// Transcripts carry lowercase API ids, NOT display names. statusline.sh
	// matches capitalized substrings and therefore prices EVERY id as Sonnet.
	cases := []struct {
		model   string
		in, out float64
	}{
		{"claude-opus-5", 5, 25},
		{"claude-opus-4-8", 5, 25},
		{"claude-fable-5", 10, 50}, // contains none of opus/sonnet/haiku
		{"claude-sonnet-5", 3, 15},
		{"claude-haiku-4-5-20251001", 1, 5},
	}
	for _, c := range cases {
		t.Run(c.model, func(t *testing.T) {
			r := priceFor(c.model)
			if r.input != c.in || r.output != c.out {
				t.Errorf("got in=%v out=%v want in=%v out=%v", r.input, r.output, c.in, c.out)
			}
			// Derived rates.
			if r.cacheRead != c.in*0.10 {
				t.Errorf("cacheRead = %v want %v", r.cacheRead, c.in*0.10)
			}
			if r.cache1h != c.in*2.0 {
				t.Errorf("cache1h = %v want %v", r.cache1h, c.in*2.0)
			}
			if r.cache5m != c.in*1.25 {
				t.Errorf("cache5m = %v want %v", r.cache5m, c.in*1.25)
			}
		})
	}
}

func TestCostDedupesByMessageID(t *testing.T) {
	// The same message.id repeats across streamed records carrying identical
	// usage. Summing naively double-counts.
	p := filepath.Join(t.TempDir(), "c.jsonl")
	line := `{"type":"assistant","message":{"id":"m1","model":"claude-opus-5","usage":` +
		`{"input_tokens":1000,"output_tokens":100,"cache_read_input_tokens":0,` +
		`"cache_creation_input_tokens":0}}}`
	os.WriteFile(p, []byte(line+"\n"+line+"\n"+line+"\n"), 0o644)

	u, cost, err := Cost(p)
	if err != nil {
		t.Fatal(err)
	}
	if u.Input != 1000 || u.Output != 100 {
		t.Fatalf("double counted: %+v", u)
	}
	want := (1000*5 + 100*25) / 1e6
	if math.Abs(cost-want) > 1e-9 {
		t.Errorf("cost = %v want %v", cost, want)
	}
}

func TestCostPrefersTTLSubfields(t *testing.T) {
	// 99.9997% of cache-creation tokens in the real corpus are 1-hour TTL at
	// 2.0x, not 5-minute at 1.25x. And 7 real records carry a top-level
	// cache_creation_input_tokens of 0 while the sub-fields are non-zero, so
	// the sub-object is authoritative.
	p := filepath.Join(t.TempDir(), "c.jsonl")
	line := `{"type":"assistant","message":{"id":"m1","model":"claude-opus-5","usage":` +
		`{"input_tokens":0,"output_tokens":0,"cache_read_input_tokens":0,` +
		`"cache_creation_input_tokens":0,` +
		`"cache_creation":{"ephemeral_1h_input_tokens":1000,"ephemeral_5m_input_tokens":200}}}}`
	os.WriteFile(p, []byte(line+"\n"), 0o644)

	u, cost, err := Cost(p)
	if err != nil {
		t.Fatal(err)
	}
	if u.Cache1h != 1000 || u.Cache5m != 200 {
		t.Fatalf("sub-fields ignored: %+v", u)
	}
	want := (1000*5*2.0 + 200*5*1.25) / 1e6
	if math.Abs(cost-want) > 1e-9 {
		t.Errorf("cost = %v want %v", cost, want)
	}
}

func TestCostSkipsNonAssistantAndUsagelessRecords(t *testing.T) {
	// Pins the two halves of `if r.Type != "assistant" || r.Message.Usage == nil`.
	// Neither had a fixture: every other test feeds only assistant records that all
	// carry usage, so deleting either half changed nothing observable.
	//
	// The user record below carries a usage block with deliberately huge numbers —
	// transcripts really do contain non-assistant records, and counting them would
	// inflate the figure enormously. The assistant record below has NO usage at all,
	// which must be skipped rather than nil-panicking.
	p := filepath.Join(t.TempDir(), "c.jsonl")
	os.WriteFile(p, []byte(
		`{"type":"user","message":{"id":"u1","model":"claude-opus-5","usage":{"input_tokens":9000000,"output_tokens":9000000}}}`+"\n"+
			`{"type":"assistant","message":{"id":"m-nousage","model":"claude-opus-5"}}`+"\n"+
			`{"type":"assistant","message":{"id":"m1","model":"claude-opus-5","usage":{"input_tokens":1000,"output_tokens":100}}}`+"\n"), 0o644)

	u, cost, err := Cost(p)
	if err != nil {
		t.Fatal(err)
	}
	if u.Input != 1000 || u.Output != 100 {
		t.Fatalf("non-assistant or usage-less record was counted: %+v", u)
	}
	want := (1000*5 + 100*25) / 1e6
	if math.Abs(cost-want) > 1e-9 {
		t.Errorf("cost = %v want %v", cost, want)
	}
}

func TestCostFallsBackToTopLevelCacheField(t *testing.T) {
	p := filepath.Join(t.TempDir(), "c.jsonl")
	line := `{"type":"assistant","message":{"id":"m1","model":"claude-opus-5","usage":` +
		`{"cache_creation_input_tokens":500}}}`
	os.WriteFile(p, []byte(line+"\n"), 0o644)

	u, _, err := Cost(p)
	if err != nil {
		t.Fatal(err)
	}
	// Absent the sub-object, the top-level count is billed at the 1h rate,
	// which is what virtually all real traffic uses.
	if u.Cache1h != 500 {
		t.Fatalf("fallback failed: %+v", u)
	}
}

// usageLine builds one assistant record with a distinguishable usage block.
func usageLine(id, model string, in, out, cacheRead, cacheCreate int64) string {
	return fmt.Sprintf(`{"type":"assistant","message":{"id":%q,"model":%q,"usage":`+
		`{"input_tokens":%d,"output_tokens":%d,"cache_read_input_tokens":%d,`+
		`"cache_creation_input_tokens":%d}}}`, id, model, in, out, cacheRead, cacheCreate)
}

// TestAccResumeMatchesFullScan grows a transcript ONE BYTE AT A TIME, calling
// Resume after every single byte, and requires the result to be indistinguishable
// from one full Cost() of the finished file.
//
// Byte-at-a-time is the load-bearing part, not thoroughness for its own sake.
// Every intermediate state — a record split after its opening brace, in the
// middle of a number, one byte short of its newline — is a state a real 2-second
// poll can and does observe, since sessions append while ccs reads. A resume that
// banks a half-written record moves Offset past bytes it never counted, and the
// rest of that record is then skipped forever.
//
// The cost comparison is `==`, deliberately not an epsilon: records are folded in
// file order either way, so the float additions happen in an identical sequence
// and the totals must agree to the last bit. A tolerance would let a single
// dropped small record slide through.
func TestAccResumeMatchesFullScan(t *testing.T) {
	dir := t.TempDir()
	models := []string{"claude-opus-5", "claude-sonnet-5", "claude-haiku-4-5-20251001", "claude-fable-5"}
	var sb strings.Builder
	for i := 0; i < 12; i++ {
		sb.WriteString(usageLine(fmt.Sprintf("m%d", i), models[i%len(models)],
			int64(100+i), int64(10+i), int64(1000*i), int64(7*i)))
		sb.WriteByte('\n')
	}
	content := []byte(sb.String())

	full := filepath.Join(dir, "full.jsonl")
	if err := os.WriteFile(full, content, 0o644); err != nil {
		t.Fatal(err)
	}
	wantU, wantC, err := Cost(full)
	if err != nil {
		t.Fatal(err)
	}
	if wantU.Total() == 0 || wantC == 0 {
		t.Fatalf("fixture proves nothing: full scan totalled %d tokens / %v", wantU.Total(), wantC)
	}

	grow := filepath.Join(dir, "grow.jsonl")
	f, err := os.Create(grow)
	if err != nil {
		t.Fatal(err)
	}
	var a Acc
	for i := range content {
		if _, err := f.Write(content[i : i+1]); err != nil {
			t.Fatal(err)
		}
		if err := a.Resume(grow); err != nil {
			t.Fatalf("Resume after byte %d: %v", i, err)
		}
		if a.Offset > int64(i+1) {
			t.Fatalf("after byte %d the file is %d bytes but Offset is %d — a record was banked before its terminator arrived",
				i, i+1, a.Offset)
		}
	}
	if err := f.Close(); err != nil {
		t.Fatal(err)
	}

	if a.Usage != wantU {
		t.Errorf("resumed usage != full scan: got %+v want %+v", a.Usage, wantU)
	}
	if a.Cost != wantC {
		t.Errorf("resumed cost != full scan: got %v want %v", a.Cost, wantC)
	}
	if a.Offset != int64(len(content)) {
		t.Errorf("offset = %d, want %d (the whole file is complete, so all of it must be folded in)",
			a.Offset, len(content))
	}
}

// TestAccResumeDedupsAcrossResumeBoundary pins that Acc.seen survives a resume.
//
// The same message.id repeats across streamed records, and a poll routinely lands
// between two of those repeats. If seen were rebuilt per Resume, the second copy
// would look brand new and its usage would be added a second time — a silent
// doubling that grows with how often the user watches a busy session.
func TestAccResumeDedupsAcrossResumeBoundary(t *testing.T) {
	p := filepath.Join(t.TempDir(), "dup.jsonl")
	rec := usageLine("dup", "claude-opus-5", 1000, 100, 0, 0)
	if err := os.WriteFile(p, []byte(rec+"\n"), 0o644); err != nil {
		t.Fatal(err)
	}

	var a Acc
	if err := a.Resume(p); err != nil {
		t.Fatal(err)
	}
	if got := a.Usage.Total(); got != 1100 {
		t.Fatalf("first resume: tokens = %d, want 1100", got)
	}

	f, err := os.OpenFile(p, os.O_APPEND|os.O_WRONLY, 0o644)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := f.WriteString(rec + "\n"); err != nil {
		t.Fatal(err)
	}
	f.Close()

	if err := a.Resume(p); err != nil {
		t.Fatal(err)
	}
	if got := a.Usage.Total(); got != 1100 {
		t.Errorf("tokens = %d, want 1100 (the duplicate id was counted twice across the resume boundary)", got)
	}
}

// TestAccResumeRestartsOnTruncation pins the size-shrink guard.
//
// A file smaller than where the last scan stopped is not the file we were
// accumulating, so every total carried over from it is wrong. Without the reset,
// Seek past EOF succeeds, the scan reads nothing, and the stale totals are
// returned forever.
func TestAccResumeRestartsOnTruncation(t *testing.T) {
	p := filepath.Join(t.TempDir(), "trunc.jsonl")
	long := usageLine("a1", "claude-opus-5", 1500, 150, 0, 0) + "\n" +
		usageLine("a2", "claude-opus-5", 1500, 150, 0, 0) + "\n"
	if err := os.WriteFile(p, []byte(long), 0o644); err != nil {
		t.Fatal(err)
	}

	var a Acc
	if err := a.Resume(p); err != nil {
		t.Fatal(err)
	}
	if got := a.Usage.Total(); got != 3300 {
		t.Fatalf("first resume: tokens = %d, want 3300", got)
	}

	short := usageLine("b1", "claude-opus-5", 50, 5, 0, 0) + "\n"
	if len(short) >= len(long) {
		t.Fatalf("fixture bug: the replacement must be SHORTER (%d vs %d)", len(short), len(long))
	}
	if err := os.WriteFile(p, []byte(short), 0o644); err != nil {
		t.Fatal(err)
	}

	if err := a.Resume(p); err != nil {
		t.Fatal(err)
	}
	if got := a.Usage.Total(); got != 55 {
		t.Errorf("tokens = %d, want 55 (stale pre-truncation totals were kept)", got)
	}
	if a.Offset != int64(len(short)) {
		t.Errorf("offset = %d, want %d", a.Offset, len(short))
	}
}
