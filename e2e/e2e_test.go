// Package e2e builds the real codexmon binary and drives it end-to-end against
// a fake `codex` so the full CLI — including detached background workers — is
// exercised without network or auth.
package e2e

import (
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"syscall"
	"testing"
	"time"
)

var (
	binPath    string
	fakePath   string
	claudePath string
	cursorPath string
)

const fakeCodex = `#!/bin/sh
while [ "$1" = "--model" ] || [ "$1" = "-m" ] || [ "$1" = "--config" ] || [ "$1" = "-c" ]; do
  shift 2
done
case "$1" in
  --version) echo "fake-codex 9.9.9"; exit 0 ;;
  doctor)    echo '{"ok":true}'; exit 0 ;;
  exec)
    out=""; prev=""
    for a in "$@"; do
      if [ "$prev" = "-o" ] || [ "$prev" = "--output-last-message" ]; then out="$a"; fi
      prev="$a"
    done
    case "$FAKE_MODE" in
      stall)
        echo '{"type":"thread.started","thread_id":"t-fake"}'
        sleep 30 ;;
      quick)
        echo '{"type":"thread.started","thread_id":"t-fake"}'
        echo '{"type":"turn.started"}'
        sleep 0.3
        echo '{"type":"item.completed","item":{"id":"i0","type":"agent_message","text":"FAKE_RESULT_OK"}}'
        echo '{"type":"turn.completed","usage":{"input_tokens":5,"cached_input_tokens":0,"output_tokens":2,"reasoning_output_tokens":1}}'
        [ -n "$out" ] && printf 'FAKE_RESULT_OK' > "$out"
        exit 0 ;;
      bigresult)
        echo '{"type":"thread.started","thread_id":"t-fake"}'
        echo '{"type":"turn.started"}'
        msg=""; i=0
        while [ $i -lt 100 ]; do msg="${msg}0123456789"; i=$((i+1)); done
        echo '{"type":"turn.completed","usage":{"input_tokens":5,"cached_input_tokens":0,"output_tokens":2,"reasoning_output_tokens":1}}'
        [ -n "$out" ] && printf '%s' "$msg" > "$out"
        exit 0 ;;
      *)
        echo '{"type":"thread.started","thread_id":"t-fake"}'
        echo '{"type":"turn.started"}'
        echo '{"type":"item.completed","item":{"id":"i0","type":"agent_message","text":"FAKE_RESULT_OK"}}'
        echo '{"type":"turn.completed","usage":{"input_tokens":5,"cached_input_tokens":0,"output_tokens":2,"reasoning_output_tokens":1}}'
        [ -n "$out" ] && printf 'FAKE_RESULT_OK' > "$out"
        exit 0 ;;
    esac ;;
  *) echo "fake: unknown $*" >&2; exit 3 ;;
esac
`

// fakeClaude emits a Claude Code stream-json transcript so the multi-agent
// review path can be exercised without a real Claude install.
const fakeClaude = `#!/bin/sh
case "$1" in
  --version) echo "fake-claude 9.9.9"; exit 0 ;;
  doctor)    echo "claude: all good"; exit 0 ;;
  *)
    echo '{"type":"system","subtype":"init","session_id":"sess-1","model":"fake"}'
    echo '{"type":"assistant","message":{"role":"assistant","content":[{"type":"tool_use","id":"t1","name":"Bash","input":{"command":"git diff"}}]}}'
    echo '{"type":"user","message":{"role":"user","content":[{"type":"tool_result","tool_use_id":"t1","content":"diff"}]}}'
    echo '{"type":"assistant","message":{"role":"assistant","content":[{"type":"text","text":"CLAUDE_REVIEW_OK"}]}}'
    echo '{"type":"result","subtype":"success","is_error":false,"result":"CLAUDE_REVIEW_OK","session_id":"sess-1","usage":{"input_tokens":10,"output_tokens":3}}'
    exit 0 ;;
esac
`

