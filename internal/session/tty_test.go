package session

import "testing"

func TestTTYName(t *testing.T) {
	cases := []struct {
		name string
		tdev int32
		want string
	}{
		// major 16 is the pty slave; minor is the number. Validated against ps
		// for all 865 processes on the dev machine with zero disagreements.
		{"pty 17", int32(16<<24 | 17), "ttys017"},
		{"pty 0", int32(16 << 24), "ttys000"},
		{"pty 234", int32(16<<24 | 234), "ttys234"},
		// Minor numbers 0/17/234 all fit in a single byte, so they cannot
		// distinguish the correct 24-bit minor mask from a narrower one that
		// truncates. macOS pty minors are not capped at 255, so an 8-bit mask
		// would misreport any tab past ttys255 as a wrapped, wrong number
		// instead of failing loudly — pin the full width with a 3-byte value.
		{"pty 300 (minor exceeds one byte)", int32(16<<24 | 300), "ttys300"},
		{"NODEV", -1, "??"},
		{"non-pty major", int32(4<<24 | 2), "??"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			if got := ttyName(c.tdev); got != c.want {
				t.Errorf("ttyName(%d) = %q want %q", c.tdev, got, c.want)
			}
		})
	}
}

func TestOutermostTTY(t *testing.T) {
	pty := func(n int32) int32 { return int32(16<<24) | n }

	t.Run("keeps the outermost tty, not the first", func(t *testing.T) {
		// The Neovim :terminal shape. claude's own tty is nvim's embedded pty,
		// which iTerm2's session list will NEVER contain — a first-match walk
		// makes Enter silently fail on exactly these sessions.
		procs := map[int32]ProcInfo{
			100: {PPID: 90, Tdev: pty(1)}, // claude, inside nvim
			90:  {PPID: 80, Tdev: -1},     // nvim's forked job process
			80:  {PPID: 1, Tdev: pty(0)},  // the real iTerm2 tab
		}
		if got := OutermostTTY(100, procs); got != "ttys000" {
			t.Errorf("got %q want ttys000", got)
		}
	})

	t.Run("traverses ?? ancestors", func(t *testing.T) {
		procs := map[int32]ProcInfo{
			10: {PPID: 9, Tdev: -1},
			9:  {PPID: 1, Tdev: pty(5)},
		}
		if got := OutermostTTY(10, procs); got != "ttys005" {
			t.Errorf("got %q want ttys005", got)
		}
	})

	t.Run("no tty anywhere", func(t *testing.T) {
		procs := map[int32]ProcInfo{
			10: {PPID: 1, Tdev: -1},
		}
		if got := OutermostTTY(10, procs); got != "" {
			t.Errorf("got %q want empty", got)
		}
	})

	t.Run("cyclic ppid terminates", func(t *testing.T) {
		// A 2-cycle. Note `PPID == cur` does not catch this (11 != 10) — only
		// maxWalk does. Without maxWalk this test HANGS rather than failing, which
		// is a poor signal; the depth-cap case below pins the cap with a definite
		// assertion instead.
		procs := map[int32]ProcInfo{
			10: {PPID: 11, Tdev: -1},
			11: {PPID: 10, Tdev: -1},
		}
		if got := OutermostTTY(10, procs); got != "" {
			t.Errorf("got %q want empty", got)
		}
	})

	t.Run("stops at maxWalk ancestors", func(t *testing.T) {
		// A chain longer than the cap, where ONLY an ancestor beyond the cap has a
		// tty. The walk must stop before reaching it and return "". This pins
		// maxWalk with a real assertion rather than by hanging, and it documents
		// that the cap is deliberate rather than incidental.
		procs := map[int32]ProcInfo{}
		const depth = maxWalk + 20
		for i := int32(1); i <= depth; i++ {
			procs[i] = ProcInfo{PPID: i + 1, Tdev: -1}
		}
		// The only real tty in the whole chain sits past the cap.
		procs[depth] = ProcInfo{PPID: 1, Tdev: int32(16<<24 | 7)}

		if got := OutermostTTY(1, procs); got != "" {
			t.Errorf("walk exceeded maxWalk: got %q, want empty", got)
		}
	})

	t.Run("missing pid", func(t *testing.T) {
		if got := OutermostTTY(999, map[int32]ProcInfo{}); got != "" {
			t.Errorf("got %q want empty", got)
		}
	})

	t.Run("stops at ppid<=1, does not consult that entry", func(t *testing.T) {
		// None of the fixtures above key an entry at the terminal ancestor's own
		// PID, so they all terminate via the natural `!ok` break regardless of
		// whether the `PPID <= 1` guard exists. This fixture keys pid 1 itself
		// with a DIFFERENT tty: if the walk kept going past the root instead of
		// stopping there, it would pick up ttys099 and overwrite the correct
		// answer. Only the `PPID <= 1` check prevents that.
		procs := map[int32]ProcInfo{
			10: {PPID: 1, Tdev: pty(9)},
			1:  {PPID: 0, Tdev: pty(99)}, // must never be consulted
		}
		if got := OutermostTTY(10, procs); got != "ttys009" {
			t.Errorf("got %q want ttys009 (walk must stop at ppid<=1)", got)
		}
	})
}
