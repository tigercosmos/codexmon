// Package render turns job statuses into the compact, human-readable text
// `codexmon status/list/wait` print when not in --json mode.
package render

import (
	"fmt"
	"strings"
	"time"

	"github.com/tigercosmos/codexmon/internal/job"
	"github.com/tigercosmos/codexmon/internal/proc"
)

// HealthIcon maps a health verdict to a glanceable marker.
func HealthIcon(h job.Health) string {
	switch h {
	case job.HealthHealthy, job.HealthDone:
		return "✅"
	case job.HealthSlow, job.HealthStarting:
		return "⚠️"
	case job.HealthStalled, job.HealthDead:
		return "❌"
	default:
		return "•"
	}
}

// Status renders a full single-job status block.
func Status(s *job.Status) string {
	var b strings.Builder
	fmt.Fprintf(&b, "%s  %s  [%s]\n", HealthIcon(s.Health), s.ID, s.Title)
	fmt.Fprintf(&b, "  state:    %s (%s)\n", s.State, s.Health)
	fmt.Fprintf(&b, "  phase:    %s\n", orDash(s.Phase))
	fmt.Fprintf(&b, "  elapsed:  %s   idle: %s\n", dur(s.ElapsedSec), dur(s.IdleSec))
	if s.CodexPID != 0 {
		fmt.Fprintf(&b, "  pid:      codex=%d worker=%d (codex alive: %t)\n", s.CodexPID, s.WorkerPID, codexAlive(s))
	}
	if s.EventCount > 0 {
		fmt.Fprintf(&b, "  events:   %d\n", s.EventCount)
	}
	if s.LastEvent != "" {
		fmt.Fprintf(&b, "  last:     %s\n", s.LastEvent)
	}
	if s.ThreadID != "" {
		fmt.Fprintf(&b, "  thread:   %s\n", s.ThreadID)
	}
	if s.Usage != nil {
		fmt.Fprintf(&b, "  tokens:   %d in / %d out (%d cached, %d reasoning)\n",
			s.Usage.InputTokens, s.Usage.OutputTokens, s.Usage.CachedInputTokens, s.Usage.ReasoningOutputTokens)
	}
	fmt.Fprintf(&b, "  limits:   slow>%s stall>%s wall>%s\n",
		durOff(s.Thresholds.SlowAfterSec), durOff(s.Thresholds.StalledSec), durOff(s.Thresholds.WallSec))
	if s.ExitCode != nil {
		fmt.Fprintf(&b, "  exit:     %d\n", *s.ExitCode)
	}
	if s.Error != "" {
		fmt.Fprintf(&b, "  error:    %s\n", s.Error)
	}
	fmt.Fprintf(&b, "  log:      %s\n", s.LogFile)
	if s.State.Active() {
		fmt.Fprintf(&b, "  hint:     codexmon wait %s   |   codexmon tail %s -f   |   codexmon cancel %s\n", s.ID, s.ID, s.ID)
	} else if s.ResultPreview != "" {
		fmt.Fprintf(&b, "  result:   %s\n", indentPreview(s.ResultPreview))
	}
	return b.String()
}

// codexAlive actually probes the OS for the codex process rather than echoing
// the recorded state, so a status block never claims a dead process is live.
// Terminal jobs always report false (their pid may have been recycled).
func codexAlive(s *job.Status) bool {
	return s.State.Active() && s.CodexPID > 0 && proc.Alive(s.CodexPID)
}

// Line renders a compact one-line summary for list views.
func Line(s *job.Status) string {
	return fmt.Sprintf("%s %-28s %-10s %-11s %-13s elapsed=%-7s idle=%-6s %s",
		HealthIcon(s.Health), s.ID, s.State, s.Health, s.Phase,
		dur(s.ElapsedSec), dur(s.IdleSec), s.Title)
}

// List renders a table of jobs.
func List(jobs []*job.Status) string {
	if len(jobs) == 0 {
		return "No codexmon jobs yet.\n"
	}
	var b strings.Builder
	for _, s := range jobs {
		b.WriteString(Line(s))
		b.WriteByte('\n')
	}
	return b.String()
}

// Result renders the final, human-facing outcome of a finished job.
func Result(s *job.Status, fullResult string) string {
	var b strings.Builder
	fmt.Fprintf(&b, "%s %s — %s in %s", HealthIcon(s.Health), s.Title, s.State, dur(s.ElapsedSec))
	if s.Usage != nil {
		fmt.Fprintf(&b, " (%d/%d tokens)", s.Usage.InputTokens, s.Usage.OutputTokens)
	}
	b.WriteByte('\n')
	if s.Error != "" {
		fmt.Fprintf(&b, "error: %s\n", s.Error)
	}
	if strings.TrimSpace(fullResult) != "" {
		b.WriteString("\n")
		b.WriteString(strings.TrimRight(fullResult, "\n"))
		b.WriteByte('\n')
	}
	return b.String()
}

func orDash(s string) string {
	if s == "" {
		return "-"
	}
	return s
}

func dur(sec float64) string {
	d := time.Duration(sec * float64(time.Second))
	if d < time.Minute {
		return fmt.Sprintf("%ds", int(d.Seconds()))
	}
	m := int(d.Minutes())
	s := int(d.Seconds()) % 60
	return fmt.Sprintf("%dm%02ds", m, s)
}

func durOff(sec float64) string {
	if sec <= 0 {
		return "off"
	}
	return dur(sec)
}

func indentPreview(s string) string {
	return strings.ReplaceAll(strings.TrimSpace(s), "\n", "\n            ")
}
