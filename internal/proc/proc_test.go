package proc

import (
	"os"
	"os/exec"
	"runtime"
	"testing"
	"time"
)

func TestAlive(t *testing.T) {
	if !Alive(os.Getpid()) {
		t.Error("current process should be alive")
	}
	if Alive(0) || Alive(-1) {
		t.Error("non-positive pids should not be alive")
	}
	// A very unlikely pid should be dead.
	if Alive(999999) {
		t.Skip("pid 999999 unexpectedly exists; skipping")
	}
}

func TestSetChildGroupAndTerminate(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("unix-only process group test")
	}
	cmd := exec.Command("sleep", "60")
	SetChildGroup(cmd)
	if err := cmd.Start(); err != nil {
		t.Fatal(err)
	}
	pid := cmd.Process.Pid
	if !Alive(pid) {
		t.Fatalf("sleep %d should be alive after start", pid)
	}

	done := make(chan struct{})
	go func() { _ = cmd.Wait(); close(done) }()

	TerminateGroup(pid, time.Second)

	select {
	case <-done:
		// reaped; good
	case <-time.After(5 * time.Second):
		t.Fatal("TerminateGroup did not stop the child")
	}
}

func TestSetDetachedSetsSid(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("unix-only")
	}
	cmd := exec.Command("true")
	SetDetached(cmd)
	if cmd.SysProcAttr == nil || !cmd.SysProcAttr.Setsid {
		t.Error("SetDetached should set Setsid")
	}
}
