package transcript

import (
	"encoding/json"
	"strings"
)

// Usage is a deduplicated token total for one transcript.
type Usage struct {
	Input     int64
	CacheRead int64
	Cache1h   int64
	Cache5m   int64
	Output    int64
}

// Total is the sum of every token bucket.
func (u Usage) Total() int64 {
	return u.Input + u.CacheRead + u.Cache1h + u.Cache5m + u.Output
}

type rates struct {
	input, output, cacheRead, cache1h, cache5m float64
}

// priceFor returns per-million-token rates for a model.
//
// Do NOT port the table from claude/statusline.sh. That table is retired
// Claude 3 Opus pricing (15/1.50/18.75/75) and its `case` arms match
// *"Sonnet"*|*"4.5"* BEFORE *"Opus"*, so "Opus 4.5" prices as Sonnet. Combined
// with the missing dedup and TTL split, statusline.sh overstates by 5.7x-6.7x.
//
// Order matters here too: check fable before the generic fallback, since
// claude-fable-5 contains none of opus/sonnet/haiku.
func priceFor(model string) rates {
	m := strings.ToLower(model)
	var in, out float64
	switch {
	case strings.Contains(m, "opus"):
		in, out = 5, 25
	case strings.Contains(m, "fable"):
		in, out = 10, 50
	case strings.Contains(m, "haiku"):
		in, out = 1, 5
	default: // sonnet and anything unrecognized
		in, out = 3, 15
	}
	return rates{
		input:     in,
		output:    out,
		cacheRead: in * 0.10,
		cache1h:   in * 2.0,
		cache5m:   in * 1.25,
	}
}

type usageRec struct {
	Type    string `json:"type"`
	Message struct {
		ID    string `json:"id"`
		Model string `json:"model"`
		Usage *struct {
			Input          int64 `json:"input_tokens"`
			Output         int64 `json:"output_tokens"`
			CacheRead      int64 `json:"cache_read_input_tokens"`
			CacheCreation  int64 `json:"cache_creation_input_tokens"`
			CacheBreakdown *struct {
				E1h int64 `json:"ephemeral_1h_input_tokens"`
				E5m int64 `json:"ephemeral_5m_input_tokens"`
			} `json:"cache_creation"`
		} `json:"usage"`
	} `json:"message"`
}

// Cost sums usage across a whole transcript and prices it.
//
// This figure is MAIN THREAD ONLY. Subagent spend lives in
// <slug>/<sessionId>/subagents/agent-*.jsonl and accounts for 4.6%-11.9% of
// true spend per session; it is deliberately not summed here, and the UI
// labels the number accordingly.
func Cost(path string) (Usage, float64, error) {
	lines, err := readAll(path)
	if err != nil {
		return Usage{}, 0, err
	}
	var u Usage
	var total float64
	seen := make(map[string]bool)

	for _, line := range lines {
		var r usageRec
		if json.Unmarshal([]byte(line), &r) != nil {
			continue
		}
		if r.Type != "assistant" || r.Message.Usage == nil {
			continue
		}
		// The same message.id repeats across streamed records with identical
		// usage. Verified corpus-wide: zero ids carry conflicting usage, so
		// first-wins is safe.
		if id := r.Message.ID; id != "" {
			if seen[id] {
				continue
			}
			seen[id] = true
		}
		usage := r.Message.Usage
		var c1h, c5m int64
		if b := usage.CacheBreakdown; b != nil {
			c1h, c5m = b.E1h, b.E5m
		} else {
			c1h = usage.CacheCreation // overwhelmingly 1h in practice
		}
		u.Input += usage.Input
		u.Output += usage.Output
		u.CacheRead += usage.CacheRead
		u.Cache1h += c1h
		u.Cache5m += c5m

		p := priceFor(r.Message.Model)
		total += (float64(usage.Input)*p.input +
			float64(usage.Output)*p.output +
			float64(usage.CacheRead)*p.cacheRead +
			float64(c1h)*p.cache1h +
			float64(c5m)*p.cache5m) / 1e6
	}
	return u, total, nil
}
