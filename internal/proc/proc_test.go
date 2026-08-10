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

func TestParseETime(t *testing.T) {
	for _, tc := range []struct {
		in   string
		want time.Duration
		ok   bool
	}{
		{"00:00", 0, true},
		{" 01:30 ", 90 * time.Second, true},
		{"02:03:04", 2*time.Hour + 3*time.Minute + 4*time.Second, true},
		{"3-04:05:06", 76*time.Hour + 5*time.Minute + 6*time.Second, true},
		{"", 0, false},
		{"garbage", 0, false},
		{"1:2:3:4", 0, false},
		{"-1:00", 0, false},
	} {
		got, ok := parseETime(tc.in)
		if ok != tc.ok || (ok && got != tc.want) {
			t.Errorf("parseETime(%q) = (%v, %v), want (%v, %v)", tc.in, got, ok, tc.want, tc.ok)
		}
	}
}

// StartTime underpins the identity check that gates every signal codexmon sends
// to a pid read out of a file, so it must agree with reality for a process we
// just started — and must refuse to guess for one that does not exist.
func TestStartTimeAndStartedBefore(t *testing.T) {
	cmd := exec.Command("sleep", "30")
	if err := cmd.Start(); err != nil {
		t.Fatal(err)
	}
	pid := cmd.Process.Pid
	defer func() { _ = cmd.Process.Kill(); _ = cmd.Wait() }()

	launched := time.Now()
	started, ok := StartTime(pid)
	if !ok {
		t.Fatal("StartTime should resolve a live process")
	}
	// ps reports whole seconds, so allow a couple either way.
	if d := started.Sub(launched); d > 2*time.Second || d < -3*time.Second {
		t.Errorf("StartTime off by %v from the actual launch", d)
	}
	if !StartedBefore(pid, launched.Add(time.Minute)) {
		t.Error("a process started now should count as started before a later cutoff")
	}
	// A cutoff before the process existed must reject it — this is the recycled
	// pid case, where the number is live but the process is somebody else's.
	if StartedBefore(pid, launched.Add(-time.Hour)) {
		t.Error("a process must not pass a cutoff predating its start")
	}

	// An unknown pid is indeterminate, which must read as "do not signal".
	reaped := exec.Command("true")
	if err := reaped.Run(); err != nil {
		t.Fatal(err)
	}
	if _, ok := StartTime(reaped.ProcessState.Pid()); ok {
		t.Error("StartTime should not resolve a dead pid")
	}
	if StartedBefore(reaped.ProcessState.Pid(), time.Now()) {
		t.Error("an unresolvable pid must never pass the identity check")
	}
	if StartedBefore(0, time.Now()) || StartedBefore(-1, time.Now()) {
		t.Error("a non-positive pid must never pass the identity check")
	}
}