// fakeCursor emits a Cursor agent stream-json transcript.
const fakeCursor = `#!/bin/sh
case "$1" in
  --version) echo "fake-cursor 2026.0.0"; exit 0 ;;
  status)    echo "Logged in as test@example.com"; exit 0 ;;
  *)
    echo '{"type":"system","subtype":"init","session_id":"c-1","cwd":"/","model":"fake","permissionMode":"default"}'
    echo '{"type":"user","message":{"role":"user","content":[{"type":"text","text":"review"}]}}'
    echo '{"type":"tool_call","subtype":"started","call_id":"k1","tool_call":{"readToolCall":{"args":{"path":"a"}}}}'
    echo '{"type":"tool_call","subtype":"completed","call_id":"k1","tool_call":{"readToolCall":{"result":{"success":{}}}}}'
    echo '{"type":"assistant","message":{"role":"assistant","content":[{"type":"text","text":"CURSOR_REVIEW_OK"}]}}'
    echo '{"type":"result","subtype":"success","is_error":false,"result":"CURSOR_REVIEW_OK","session_id":"c-1","duration_ms":5}'
    exit 0 ;;
esac
`

func TestMain(m *testing.M) {
	if runtime.GOOS == "windows" {
		os.Exit(0) // e2e relies on a /bin/sh fake codex
	}
	tmp, err := os.MkdirTemp("", "codexmon-e2e-*")
	if err != nil {
		panic(err)
	}
	defer os.RemoveAll(tmp)

	binPath = filepath.Join(tmp, "codexmon")
	wd, _ := os.Getwd()
	repoRoot := filepath.Dir(wd) // e2e/ -> module root
	build := exec.Command("go", "build", "-o", binPath, "./cmd/codexmon")
	build.Dir = repoRoot
	build.Stderr = os.Stderr
	if err := build.Run(); err != nil {
		panic("build codexmon: " + err.Error())
	}

	fakePath = filepath.Join(tmp, "fakecodex")
	if err := os.WriteFile(fakePath, []byte(fakeCodex), 0o755); err != nil {
		panic(err)
	}
	claudePath = filepath.Join(tmp, "fakeclaude")
	if err := os.WriteFile(claudePath, []byte(fakeClaude), 0o755); err != nil {
		panic(err)
	}
	cursorPath = filepath.Join(tmp, "fakecursor")
	if err := os.WriteFile(cursorPath, []byte(fakeCursor), 0o755); err != nil {
		panic(err)
	}

	os.Exit(m.Run())
}

type result struct {
	stdout string
	stderr string
	code   int
}

func runCodexmon(t *testing.T, home string, extraEnv []string, args ...string) result {
	t.Helper()
	cmd := exec.Command(binPath, args...)
	cmd.Env = append(os.Environ(),
		"CODEXMON_HOME="+home,
		"CODEXMON_CODEX="+fakePath,
		"CODEXMON_CLAUDE="+claudePath,
		"CODEXMON_CURSOR="+cursorPath,
	)
	cmd.Env = append(cmd.Env, extraEnv...)
	var out, errBuf strings.Builder
	cmd.Stdout = &out
	cmd.Stderr = &errBuf
	err := cmd.Run()
	code := 0
	if err != nil {
		if ee, ok := err.(*exec.ExitError); ok {
			code = ee.ExitCode()
		} else {
			t.Fatalf("run %v: %v", args, err)
		}
	}
	return result{out.String(), errBuf.String(), code}
}

func TestE2EForegroundCompleted(t *testing.T) {
	home := t.TempDir()
	r := runCodexmon(t, home, nil, "exec", "--skip-git-repo-check", "say hi")
	if r.code != 0 {
		t.Fatalf("exit = %d, stderr=%s", r.code, r.stderr)
	}
	if !strings.Contains(r.stdout, "FAKE_RESULT_OK") {
		t.Errorf("stdout missing result:\n%s", r.stdout)
	}
	if !strings.Contains(r.stdout, "completed") {
		t.Errorf("stdout missing 'completed':\n%s", r.stdout)
	}
}

