package job

import (
	"encoding/json"
	"os"
	"os/exec"
	"strings"
	"testing"
	"time"

	"github.com/tigercosmos/codexmon/internal/agent"
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
		Usage:      &agent.Usage{InputTokens: 10, OutputTokens: 2},
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
	in := &Spec{ID: "x", Agent: "codex", AgentBin: "/bin/codex", Args: []string{"exec", "--json"}, JSONMode: true, Thresholds: Thresholds{WallSec: 60}}
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

func TestReconcilePersistsTerminalState(t *testing.T) {
	t.Setenv("CODEXMON_HOME", t.TempDir())
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

	// Reading a dead-worker job reconciles it AND persists the terminal state, so
	// processes reading status.json directly (not via ReadStatus) see it too.
	if st, _ := ReadStatus(dir); st.State != StateFailed {
		t.Fatalf("reconcile returned %s, want failed", st.State)
	}
	_, statusPath, _, _, _, _ := Paths(dir)
	raw, err := os.ReadFile(statusPath)
	if err != nil {
		t.Fatal(err)
	}
	var persisted Status
	if err := json.Unmarshal(raw, &persisted); err != nil {
		t.Fatal(err)
	}
	if persisted.State != StateFailed || persisted.EndedAt == nil {
		t.Errorf("status.json not persisted as terminal: state=%s ended=%v", persisted.State, persisted.EndedAt)
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

// mkJob writes a job dir with a status in the given state, ended at the given
// time (ignored for active states), and returns its id.
func mkJob(t *testing.T, state State, ended time.Time) string {
	t.Helper()
	id := NewID()
	dir, err := Dir(id)
	if err != nil {
		t.Fatal(err)
	}
	st := &Status{ID: id, State: state, StartedAt: ended.Add(-time.Minute), UpdatedAt: ended}
	if !state.Active() {
		st.EndedAt = &ended
	}
	if err := WriteStatus(dir, st); err != nil {
		t.Fatal(err)
	}
	return id
}

func exists(t *testing.T, id string) bool {
	t.Helper()
	_, err := ReadStatusByID(id)
	return err == nil
}

func TestPruneByAge(t *testing.T) {
	t.Setenv("CODEXMON_HOME", t.TempDir())
	now := time.Now()
	old := mkJob(t, StateCompleted, now.Add(-10*24*time.Hour))
	fresh := mkJob(t, StateCompleted, now.Add(-time.Hour))
	active := mkJob(t, StateRunning, now)

	removed, err := Prune(PruneOptions{MaxAge: 7 * 24 * time.Hour})
	if err != nil {
		t.Fatal(err)
	}
	if removed != 1 {
		t.Errorf("removed = %d, want 1", removed)
	}
	if exists(t, old) {
		t.Error("old terminal job should be pruned")
	}
	if !exists(t, fresh) || !exists(t, active) {
		t.Error("fresh and active jobs must survive an age prune")
	}
}

func TestPruneByCount(t *testing.T) {
	t.Setenv("CODEXMON_HOME", t.TempDir())
	now := time.Now()
	oldest := mkJob(t, StateFailed, now.Add(-3*time.Hour))
	middle := mkJob(t, StateCompleted, now.Add(-2*time.Hour))
	newest := mkJob(t, StateCompleted, now.Add(-time.Hour))
	active := mkJob(t, StateQueued, now)

	removed, err := Prune(PruneOptions{MaxCount: 2})
	if err != nil {
		t.Fatal(err)
	}
	if removed != 1 {
		t.Errorf("removed = %d, want 1", removed)
	}
	if exists(t, oldest) {
		t.Error("oldest terminal job should be pruned past the count cap")
	}
	if !exists(t, middle) || !exists(t, newest) || !exists(t, active) {
		t.Error("newest terminal jobs and active jobs must survive a count prune")
	}
}

func TestPruneAllKeepsActive(t *testing.T) {
	t.Setenv("CODEXMON_HOME", t.TempDir())
	now := time.Now()
	done := mkJob(t, StateCompleted, now)
	cancelled := mkJob(t, StateCancelled, now)
	active := mkJob(t, StateRunning, now)

	// A fresh dir with no status yet (a launch in progress) must survive even
	// --all; only once it is older than the grace age is it debris.
	halfID := NewID()
	if _, err := Dir(halfID); err != nil {
		t.Fatal(err)
	}

	removed, err := Prune(PruneOptions{All: true})
	if err != nil {
		t.Fatal(err)
	}
	if removed != 2 {
		t.Errorf("removed = %d, want 2", removed)
	}
	if exists(t, done) || exists(t, cancelled) {
		t.Error("--all should remove every terminal job")
	}
	if !exists(t, active) {
		t.Error("--all must never remove an active job")
	}
	root, _ := jobsRoot()
	halfDir := root + "/" + halfID
	if _, err := os.Stat(halfDir); err != nil {
		t.Error("--all must not race a launch: a fresh status-less dir must survive")
	}
	old := time.Now().Add(-time.Hour)
	if err := os.Chtimes(halfDir, old, old); err != nil {
		t.Fatal(err)
	}
	if n, _ := Prune(PruneOptions{All: true}); n != 1 {
		t.Errorf("aged status-less dir should be reaped by --all, removed = %d", n)
	}
}

func TestPruneIgnoresForeignAndFreshUnreadableDirs(t *testing.T) {
	home := t.TempDir()
	t.Setenv("CODEXMON_HOME", home)
	// A foreign (non-job-id) dir and a fresh half-initialized job dir (no
	// status.json yet — a launch in progress) must both survive.
	root, _ := jobsRoot()
	foreign := root + "/notes"
	if err := os.MkdirAll(foreign, 0o700); err != nil {
		t.Fatal(err)
	}
	halfID := NewID()
	if _, err := Dir(halfID); err != nil {
		t.Fatal(err)
	}

	if _, err := Prune(PruneOptions{MaxAge: 7 * 24 * time.Hour, MaxCount: 1}); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(foreign); err != nil {
		t.Error("foreign dir must never be touched")
	}
	halfDir := root + "/" + halfID
	if _, err := os.Stat(halfDir); err != nil {
		t.Error("a fresh dir without a status must survive (launch in progress)")
	}

	// Once the unreadable dir is older than MaxAge it is reaped.
	old := time.Now().Add(-8 * 24 * time.Hour)
	if err := os.Chtimes(halfDir, old, old); err != nil {
		t.Fatal(err)
	}
	removed, err := Prune(PruneOptions{MaxAge: 7 * 24 * time.Hour})
	if err != nil {
		t.Fatal(err)
	}
	if removed != 1 {
		t.Errorf("removed = %d, want 1 (the aged half-initialized dir)", removed)
	}
	if _, err := os.Stat(halfDir); err == nil {
		t.Error("aged status-less dir should be reaped")
	}
}

func TestDefaultPruneOptionsEnv(t *testing.T) {
	t.Setenv("CODEXMON_KEEP_DAYS", "")
	t.Setenv("CODEXMON_KEEP_JOBS", "")
	opts := DefaultPruneOptions()
	if opts.MaxAge != defaultKeepAge || opts.MaxCount != defaultKeepCount {
		t.Errorf("defaults = %+v", opts)
	}

	t.Setenv("CODEXMON_KEEP_DAYS", "0")
	t.Setenv("CODEXMON_KEEP_JOBS", "5")
	opts = DefaultPruneOptions()
	if opts.MaxAge != 0 || opts.MaxCount != 5 {
		t.Errorf("env override = %+v, want MaxAge 0 MaxCount 5", opts)
	}

	// Garbage values are ignored, not fatal.
	t.Setenv("CODEXMON_KEEP_DAYS", "banana")
	t.Setenv("CODEXMON_KEEP_JOBS", "-3")
	opts = DefaultPruneOptions()
	if opts.MaxAge != defaultKeepAge || opts.MaxCount != defaultKeepCount {
		t.Errorf("bad env should fall back to defaults, got %+v", opts)
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
