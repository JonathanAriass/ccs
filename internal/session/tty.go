package session

import (
	"fmt"

	"golang.org/x/sys/unix"
)

// ptyMajor is the device major number for pty slaves on darwin.
const ptyMajor = 16

// maxWalk caps the ancestor walk against a corrupted ppid chain.
const maxWalk = 64

type ProcInfo struct {
	PPID int32
	Tdev int32
}

// ttyName converts a kinfo_proc e_tdev into a tty name.
//
// This is pure arithmetic on purpose. Do NOT build a device-number -> name map
// by scanning /dev at startup: devfs creates /dev/ttysNNN nodes ON DEMAND, so
// such a map goes stale the moment the user opens a new tab, and that tab's
// session would silently render as "??" and refuse to focus.
func ttyName(tdev int32) string {
	if tdev == -1 { // NODEV
		return "??"
	}
	u := uint32(tdev)
	if (u>>24)&0xff != ptyMajor {
		return "??"
	}
	return fmt.Sprintf("ttys%03d", u&0xffffff)
}

// Snapshot reads every process once. One syscall serves both the liveness pass
// and every session's ancestor walk.
func Snapshot() (map[int32]ProcInfo, error) {
	kps, err := unix.SysctlKinfoProcSlice("kern.proc.all")
	if err != nil {
		return nil, err
	}
	m := make(map[int32]ProcInfo, len(kps))
	for i := range kps {
		m[kps[i].Proc.P_pid] = ProcInfo{
			PPID: kps[i].Eproc.Ppid,
			Tdev: kps[i].Eproc.Tdev,
		}
	}
	return m, nil
}

// OutermostTTY walks pid's ancestors and returns the LAST real tty seen.
//
// Two rules that a naive walk gets wrong, both load-bearing:
//
//   - Keep the OUTERMOST tty, not the first. A session running inside a Neovim
//     :terminal has nvim's embedded pty as its own tty; the real iTerm2 tab is
//     two or three levels up. iTerm2's AppleScript session list contains only
//     the latter.
//   - Do NOT stop at an ancestor whose tty is "??". In the Neovim chain, nvim's
//     forked job process sits between the two with no controlling terminal.
//
// Terminating at ppid <= 1 is safe: iTerm2 itself is a direct child of launchd
// and has no tty, so the walk cannot escape the terminal emulator.
//
// Returns "" when no ancestor has a tty (background/daemon sessions).
//
// COVERAGE NOTE for whoever tests this. Two of the loop's termination guards
// cannot be pinned by any fixture, and that is expected rather than a gap:
//
//   - the `!ok` break: deleting it leaves `p` as the zero ProcInfo, whose Tdev 0
//     is not the pty major so ttyName returns "??", and whose PPID 0 trips the
//     `<= 1` break on the next line. Same result, so no fixture distinguishes it.
//   - the `p.PPID == cur` self-loop break: maxWalk terminates a self-loop anyway.
//
// Both are cheap defensive redundancy against a corrupted process table. Do not
// delete them as dead code, and do not invent coverage for them — report them as
// unpinnable, the way Task 1 reported IsDir(). maxWalk itself IS pinned, by the
// depth-cap test.
func OutermostTTY(pid int, procs map[int32]ProcInfo) string {
	cur := int32(pid)
	out := ""
	for i := 0; i < maxWalk; i++ {
		p, ok := procs[cur]
		if !ok {
			break
		}
		if n := ttyName(p.Tdev); n != "??" {
			out = n
		}
		if p.PPID <= 1 || p.PPID == cur {
			break
		}
		cur = p.PPID
	}
	return out
}
