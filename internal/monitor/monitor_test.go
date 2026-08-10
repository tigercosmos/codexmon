package monitor

import (
	"bufio"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"

	"github.com/tigercosmos/codexmon/internal/agent"
	"github.com/tigercosmos/codexmon/internal/job"
	"github.com/tigercosmos/codexmon/internal/proc"
)

// fakeCodex is a stand-in for the codex binary so monitor tests need no network
// or auth. Behavior is driven by FAKE_MODE / FAKE_SLEEP env vars.
const fakeCodex = `#!/bin/sh
while [ "$1" = "--model" ] || [ "$1" = "-m" ] || [ "$1" = "--config" ] || [ "$1" = "-c" ]; do
  shift 2
done
case "$1" in
  --version) echo "fake-codex 9.9.9"; exit 0 ;;
  doctor)    echo '{"ok":true}'; exit 0 ;;
  plain)     echo "hello world"; exit 0 ;;
  exec)
    # locate the --output-last-message file
    out=""; prev=""
    for a in "$@"; do
      if [ "$prev" = "-o" ] || [ "$prev" = "--output-last-message" ]; then out="$a"; fi
      prev="$a"
    done
    case "$FAKE_MODE" in
      stall)
        echo '{"type":"thread.started","thread_id":"t-fake"}'
        sleep 30 ;;
      fail)
        echo "boom: something broke" >&2
        exit 7 ;;
      review)
        echo '{"type":"thread.started","thread_id":"t-fake"}'
        echo '{"type":"turn.started"}'
        echo '{"type":"item.completed","item":{"id":"r0","type":"exited_review_mode","review":"REVIEW: found 1 issue in calc.go"}}'
        echo '{"type":"turn.completed","usage":{"input_tokens":5,"cached_input_tokens":0,"output_tokens":2,"reasoning_output_tokens":1}}'
        exit 0 ;;
      trap0)
        # Exit 0 in response to SIGTERM, like a real codex caught mid-hang.
        trap 'exit 0' TERM
        echo '{"type":"thread.started","thread_id":"t-fake"}'
        sleep 30 ;;
      errorzero)
        echo '{"type":"thread.started","thread_id":"t-fake"}'
        echo '{"type":"error","message":"model error happened"}'
        exit 0 ;;
      slowtool)
        echo '{"type":"thread.started","thread_id":"t-fake"}'
        echo '{"type":"turn.started"}'
        echo '{"type":"item.started","item":{"id":"t0","type":"mcp_tool_call","server":"s","tool":"slow","status":"in_progress"}}'
        sleep "${FAKE_SLEEP:-2}"
        echo '{"type":"item.completed","item":{"id":"t0","type":"mcp_tool_call","server":"s","tool":"slow","status":"completed"}}'
        echo '{"type":"item.completed","item":{"id":"i1","type":"agent_message","text":"FAKE_RESULT_OK"}}'
        echo '{"type":"turn.completed","usage":{"input_tokens":5,"cached_input_tokens":0,"output_tokens":2,"reasoning_output_tokens":1}}'
        [ -n "$out" ] && printf 'FAKE_RESULT_OK' > "$out"
        exit 0 ;;
      hungtool)
        echo '{"type":"thread.started","thread_id":"t-fake"}'
        echo '{"type":"turn.started"}'
        echo '{"type":"item.started","item":{"id":"t0","type":"mcp_tool_call","server":"s","tool":"hung","status":"in_progress"}}'
        sleep 30 ;;
      grandchild)
        echo '{"type":"thread.started","thread_id":"t-fake"}'
        echo '{"type":"item.completed","item":{"id":"i0","type":"agent_message","text":"FAKE_RESULT_OK"}}'
        echo '{"type":"turn.completed","usage":{"input_tokens":5,"cached_input_tokens":0,"output_tokens":2,"reasoning_output_tokens":1}}'
        [ -n "$out" ] && printf 'FAKE_RESULT_OK' > "$out"
        # leave a backgrounded grandchild holding the inherited stdout, then exit
        sleep 30 &
        exit 0 ;;
      *)
        echo '{"type":"thread.started","thread_id":"t-fake"}'
        echo '{"type":"turn.started"}'
        [ -n "$FAKE_SLEEP" ] && sleep "$FAKE_SLEEP"
        echo '{"type":"item.completed","item":{"id":"i0","type":"agent_message","text":"FAKE_RESULT_OK"}}'
        echo '{"type":"turn.completed","usage":{"input_tokens":5,"cached_input_tokens":0,"output_tokens":2,"reasoning_output_tokens":1}}'
        [ -n "$out" ] && printf 'FAKE_RESULT_OK' > "$out"
        exit 0 ;;
    esac ;;
  *) echo "fake: unknown invocation: $*" >&2; exit 3 ;;
esac
`