func TestE2EBackgroundLifecycle(t *testing.T) {
	home := t.TempDir()
	start := runCodexmon(t, home, nil, "start", "--", "exec", "say hi")
	if start.code != 0 {
		t.Fatalf("start exit = %d, stderr=%s", start.code, start.stderr)
	}
	id := firstToken(start.stdout)
	if !strings.HasPrefix(id, "cdx-") {
		t.Fatalf("did not get a job id from start: %q", start.stdout)
	}

	// Block until it finishes and get final JSON.
	wait := runCodexmon(t, home, nil, "wait", id, "--timeout", "20", "--json")
	if wait.code != 0 {
		t.Fatalf("wait exit = %d, stderr=%s, out=%s", wait.code, wait.stderr, wait.stdout)
	}
	var st struct {
		ID            string `json:"id"`
		State         string `json:"state"`
		ResultPreview string `json:"result_preview"`
	}
	if err := json.Unmarshal([]byte(wait.stdout), &st); err != nil {
		t.Fatalf("wait json: %v\n%s", err, wait.stdout)
	}
	if st.State != "completed" {
		t.Errorf("state = %s, want completed", st.State)
	}
	if !strings.Contains(st.ResultPreview, "FAKE_RESULT_OK") {
		t.Errorf("result = %q", st.ResultPreview)
	}

	// list should include the job.
	list := runCodexmon(t, home, nil, "list")
	if !strings.Contains(list.stdout, id) {
		t.Errorf("list missing job %s:\n%s", id, list.stdout)
	}
	// tail should show the log.
	tail := runCodexmon(t, home, nil, "tail", id)
	if !strings.Contains(tail.stdout, "thread started") && !strings.Contains(tail.stdout, "started codex") {
		t.Errorf("tail missing log lines:\n%s", tail.stdout)
	}
}

func TestE2EStall(t *testing.T) {
	home := t.TempDir()
	r := runCodexmon(t, home, []string{"FAKE_MODE=stall"},
		"run", "--idle-timeout", "1", "--wall-timeout", "0", "--heartbeat", "0", "--", "exec", "go forever")
	if r.code != 124 {
		t.Fatalf("stall exit = %d, want 124\nstderr=%s", r.code, r.stderr)
	}
	status := runCodexmon(t, home, nil, "status", "--json")
	if !strings.Contains(status.stdout, `"state": "stalled"`) {
		t.Errorf("status not stalled:\n%s", status.stdout)
	}
}

func TestE2ECancel(t *testing.T) {
	home := t.TempDir()
	start := runCodexmon(t, home, []string{"FAKE_MODE=stall"}, "start", "--wall-timeout", "0", "--idle-timeout", "0", "--", "exec", "go forever")
	id := firstToken(start.stdout)
	if !strings.HasPrefix(id, "cdx-") {
		t.Fatalf("no job id: %q", start.stdout)
	}
	// Let the worker actually start codex.
	time.Sleep(800 * time.Millisecond)

	cancel := runCodexmon(t, home, nil, "cancel", id)
	if cancel.code != 0 {
		t.Fatalf("cancel exit = %d, stderr=%s", cancel.code, cancel.stderr)
	}
	// Poll until terminal.
	deadline := time.Now().Add(8 * time.Second)
	for time.Now().Before(deadline) {
		s := runCodexmon(t, home, nil, "status", id, "--json")
		if strings.Contains(s.stdout, `"state": "cancelled"`) {
			return
		}
		time.Sleep(200 * time.Millisecond)
	}
	final := runCodexmon(t, home, nil, "status", id, "--json")
	t.Fatalf("job %s never reached cancelled:\n%s", id, final.stdout)
}

func TestE2EDeadWorkerReconciled(t *testing.T) {
	home := t.TempDir()
	start := runCodexmon(t, home, []string{"FAKE_MODE=stall"}, "start", "--wall-timeout", "0", "--idle-timeout", "0", "--", "exec", "go forever")
	id := firstToken(start.stdout)
	if !strings.HasPrefix(id, "cdx-") {
		t.Fatalf("no job id: %q", start.stdout)
	}

	// Wait for the worker to actually be running codex.
	var worker, codex int
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		s := runCodexmon(t, home, nil, "status", id, "--json")
		var st struct {
			State     string `json:"state"`
			WorkerPID int    `json:"worker_pid"`
			AgentPID  int    `json:"agent_pid"`
		}
		if json.Unmarshal([]byte(s.stdout), &st) == nil && st.State == "running" && st.AgentPID > 0 {
			worker, codex = st.WorkerPID, st.AgentPID
			break
		}
		time.Sleep(150 * time.Millisecond)
	}
	if worker == 0 {
		t.Fatal("job never reached running with a codex pid")
	}

	// Simulate the worker dying without finalizing (e.g. OOM/crash).
	_ = syscall.Kill(worker, syscall.SIGKILL)
	if codex > 0 {
		_ = syscall.Kill(-codex, syscall.SIGKILL) // clean up the orphaned sleep
	}

	// status must reconcile the stale 'running' to a terminal failed state.
	deadline = time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		s := runCodexmon(t, home, nil, "status", id, "--json")
		if strings.Contains(s.stdout, `"state": "failed"`) {
			if !strings.Contains(s.stdout, "no longer running") {
				t.Errorf("reconciled status should explain the worker death:\n%s", s.stdout)
			}
			return
		}
		time.Sleep(150 * time.Millisecond)
	}
	final := runCodexmon(t, home, nil, "status", id, "--json")
	t.Fatalf("dead-worker job was not reconciled:\n%s", final.stdout)
}

