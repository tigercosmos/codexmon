package proc

import (
	"os"
	"os/exec"
	"runtime"
	"strconv"
	"strings"
	"syscall"
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

func TestTerminateGroupEscalatesToSurvivingDescendant(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("unix-only process group test")
	}
	// Leader spawns a SIGTERM-ignoring grandchild (stdout to /dev/null so it
	// doesn't hold the Output pipe), prints its pid, then exits. After Output
	// returns the leader is reaped, but the grandchild lingers in the group.
	cmd := exec.Command("sh", "-c", `sh -c 'trap "" TERM; sleep 30' >/dev/null 2>&1 & echo $!; exit 0`)
	SetChildGroup(cmd)
	out, err := cmd.Output()
	if err != nil {
		t.Fatal(err)
	}
	childPID, _ := strconv.Atoi(strings.TrimSpace(string(out)))
	if childPID <= 0 || !Alive(childPID) {
		t.Skipf("could not set up a surviving descendant (pid=%d)", childPID)
	}
	t.Cleanup(func() { _ = syscall.Kill(childPID, syscall.SIGKILL) })

	leaderPID := cmd.Process.Pid // reaped; probing it alone would miss the child
	TerminateGroup(leaderPID, 300*time.Millisecond)

	deadline := time.Now().Add(3 * time.Second)
	for time.Now().Before(deadline) {
		if !Alive(childPID) {
			return // escalated SIGKILL reached the surviving descendant
		}
		time.Sleep(50 * time.Millisecond)
	}
	t.Fatalf("surviving descendant %d ignored SIGTERM and was not SIGKILLed", childPID)
}

func TestGroupAlive(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("unix-only")
	}
	cmd := exec.Command("sleep", "30")
	SetChildGroup(cmd)
	if err := cmd.Start(); err != nil {
		t.Fatal(err)
	}
	pid := cmd.Process.Pid
	if !groupAlive(pid) {
		t.Error("group should be alive while the child runs")
	}
	TerminateGroup(pid, time.Second)
	_ = cmd.Wait()
	if groupAlive(pid) {
		t.Error("group should be gone after TerminateGroup")
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