func writeFakeCodex(t *testing.T) string {
	t.Helper()
	if runtime.GOOS == "windows" {
		t.Skip("fake codex shell script not supported on Windows")
	}
	dir := t.TempDir()
	path := filepath.Join(dir, "fakecodex")
	if err := os.WriteFile(path, []byte(fakeCodex), 0o755); err != nil {
		t.Fatal(err)
	}
	return path
}

func newSpec(t *testing.T, bin string, jsonMode bool, args []string, th job.Thresholds) (*job.Spec, string, string) {
	t.Helper()
	dir := t.TempDir()
	_, _, _, _, resultFile, _ := job.Paths(dir)
	spec := &job.Spec{
		ID:         "test-job",
		Agent:      "codex",
		AgentBin:   bin,
		Args:       args,
		Cwd:        dir,
		JSONMode:   jsonMode,
		Thresholds: th,
		Title:      "fake",
	}
	return spec, dir, resultFile
}

func TestMonitorCompleted(t *testing.T) {
	bin := writeFakeCodex(t)
	th := job.Thresholds{HeartbeatSec: 0, SlowAfterSec: 0, StalledSec: 0, WallSec: 0}
	spec, dir, resultFile := newSpec(t, bin, true,
		[]string{"exec", "--json", "--output-last-message", "", "prompt"}, th)
	// patch the empty -o slot with the real result path
	spec.Args[3] = resultFile

	st, err := Run(dir, spec, Options{})
	if err != nil {
		t.Fatal(err)
	}
	if st.State != job.StateCompleted {
		t.Fatalf("state = %s, want completed (err=%s)", st.State, st.Error)
	}
	if st.Health != job.HealthDone {
		t.Errorf("health = %s, want done", st.Health)
	}
	if st.EventCount < 4 {
		t.Errorf("event count = %d, want >=4", st.EventCount)
	}
	if st.ThreadID != "t-fake" {
		t.Errorf("thread id = %q, want t-fake", st.ThreadID)
	}
	if st.Usage == nil || st.Usage.InputTokens != 5 {
		t.Errorf("usage not captured: %+v", st.Usage)
	}
	if !strings.Contains(st.ResultPreview, "FAKE_RESULT_OK") {
		t.Errorf("result preview = %q", st.ResultPreview)
	}
	data, _ := os.ReadFile(resultFile)
	if !strings.Contains(string(data), "FAKE_RESULT_OK") {
		t.Errorf("result file = %q", string(data))
	}
	// status.json must reflect the terminal state.
	persisted, err := job.ReadStatus(dir)
	if err != nil || persisted.State != job.StateCompleted {
		t.Errorf("persisted status = %v %v", persisted, err)
	}
}

func TestMonitorStallKill(t *testing.T) {
	bin := writeFakeCodex(t)
	t.Setenv("FAKE_MODE", "stall")
	th := job.Thresholds{HeartbeatSec: 0, StalledSec: 0.5, WallSec: 0}
	spec, dir, resultFile := newSpec(t, bin, true, []string{"exec", "--json"}, th)
	_ = resultFile

	start := time.Now()
	st, err := Run(dir, spec, Options{})
	if err != nil {
		t.Fatal(err)
	}
	if st.State != job.StateStalled {
		t.Fatalf("state = %s, want stalled", st.State)
	}
	if time.Since(start) > 5*time.Second {
		t.Errorf("stall kill took too long: %s", time.Since(start))
	}
	if !strings.Contains(st.Error, "stall") {
		t.Errorf("error = %q, want stall mention", st.Error)
	}
}