func TestE2EDoctor(t *testing.T) {
	home := t.TempDir()
	r := runCodexmon(t, home, nil, "doctor", "--json")
	if r.code != 0 {
		t.Fatalf("doctor exit = %d, stderr=%s, out=%s", r.code, r.stderr, r.stdout)
	}
	var rep struct {
		Ready   bool   `json:"ready"`
		Version string `json:"version"`
	}
	if err := json.Unmarshal([]byte(r.stdout), &rep); err != nil {
		t.Fatalf("doctor json: %v\n%s", err, r.stdout)
	}
	if !rep.Ready {
		t.Errorf("doctor should be ready with fake codex:\n%s", r.stdout)
	}
	if !strings.Contains(rep.Version, "fake-codex") {
		t.Errorf("version = %q", rep.Version)
	}
}

func TestE2EReviewMultiAgent(t *testing.T) {
	cases := []struct {
		agent  string
		want   string
		stream string // a phrase that must appear in the streamed log
	}{
		{"claude", "CLAUDE_REVIEW_OK", "session started"},
		{"cursor", "CURSOR_REVIEW_OK", "session started"},
	}
	for _, c := range cases {
		t.Run(c.agent, func(t *testing.T) {
			home := t.TempDir()
			// Background review so we exercise the detached worker path too.
			start := runCodexmon(t, home, nil, "review", "--agent", c.agent, "--uncommitted", "-b")
			if start.code != 0 {
				t.Fatalf("review start exit = %d, stderr=%s", start.code, start.stderr)
			}
			id := firstToken(start.stdout)
			if !strings.HasPrefix(id, "cdx-") {
				t.Fatalf("no job id from review: %q", start.stdout)
			}

			wait := runCodexmon(t, home, nil, "wait", id, "--timeout", "20", "--json")
			if wait.code != 0 {
				t.Fatalf("wait exit = %d, stderr=%s out=%s", wait.code, wait.stderr, wait.stdout)
			}
			var st struct {
				State         string `json:"state"`
				Agent         string `json:"agent"`
				JSONMode      bool   `json:"json_mode"`
				ResultPreview string `json:"result_preview"`
			}
			if err := json.Unmarshal([]byte(wait.stdout), &st); err != nil {
				t.Fatalf("wait json: %v\n%s", err, wait.stdout)
			}
			if st.State != "completed" {
				t.Errorf("state = %s, want completed\n%s", st.State, wait.stdout)
			}
			if st.Agent != c.agent {
				t.Errorf("agent = %q, want %q", st.Agent, c.agent)
			}
			if !st.JSONMode {
				t.Errorf("review should be json-monitored for %s", c.agent)
			}
			if !strings.Contains(st.ResultPreview, c.want) {
				t.Errorf("result = %q, want %q", st.ResultPreview, c.want)
			}

			tail := runCodexmon(t, home, nil, "tail", id)
			if !strings.Contains(tail.stdout, c.stream) {
				t.Errorf("log should show streamed events:\n%s", tail.stdout)
			}
		})
	}
}

