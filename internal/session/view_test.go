package session

import (
	"fmt"
	"math"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/JonathanAriass/ccs/internal/transcript"
)

func TestSortViews(t *testing.T) {
	v := []View{
		{Session: Session{Name: "i-old", Status: "idle", StatusUpdatedAt: 100}},
		{Session: Session{Name: "busy", Status: "busy", StatusUpdatedAt: 1}},
		{Session: Session{Name: "i-new", Status: "idle", StatusUpdatedAt: 900}},
		{Session: Session{Name: "wait", Status: "waiting", StatusUpdatedAt: 1}},
		{Session: Session{Name: "shell", Status: "shell", StatusUpdatedAt: 1}},
		{Session: Session{Name: "weird", Status: "", StatusUpdatedAt: 1}},
	}
	sortViews(v)

	want := []string{"wait", "busy", "shell", "i-new", "i-old", "weird"}
	for i, w := range want {
		if v[i].Name != w {
			t.Fatalf("position %d: got %q want %q (full: %v)", i, v[i].Name, w, names(v))
		}
	}
}

// TestStatusRank pins statusRank directly, one case at a time. TestSortViews
// alone is not enough: mutation testing found that deleting the "idle" case
// left TestSortViews green, because in that fixture idle's two sessions
// (StatusUpdatedAt 900 and 100) both still sorted ahead of "weird"
// (StatusUpdatedAt 1) purely via the numeric tie-break, coincidentally
// reproducing the same order rank 3 would have produced. Exact-value assertions
// here catch that regardless of which timestamps sortViews's fixture happens to
// use.
func TestStatusRank(t *testing.T) {
	cases := []struct {
		status string
		want   int
	}{
		{"waiting", 0},
		{"busy", 1},
		{"shell", 2},
		{"idle", 3},
		{"", 4},        // empty status (older/malformed registry entries)
		{"unknown", 4}, // any future/unrecognized status
	}
	for _, c := range cases {
		t.Run(c.status, func(t *testing.T) {
			if got := statusRank(c.status); got != c.want {
				t.Errorf("statusRank(%q) = %d want %d", c.status, got, c.want)
			}
		})
	}
}

func names(v []View) []string {
	out := make([]string, len(v))
	for i := range v {
		out[i] = v[i].Name
	}
	return out
}

func TestDisplayName(t *testing.T) {
	cases := []struct {
		name string
		v    View
		want string
	}{
		{
			name: "registry name wins",
			v:    View{Session: Session{Name: "okt-api-87", CWD: "/x/y"}, Title: "An AI Title"},
			want: "okt-api-87",
		},
		{
			// 4 of 14 live sessions have no registry name but do carry an
			// ai-title like "Build cross-platform database connector application"
			name: "ai-title beats the cwd basename",
			v:    View{Session: Session{CWD: "/Users/x/Desktop/db"}, Title: "Build a connector"},
			want: "Build a connector",
		},
		{
			name: "cwd basename is the last resort",
			v:    View{Session: Session{CWD: "/Users/x/Desktop/db"}},
			want: "db",
		},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			if got := c.v.DisplayName(); got != c.want {
				t.Errorf("got %q want %q", got, c.want)
			}
		})
	}
}

// The two tests above (from the brief) exercise sortViews and DisplayName, which
// are pure functions. Collect and CostFor have their own guards — the live-filter,
// the transcript-read degrade, the HasPreview OR, and the cost cache's three-part
// AND — that neither of those tests reaches. The tests below pin those guards the
// same way the rest of this package does: real fixtures, not mocks.

// registryWithSession writes a single registry entry pointing at pid, so IsLive's
// signal probe and start-time check see a real process.
func registryWithSession(t *testing.T, pid int, procStart, cwd, sessionID string) string {
	t.Helper()
	dir := t.TempDir()
	body := fmt.Sprintf(`{"pid":%d,"sessionId":%q,"cwd":%q,"procStart":%q,"status":"busy"}`,
		pid, sessionID, cwd, procStart)
	if err := os.WriteFile(filepath.Join(dir, "s.json"), []byte(body), 0o644); err != nil {
		t.Fatal(err)
	}
	return dir
}

// writeTranscript places lines at the exact path transcript.Path derives for
// (home, cwd, sessionID), creating the project-slug directory Collect expects.
func writeTranscript(t *testing.T, home, cwd, sessionID string, lines ...string) {
	t.Helper()
	p := transcript.Path(home, cwd, sessionID)
	if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(p, []byte(strings.Join(lines, "\n")+"\n"), 0o644); err != nil {
		t.Fatal(err)
	}
}