func TestMonitorWallTimeout(t *testing.T) {
	bin := writeFakeCodex(t)
	t.Setenv("FAKE_SLEEP", "5")
	th := job.Thresholds{HeartbeatSec: 0, StalledSec: 0, WallSec: 1}
	spec, dir, _ := newSpec(t, bin, true, []string{"exec", "--json"}, th)

	st, err := Run(dir, spec, Options{})
	if err != nil {
		t.Fatal(err)
	}
	if st.State != job.StateTimeout {
		t.Fatalf("state = %s, want timeout", st.State)
	}
}

func TestMonitorCancel(t *testing.T) {
	bin := writeFakeCodex(t)
	t.Setenv("FAKE_MODE", "stall")
	th := job.Thresholds{HeartbeatSec: 0, StalledSec: 0, WallSec: 0} // nothing auto-kills
	spec, dir, _ := newSpec(t, bin, true, []string{"exec", "--json"}, th)

	done := make(chan *job.Status, 1)
	go func() {
		st, _ := Run(dir, spec, Options{})
		done <- st
	}()

	time.Sleep(300 * time.Millisecond)
	if err := job.RequestCancel(dir); err != nil {
		t.Fatal(err)
	}

	select {
	case st := <-done:
		if st.State != job.StateCancelled {
			t.Fatalf("state = %s, want cancelled", st.State)
		}
	case <-time.After(6 * time.Second):
		t.Fatal("cancel did not stop the monitor in time")
	}
}

func TestMonitorFailure(t *testing.T) {
	bin := writeFakeCodex(t)
	t.Setenv("FAKE_MODE", "fail")
	th := job.Thresholds{HeartbeatSec: 0, StalledSec: 0, WallSec: 0}
	spec, dir, _ := newSpec(t, bin, true, []string{"exec", "--json"}, th)

	st, err := Run(dir, spec, Options{})
	if err != nil {
		t.Fatal(err)
	}
	if st.State != job.StateFailed {
		t.Fatalf("state = %s, want failed", st.State)
	}
	if st.ExitCode == nil || *st.ExitCode != 7 {
		t.Errorf("exit code = %v, want 7", st.ExitCode)
	}
	if !strings.Contains(st.Error, "boom") {
		t.Errorf("error = %q, want boom", st.Error)
	}
}

func TestMonitorReviewCapture(t *testing.T) {
	// `codex exec review` delivers findings via an exited_review_mode item.
	bin := writeFakeCodex(t)
	t.Setenv("FAKE_MODE", "review")
	th := job.Thresholds{HeartbeatSec: 0, StalledSec: 0, WallSec: 0}
	spec, dir, _ := newSpec(t, bin, true, []string{"exec", "--json", "review"}, th)

	st, err := Run(dir, spec, Options{})
	if err != nil {
		t.Fatal(err)
	}
	if st.State != job.StateCompleted {
		t.Fatalf("state = %s, want completed", st.State)
	}
	if !strings.Contains(st.ResultPreview, "found 1 issue") {
		t.Errorf("review text not captured: %q", st.ResultPreview)
	}
}

func TestMonitorStallKillHonoredEvenOnCleanExit(t *testing.T) {
	// Regression: a stalled codex that exits 0 in response to our SIGTERM must
	// still be reported as stalled — not silently masked as completed.
	bin := writeFakeCodex(t)
	t.Setenv("FAKE_MODE", "trap0")
	th := job.Thresholds{HeartbeatSec: 0, StalledSec: 0.5, WallSec: 0}
	spec, dir, _ := newSpec(t, bin, true, []string{"exec", "--json"}, th)

	st, err := Run(dir, spec, Options{})
	if err != nil {
		t.Fatal(err)
	}
	if st.State != job.StateStalled {
		t.Fatalf("state = %s, want stalled (exit 0 on SIGTERM must not mask the kill)", st.State)
	}
}

