package render

import (
	"strings"
	"testing"
	"time"
	"unicode/utf8"

	"github.com/tigercosmos/codexmon/internal/agent"
	"github.com/tigercosmos/codexmon/internal/job"
)

func sampleRunning() *job.Status {
	return &job.Status{
		ID: "cdx-1", State: job.StateRunning, Health: job.HealthHealthy, Phase: "reviewing",
		Title: "codex exec review", Agent: "codex", AgentPID: 123, WorkerPID: 100,
		ElapsedSec: 47, IdleSec: 3, EventCount: 12, LastEvent: "ran: go test (exit 0)",
		ThreadID:   "thr-1",
		Thresholds: job.Thresholds{SlowAfterSec: 30, StalledSec: 180, ToolStuckSec: 120, WallSec: 600},
		LogFile:    "/tmp/x/output.log", StartedAt: time.Now(),
	}
}

func TestStatusShowsToolLimit(t *testing.T) {
	out := Status(sampleRunning())
	if !strings.Contains(out, "tool>2m00s") {
		t.Errorf("status should show the tool limit\n%s", out)
	}
}

func TestStatusRendersKeyFields(t *testing.T) {
	out := Status(sampleRunning())
	for _, want := range []string{"cdx-1", "running", "reviewing", "47s", "idle: 3s", "go test", "thr-1", "codexmon wait"} {
		if !strings.Contains(out, want) {
			t.Errorf("Status output missing %q\n%s", want, out)
		}
	}
}

func TestStatusTerminalShowsResult(t *testing.T) {
	s := sampleRunning()
	s.State = job.StateCompleted
	s.Health = job.HealthDone
	s.ResultPreview = "All good, no issues found."
	out := Status(s)
	if !strings.Contains(out, "result:") || !strings.Contains(out, "All good") {
		t.Errorf("terminal status should show result preview\n%s", out)
	}
	if strings.Contains(out, "codexmon wait") {
		t.Errorf("terminal status should not show wait hint\n%s", out)
	}
}

func TestHealthIcon(t *testing.T) {
	if HealthIcon(job.HealthHealthy) != "✅" {
		t.Error("healthy icon")
	}
	if HealthIcon(job.HealthStalled) != "❌" {
		t.Error("stalled icon")
	}
	if HealthIcon(job.HealthSlow) != "⚠️" {
		t.Error("slow icon")
	}
}

func TestListEmptyAndNonEmpty(t *testing.T) {
	if !strings.Contains(List(nil), "No codexmon jobs") {
		t.Error("empty list message")
	}
	out := List([]*job.Status{sampleRunning()})
	if !strings.Contains(out, "cdx-1") {
		t.Errorf("list should include id\n%s", out)
	}
}

func TestResult(t *testing.T) {
	s := sampleRunning()
	s.State = job.StateCompleted
	s.Usage = &agent.Usage{InputTokens: 100, OutputTokens: 20}
	out := Result(s, "The review found 2 issues.")
	for _, want := range []string{"codex exec review", "completed", "100", "20", "found 2 issues"} {
		if !strings.Contains(out, want) {
			t.Errorf("Result missing %q\n%s", want, out)
		}
	}
}

func TestDurFormatting(t *testing.T) {
	if dur(45) != "45s" {
		t.Errorf("dur(45) = %q", dur(45))
	}
	if dur(125) != "2m05s" {
		t.Errorf("dur(125) = %q", dur(125))
	}
	if durOff(0) != "off" {
		t.Errorf("durOff(0) = %q", durOff(0))
	}
	if durOff(600) != "10m00s" {
		t.Errorf("durOff(600) = %q", durOff(600))
	}
}

func TestDurUnits(t *testing.T) {
	for _, tc := range []struct {
		sec  float64
		want string
	}{
		{0, "0s"},
		{45, "45s"},
		{59.9, "59s"},
		{60, "1m00s"},
		{125, "2m05s"},
		{3599, "59m59s"},
		{3600, "1h00m00s"},
		{7503, "2h05m03s"},
	} {
		if got := dur(tc.sec); got != tc.want {
			t.Errorf("dur(%v) = %q, want %q", tc.sec, got, tc.want)
		}
	}
}

// A failure detail can carry a provider's whole error payload; the human views
// must stay readable while --json keeps the full text.
func TestClampError(t *testing.T) {
	short := "codex exited 1: boom"
	if got := clampError(short); got != short {
		t.Errorf("a short error should pass through unchanged, got %q", got)
	}
	long := strings.Repeat("é", 4096) // multi-byte, to catch a mid-rune cut
	got := clampError(long)
	if len(got) > maxErrorDisplay+64 {
		t.Errorf("clamped error is %d bytes, want ~%d", len(got), maxErrorDisplay)
	}
	if !utf8.ValidString(got) {
		t.Error("clamped error must remain valid UTF-8")
	}
	if !strings.Contains(got, "--json") {
		t.Error("a clamped error should say where the full text is")
	}
	st := &job.Status{ID: "cdx-1", State: job.StateFailed, Health: job.HealthDead, Error: long}
	if out := Status(st); len(out) > maxErrorDisplay+512 {
		t.Errorf("status block grew to %d bytes on a huge error", len(out))
	}
}
