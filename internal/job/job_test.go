package job

import (
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/tigercosmos/codexmon/internal/agent"
	"github.com/tigercosmos/codexmon/internal/proc"
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

// A job seeded moments ago has no worker pid yet — the launcher records it just
// after spawning — so it must be left alone.
func TestReconcileSkipsFreshQueuedJob(t *testing.T) {
	t.Setenv("CODEXMON_HOME", t.TempDir())
	id := NewID()
	dir, _ := Dir(id)
	now := time.Now()
	_ = WriteStatus(dir, &Status{ID: id, State: StateQueued, StartedAt: now, UpdatedAt: now}) // WorkerPID 0
	st, _ := ReadStatus(dir)
	if st.State != StateQueued {
		t.Errorf("freshly queued status should not reconcile, got %s", st.State)
	}
}

// A queued job that never got a worker pid is debris from a launcher killed
// mid-launch. It must age out: Latest() prefers active jobs, so leaving it
// active would shadow every later job forever.
func TestReconcileStaleQueuedJobWithoutWorker(t *testing.T) {
	t.Setenv("CODEXMON_HOME", t.TempDir())
	id := NewID()
	dir, _ := Dir(id)
	old := time.Now().Add(-queuedStaleLimit - time.Minute)
	_ = WriteStatus(dir, &Status{ID: id, State: StateQueued, StartedAt: old, UpdatedAt: old}) // WorkerPID 0
	st, _ := ReadStatus(dir)
	if st.State != StateFailed {
		t.Fatalf("stale queued job with no worker should reconcile to failed, got %s", st.State)
	}
	if st.Error == "" {
		t.Error("reconciled job should explain why it failed")
	}
	// It must also stop shadowing `status`/`wait` with no id.
	latest, err := Latest()
	if err != nil || latest.State.Active() {
		t.Errorf("Latest() should not report the zombie as active (err=%v)", err)
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

func TestReconcileWedgedWorker(t *testing.T) {
	t.Setenv("CODEXMON_HOME", t.TempDir())
	id := NewID()
	dir, _ := Dir(id)
	old := time.Now().Add(-workerWedgedLimit - time.Minute)
	_ = WriteStatus(dir, &Status{
		ID: id, State: StateRunning, Health: HealthHealthy,
		WorkerPID: os.Getpid(), StartedAt: old, UpdatedAt: old, // alive pid, but stale file
	})
	st, _ := ReadStatus(dir)
	if st.State != StateFailed {
		t.Errorf("alive-but-wedged job should reconcile to failed, got %s", st.State)
	}
}

// A suspended laptop stops the worker's once-per-second status writes without
// anything being wrong: on resume, the worker and its poller wake together and
// the poller may read first. Briefly-stale-but-alive must therefore stay running
// — and must not be persisted as failed, or the healthy run that resumes would
// flip-flop between failed and running.
func TestReconcileToleratesBriefStalenessWhileWorkerAlive(t *testing.T) {
	t.Setenv("CODEXMON_HOME", t.TempDir())
	id := NewID()
	dir, _ := Dir(id)
	old := time.Now().Add(-2 * time.Minute) // a nap, not a wedge
	_ = WriteStatus(dir, &Status{
		ID: id, State: StateRunning, Health: HealthHealthy,
		WorkerPID: os.Getpid(), StartedAt: old, UpdatedAt: old,
	})
	st, _ := ReadStatus(dir)
	if st.State != StateRunning {
		t.Fatalf("briefly stale job with a live worker should stay running, got %s", st.State)
	}
	raw, err := os.ReadFile(filepath.Join(dir, "status.json"))
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(raw), string(StateFailed)) {
		t.Error("a live worker's status must not be overwritten as failed on disk")
	}
}

// The orphan reaper is the most dangerous line in this package: it signals a pid
// recorded in a file. It must fire for a genuine orphan...
func TestReconcileReapsOrphanedAgentGroup(t *testing.T) {
	t.Setenv("CODEXMON_HOME", t.TempDir())
	agentPID, exited := startGroupLeader(t)
	id := NewID()
	dir, _ := Dir(id)
	now := time.Now()
	_ = WriteStatus(dir, &Status{
		ID: id, State: StateRunning, Health: HealthHealthy,
		WorkerPID: reapedPID(t), AgentPID: agentPID,
		StartedAt: now, UpdatedAt: now, // fresh status: the orphan was just abandoned
	})
	if _, err := ReadStatus(dir); err != nil {
		t.Fatal(err)
	}
	if !exited(3 * time.Second) {
		t.Error("a freshly orphaned agent group should have been terminated")
	}
}

// ...and must NOT fire once the record is old enough that the pid may have been
// recycled by an unrelated process.
func TestReconcileLeavesStaleAgentPIDAlone(t *testing.T) {
	t.Setenv("CODEXMON_HOME", t.TempDir())
	agentPID, exited := startGroupLeader(t)
	id := NewID()
	dir, _ := Dir(id)
	old := time.Now().Add(-orphanReapLimit - time.Minute)
	_ = WriteStatus(dir, &Status{
		ID: id, State: StateRunning, Health: HealthHealthy,
		WorkerPID: reapedPID(t), AgentPID: agentPID,
		StartedAt: old, UpdatedAt: old, // too old to trust the pid
	})
	if _, err := ReadStatus(dir); err != nil {
		t.Fatal(err)
	}
	if exited(200 * time.Millisecond) {
		t.Error("a pid from a stale record must not be signalled; it may have been reused")
	}
}

// startGroupLeader launches a long sleep as the leader of its own process group
// — the shape codexmon gives every agent child — and returns its pid plus a
// predicate that reports whether it has exited within a deadline.
//
// Liveness is checked by waiting on the process, not by probing the pid: a
// signalled child that has not yet been reaped is a zombie, and a signal-0 probe
// still succeeds against a zombie, so probing would report a killed process as
// alive.
func startGroupLeader(t *testing.T) (pid int, exited func(time.Duration) bool) {
	t.Helper()
	cmd := exec.Command("sleep", "60")
	proc.SetChildGroup(cmd)
	if err := cmd.Start(); err != nil {
		t.Fatal(err)
	}
	pid = cmd.Process.Pid
	done := make(chan struct{})
	go func() { _ = cmd.Wait(); close(done) }()
	t.Cleanup(func() {
		proc.KillGroupNow(pid)
		<-done
	})
	return pid, func(d time.Duration) bool {
		select {
		case <-done:
			return true
		case <-time.After(d):
			return false
		}
	}
}

// reapedPID returns a pid that is certainly not running: a child is started, waited
// on, and reaped, so nothing can be listening on that pid for the test's duration
// (pid reuse aside, which the OS will not do this quickly).
func reapedPID(t *testing.T) int {
	t.Helper()
	cmd := exec.Command("true")
	if err := cmd.Run(); err != nil {
		t.Fatal(err)
	}
	return cmd.ProcessState.Pid()
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

// Even --all must not delete a job whose worker is still running. Such a job is
// not finished, whatever its record says — a status can reconcile to "failed"
// while a merely-suspended worker is still very much alive — and removing the
// directory would strand it writing into deleted files with no status, no log,
// and no cancel marker.
func TestPruneAllStillProtectsALiveWorker(t *testing.T) {
	t.Setenv("CODEXMON_HOME", t.TempDir())
	live := NewID()
	liveDir, _ := Dir(live)
	now := time.Now()
	_ = WriteStatus(liveDir, &Status{
		ID: live, State: StateCompleted, Health: HealthDone,
		WorkerPID: os.Getpid(), // alive
		StartedAt: now, UpdatedAt: now, EndedAt: &now,
	})
	// A genuinely finished job alongside it, to prove --all still clears history.
	done := mkJob(t, StateCompleted, now)

	removed, err := Prune(PruneOptions{All: true})
	if err != nil {
		t.Fatal(err)
	}
	if removed != 1 {
		t.Errorf("--all removed %d jobs, want 1 (only the finished one)", removed)
	}
	if _, err := ReadStatus(liveDir); err != nil {
		t.Error("the job with a live worker should have survived --all")
	}
	if _, err := ReadStatusByID(done); err == nil {
		t.Error("the finished job should have been removed by --all")
	}
}

// Routine retention, by contrast, leaves a job alone while its worker is still
// running — deleting the directory would pull the files out from under it.
func TestPruneSkipsJobWithLiveWorker(t *testing.T) {
	t.Setenv("CODEXMON_HOME", t.TempDir())
	id := NewID()
	dir, _ := Dir(id)
	now := time.Now()
	_ = WriteStatus(dir, &Status{
		ID: id, State: StateCompleted, Health: HealthDone,
		WorkerPID: os.Getpid(),
		StartedAt: now, UpdatedAt: now, EndedAt: &now,
	})
	removed, err := Prune(PruneOptions{MaxCount: 0, MaxAge: time.Nanosecond})
	if err != nil {
		t.Fatal(err)
	}
	if removed != 0 {
		t.Errorf("retention removed %d jobs, want 0 while the worker is alive", removed)
	}
}