func TestMonitorFailureEventWithCleanExit(t *testing.T) {
	// codex can emit an error event yet still exit 0; the run must be failed.
	bin := writeFakeCodex(t)
	t.Setenv("FAKE_MODE", "errorzero")
	th := job.Thresholds{HeartbeatSec: 0, StalledSec: 0, WallSec: 0}
	spec, dir, _ := newSpec(t, bin, true, []string{"exec", "--json"}, th)

	st, err := Run(dir, spec, Options{})
	if err != nil {
		t.Fatal(err)
	}
	if st.State != job.StateFailed {
		t.Fatalf("state = %s, want failed (error event + exit 0)", st.State)
	}
	if !strings.Contains(st.Error, "model error") {
		t.Errorf("error = %q, want model error", st.Error)
	}
}

func TestMonitorGrandchildPipeDoesNotHang(t *testing.T) {
	// A grandchild that inherits the stdout pipe and outlives codex must not
	// block the monitor: process exit is authoritative, and the pipe is
	// force-closed after the drain grace.
	bin := writeFakeCodex(t)
	t.Setenv("FAKE_MODE", "grandchild")
	th := job.Thresholds{HeartbeatSec: 0, StalledSec: 0, WallSec: 0} // no timeouts
	spec, dir, resultFile := newSpec(t, bin, true,
		[]string{"exec", "--json", "--output-last-message", "", "prompt"}, th)
	spec.Args[3] = resultFile

	start := time.Now()
	st, err := Run(dir, spec, Options{})
	if err != nil {
		t.Fatal(err)
	}
	if st.State != job.StateCompleted {
		t.Fatalf("state = %s, want completed", st.State)
	}
	// Should finish within ~drainGrace, not wait for the 30s grandchild.
	if elapsed := time.Since(start); elapsed > drainGrace+5*time.Second {
		t.Errorf("monitor hung on grandchild for %s", elapsed)
	}
	if !strings.Contains(st.ResultPreview, "FAKE_RESULT_OK") {
		t.Errorf("result = %q", st.ResultPreview)
	}
}

func TestMonitorNonJSON(t *testing.T) {
	bin := writeFakeCodex(t)
	th := job.Thresholds{HeartbeatSec: 0, StalledSec: 0, WallSec: 0}
	spec, dir, _ := newSpec(t, bin, false, []string{"plain"}, th)

	st, err := Run(dir, spec, Options{})
	if err != nil {
		t.Fatal(err)
	}
	if st.State != job.StateCompleted {
		t.Fatalf("state = %s, want completed", st.State)
	}
	if !strings.Contains(st.ResultPreview, "hello world") {
		t.Errorf("non-json result = %q", st.ResultPreview)
	}
}

func TestMonitorStdinDoesNotHang(t *testing.T) {
	// A non-forwarded stdin must be /dev/null so codex never blocks reading it.
	bin := writeFakeCodex(t)
	th := job.Thresholds{HeartbeatSec: 0, StalledSec: 2, WallSec: 3}
	spec, dir, resultFile := newSpec(t, bin, true,
		[]string{"exec", "--json", "--output-last-message", "", "prompt"}, th)
	spec.Args[3] = resultFile
	spec.ForwardStdin = false

	st, err := Run(dir, spec, Options{})
	if err != nil {
		t.Fatal(err)
	}
	if st.State != job.StateCompleted {
		t.Fatalf("state = %s, want completed (stdin should be /dev/null)", st.State)
	}
}

// ---- white-box unit tests --------------------------------------------------

