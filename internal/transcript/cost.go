package transcript

import (
	"bufio"
	"bytes"
	"encoding/json"
	"os"
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

// Acc is a resumable cost accumulation over ONE append-only transcript.
//
// The zero value scans from the start; handing the SAME Acc back after the file
// has grown folds in only the newly appended records. That is the whole point:
// a full scan of the real 88MB transcript is ~500ms and a ~160MB heap peak, and
// the UI re-costs the selected session on every 2-second poll. Resuming over the
// handful of records appended since the last poll costs microseconds and a few
// KB, so the number can keep climbing on screen without a rescan behind it.
//
// APPEND-ONLY is the load-bearing assumption, and only a SHRINK is detected. A
// transcript rewritten in place at the same or a larger size keeps a stale
// total until it shrinks or ccs restarts. Claude Code only appends, and both a
// restart and the manual refresh recover, so that is accepted rather than
// guarded — this is a displayed figure, not an input to any decision.
//
// An Acc is NOT safe for concurrent use: seen is a plain map, and copying an Acc
// copies that map's header rather than the map. Callers that share one across
// goroutines must serialise (see session.CostFor's per-path lock).
type Acc struct {
	Usage Usage
	Cost  float64
	// Offset is just past the last COMPLETE record folded in — the byte the
	// next Resume starts reading at.
	Offset int64
	// seen dedups message.id and MUST survive across resumes. The streamed
	// repeats of one id straddle a resume boundary whenever a poll lands
	// mid-message, and a freshly built map would count the later half again.
	seen map[string]bool
}

// Resume folds every record appended since the previous Resume into a.
//
// Errors are only returned for failures that mean nothing could be read at all
// (open, stat, seek). A mid-file scanner error is swallowed, exactly as the
// full scan did: the caller degrades to a partial total rather than losing the
// figure entirely. Offset is left parked in FRONT of the record that failed, so
// the next Resume retries it instead of baking the skip in permanently.
func (a *Acc) Resume(path string) error {
	f, err := os.Open(path)
	if err != nil {
		return err
	}
	defer f.Close()
	st, err := f.Stat()
	if err != nil {
		return err
	}
	// Smaller than where we stopped means truncated or replaced, so nothing
	// accumulated so far can be trusted. Start over from byte 0.
	if st.Size() < a.Offset {
		*a = Acc{}
	}
	if a.seen == nil {
		a.seen = make(map[string]bool)
	}
	if _, err := f.Seek(a.Offset, 0); err != nil {
		return err
	}

	sc := bufio.NewScanner(f)
	// The default 64KB buffer fails with "token too long" on real transcripts;
	// the largest single record observed is 1.28MB.
	sc.Buffer(make([]byte, 0, 64*1024), MaxBytes)
	sc.Split(scanLinesExact)

	// Hold one token back so its TERMINATOR can be checked before it counts. A
	// session appends while we read, so the last line of the file is routinely
	// half a record; folding it would bank a fraction of a message's usage and
	// then skip the rest of it forever, since Offset would have moved past. A
	// held token is folded on a later Resume, once the newline behind it lands.
	var held []byte
	haveHeld := false
	for sc.Scan() {
		tok := sc.Bytes()
		if haveHeld {
			// A newline was seen after held, so it is a complete record.
			a.fold(held)
			a.Offset += int64(len(held)) + 1
		}
		// sc.Bytes() is only valid until the next Scan, so keep a copy.
		held = append(held[:0], tok...)
		haveHeld = true
	}
	// The final token counts only if its newline is inside the file we measured.
	if haveHeld && a.Offset+int64(len(held))+1 <= st.Size() {
		a.fold(held)
		a.Offset += int64(len(held)) + 1
	}
	return nil
}

// scanLinesExact splits on '\n' WITHOUT bufio.ScanLines' trailing-'\r' strip, so
// that len(token)+1 is always the exact number of bytes consumed. Offset
// arithmetic depends on that: ScanLines would under-count a CRLF line by one and
// silently misalign every subsequent resume.
func scanLinesExact(data []byte, atEOF bool) (int, []byte, error) {
	if atEOF && len(data) == 0 {
		return 0, nil, nil
	}
	if i := bytes.IndexByte(data, '\n'); i >= 0 {
		return i + 1, data[:i], nil
	}
	if atEOF {
		return len(data), data, nil
	}
	return 0, nil, nil // request more data
}

// fold accumulates one complete record. Blank and unparseable lines are simply
// skipped, which is also how a partially written record that somehow reached
// here is absorbed.
func (a *Acc) fold(line []byte) {
	var r usageRec
	if json.Unmarshal(line, &r) != nil {
		return
	}
	if r.Type != "assistant" || r.Message.Usage == nil {
		return
	}
	// The same message.id repeats across streamed records with identical
	// usage. Verified corpus-wide: zero ids carry conflicting usage, so
	// first-wins is safe.
	if id := r.Message.ID; id != "" {
		if a.seen[id] {
			return
		}
		a.seen[id] = true
	}
	usage := r.Message.Usage
	var c1h, c5m int64
	if b := usage.CacheBreakdown; b != nil {
		c1h, c5m = b.E1h, b.E5m
	} else {
		c1h = usage.CacheCreation // overwhelmingly 1h in practice
	}
	a.Usage.Input += usage.Input
	a.Usage.Output += usage.Output
	a.Usage.CacheRead += usage.CacheRead
	a.Usage.Cache1h += c1h
	a.Usage.Cache5m += c5m

	p := priceFor(r.Message.Model)
	a.Cost += (float64(usage.Input)*p.input +
		float64(usage.Output)*p.output +
		float64(usage.CacheRead)*p.cacheRead +
		float64(c1h)*p.cache1h +
		float64(c5m)*p.cache5m) / 1e6
}

// Cost sums usage across a whole transcript and prices it, from byte 0.
//
// This is the one-shot form of Acc: anything on a repeat schedule should hold an
// Acc and call Resume instead, or it pays for a full rescan every time.
//
// This figure is MAIN THREAD ONLY. Subagent spend lives in
// <slug>/<sessionId>/subagents/agent-*.jsonl and accounts for 4.6%-11.9% of
// true spend per session; it is deliberately not summed here, and the UI
// labels the number accordingly.
func Cost(path string) (Usage, float64, error) {
	var a Acc
	if err := a.Resume(path); err != nil {
		return Usage{}, 0, err
	}
	return a.Usage, a.Cost, nil
}
