package job

import (
	"os"
	"os/exec"
	"strings"
	"testing"
	"time"

	"github.com/tigercosmos/codexmon/internal/events"
)

func TestNewIDFormat(t *testing.T) {
	id := NewID()
	if !strings.HasPrefix(id, "cdx-") {
		t.Errorf("id %q should start with cdx-", id)
	}
	a, b := NewID(), NewID()
	if a == b {
		t.Error("NewID should be unique")
	}
}

func TestStatusRoundTrip(t *testing.T) {
	t.Setenv("CODEXMON_HOME", t.TempDir())
	id := NewID()
	dir, err := Dir(id)
	if err != nil {
		t.Fatal(err)
	}
	ec := 0
	in := &Status{
		ID: id, State: StateCompleted, Health: HealthDone, Phase: "completed",
		Args: []string{"exec", "review"}, ExitCode: &ec,
		Usage:      &events.Usage{InputTokens: 10, OutputTokens: 2},
		Thresholds: Thresholds{WallSec: 600},
		StartedAt:  time.Now(),
	}
	if err := WriteStatus(dir, in); err != nil {
		t.Fatal(err)
	}
	out, err := ReadStatus(dir)
	if err != nil {
		t.Fatal(err)
	}
	if out.ID != id || out.State != StateCompleted || out.Usage == nil || out.Usage.InputTokens != 10 {
		t.Errorf("round-trip mismatch: %+v", out)
	}
	byID, err := ReadStatusByID(id)
	if err != nil || byID.ID != id {
		t.Errorf("ReadStatusByID failed: %v %+v", err, byID)
	}
}

func TestSpecRoundTrip(t *testing.T) {
	t.Setenv("CODEXMON_HOME", t.TempDir())
	dir, _ := Dir(NewID())
	in := &Spec{ID: "x", CodexBin: "/bin/codex", Args: []string{"exec", "--json"}, JSONMode: true, Thresholds: Thresholds{WallSec: 60}}
	if err := WriteSpec(dir, in); err != nil {
		t.Fatal(err)
	}
	out, err := ReadSpec(dir)
	if err != nil || !out.JSONMode || len(out.Args) != 2 {
		t.Fatalf("spec round-trip: %v %+v", err, out)
	}
}

func TestListLatestResolve(t *testing.T) {
	t.Setenv("CODEXMON_HOME", t.TempDir())

	mk := func(state State, started time.Time) string {
		id := NewID()
		dir, _ := Dir(id)
		_ = WriteStatus(dir, &Status{ID: id, State: state, StartedAt: started})
		return id
	}
	base := time.Now()
	oldDone := mk(StateCompleted, base.Add(-2*time.Hour))
	_ = oldDone
	newDone := mk(StateCompleted, base.Add(-1*time.Hour))
	running := mk(StateRunning, base.Add(-30*time.Minute))

	all, err := List()
	if err != nil {
		t.Fatal(err)
	}
	if len(all) != 3 {
		t.Fatalf("List len = %d, want 3", len(all))
	}
	// newest first
	if all[0].ID != running {
		t.Errorf("List[0] = %s, want running %s", all[0].ID, running)
	}

	// Latest prefers an active job even if not the newest-started terminal one.
	latest, err := Latest()
	if err != nil || latest.ID != running {
		t.Errorf("Latest = %v (%v), want %s", latest, err, running)
	}

	got, err := Resolve("")
	if err != nil || got.ID != running {
		t.Errorf("Resolve(\"\") = %v, want %s", got, running)
	}
	got, err = Resolve(newDone)
	if err != nil || got.ID != newDone {
		t.Errorf("Resolve(%s) = %v", newDone, got)
	}
	if _, err := Resolve("cdx-does-not-exist"); err == nil {
		t.Error("Resolve(missing) should error")
	}
}

func TestLatestEmpty(t *testing.T) {
	t.Setenv("CODEXMON_HOME", t.TempDir())
	if _, err := Latest(); err != ErrNoJobs {
		t.Errorf("Latest on empty = %v, want ErrNoJobs", err)
	}
}

func TestCancelMarker(t *testing.T) {
	t.Setenv("CODEXMON_HOME", t.TempDir())
	dir, _ := Dir(NewID())
	if CancelRequested(dir) {
		t.Error("cancel should not be requested initially")
	}
	if err := RequestCancel(dir); err != nil {
		t.Fatal(err)
	}
	if !CancelRequested(dir) {
		t.Error("cancel should be requested after RequestCancel")
	}
}