func TestClassifyHealth(t *testing.T) {
	th := job.Thresholds{SlowAfterSec: 30, StalledSec: 180, ToolStuckSec: 120}
	mk := func(events int) *job.Status {
		return &job.Status{EventCount: events, JSONMode: true, Thresholds: th}
	}
	if got := classifyHealth(mk(0), liveness{idle: 1}); got != job.HealthStarting {
		t.Errorf("no events, low idle = %s, want starting", got)
	}
	// No events but idle past slow → must escalate, not stay 'starting' forever.
	if got := classifyHealth(mk(0), liveness{idle: 45}); got != job.HealthSlow {
		t.Errorf("no events but idle past slow = %s, want slow", got)
	}
	if got := classifyHealth(mk(3), liveness{idle: 5}); got != job.HealthHealthy {
		t.Errorf("low idle = %s, want healthy", got)
	}
	if got := classifyHealth(mk(3), liveness{idle: 45}); got != job.HealthSlow {
		t.Errorf("medium idle = %s, want slow", got)
	}
	if got := classifyHealth(mk(3), liveness{idle: 200}); got != job.HealthStalled {
		t.Errorf("high idle = %s, want stalled", got)
	}
	// A command in flight is liveness: never 'stalled' even when very idle.
	if got := classifyHealth(mk(3), liveness{idle: 200, cmdInFlight: true}); got != job.HealthHealthy {
		t.Errorf("in-flight command should be healthy, got %s", got)
	}
	// A command batched with a quick tool whose result hasn't returned yet: the
	// command wins, so the tool's age must not make the run look stalled.
	if got := classifyHealth(mk(3), liveness{cmdInFlight: true, toolInFlight: true, oldestTool: 999}); got != job.HealthHealthy {
		t.Errorf("command + batched tool should be healthy, got %s", got)
	}
	// A tool call is judged against the tool timeout, not idle.
	if got := classifyHealth(mk(3), liveness{idle: 200, toolInFlight: true, oldestTool: 10}); got != job.HealthHealthy {
		t.Errorf("fresh tool call should be healthy, got %s", got)
	}
	if got := classifyHealth(mk(3), liveness{toolInFlight: true, oldestTool: 70}); got != job.HealthSlow {
		t.Errorf("tool past half the limit should be slow, got %s", got)
	}
	if got := classifyHealth(mk(3), liveness{toolInFlight: true, oldestTool: 130}); got != job.HealthStalled {
		t.Errorf("tool past the limit should be stalled, got %s", got)
	}
}

func TestDecideKillCommandHoldsBatchedToolsOpen(t *testing.T) {
	r := &runner{dir: t.TempDir(), spec: &job.Spec{
		Thresholds: job.Thresholds{ToolStuckSec: 1, StalledSec: 0, WallSec: 0},
	}}
	// A long command in flight alongside a batched tool that is past the tool
	// timeout must NOT be killed as a stuck tool — the command holds it open.
	if st, reason := r.decideKill(liveness{cmdInFlight: true, toolInFlight: true, oldestTool: 100}); st != "" {
		t.Errorf("command in flight should suppress the tool-stuck kill, got %s (%q)", st, reason)
	}
	// With no command, the same stuck tool IS caught (codex behavior preserved).
	if st, _ := r.decideKill(liveness{toolInFlight: true, oldestTool: 100}); st != job.StateStalled {
		t.Errorf("a lone stuck tool should stall, got %s", st)
	}
}

func TestRound1(t *testing.T) {
	cases := map[float64]float64{1.24: 1.2, 1.26: 1.3, 0: 0, 9.99: 10}
	for in, want := range cases {
		if got := round1(in); got != want {
			t.Errorf("round1(%v) = %v, want %v", in, got, want)
		}
	}
}

func TestPreview(t *testing.T) {
	if preview("short", 100) != "short" {
		t.Error("short preview changed")
	}
	long := strings.Repeat("x", 50)
	if got := preview(long, 10); len([]rune(got)) != 11 { // 10 + ellipsis rune
		t.Errorf("preview length = %d", len([]rune(got)))
	}
}

func TestIsMeaningfulStderr(t *testing.T) {
	if isMeaningfulStderr("Reading additional input from stdin...") {
		t.Error("stdin notice should be benign")
	}
	if isMeaningfulStderr("   ") {
		t.Error("blank should be benign")
	}
	if !isMeaningfulStderr("error: real problem") {
		t.Error("real error should be meaningful")
	}
}

func TestKillMessage(t *testing.T) {
	for _, s := range []job.State{job.StateCancelled, job.StateStalled, job.StateTimeout} {
		if killMessage(s) == "" {
			t.Errorf("killMessage(%s) empty", s)
		}
	}
}