func TestE2EDoctorMultiAgent(t *testing.T) {
	for _, agent := range []string{"claude", "cursor"} {
		t.Run(agent, func(t *testing.T) {
			home := t.TempDir()
			r := runCodexmon(t, home, nil, "doctor", "--agent", agent, "--json")
			if r.code != 0 {
				t.Fatalf("doctor exit = %d, stderr=%s out=%s", r.code, r.stderr, r.stdout)
			}
			var rep struct {
				Ready bool   `json:"ready"`
				Agent string `json:"agent"`
			}
			if err := json.Unmarshal([]byte(r.stdout), &rep); err != nil {
				t.Fatalf("doctor json: %v\n%s", err, r.stdout)
			}
			if !rep.Ready || rep.Agent != agent {
				t.Errorf("doctor %s = %+v\n%s", agent, rep, r.stdout)
			}
		})
	}
}

func TestE2EVersionAndHelp(t *testing.T) {
	home := t.TempDir()
	v := runCodexmon(t, home, nil, "version")
	if v.code != 0 || !strings.Contains(v.stdout, "codexmon") {
		t.Errorf("version: code=%d out=%s", v.code, v.stdout)
	}
	h := runCodexmon(t, home, nil, "help")
	if h.code != 0 || !strings.Contains(h.stdout, "USAGE") {
		t.Errorf("help: code=%d", h.code)
	}
}

// TestE2EWaitEmbedsFullResult proves wait --json carries the complete final
// output as `result`, beyond the 600-char result_preview truncation.
func TestE2EWaitEmbedsFullResult(t *testing.T) {
	home := t.TempDir()
	start := runCodexmon(t, home, []string{"FAKE_MODE=bigresult"}, "start", "--", "exec", "big answer")
	id := firstToken(start.stdout)
	if !strings.HasPrefix(id, "cdx-") {
		t.Fatalf("no job id: %q", start.stdout)
	}
	wait := runCodexmon(t, home, nil, "wait", id, "--timeout", "20", "--json")
	if wait.code != 0 {
		t.Fatalf("wait exit = %d, stderr=%s out=%s", wait.code, wait.stderr, wait.stdout)
	}
	var st struct {
		State         string `json:"state"`
		Result        string `json:"result"`
		ResultPreview string `json:"result_preview"`
	}
	if err := json.Unmarshal([]byte(wait.stdout), &st); err != nil {
		t.Fatalf("wait json: %v\n%s", err, wait.stdout)
	}
	if st.State != "completed" {
		t.Fatalf("state = %s, want completed\n%s", st.State, wait.stdout)
	}
	if len(st.Result) != 1000 || !strings.HasPrefix(st.Result, "0123456789") {
		t.Errorf("result should be the full 1000-char output, got %d chars", len(st.Result))
	}
	if len(st.ResultPreview) >= len(st.Result) {
		t.Errorf("preview (%d chars) should stay truncated below the full result", len(st.ResultPreview))
	}

	// status --json stays lightweight: preview only, no full result.
	status := runCodexmon(t, home, nil, "status", id, "--json")
	var lite struct {
		Result string `json:"result"`
	}
	if err := json.Unmarshal([]byte(status.stdout), &lite); err != nil {
		t.Fatalf("status json: %v", err)
	}
	if lite.Result != "" {
		t.Error("status --json must not embed the full result (it is the 1Hz poll)")
	}
}

// TestE2EWaitReturnsPromptly locks in the adaptive poll: a short job must be
// detected well before the old fixed 2s first sleep would even have woken.
func TestE2EWaitReturnsPromptly(t *testing.T) {
	home := t.TempDir()
	start := runCodexmon(t, home, []string{"FAKE_MODE=quick"}, "start", "--", "exec", "quick answer")
	id := firstToken(start.stdout)
	if !strings.HasPrefix(id, "cdx-") {
		t.Fatalf("no job id: %q", start.stdout)
	}
	began := time.Now()
	wait := runCodexmon(t, home, nil, "wait", id, "--timeout", "20", "--json")
	elapsed := time.Since(began)
	if wait.code != 0 {
		t.Fatalf("wait exit = %d, stderr=%s out=%s", wait.code, wait.stderr, wait.stdout)
	}
	if !strings.Contains(wait.stdout, `"state": "completed"`) {
		t.Fatalf("job did not complete:\n%s", wait.stdout)
	}
	// The fake finishes ~0.3s in; adaptive polling checks at ~0.15/0.4/0.7/1.2s,
	// so even a slow CI box detects it far inside 1.9s. The old fixed interval
	// could not return before 2s once the job was still running at wait start.
	if elapsed >= 1900*time.Millisecond {
		t.Errorf("wait took %v; adaptive polling should detect a ~0.3s job well under 1.9s", elapsed)
	}
}

