package session

import (
	"os"
	"testing"
	"time"
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
		for _, pid := range []int{0, -1} {
			if IsLive(Session{PID: pid, ProcStart: actual}) {
				t.Errorf("pid %d must be rejected", pid)
			}
		}
	})
}
