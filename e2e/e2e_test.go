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
	binPath  string
	fakePath string
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
			CodexPID  int    `json:"codex_pid"`
		}
		if json.Unmarshal([]byte(s.stdout), &st) == nil && st.State == "running" && st.CodexPID > 0 {
			worker, codex = st.WorkerPID, st.CodexPID
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