// TestE2EWaitTimeoutPrecise locks in deadline clamping: --timeout must be
// honored on time instead of overshooting by up to one full poll interval.
func TestE2EWaitTimeoutPrecise(t *testing.T) {
	home := t.TempDir()
	start := runCodexmon(t, home, []string{"FAKE_MODE=stall"}, "start", "--wall-timeout", "0", "--idle-timeout", "0", "--", "exec", "go forever")
	id := firstToken(start.stdout)
	if !strings.HasPrefix(id, "cdx-") {
		t.Fatalf("no job id: %q", start.stdout)
	}
	began := time.Now()
	wait := runCodexmon(t, home, nil, "wait", id, "--timeout", "1", "--json")
	elapsed := time.Since(began)
	if wait.code != 75 {
		t.Fatalf("wait exit = %d, want 75 (own timeout)\nstderr=%s out=%s", wait.code, wait.stderr, wait.stdout)
	}
	if elapsed < 900*time.Millisecond || elapsed >= 1900*time.Millisecond {
		t.Errorf("wait --timeout 1 returned after %v; want ~1s (was up to ~3s before deadline clamping)", elapsed)
	}
	// Cleanup: stop the stalled job so nothing outlives the test.
	runCodexmon(t, home, nil, "cancel", id)
}

// TestE2EClean drives the retention command end-to-end: finished jobs are
// removed, active ones survive.
func TestE2EClean(t *testing.T) {
	home := t.TempDir()
	// One finished job.
	done := runCodexmon(t, home, nil, "start", "--", "exec", "say hi")
	doneID := firstToken(done.stdout)
	w := runCodexmon(t, home, nil, "wait", doneID, "--timeout", "20", "--json")
	if w.code != 0 {
		t.Fatalf("setup job failed: %s", w.stdout)
	}
	// One still-running job.
	active := runCodexmon(t, home, []string{"FAKE_MODE=stall"}, "start", "--wall-timeout", "0", "--idle-timeout", "0", "--", "exec", "go forever")
	activeID := firstToken(active.stdout)

	if r := runCodexmon(t, home, nil, "clean", "--bogus"); r.code == 0 {
		t.Error("clean with an unknown flag should fail")
	}
	clean := runCodexmon(t, home, nil, "clean", "--all")
	if clean.code != 0 {
		t.Fatalf("clean exit = %d, stderr=%s", clean.code, clean.stderr)
	}
	if !strings.Contains(clean.stdout, "removed 1 finished job(s)") {
		t.Errorf("clean output = %q, want removed 1", clean.stdout)
	}
	list := runCodexmon(t, home, nil, "list")
	if strings.Contains(list.stdout, doneID) {
		t.Errorf("finished job %s should be gone after clean --all:\n%s", doneID, list.stdout)
	}
	if !strings.Contains(list.stdout, activeID) {
		t.Errorf("active job %s must survive clean --all:\n%s", activeID, list.stdout)
	}
	runCodexmon(t, home, nil, "cancel", activeID)
}

// TestE2EAutoPrune proves an old finished job is reaped by the next launch —
// and left alone when retention is disabled via env.
func TestE2EAutoPrune(t *testing.T) {
	seedOldJob := func(t *testing.T, home string) string {
		oldID := "cdx-20200101-000000-abcdef"
		dir := filepath.Join(home, "jobs", oldID)
		if err := os.MkdirAll(dir, 0o700); err != nil {
			t.Fatal(err)
		}
		status := `{"id":"` + oldID + `","state":"completed","health":"done","phase":"completed",` +
			`"started_at":"2020-01-01T00:00:00Z","updated_at":"2020-01-01T00:01:00Z",` +
			`"ended_at":"2020-01-01T00:01:00Z","thresholds":{}}`
		if err := os.WriteFile(filepath.Join(dir, "status.json"), []byte(status), 0o600); err != nil {
			t.Fatal(err)
		}
		return dir
	}

	t.Run("default retention prunes", func(t *testing.T) {
		home := t.TempDir()
		oldDir := seedOldJob(t, home)
		r := runCodexmon(t, home, nil, "run", "--", "exec", "say hi")
		if r.code != 0 {
			t.Fatalf("run exit = %d, stderr=%s", r.code, r.stderr)
		}
		if _, err := os.Stat(oldDir); !os.IsNotExist(err) {
			t.Error("a years-old finished job should be auto-pruned on launch")
		}
	})

	t.Run("env can disable retention", func(t *testing.T) {
		home := t.TempDir()
		oldDir := seedOldJob(t, home)
		r := runCodexmon(t, home, []string{"CODEXMON_KEEP_DAYS=0", "CODEXMON_KEEP_JOBS=0"}, "run", "--", "exec", "say hi")
		if r.code != 0 {
			t.Fatalf("run exit = %d, stderr=%s", r.code, r.stderr)
		}
		if _, err := os.Stat(oldDir); err != nil {
			t.Error("with retention disabled the old job must survive")
		}
	})
}

