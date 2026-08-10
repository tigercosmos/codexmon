// Package proc handles process-group lifecycle for the monitored Codex child.
//
// Codex spawns its own shell commands (e.g. /bin/zsh -lc 'go test'), so killing
// only the codex PID would orphan those children. We therefore launch codex in
// its own process group and signal the whole group.
package proc

import (
	"os/exec"
	"strconv"
	"strings"
	"syscall"
	"time"
)

// SetChildGroup configures cmd so the child becomes the leader of a fresh
// process group, letting us signal it and all of its descendants together.
//
// It also pins Stdin: if the caller leaves cmd.Stdin nil, exec connects the
// child to /dev/null, which is exactly what we want — a piped, never-closing
// stdin is the classic cause of `codex exec` hanging on
// "Reading additional input from stdin...".
func SetChildGroup(cmd *exec.Cmd) {
	if cmd.SysProcAttr == nil {
		cmd.SysProcAttr = &syscall.SysProcAttr{}
	}
	cmd.SysProcAttr.Setpgid = true
}

// SetDetached configures cmd to survive its launcher: a new session means the
// process is reparented to init and is unaffected when the spawning shell (and
// the Claude Bash tool that ran it) exits.
func SetDetached(cmd *exec.Cmd) {
	if cmd.SysProcAttr == nil {
		cmd.SysProcAttr = &syscall.SysProcAttr{}
	}
	cmd.SysProcAttr.Setsid = true
}

// Alive reports whether a process is still running (signal 0 probe).
func Alive(pid int) bool {
	if pid <= 0 {
		return false
	}
	// On Unix, signal 0 performs error checking without sending a signal.
	return syscall.Kill(pid, 0) == nil
}

// IsGroupLeader reports whether pid still leads its own process group — the
// shape codexmon gives every agent child via SetChildGroup.
//
// It exists to filter pid reuse before signalling a recorded pid: most processes
// inherit their parent's group, so a recycled pid often fails this check. It is
// only a cheap filter, not proof of identity — every shell job leader, every
// detached codexmon worker, and every agent child leads a group — so callers
// pair it with a freshness bound or an explicit user request.
func IsGroupLeader(pid int) bool {
	if pid <= 0 {
		return false
	}
	pgid, err := syscall.Getpgid(pid)
	return err == nil && pgid == pid
}

// StartTime returns approximately when the process was started, and whether it
// could be determined at all.
//
// It shells out to ps(1)'s elapsed-time column, which every Unix provides (macOS
// has no `etimes`, so the `[[dd-]hh:]mm:ss` form is the portable one). Resolution
// is one second, which is ample for its only purpose: telling codexmon's own
// agent — started within a second of its job — apart from an unrelated process
// that happens to have inherited the same pid much later.
func StartTime(pid int) (time.Time, bool) {
	if pid <= 0 {
		return time.Time{}, false
	}
	out, err := exec.Command("ps", "-o", "etime=", "-p", strconv.Itoa(pid)).Output()
	if err != nil {
		return time.Time{}, false
	}
	elapsed, ok := parseETime(string(out))
	if !ok {
		return time.Time{}, false
	}
	return time.Now().Add(-elapsed), true
}

// parseETime parses ps(1)'s elapsed-time field: "mm:ss", "hh:mm:ss", or
// "dd-hh:mm:ss".
func parseETime(s string) (time.Duration, bool) {
	s = strings.TrimSpace(s)
	if s == "" {
		return 0, false
	}
	var days int
	if dash := strings.IndexByte(s, '-'); dash >= 0 {
		d, err := strconv.Atoi(s[:dash])
		if err != nil || d < 0 {
			return 0, false
		}
		days, s = d, s[dash+1:]
	}
	parts := strings.Split(s, ":")
	if len(parts) < 2 || len(parts) > 3 {
		return 0, false
	}
	var hours, mins, secs int
	if len(parts) == 3 {
		h, err := strconv.Atoi(parts[0])
		if err != nil || h < 0 {
			return 0, false
		}
		hours = h
		parts = parts[1:]
	}
	m, err := strconv.Atoi(parts[0])
	if err != nil || m < 0 {
		return 0, false
	}
	sec, err := strconv.Atoi(parts[1])
	if err != nil || sec < 0 {
		return 0, false
	}
	mins, secs = m, sec
	return time.Duration(days)*24*time.Hour +
		time.Duration(hours)*time.Hour +
		time.Duration(mins)*time.Minute +
		time.Duration(secs)*time.Second, true
}

// StartedBefore reports whether pid's process began no later than t.
//
// This is the identity check that makes it safe to signal a pid read out of a
// job file: codexmon's agent starts within a second of its job, so a pid that
// began well after that window is a different process wearing a recycled number.
// An indeterminate answer counts as "no" — declining to signal is always the
// recoverable direction.
func StartedBefore(pid int, t time.Time) bool {
	started, ok := StartTime(pid)
	if !ok {
		return false
	}
	return !started.After(t)
}

// KillGroupNow sends an immediate SIGKILL to the whole process group led by
// pid. Use for timeout cancellation where a graceful SIGTERM grace is not worth
// waiting for.
func KillGroupNow(pid int) {
	if pid > 0 {
		_ = syscall.Kill(-pid, syscall.SIGKILL)
	}
}

// groupAlive reports whether any process remains in the group led by pid.
// kill(-pgid, 0) succeeds while the group has at least one member and returns
// ESRCH once it is empty — unlike probing the leader pid, which can be gone
// while descendants linger.
func groupAlive(pid int) bool {
	if pid <= 0 {
		return false
	}
	return syscall.Kill(-pid, 0) == nil
}

// TerminateGroup asks the process group led by pid to stop: SIGTERM first, then
// SIGKILL after grace if anything is still alive. The negative pid targets the
// whole group. It is safe to call on a dead process.
func TerminateGroup(pid int, grace time.Duration) {
	if pid <= 0 {
		return
	}
	_ = syscall.Kill(-pid, syscall.SIGTERM)
	if grace <= 0 {
		grace = 3 * time.Second
	}
	deadline := time.Now().Add(grace)
	for time.Now().Before(deadline) {
		// Probe the whole group, not just the leader: the leader can exit while
		// a descendant keeps running (and holding, e.g., a pipe). Returning on
		// leader death alone would skip the SIGKILL escalation for that child.
		if !groupAlive(pid) {
			return
		}
		time.Sleep(50 * time.Millisecond)
	}
	// Escalate to the whole group. Harmless if the group is already empty
	// (ESRCH). We deliberately do NOT also send a positive-pid SIGKILL: once the
	// leader has been reaped its pid may have been recycled by another process.
	_ = syscall.Kill(-pid, syscall.SIGKILL)
}
