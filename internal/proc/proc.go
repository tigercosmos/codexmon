// Package proc handles process-group lifecycle for the monitored Codex child.
//
// Codex spawns its own shell commands (e.g. /bin/zsh -lc 'go test'), so killing
// only the codex PID would orphan those children. We therefore launch codex in
// its own process group and signal the whole group.
package proc

import (
	"os/exec"
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
