package cli

import (
	"bytes"
	"context"
	"os"
	"os/exec"
	"time"

	"github.com/tigercosmos/codexmon/internal/proc"
)

// spawnWorker launches a detached `codexmon __worker --job <id>` that owns the
// codex child and survives the launching shell. Its stdio is sent to /dev/null;
// all of its output goes to the job's files.
func spawnWorker(self, id, cwd, _ string) (int, error) {
	devnull, err := os.OpenFile(os.DevNull, os.O_RDWR, 0)
	if err != nil {
		return 0, err
	}
	defer devnull.Close()

	cmd := exec.Command(self, "__worker", "--job", id)
	cmd.Dir = cwd
	cmd.Env = os.Environ()
	cmd.Stdin = devnull
	cmd.Stdout = devnull
	cmd.Stderr = devnull
	proc.SetDetached(cmd)

	if err := cmd.Start(); err != nil {
		return 0, err
	}
	pid := cmd.Process.Pid
	_ = cmd.Process.Release()
	return pid, nil
}

// killGroup terminates a process group (SIGTERM then SIGKILL).
func killGroup(pid int) {
	proc.TerminateGroup(pid, 2*time.Second)
}

// alive reports whether a process is still running.
func alive(pid int) bool {
	return proc.Alive(pid)
}

// runCapture runs a short command with a hard timeout and a /dev/null stdin,
// returning combined output. The timeout protects doctor/version from the same
// hangs codexmon exists to surface.
func runCapture(timeout time.Duration, name string, args ...string) (string, error) {
	ctx, cancel := context.WithTimeout(context.Background(), timeout)
	defer cancel()
	cmd := exec.CommandContext(ctx, name, args...)
	if devnull, err := os.Open(os.DevNull); err == nil {
		cmd.Stdin = devnull
		defer devnull.Close()
	}
	var buf bytes.Buffer
	cmd.Stdout = &buf
	cmd.Stderr = &buf
	proc.SetChildGroup(cmd)
	// On timeout, kill the whole process group (not just the leader). WaitDelay
	// bounds how long Run will block draining pipes a lingering grandchild holds
	// open after the process exits — so doctor/version can never hang past the
	// deadline, the very failure codexmon exists to surface.
	cmd.Cancel = func() error {
		if cmd.Process != nil {
			proc.KillGroupNow(cmd.Process.Pid)
		}
		return nil
	}
	cmd.WaitDelay = 2 * time.Second
	err := cmd.Run()
	if ctx.Err() == context.DeadlineExceeded {
		return buf.String(), context.DeadlineExceeded
	}
	return buf.String(), err
}