func TestCollectFiltersLiveSessions(t *testing.T) {
	self := os.Getpid()
	actual, err := procStartString(self)
	if err != nil {
		t.Fatalf("procStartString(self): %v", err)
	}

	registryDir := t.TempDir()
	home := t.TempDir()
	write := func(name, body string) {
		if err := os.WriteFile(filepath.Join(registryDir, name), []byte(body), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	// Live: self pid with the real procStart, matching TestIsLive's own fixture.
	write("live.json", fmt.Sprintf(`{"pid":%d,"sessionId":"live-1","cwd":"/tmp/live","procStart":%q,"status":"busy"}`,
		self, actual))
	// Dead: a pid that is not running, same fixture liveness_test.go uses for
	// "dead PID is rejected". Only IsLive's process-probe guard can reject this
	// entry — the record itself is otherwise well-formed.
	write("dead.json", fmt.Sprintf(`{"pid":999999,"sessionId":"dead-1","cwd":"/tmp/dead","procStart":%q,"status":"idle"}`,
		actual))

	views, err := Collect(registryDir, home)
	if err != nil {
		t.Fatalf("Collect: %v", err)
	}
	if len(views) != 1 {
		t.Fatalf("want 1 live view (dead-1 must be filtered), got %d: %v", len(views), views)
	}
	if views[0].SessionID != "live-1" {
		t.Errorf("wrong session survived filtering: %+v", views[0])
	}
}

func TestCollectPreview(t *testing.T) {
	self := os.Getpid()
	actual, err := procStartString(self)
	if err != nil {
		t.Fatalf("procStartString(self): %v", err)
	}
	newRegistry := func(t *testing.T, cwd, sessionID string) string {
		return registryWithSession(t, self, actual, cwd, sessionID)
	}
	collectOne := func(t *testing.T, registryDir, home string) View {
		t.Helper()
		views, err := Collect(registryDir, home)
		if err != nil {
			t.Fatalf("Collect: %v", err)
		}
		if len(views) != 1 {
			t.Fatalf("want 1 view, got %d", len(views))
		}
		return views[0]
	}

	t.Run("missing transcript degrades to no preview, not an error", func(t *testing.T) {
		// The FIRST of the two "no preview" routes: the file is simply absent.
		// transcript.Read returns an error here, and Collect must degrade rather
		// than propagate it — pins the `err == nil` guard's reject direction.
		cwd, sessionID := "/tmp/no-transcript", "sess-missing"
		registryDir := newRegistry(t, cwd, sessionID)
		home := t.TempDir() // nothing written under here

		v := collectOne(t, registryDir, home)
		if v.HasPreview {
			t.Error("HasPreview must be false when the transcript file does not exist")
		}
		if v.Title != "" || v.LastHuman != "" || v.LastAssistant != "" {
			t.Errorf("want all preview fields empty, got %+v", v)
		}
	})

	t.Run("existing transcript populates title and preview", func(t *testing.T) {
		// Pins the `err == nil` guard's accept direction: a real transcript must
		// actually flow through into the View's fields.
		cwd, sessionID := "/tmp/has-transcript", "sess-present"
		registryDir := newRegistry(t, cwd, sessionID)
		home := t.TempDir()
		writeTranscript(t, home, cwd, sessionID,
			`{"type":"ai-title","aiTitle":"a real title"}`,
			`{"type":"user","origin":{"kind":"human"},"message":{"content":"hi there"}}`,
			`{"type":"assistant","message":{"id":"m1","content":[{"type":"text","text":"hello back"}]}}`,
		)

		v := collectOne(t, registryDir, home)
		if !v.HasPreview {
			t.Error("HasPreview must be true when the transcript has a real exchange")
		}
		if v.Title != "a real title" || v.LastHuman != "hi there" || v.LastAssistant != "hello back" {
			t.Errorf("preview fields not populated: %+v", v)
		}
	})

	t.Run("title-only transcript (no conversation) does not count as preview", func(t *testing.T) {
		// The SECOND "no preview" route: the file parses cleanly but holds only
		// ai-title/agent-name records (a real shape on the dev machine). Title is
		// non-empty here, so this also proves HasPreview does not just mirror
		// "Title != """ — it is driven only by LastHuman/LastAssistant.
		cwd, sessionID := "/tmp/title-only", "sess-title-only"
		registryDir := newRegistry(t, cwd, sessionID)
		home := t.TempDir()
		writeTranscript(t, home, cwd, sessionID,
			`{"type":"ai-title","aiTitle":"titled but empty"}`,
			`{"type":"agent-name","name":"x"}`,
		)

		v := collectOne(t, registryDir, home)
		if v.HasPreview {
			t.Error("a title-only transcript must not count as a preview")
		}
		if v.Title != "titled but empty" {
			t.Errorf("title should still be recovered, got %q", v.Title)
		}
	})

	t.Run("human-only exchange still counts as a preview", func(t *testing.T) {
		// Pins the LastHuman half of `LastHuman != "" || LastAssistant != ""`.
		// LastAssistant is empty here, so a `||`-to-`&&` mutation reddens this.
		cwd, sessionID := "/tmp/human-only", "sess-human-only"
		registryDir := newRegistry(t, cwd, sessionID)
		home := t.TempDir()
		writeTranscript(t, home, cwd, sessionID,
			`{"type":"user","origin":{"kind":"human"},"message":{"content":"still waiting on a reply"}}`,
		)

		v := collectOne(t, registryDir, home)
		if !v.HasPreview {
			t.Error("a human message with no reply yet must still count as a preview")
		}
		if v.LastAssistant != "" {
			t.Errorf("no assistant reply should exist yet, got %q", v.LastAssistant)
		}
	})

	t.Run("assistant-only exchange still counts as a preview", func(t *testing.T) {
		// Pins the LastAssistant half of the OR. LastHuman is empty here, so a
		// `||`-to-`&&` mutation reddens this the other way.
		cwd, sessionID := "/tmp/assistant-only", "sess-assistant-only"
		registryDir := newRegistry(t, cwd, sessionID)
		home := t.TempDir()
		writeTranscript(t, home, cwd, sessionID,
			`{"type":"assistant","message":{"id":"m1","content":[{"type":"text","text":"a reply with no visible prompt"}]}}`,
		)

		v := collectOne(t, registryDir, home)
		if !v.HasPreview {
			t.Error("an assistant reply with no human message in the tail must still count as a preview")
		}
		if v.LastHuman != "" {
			t.Errorf("no human message should exist in this fixture, got %q", v.LastHuman)
		}
	})
}

func TestCostForMissingFile(t *testing.T) {
	home := t.TempDir()
	v := View{Session: Session{CWD: "/tmp/nope", SessionID: "gone"}}
	tokens, cost := CostFor(home, v)
	if tokens != 0 || cost != 0 {
		t.Errorf("want 0,0 for a missing transcript, got %d,%v", tokens, cost)
	}
}

// TestCostForMissingFileWithStaleCache pins the os.Stat error guard's reject
// direction properly. A plain missing-file call (above) does NOT actually
// exercise this guard: with no cache entry for a fresh path, `ok` in
// `ok && e.size == st.Size() && ...` is false and short-circuits before
// `st.Size()` is ever evaluated, so transcript.Cost's own error guard alone
// produces the right answer even with the os.Stat guard deleted entirely.
//
// The guard earns its keep in a narrower, real scenario: a transcript gets
// costed and cached, then the file vanishes (e.g. project cleanup) before the
// next poll. Now `ok` IS true, so `st.Size()` on the nil FileInfo a failed Stat
// returns would be evaluated — a nil-pointer panic — if the guard were gone.
// This fixture seeds that stale entry to force exactly that code path.
func TestCostForMissingFileWithStaleCache(t *testing.T) {
	home := t.TempDir()
	cwd, sessionID := "/tmp/vanished", "sess-vanished"
	v := View{Session: Session{CWD: cwd, SessionID: sessionID}}
	p := transcript.Path(home, cwd, sessionID)
	costCache[p] = costEntry{size: 123, mtime: time.Now(), tokens: 999, cost: 9.99}
	t.Cleanup(func() { delete(costCache, p) })

	tokens, cost := CostFor(home, v)
	if tokens != 0 || cost != 0 {
		t.Errorf("want 0,0 for a missing file even with a stale cache entry, got %d,%v", tokens, cost)
	}
}

// TestCostForCache pins the three-part AND that gates a cache hit:
// `ok && e.size == st.Size() && e.mtime.Equal(st.ModTime())`. Each subtest pokes
// a distinguishable, wrong value directly into the cache under a key that matches
// on every field except the one under test, then checks whether CostFor trusted
// the poked value or recomputed the real one — the only way to tell a hit from a
// miss from outside the package.
func TestCostForCache(t *testing.T) {
	home := t.TempDir()
	cwd, sessionID := "/tmp/cost-cache", "sess-cost"
	v := View{Session: Session{CWD: cwd, SessionID: sessionID}}
	p := transcript.Path(home, cwd, sessionID)
	if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
		t.Fatal(err)
	}
	line := `{"type":"assistant","message":{"id":"m1","model":"claude-opus-5","usage":` +
		`{"input_tokens":1000,"output_tokens":100}}}`
	if err := os.WriteFile(p, []byte(line+"\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { delete(costCache, p) })

	wantTokens, wantCost := int64(1100), (1000*5.0+100*25.0)/1e6

	gotTokens, gotCost := CostFor(home, v)
	if gotTokens != wantTokens || math.Abs(gotCost-wantCost) > 1e-9 {
		t.Fatalf("first call: got %d,%v want %d,%v", gotTokens, gotCost, wantTokens, wantCost)
	}

	st, err := os.Stat(p)
	if err != nil {
		t.Fatal(err)
	}

	t.Run("matching size and mtime returns the cached value, not a recompute", func(t *testing.T) {
		costCache[p] = costEntry{size: st.Size(), mtime: st.ModTime(), tokens: 999, cost: 9.99}
		tokens, cost := CostFor(home, v)
		if tokens != 999 || cost != 9.99 {
			t.Errorf("cache hit not honored: got %d,%v want the poked 999,9.99", tokens, cost)
		}
	})

	t.Run("size mismatch bypasses the cache", func(t *testing.T) {
		costCache[p] = costEntry{size: st.Size() + 1, mtime: st.ModTime(), tokens: 999, cost: 9.99}
		tokens, cost := CostFor(home, v)
		if tokens != wantTokens || math.Abs(cost-wantCost) > 1e-9 {
			t.Errorf("size mismatch should force a recompute: got %d,%v want %d,%v", tokens, cost, wantTokens, wantCost)
		}
	})

	t.Run("mtime mismatch bypasses the cache", func(t *testing.T) {
		costCache[p] = costEntry{size: st.Size(), mtime: st.ModTime().Add(time.Second), tokens: 999, cost: 9.99}
		tokens, cost := CostFor(home, v)
		if tokens != wantTokens || math.Abs(cost-wantCost) > 1e-9 {
			t.Errorf("mtime mismatch should force a recompute: got %d,%v want %d,%v", tokens, cost, wantTokens, wantCost)
		}
	})
}

// TestCostForPropagatesTranscriptError pins the `err != nil` guard after
// transcript.Cost. os.Stat only needs search permission on the parent
// directories, not read permission on the file itself, so a mode-000 file passes
// CostFor's earlier os.Stat guard and reaches transcript.Cost, which fails to
// Open it — the only realistic way to make Cost() itself return an error, since
// readAll/Cost treat a merely-empty-of-usable-records file as success (Usage{},
// 0, nil), not an error.
//
// The plain "returns 0,0" assertion alone does not fully pin this guard: Cost()
// itself already returns a zero Usage and a 0 cost alongside any error, so
// deleting the guard's early return produces the SAME (0,0) result on this one
// call — verified by running that variant. What the guard actually prevents is
// worse: without it, the fall-through still executes
// `costCache[p] = costEntry{..., tokens: u.Total(), cost: c}`, caching the
// FAILED (0,0) result under the file's real (size, mtime). chmod does not
// change either, so the second half of this test — making the file readable
// again with its content untouched — is what actually distinguishes "guard
// present" from "guard deleted": present, the error path never touches the
// cache, so the retry recomputes the real cost; deleted, the retry serves the
// poisoned 0,0 from cache forever (until the file's size or mtime happens to
// change), permanently hiding a real session's cost after one transient error.
func TestCostForPropagatesTranscriptError(t *testing.T) {
	if os.Geteuid() == 0 {
		t.Skip("running as root: permission checks are bypassed, so this fixture cannot deny read access")
	}
	home := t.TempDir()
	cwd, sessionID := "/tmp/cost-unreadable", "sess-unreadable"
	v := View{Session: Session{CWD: cwd, SessionID: sessionID}}
	p := transcript.Path(home, cwd, sessionID)
	if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
		t.Fatal(err)
	}
	line := `{"type":"assistant","message":{"id":"m1","model":"claude-opus-5","usage":` +
		`{"input_tokens":1000,"output_tokens":100}}}`
	if err := os.WriteFile(p, []byte(line+"\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { delete(costCache, p) })

	if err := os.Chmod(p, 0o000); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { os.Chmod(p, 0o644) }) // let TempDir cleanup remove it
	if _, err := os.Stat(p); err != nil {
		t.Skipf("os.Stat unexpectedly failed on a mode-000 file, cannot isolate the guard: %v", err)
	}

	tokens, cost := CostFor(home, v)
	if tokens != 0 || cost != 0 {
		t.Fatalf("want 0,0 when transcript.Cost errors, got %d,%v", tokens, cost)
	}

	// Restore readability WITHOUT touching the file's content, size, or mtime —
	// chmod alone changes none of those. A retry must now recompute the real
	// cost, not serve a cached failure.
	if err := os.Chmod(p, 0o644); err != nil {
		t.Fatal(err)
	}
	wantTokens, wantCost := int64(1100), (1000*5.0+100*25.0)/1e6
	gotTokens, gotCost := CostFor(home, v)
	if gotTokens != wantTokens || math.Abs(gotCost-wantCost) > 1e-9 {
		t.Errorf("a failed Cost() call must not poison the cache: retry got %d,%v want %d,%v",
			gotTokens, gotCost, wantTokens, wantCost)
	}
}