func TestDefaultThresholds(t *testing.T) {
	th := DefaultThresholds()
	if th.WallSec != 600 || th.StalledSec != 180 || th.SlowAfterSec != 30 || th.HeartbeatSec != 10 || th.ToolStuckSec != 120 {
		t.Errorf("unexpected defaults: %+v", th)
	}
}

func TestMonitorSlowToolNotKilled(t *testing.T) {
	// A tool call that returns before the tool timeout must not be killed, even
	// under an aggressive idle ceiling (the idle clock is irrelevant while a
	// tool is in flight).
	bin := writeFakeCodex(t)
	t.Setenv("FAKE_MODE", "slowtool")
	t.Setenv("FAKE_SLEEP", "2")
	th := job.Thresholds{HeartbeatSec: 0, SlowAfterSec: 0, StalledSec: 1, ToolStuckSec: 10, WallSec: 0}
	spec, dir, resultFile := newSpec(t, bin, true,
		[]string{"exec", "--json", "--output-last-message", "", "prompt"}, th)
	spec.Args[3] = resultFile

	st, err := Run(dir, spec, Options{})
	if err != nil {
		t.Fatal(err)
	}
	if st.State != job.StateCompleted {
		t.Fatalf("slow-but-returning tool should complete, got %s (%s)", st.State, st.Error)
	}
}

func TestMonitorHungToolKilledByToolTimeout(t *testing.T) {
	// With the idle ceiling OFF, only the tool timeout should catch a hung tool.
	bin := writeFakeCodex(t)
	t.Setenv("FAKE_MODE", "hungtool")
	th := job.Thresholds{HeartbeatSec: 0, SlowAfterSec: 0, StalledSec: 0, ToolStuckSec: 1, WallSec: 0}
	spec, dir, _ := newSpec(t, bin, true, []string{"exec", "--json"}, th)

	start := time.Now()
	st, err := Run(dir, spec, Options{})
	if err != nil {
		t.Fatal(err)
	}
	if st.State != job.StateStalled {
		t.Fatalf("hung tool should be stalled, got %s", st.State)
	}
	if !strings.Contains(st.Error, "tool call") {
		t.Errorf("error should name the stuck tool, got %q", st.Error)
	}
	if time.Since(start) > 5*time.Second {
		t.Errorf("tool timeout took too long: %s", time.Since(start))
	}
}

// A Ctrl+C at the terminal reaches codexmon alone — the agent deliberately runs
// in its own process group — so an unhandled interrupt would kill the supervisor
// and leave the agent running unwatched. The run must instead stop the agent and
// finalize as cancelled.
func TestMonitorInterruptCancelsRunAndStopsAgent(t *testing.T) {
	bin := writeFakeCodex(t)
	t.Setenv("FAKE_MODE", "stall") // sleeps 30s; only the signal can end it
	th := job.Thresholds{HeartbeatSec: 0, SlowAfterSec: 0, StalledSec: 0, ToolStuckSec: 0, WallSec: 0}
	spec, dir, _ := newSpec(t, bin, true, []string{"exec", "--json"}, th)

	// A channel stands in for the real subscription: delivering a genuine SIGINT
	// to the test process would kill the test binary if it landed before the
	// handler was installed.
	sigs := make(chan os.Signal, 1)
	done := make(chan *job.Status, 1)
	go func() {
		st, _ := Run(dir, spec, Options{signals: sigs})
		done <- st
	}()

	// Interrupt only once the agent is actually up, so the test exercises a
	// running child rather than a race with startup.
	agentPID := waitForAgentPID(t, dir)
	sigs <- os.Interrupt

	var st *job.Status
	select {
	case st = <-done:
	case <-time.After(15 * time.Second):
		t.Fatal("interrupted run never finished")
	}
	if st.State != job.StateCancelled {
		t.Fatalf("state = %s, want cancelled (error=%q)", st.State, st.Error)
	}
	if !strings.Contains(st.Error, "signal") {
		t.Errorf("error = %q, want it to name the signal", st.Error)
	}
	// The whole point: the agent must not outlive the interrupted supervisor.
	deadline := time.Now().Add(3 * time.Second)
	for proc.Alive(agentPID) && time.Now().Before(deadline) {
		time.Sleep(20 * time.Millisecond)
	}
	if proc.Alive(agentPID) {
		t.Errorf("agent pid %d survived the interrupt", agentPID)
	}
}

