package transcript

import (
	"math"
	"os"
	"path/filepath"
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
