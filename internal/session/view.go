package session

import (
	"os"
	"path/filepath"
	"sort"
	"sync"
	"time"

	"github.com/JonathanAriass/ccs/internal/transcript"
)

// View is a live session plus everything the UI shows for it.
type View struct {
	Session
	TTY           string
	Title         string // last ai-title from the transcript
	LastHuman     string
	LastAssistant string
	Tokens        int64
	Cost          float64
	HasPreview    bool
}

// DisplayName resolves the label for the name column.
//
// The registry `name` is null on 4 of 14 live sessions, but their transcripts
// carry an ai-title that is far more useful than a directory basename.
func (v View) DisplayName() string {
	if v.Name != "" {
		return v.Name
	}
	if v.Title != "" {
		return v.Title
	}
	return filepath.Base(v.CWD)
}

// statusRank orders the list so whatever needs the user is always on top.
func statusRank(status string) int {
	switch status {
	case "waiting":
		return 0
	case "busy":
		return 1
	case "shell":
		return 2
	case "idle":
		return 3
	default:
		return 4
	}
}

// sortViews sorts by urgency, then by most recent activity within idle.
func sortViews(v []View) {
	sort.SliceStable(v, func(i, j int) bool {
		ri, rj := statusRank(v[i].Status), statusRank(v[j].Status)
		if ri != rj {
			return ri < rj
		}
		return v[i].StatusUpdatedAt > v[j].StatusUpdatedAt
	})
}

// Collect gathers every live session with its transcript summary, sorted.
//
// One kern.proc.all snapshot serves every session's ancestor walk (see
// OutermostTTY). It does NOT also serve the liveness check below: IsLive makes
// its own per-pid syscall per session. See the COVERAGE NOTE on Snapshot in
// tty.go for why that duplication is left as-is.
func Collect(registryDir, home string) ([]View, error) {
	all, err := Load(registryDir)
	if err != nil {
		return nil, err
	}
	procs, err := Snapshot()
	if err != nil {
		return nil, err
	}

	var out []View
	for _, s := range all {
		if !IsLive(s) {
			continue
		}
		v := View{Session: s, TTY: OutermostTTY(s.PID, procs)}

		// "No preview" is the common case, not an edge case: it applies to 4 of
		// 14 live sessions via two distinct routes — the file is absent, or it
		// parses cleanly but holds only ai-title/agent-name records.
		//
		// COVERAGE NOTE: `err == nil` is currently unpinnable by any fixture.
		// transcript.Read only ever returns (Summary{}, err) or (populated
		// Summary, nil) — never a non-zero Summary alongside an error — and v's
		// preview fields are already "" / false by construction. So deleting this
		// guard entirely is behaviorally identical for every input Read can
		// produce today; verified by running that variant against the full suite.
		// Kept anyway as an explicit contract (never let a future Read() that DID
		// return partial data on error silently leak into a View), not because
		// any current test can catch its removal.
		p := transcript.Path(home, s.CWD, s.SessionID)
		if sum, err := transcript.Read(p); err == nil {
			v.Title = sum.AITitle
			v.LastHuman = sum.LastHuman
			v.LastAssistant = sum.LastAssistant
			v.HasPreview = sum.LastHuman != "" || sum.LastAssistant != ""
		}
		// DELIBERATELY NOT computing cost here. Cost() is a FULL scan — measured at
		// 396ms and 203MB allocated on the real 88MB transcript. Collect runs on a
		// 2-second poll across ~15 sessions, so calling it here would be ~6 seconds
		// of work and gigabytes of allocation every 2 seconds. The TUI would be
		// unusable and it would burn the battery for nothing.
		//
		// It is also unnecessary: the list columns are status, name, directory, age
		// and last message. Cost appears ONLY in the preview pane, for ONE session at
		// a time. See CostFor below — it is called lazily for the selected row.
		out = append(out, v)
	}
	sortViews(out)
	return out, nil
}

// costCache memoises Cost by path, invalidated on size or mtime change. A session
// appends to its transcript constantly, so caching by path alone would go stale;
// caching on (size, mtime) recomputes exactly when the file has actually changed.
type costEntry struct {
	size   int64
	mtime  time.Time
	tokens int64
	cost   float64
}

var (
	costCache = map[string]costEntry{}
	// costMu guards costCache. bubbletea runs tea.Cmds in their own goroutines,
	// and Task 9 wires CostFor into one: the 2-second poll and a per-selection
	// cost lookup can both be in flight at once, each potentially populating a
	// NEW cache entry for a different session it has never seen before (a cache
	// HIT needs no write, but two concurrent MISSES for different paths both
	// write). Concurrent writes to a Go map don't corrupt quietly — the runtime
	// detects it and panics with "concurrent map writes", taking the whole TUI
	// down. See TestCostForConcurrentAccess, which calls CostFor from many
	// goroutines at once and only stays green because of this lock.
	costMu sync.Mutex
)

// CostFor returns the token total and cost for one session, computed lazily.
//
// This is deliberately NOT part of Collect. Cost() is a full scan (396ms / 203MB on
// the real 88MB transcript), and Collect runs every 2 seconds across every live
// session — doing it there would cost seconds per poll to render a number that only
// the preview pane shows, for one session at a time.
//
// Call this for the SELECTED row only.
func CostFor(home string, v View) (int64, float64) {
	p := transcript.Path(home, v.CWD, v.SessionID)
	st, err := os.Stat(p)
	if err != nil {
		return 0, 0
	}
	costMu.Lock()
	// COVERAGE NOTE: the leading `ok &&` is currently unpinnable by any realistic
	// fixture. A missing map entry yields the zero costEntry{} (size 0, zero
	// time.Time), so `ok` only changes the outcome when a real file's Stat also
	// reports size 0 AND ModTime() equal to Go's year-1 zero time — the latter
	// never happens for anything the OS actually wrote. Kept for the same reason
	// as the `err == nil` guard above: it states the real precondition ("was
	// this ever cached") rather than relying on that coincidence.
	e, ok := costCache[p]
	costMu.Unlock()
	if ok && e.size == st.Size() && e.mtime.Equal(st.ModTime()) {
		return e.tokens, e.cost
	}
	u, c, err := transcript.Cost(p)
	if err != nil {
		return 0, 0
	}
	costMu.Lock()
	costCache[p] = costEntry{size: st.Size(), mtime: st.ModTime(), tokens: u.Total(), cost: c}
	costMu.Unlock()
	return u.Total(), c
}