// waitForAgentPID blocks until the monitor has recorded the agent's pid.
func waitForAgentPID(t *testing.T, dir string) int {
	t.Helper()
	deadline := time.Now().Add(10 * time.Second)
	for time.Now().Before(deadline) {
		if st, err := job.ReadStatus(dir); err == nil && st.AgentPID > 0 {
			return st.AgentPID
		}
		time.Sleep(20 * time.Millisecond)
	}
	t.Fatal("agent pid never appeared in status.json")
	return 0
}

// In non-JSON mode every stdout line is accumulated in memory as the result. The
// buffer must be capped: uncapped, a chatty agent would OOM the worker, which
// then surfaces as the far more confusing "worker died without a result".
func TestAppendResultIsCapped(t *testing.T) {
	r := &runner{}
	chunk := strings.Repeat("x", 64<<10) + "\n"
	for r.resultBuf.Len() < maxResultBufBytes+len(chunk) {
		before := r.resultBuf.Len()
		r.appendResultLocked(chunk)
		if r.resultBuf.Len() == before && !r.resultCapped {
			t.Fatal("buffer stopped growing without being marked capped")
		}
		if r.resultCapped {
			break
		}
	}
	if !r.resultCapped {
		t.Fatal("buffer never hit the cap")
	}
	if got := r.resultBuf.Len(); got > maxResultBufBytes+256 {
		t.Errorf("buffer grew to %d bytes, want <= cap (%d) plus the notice", got, maxResultBufBytes)
	}
	if !strings.Contains(r.resultBuf.String(), "truncated") {
		t.Error("a capped result should say so, not just stop")
	}
	// Further output is dropped rather than reopening the buffer.
	capped := r.resultBuf.Len()
	r.appendResultLocked(chunk)
	if r.resultBuf.Len() != capped {
		t.Error("writes after the cap should be dropped")
	}
}

// output.log and events.jsonl are bounded on disk; past the cap each stops
// appending and says so, instead of silently going quiet or filling the disk.
func TestLogAndEventWritesAreCapped(t *testing.T) {
	dir := t.TempDir()
	logF, err := os.Create(filepath.Join(dir, "output.log"))
	if err != nil {
		t.Fatal(err)
	}
	defer logF.Close()
	evF, err := os.Create(filepath.Join(dir, "events.jsonl"))
	if err != nil {
		t.Fatal(err)
	}
	defer evF.Close()

	// Start just under the cap so a single write crosses it.
	r := &runner{logF: logF, evF: evF, logBytes: maxLogBytes - 8, evBytes: maxLogBytes - 8}
	r.writeLogRaw(strings.Repeat("y", 64) + "\n")
	r.writeEvents(`{"type":"noise"}` + "\n")
	if !r.logCapped || !r.evCapped {
		t.Fatalf("caps not tripped: log=%v events=%v", r.logCapped, r.evCapped)
	}
	r.writeLogRaw("more\n")
	r.writeEvents(`{"type":"more"}` + "\n")

	logged, _ := os.ReadFile(filepath.Join(dir, "output.log"))
	if !strings.Contains(string(logged), "log truncated") {
		t.Errorf("capped log should carry a truncation notice, got %q", string(logged))
	}
	if strings.Contains(string(logged), "more") {
		t.Error("writes after the log cap should be dropped")
	}
	events, _ := os.ReadFile(filepath.Join(dir, "events.jsonl"))
	if !strings.Contains(string(events), "codexmon.truncated") {
		t.Errorf("capped events should carry a JSONL truncation notice, got %q", string(events))
	}
	if strings.Contains(string(events), `"more"`) {
		t.Error("writes after the events cap should be dropped")
	}
}

// panicProvider registers an agent whose parser panics, so the reader
// goroutine's recover path can be exercised end to end.
type panicProvider struct{ agent.Provider }

