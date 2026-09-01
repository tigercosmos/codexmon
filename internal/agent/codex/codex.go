package codex

import (
	"errors"
	"strings"
	"time"

	"github.com/tigercosmos/codexmon/internal/agent"
)

// Provider drives the OpenAI Codex CLI.
type Provider struct{}

func init() { agent.Register(Provider{}) }

var _ agent.EffortProvider = Provider{}

func (Provider) Name() string            { return "codex" }
func (Provider) BinEnv() string          { return "CODEXMON_CODEX" }
func (Provider) BinCandidates() []string { return []string{"codex"} }

// Analyze injects the `exec --json` / `--output-last-message` flags that make a
// codex run observable. See the package-level Analyze.
func (Provider) Analyze(args []string, resultFile string, allowJSON bool) agent.Analysis {
	return Analyze(args, resultFile, allowJSON)
}

// ApplyEffort converts codexmon's --effort option to Codex's native config.
func (Provider) ApplyEffort(args []string, effort string) ([]string, error) {
	return ApplyEffort(args, effort)
}

// ReviewArgs builds a native `codex exec review` invocation; codex has a
// purpose-built reviewer, so we use it rather than a constructed prompt.
func (Provider) ReviewArgs(spec agent.ReviewSpec) ([]string, error) {
	switch spec.Scope {
	case agent.ScopeBase:
		base := strings.TrimSpace(spec.Base)
		if base == "" {
			return nil, errors.New("review --base requires a base ref")
		}
		return []string{"exec", "review", "--base", base}, nil
	default:
		return []string{"exec", "review", "--uncommitted"}, nil
	}
}

// ParseLine normalizes a `codex exec --json` line into an agent.Event: it maps
// the phase/summary via Describe, tracks command/tool items for the watchdog,
// captures the final agent_message (or review payload) as the result, and flags
// error/turn.failed lines as failures.
func (Provider) ParseLine(line string) (agent.Event, bool) {
	ev, ok := Parse(line)
	if !ok {
		return agent.Event{}, false
	}
	phase, summary := ev.Describe()
	out := agent.Event{
		Phase:    phase,
		Summary:  summary,
		ThreadID: ev.ThreadID,
		Usage:    ev.Usage,
	}
	if it := ev.Item; it != nil && it.ID != "" {
		switch ev.Type {
		case "item.started":
			if kind := classifyItem(it.Type); kind != agent.KindOther {
				out.Started = []agent.ActiveItem{{ID: it.ID, Kind: kind, Label: itemLabel(it)}}
			}
		case "item.completed", "item.failed":
			out.Finished = []string{it.ID}
		}
	}
	if it := ev.Item; it != nil && ev.Type == "item.completed" {
		if it.Type == "agent_message" && it.Text != "" {
			out.Result = it.Text
		}
		// `codex exec review` delivers its findings as the review payload of an
		// exited-review-mode item rather than a plain agent_message.
		if it.Review != "" {
			out.Result = it.Review
		}
	}
	if ev.Type == "error" || ev.Type == "turn.failed" {
		out.Failure = true
		// FailureText, not summary: the summary is cut to one display line, and
		// the fallback chain scans this text for usage-limit phrases that a
		// verbose provider error can push well past that cut.
		out.FailMsg = agent.FirstNonEmpty(ev.FailureText(), summary)
	}
	return out, true
}

// Doctor checks that codex is installed and responding via `codex --version` and
// `codex doctor --json` (which covers install/config/auth/runtime health).
func (Provider) Doctor(bin string, run agent.RunFunc) agent.DoctorReport {
	rep := agent.DoctorReport{Agent: "codex", Bin: bin, HealthName: "codex doctor"}

	out, err := run(5*time.Second, bin, "--version")
	switch {
	case errors.Is(err, agent.ErrTimeout):
		rep.Problems = append(rep.Problems, "`codex --version` timed out — the codex install may be wedged")
	case err != nil:
		rep.Problems = append(rep.Problems, "`codex --version` failed: "+strings.TrimSpace(out))
	default:
		rep.Version = strings.TrimSpace(out)
	}

	dout, derr := run(30*time.Second, bin, "doctor", "--json")
	switch {
	case errors.Is(derr, agent.ErrTimeout):
		rep.Problems = append(rep.Problems, "`codex doctor` timed out after 30s — Codex is not responding")
		rep.DetailText = strings.TrimSpace(dout)
	case derr != nil:
		rep.Problems = append(rep.Problems, "`codex doctor` failed")
		rep.DetailText = strings.TrimSpace(dout)
	default:
		rep.HealthOK = true
		if trimmed := strings.TrimSpace(dout); strings.HasPrefix(trimmed, "{") || strings.HasPrefix(trimmed, "[") {
			rep.Detail = []byte(trimmed)
		} else {
			rep.DetailText = trimmed
		}
	}

	rep.Ready = rep.Version != "" && rep.HealthOK && len(rep.Problems) == 0
	return rep
}