func TestReconcileDeadWorker(t *testing.T) {
	t.Setenv("CODEXMON_HOME", t.TempDir())
	// A reaped process is a definitely-dead pid.
	cmd := exec.Command("sh", "-c", "exit 0")
	if err := cmd.Run(); err != nil {
		t.Fatal(err)
	}
	deadPID := cmd.Process.Pid

	id := NewID()
	dir, _ := Dir(id)
	_ = WriteStatus(dir, &Status{
		ID: id, State: StateRunning, Health: HealthHealthy,
		WorkerPID: deadPID, StartedAt: time.Now(),
	})
	st, err := ReadStatus(dir)
	if err != nil {
		t.Fatal(err)
	}
	if st.State != StateFailed || st.Health != HealthDead {
		t.Errorf("dead-worker job should reconcile to failed/dead, got %s/%s", st.State, st.Health)
	}
	if st.EndedAt == nil {
		t.Error("reconciled job should have an EndedAt")
	}
	if st.Error == "" {
		t.Error("reconciled job should explain why")
	}
}

func TestReconcileAliveWorkerUnchanged(t *testing.T) {
	t.Setenv("CODEXMON_HOME", t.TempDir())
	id := NewID()
	dir, _ := Dir(id)
	_ = WriteStatus(dir, &Status{
		ID: id, State: StateRunning, Health: HealthHealthy,
		WorkerPID: os.Getpid(), StartedAt: time.Now(),
	})
	st, _ := ReadStatus(dir)
	if st.State != StateRunning {
		t.Errorf("alive-worker job should stay running, got %s", st.State)
	}
}

func TestReconcileSkipsWhenNoWorkerPID(t *testing.T) {
	t.Setenv("CODEXMON_HOME", t.TempDir())
	id := NewID()
	dir, _ := Dir(id)
	_ = WriteStatus(dir, &Status{ID: id, State: StateQueued, StartedAt: time.Now()}) // WorkerPID 0
	st, _ := ReadStatus(dir)
	if st.State != StateQueued {
		t.Errorf("no-worker-pid status should not reconcile, got %s", st.State)
	}
}

func TestValidID(t *testing.T) {
	if !ValidID("cdx-20260603-150405-9f3a1c") {
		t.Error("canonical id should be valid")
	}
	if !ValidID(NewID()) {
		t.Error("generated id should be valid")
	}
	for _, bad := range []string{"", "cdx-1", "../escape", "cdx-20260603-150405-XYZ", "foo/bar", "cdx-20260603-150405-9f3a1c/.."} {
		if ValidID(bad) {
			t.Errorf("%q should be invalid", bad)
		}
	}
}

func TestDirAndReadRejectTraversalID(t *testing.T) {
	t.Setenv("CODEXMON_HOME", t.TempDir())
	if _, err := Dir("../escape"); err == nil {
		t.Error("Dir should reject a traversal id")
	}
	if _, err := ReadStatusByID("../../etc/passwd"); err == nil {
		t.Error("ReadStatusByID should reject a traversal id")
	}
}

func TestReconcileStaleWorker(t *testing.T) {
	t.Setenv("CODEXMON_HOME", t.TempDir())
	id := NewID()
	dir, _ := Dir(id)
	old := time.Now().Add(-time.Minute) // far past the staleness limit
	_ = WriteStatus(dir, &Status{
		ID: id, State: StateRunning, Health: HealthHealthy,
		WorkerPID: os.Getpid(), StartedAt: old, UpdatedAt: old, // alive pid, but stale file
	})
	st, _ := ReadStatus(dir)
	if st.State != StateFailed {
		t.Errorf("alive-but-stale job should reconcile to failed (pid-reuse guard), got %s", st.State)
	}
}

func TestStateActive(t *testing.T) {
	active := []State{StateQueued, StateRunning}
	terminal := []State{StateCompleted, StateFailed, StateStalled, StateTimeout, StateCancelled}
	for _, s := range active {
		if !s.Active() {
			t.Errorf("%s should be active", s)
		}
	}
	for _, s := range terminal {
		if s.Active() {
			t.Errorf("%s should be terminal", s)
		}
	}
}
