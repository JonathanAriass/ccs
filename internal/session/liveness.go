package session

import (
	"syscall"
	"time"

	"golang.org/x/sys/unix"
)

// szomb is kp_proc.p_stat for a zombie. A zombie passes the signal-0 probe, so
// this check is genuinely load-bearing rather than defensive.
const szomb = 5

// procStartLayout matches ctime(3). The underscore in "_2" is required: a
// single-digit day is SPACE-PADDED to two columns ("Jul  7"), so both "Jan 02"
// and "Jan 2" produce a string that never matches the registry.
const procStartLayout = "Mon Jan _2 15:04:05 2006"

// formatProcStart renders a start time exactly as the registry records it.
//
// procStart is written in UTC with the C locale, NOT local time — on a
// Europe/Madrid machine it reads two hours behind the wall clock. Formatting
// without .UTC() rejects every live session.
//
// usec is accepted and deliberately discarded: the registry truncates the
// sub-second component, so rounding produces an off-by-one-second mismatch on
// any process that started past the half-second mark.
func formatProcStart(sec, usec int64) string {
	_ = usec
	return time.Unix(sec, 0).UTC().Format(procStartLayout)
}

// procStartString returns pid's start time in registry format.
func procStartString(pid int) (string, error) {
	kp, err := unix.SysctlKinfoProc("kern.proc.pid", pid)
	if err != nil {
		return "", err // EIO for a nonexistent pid
	}
	return formatProcStart(kp.Proc.P_starttime.Sec, int64(kp.Proc.P_starttime.Usec)), nil
}

// IsLive reports whether s's process is still the process the registry meant.
//
// Three conditions, each independently necessary:
//   - pid > 0, or the signal probe would target a process GROUP
//   - the process exists and is not a zombie
//   - its start time matches procStart, so a recycled PID cannot resurrect a
//     dead session under a live process's identity
func IsLive(s Session) bool {
	if s.PID <= 0 {
		return false
	}
	// ESRCH means gone. EPERM means alive and owned by another user — treating
	// any error as dead would be wrong.
	if err := syscall.Kill(s.PID, 0); err != nil && err != syscall.EPERM {
		return false
	}
	kp, err := unix.SysctlKinfoProc("kern.proc.pid", s.PID)
	if err != nil {
		return false
	}
	if kp.Proc.P_stat == szomb {
		return false
	}
	if s.ProcStart == "" {
		return true // older registry files predate the field
	}
	return formatProcStart(kp.Proc.P_starttime.Sec, int64(kp.Proc.P_starttime.Usec)) == s.ProcStart
}

// Live filters all down to sessions whose process is still running.
func Live(all []Session) []Session {
	out := make([]Session, 0, len(all))
	for _, s := range all {
		if IsLive(s) {
			out = append(out, s)
		}
	}
	return out
}