func firstToken(s string) string {
	s = strings.TrimSpace(s)
	if i := strings.IndexAny(s, " \t\n"); i >= 0 {
		return s[:i]
	}
	return s
}

// Ctrl+C on a foreground run must stop the agent, not orphan it. The agent runs
// in its own process group, so the interrupt reaches codexmon alone; without a
// handler codexmon would die instantly and leave the agent running unwatched and
// unattributed, still spending tokens. This drives the real binary end to end:
// SIGINT goes to codexmon, and both the recorded job and the agent process must
// come to rest.
func TestE2EInterruptStopsAgent(t *testing.T) {
	home := t.TempDir()
	cmd := exec.Command(binPath, "run", "--wall-timeout", "0", "--idle-timeout", "0", "--", "exec", "go forever")
	cmd.Env = append(os.Environ(),
		"CODEXMON_HOME="+home,
		"CODEXMON_CODEX="+fakePath,
		"FAKE_MODE=stall",
	)
	var out, errBuf strings.Builder
	cmd.Stdout = &out
	cmd.Stderr = &errBuf
	if err := cmd.Start(); err != nil {
		t.Fatal(err)
	}
	done := make(chan error, 1)
	go func() { done <- cmd.Wait() }()

	// Interrupt only once the agent is genuinely up and recorded.
	id, agentPID := waitForRunningAgent(t, home)
	if err := cmd.Process.Signal(os.Interrupt); err != nil {
		t.Fatal(err)
	}

	select {
	case <-done:
	case <-time.After(15 * time.Second):
		_ = cmd.Process.Kill()
		t.Fatalf("codexmon ignored SIGINT; stderr=%s", errBuf.String())
	}

	// The agent must not outlive the supervisor that was watching it.
	deadline := time.Now().Add(5 * time.Second)
	for syscall.Kill(agentPID, 0) == nil && time.Now().Before(deadline) {
		time.Sleep(50 * time.Millisecond)
	}
	if syscall.Kill(agentPID, 0) == nil {
		_ = syscall.Kill(-agentPID, syscall.SIGKILL)
		t.Errorf("agent pid %d survived the interrupt", agentPID)
	}

	// And the job must be recorded as cancelled, not left claiming to run.
	s := runCodexmon(t, home, nil, "status", id, "--json")
	if !strings.Contains(s.stdout, `"state": "cancelled"`) {
		t.Errorf("interrupted job should be cancelled:\n%s", s.stdout)
	}
}

// waitForRunningAgent blocks until some job in home reports a live agent pid,
// returning the job id and that pid.
func waitForRunningAgent(t *testing.T, home string) (string, int) {
	t.Helper()
	deadline := time.Now().Add(10 * time.Second)
	for time.Now().Before(deadline) {
		s := runCodexmon(t, home, nil, "status", "--json")
		var st struct {
			ID       string `json:"id"`
			State    string `json:"state"`
			AgentPID int    `json:"agent_pid"`
		}
		if json.Unmarshal([]byte(s.stdout), &st) == nil && st.State == "running" && st.AgentPID > 0 {
			return st.ID, st.AgentPID
		}
		time.Sleep(100 * time.Millisecond)
	}
	t.Fatal("no job reached running with an agent pid")
	return "", 0
}