func (panicProvider) Name() string            { return "panicagent" }
func (panicProvider) BinEnv() string          { return "CODEXMON_PANICAGENT" }
func (panicProvider) BinCandidates() []string { return []string{"panicagent"} }
func (panicProvider) Analyze(args []string, _ string, allowJSON bool) agent.Analysis {
	return agent.Analysis{JSONMode: allowJSON, Args: args, Title: "panicagent"}
}
func (panicProvider) ReviewArgs(agent.ReviewSpec) ([]string, error) { return nil, nil }
func (panicProvider) ParseLine(string) (agent.Event, bool)          { panic("boom in ParseLine") }
func (panicProvider) Doctor(string, agent.RunFunc) agent.DoctorReport {
	return agent.DoctorReport{}
}

func init() { agent.Register(panicProvider{}) }

// A panic inside a monitor goroutine must not leave the agent orphaned or the
// run reported as clean: it tears the child down and finalizes as failed.
func TestMonitorPanicInParserFailsRunAndKillsAgent(t *testing.T) {
	bin := writeFakeCodex(t)
	t.Setenv("FAKE_MODE", "stall") // emits one line, then sleeps 30s
	th := job.Thresholds{HeartbeatSec: 0, SlowAfterSec: 0, StalledSec: 0, ToolStuckSec: 0, WallSec: 0}
	spec, dir, _ := newSpec(t, bin, true, []string{"exec", "--json"}, th)
	spec.Agent = "panicagent"

	done := make(chan *job.Status, 1)
	go func() {
		st, _ := Run(dir, spec, Options{})
		done <- st
	}()
	select {
	case st := <-done:
		if st.State != job.StateFailed {
			t.Fatalf("state = %s, want failed after a parser panic", st.State)
		}
		if !strings.Contains(st.Error, "panic") {
			t.Errorf("error = %q, want it to mention the panic", st.Error)
		}
	case <-time.After(20 * time.Second):
		t.Fatal("a panicking parser hung the run instead of tearing it down")
	}
}

// bufio's ReadString grows without limit until it finds a newline, so an agent
// emitting a huge newline-free blob could exhaust memory before any downstream
// cap applied. readLine bounds the copy and discards the overflow.
func TestReadLineBoundsAHugeNewlineFreeChunk(t *testing.T) {
	const max = 4096
	// One line far larger than both the cap and the reader's own buffer, then a
	// normal line after it.
	huge := strings.Repeat("z", 5<<20)
	br := bufio.NewReaderSize(strings.NewReader(huge+"\nsecond line\n"), 1024)

	line, truncated, err := readLine(br, max)
	if err != nil {
		t.Fatalf("err = %v, want nil", err)
	}
	if len(line) != max {
		t.Errorf("kept %d bytes, want the cap of %d", len(line), max)
	}
	if !truncated {
		t.Error("readLine must report that it dropped part of the line, or the loss is silent")
	}
	if strings.Trim(line, "z") != "" {
		t.Error("the kept prefix should be the head of the line")
	}
	// The overflow must be discarded, not re-emitted as bogus extra lines.
	next, nextTruncated, err := readLine(br, max)
	if nextTruncated {
		t.Error("a short line should not be reported as truncated")
	}
	if err != nil {
		t.Fatalf("err = %v, want nil", err)
	}
	if next != "second line\n" {
		t.Errorf("next line = %q, want the line that followed the huge one", next)
	}
}

// Ordinary lines must be unaffected, including a final one with no newline.
func TestReadLinePreservesNormalLines(t *testing.T) {
	br := bufio.NewReaderSize(strings.NewReader("alpha\nbeta\nno trailing newline"), 64)
	var got []string
	for {
		line, _, err := readLine(br, maxLineBytes)
		if len(line) > 0 {
			got = append(got, line)
		}
		if err != nil {
			break
		}
	}
	want := []string{"alpha\n", "beta\n", "no trailing newline"}
	if len(got) != len(want) {
		t.Fatalf("got %d lines (%q), want %d", len(got), got, len(want))
	}
	for i := range want {
		if got[i] != want[i] {
			t.Errorf("line %d = %q, want %q", i, got[i], want[i])
		}
	}
}
