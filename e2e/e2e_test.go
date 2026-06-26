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

func firstToken(s string) string {
	s = strings.TrimSpace(s)
	if i := strings.IndexAny(s, " \t\n"); i >= 0 {
		return s[:i]
	}
	return s
}
