package session

import (
	"os"
	"os/exec"
	"testing"
	"time"

	"golang.org/x/sys/unix"
)

// formatProcStart is the exact rendering the registry uses. These cases are the
// two that break naive implementations.
func TestFormatProcStart(t *testing.T) {
	cases := []struct {
		name string
		sec  int64
		usec int64
		want string
	}{
		{
			// Single-digit day renders with TWO spaces. A layout of "Jan 02" or
			// "Jan 2" passes every test written on a two-digit day and fails in
			// production for the first nine days of every month.
			name: "single digit day",
			sec:  time.Date(2026, 7, 7, 7, 5, 15, 0, time.UTC).Unix(),
			want: "Tue Jul  7 07:05:15 2026",
		},
		{
			// Sub-second component must be TRUNCATED, not rounded. Rounding
			// matched only 9 of 14 real sessions on this machine.
			name: "usec over half a second must truncate",
			sec:  time.Date(2026, 7, 29, 9, 4, 10, 0, time.UTC).Unix(),
			usec: 900000,
			want: "Wed Jul 29 09:04:10 2026",
		},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			if got := formatProcStart(c.sec, c.usec); got != c.want {
				t.Errorf("got %q want %q", got, c.want)
			}
		})
	}
}

func TestIsLive(t *testing.T) {
	self := os.Getpid()
	actual, err := procStartString(self)
	if err != nil {
		t.Fatalf("procStartString(self): %v", err)
	}

	t.Run("self with matching procStart is live", func(t *testing.T) {
		if !IsLive(Session{PID: self, ProcStart: actual}) {
			t.Error("want live")
		}
	})

	t.Run("PID reuse is rejected", func(t *testing.T) {
		// Live PID, but the registry recorded a different start time — the
		// exact shape of a recycled PID resurrecting a dead session.
		if IsLive(Session{PID: self, ProcStart: "Mon Jan  1 00:00:00 2001"}) {
			t.Error("want dead")
		}
	})

	t.Run("dead PID is rejected", func(t *testing.T) {
		if IsLive(Session{PID: 999999, ProcStart: actual}) {
			t.Error("want dead")
		}
	})

	t.Run("a zombie is not alive", func(t *testing.T) {
		// A zombie passes syscall.Kill(pid, 0) — its PID entry still exists and is
		// signalable — so ONLY the SZOMB check can reject it. This is a single-guard
		// fixture: pid > 0, ProcStart empty (so the timestamp comparison never runs),
		// and the signal probe returns nil. Delete the SZOMB check and IsLive returns
		// true for a dead process.
		//
		// Measured on macOS 26.5: the child reaches P_stat == SZOMB on the first or
		// second poll, ~10ms, and Wait() reaps it cleanly afterwards. This was twice
		// dismissed as "would require spawning and reaping a child" — it is one
		// exec.Command, a bounded poll, and a Wait.
		cmd := exec.Command("/bin/sh", "-c", "exit 0")
		if err := cmd.Start(); err != nil {
			t.Fatalf("start child: %v", err)
		}
		pid := cmd.Process.Pid
		defer cmd.Wait() // reap, so the test leaves no zombie behind

		var sawZombie bool
		deadline := time.Now().Add(3 * time.Second)
		for time.Now().Before(deadline) {
			kp, err := unix.SysctlKinfoProc("kern.proc.pid", pid)
			if err == nil && kp.Proc.P_stat == szomb {
				sawZombie = true
				break
			}
			time.Sleep(10 * time.Millisecond)
		}
		if !sawZombie {
			t.Skip("child never observed in SZOMB state within 3s")
		}

		if IsLive(Session{PID: pid}) {
			t.Error("a zombie must not count as live: it passes kill(pid,0), so only the SZOMB check rejects it")
		}
	})

	t.Run("EPERM means alive, not dead", func(t *testing.T) {
		// pid 1 is launchd, owned by root. As a non-root user syscall.Kill(1, 0)
		// returns EPERM — which means the process EXISTS but we may not signal it.
		// Verified on this machine: kill(1,0)=EPERM, kill(self,0)=nil,
		// kill(999999,0)=ESRCH.
		//
		// Without this case, simplifying the probe to `if err != nil { return false }`
		// passes every other test and silently drops every session owned by another
		// user. ProcStart is left empty deliberately so this exercises ONLY the
		// signal-probe branch and does not depend on procStartString — the function
		// this same file is otherwise testing.
		if !IsLive(Session{PID: 1}) {
			t.Error("pid 1 (root-owned launchd) must count as live: EPERM means alive")
		}
	})

	t.Run("non-positive PID never reaches the signal probe", func(t *testing.T) {
		// kill(0, sig) signals the caller's whole process group and kill(-1, sig)
		// signals everything the user can signal. A truncated registry file with
		// no pid field unmarshals to 0 and lands here.
		//
		// ProcStart is deliberately EMPTY. Measured on macOS 26.5: pid 0 is
		// kernel_task — a real, live, non-zombie process — so kill(0,0) returns nil
		// AND the sysctl lookup succeeds (P_stat=2). With a non-empty ProcStart the
		// timestamp comparison rejects it, so this subtest would pass even with the
		// guard deleted — testing nothing. With ProcStart empty, IsLive returns early
		// at `if s.ProcStart == "" { return true }`, leaving the pid guard as the ONLY
		// thing that can reject pid 0. Delete the guard and this reddens.
		if IsLive(Session{PID: 0}) {
			t.Error("pid 0 must be rejected before the signal probe: kill(0, sig) hits the caller's process group")
		}

		// pid -1 cannot be pinned the same way: kill(-1, 0) also returns nil, but the
		// sysctl lookup fails with EIO and rejects it regardless of the guard.
		// Asserted for the behaviour; NOT claimed as coverage of the guard.
		if IsLive(Session{PID: -1}) {
			t.Error("pid -1 must be rejected")
		}
	})
}
