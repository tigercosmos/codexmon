// Package cursor adapts the Cursor agent CLI (`cursor-agent`, also installed as
// `agent`) to codexmon's agent contract. A headless review runs
// `cursor-agent -p "<prompt>"`; codexmon adds `--output-format stream-json`,
// defaults `--model cursor-grok-4.5-high` (unless the caller picked a model), and
// parses the newline-delimited event stream:
//
//	{"type":"system","subtype":"init","session_id":"...","model":"...","cwd":"..."}
//	{"type":"user","message":{"role":"user","content":[{"type":"text","text":"..."}]}}
//	{"type":"tool_call","subtype":"started","call_id":"t1","tool_call":{"readToolCall":{"args":{"path":"a.ts"}}}}
//	{"type":"tool_call","subtype":"completed","call_id":"t1","tool_call":{"readToolCall":{"args":{...},"result":{...}}}}
//	{"type":"assistant","message":{"role":"assistant","content":[{"type":"text","text":"Findings: ..."}]}}
//	{"type":"result","subtype":"success","is_error":false,"result":"Findings: ...","duration_ms":1234}
//
// Cursor exposes no `--output-last-message`-style flag and reports no token
// usage, so the final answer comes from the `result` event's `result` field (the
// streamed assistant text is a progressive fallback) and Usage is left nil.
package cursor

import (
	"encoding/json"
	"errors"
	"strings"
	"time"

	"github.com/tigercosmos/codexmon/internal/agent"
)

// defaultModel is the Cursor model codexmon selects when the caller hasn't
// chosen one with --model. cursor-agent's own default is "auto" (server-side
// selection); we pin Cursor Grok 4.5 so reviews run on a known model.
const defaultModel = "cursor-grok-4.5-high"

// Provider drives the Cursor agent CLI.
type Provider struct{}

func init() { agent.Register(Provider{}) }

func (Provider) Name() string   { return "cursor" }
func (Provider) BinEnv() string { return "CODEXMON_CURSOR" }

// BinCandidates resolves only the unambiguous "cursor-agent" name, which the
// Cursor installer always provides. The generic "agent" name the installer also
// drops is intentionally not a fallback — it collides too easily with unrelated
// binaries on PATH; for a nonstandard install set CODEXMON_CURSOR or --agent-bin.
func (Provider) BinCandidates() []string { return []string{"cursor-agent"} }

// Analyze adds the streaming-JSON flag for a headless (`-p`) run so codexmon can
// monitor the event stream, unless the caller already chose an output format. It
// also defaults --model to Cursor Grok 4.5 unless the caller specified a model.
//
// Both injections are gated on print mode, mirroring how the codex adapter gates
// on `exec` and the claude adapter on `-p`: only a headless run takes a model,
// and a management subcommand (`cursor-agent status`, `login`, `ls`, …) must be
// forwarded exactly as the user wrote it — one that rejects an unexpected
// --model would fail for a flag codexmon added behind their back.
//
// Everything at or after Cursor's `--` terminator is prompt text, not options
// (callers need it for prompts that start with `-`). So injected flags go before
// the terminator, and existing-flag detection scans only the real options — a
// literal `--model` sitting inside the prompt must not suppress the default.
func (Provider) Analyze(args []string, _ string, allowJSON bool) agent.Analysis {
	term := terminatorIndex(args)
	opts := args[:term]
	printMode := agent.HasFlag(opts, "-p", "--print")

	var inject []string
	if printMode && !agent.HasFlag(opts, "--model") {
		inject = append(inject, "--model", defaultModel)
	}
	jsonMode := false
	if allowJSON && printMode {
		val, present := agent.FlagValue(opts, "--output-format")
		switch {
		case !present:
			inject = append(inject, "--output-format", "stream-json")
			jsonMode = true
		case val == "stream-json":
			jsonMode = true
		}
	}

	out := make([]string, 0, len(args)+len(inject))
	out = append(out, args[:term]...)
	out = append(out, inject...)
	out = append(out, args[term:]...)
	return agent.Analysis{JSONMode: jsonMode, Args: out, Title: "cursor"}
}

// terminatorIndex returns the index of Cursor's first `--` argument separator,
// or len(args) if there is none. Args at or after it are prompt operands.
func terminatorIndex(args []string) int {
	for i, a := range args {
		if a == "--" {
			return i
		}
	}
	return len(args)
}

// ReviewArgs runs a headless review with a constructed read-only prompt. Print
// mode (`-p`) is non-interactive, so no approval prompt can stall the run.
func (Provider) ReviewArgs(spec agent.ReviewSpec) ([]string, error) {
	return []string{"-p", agent.ReviewPrompt(spec)}, nil
}

// ParseLine normalizes one Cursor stream-json line into an agent.Event.
func (Provider) ParseLine(line string) (agent.Event, bool) {
	trimmed := strings.TrimSpace(line)
	if trimmed == "" || trimmed[0] != '{' {
		return agent.Event{}, false
	}
	var ln streamLine
	if err := json.Unmarshal([]byte(trimmed), &ln); err != nil {
		return agent.Event{}, false
	}
	if ln.Type == "" {
		return agent.Event{}, false
	}
	switch ln.Type {
	case "system":
		out := agent.Event{ThreadID: ln.SessionID}
		switch {
		case ln.Subtype == "init":
			out.Phase = agent.PhaseStarting
			out.Summary = "session started"
		case ln.Subtype != "":
			out.Summary = "system: " + ln.Subtype
		}
		return out, true
	case "assistant":
		return assistantEvent(ln), true
	case "tool_call":
		return toolEvent(ln), true
	case "user":
		// The prompt echo: liveness, nothing worth logging.
		return agent.Event{}, true
	case "result":
		return resultEvent(ln), true
	default:
		return agent.Event{}, true
	}
}

func assistantEvent(ln streamLine) agent.Event {
	var out agent.Event
	var texts []string
	for _, b := range agent.DecodeBlocks(ln.Message) {
		if b.Type == "text" {
			if t := strings.TrimSpace(b.Text); t != "" {
				texts = append(texts, t)
			}
		}
	}
	if len(texts) > 0 {
		joined := strings.Join(texts, " ")
		out.Result = joined // progressive fallback for the final answer
		out.Phase = agent.PhaseWriting
		out.Summary = "message: " + agent.Shorten(joined, 120)
	}
	return out
}

func toolEvent(ln streamLine) agent.Event {
	var out agent.Event
	if ln.CallID == "" {
		return out
	}
	kind, label := classifyTool(firstKey(ln.ToolCall))
	switch ln.Subtype {
	case "started":
		out.Started = []agent.ActiveItem{{ID: ln.CallID, Kind: kind, Label: label}}
		out.Phase = agent.PhaseInvestigate
		if kind == agent.KindCommand {
			out.Phase = agent.PhaseRunning
		}
		out.Summary = "tool " + label
	case "completed":
		out.Finished = []string{ln.CallID}
		out.Summary = "tool " + label + " done"
	case "failed":
		out.Finished = []string{ln.CallID}
		out.Summary = "tool " + label + " failed"
	}
	return out
}

func resultEvent(ln streamLine) agent.Event {
	var out agent.Event
	out.ThreadID = ln.SessionID
	if ln.IsError || (ln.Subtype != "" && ln.Subtype != "success") {
		out.Phase = agent.PhaseFailed
		out.Failure = true
		out.FailMsg = agent.FirstNonEmpty(strings.TrimSpace(ln.Result), "run ended: "+ln.Subtype)
		out.Summary = "error: " + agent.Shorten(out.FailMsg, 120)
		return out
	}
	out.Phase = agent.PhaseCompleted
	out.Result = ln.Result
	out.Summary = "result"
	return out
}

// Doctor checks the install with `--version` and authentication with
// `cursor-agent status`, which reports the logged-in account / API-key state.
func (Provider) Doctor(bin string, run agent.RunFunc) agent.DoctorReport {
	rep := agent.DoctorReport{Agent: "cursor", Bin: bin, HealthName: "cursor-agent status"}

	out, err := run(5*time.Second, bin, "--version")
	switch {
	case errors.Is(err, agent.ErrTimeout):
		rep.Problems = append(rep.Problems, "`cursor-agent --version` timed out — the install may be wedged")
	case err != nil:
		rep.Problems = append(rep.Problems, "`cursor-agent --version` failed: "+strings.TrimSpace(out))
	default:
		rep.Version = strings.TrimSpace(out)
	}

	sout, serr := run(10*time.Second, bin, "status")
	switch {
	case errors.Is(serr, agent.ErrTimeout):
		rep.Problems = append(rep.Problems, "`cursor-agent status` timed out after 10s")
		rep.DetailText = strings.TrimSpace(sout)
	case serr != nil:
		rep.Problems = append(rep.Problems, "`cursor-agent status` failed — not authenticated? (set CURSOR_API_KEY or run `cursor-agent login`)")
		rep.DetailText = strings.TrimSpace(sout)
	default:
		rep.HealthOK = true
		rep.DetailText = strings.TrimSpace(sout)
	}

	rep.Ready = rep.Version != "" && rep.HealthOK
	return rep
}

// ---- stream types -----------------------------------------------------------

type streamLine struct {
	Type      string                     `json:"type"`
	Subtype   string                     `json:"subtype"`
	SessionID string                     `json:"session_id"`
	CallID    string                     `json:"call_id"`
	Message   json.RawMessage            `json:"message"`
	ToolCall  map[string]json.RawMessage `json:"tool_call"`
	Result    string                     `json:"result"`
	IsError   bool                       `json:"is_error"`
}

// firstKey returns the single tool key in a tool_call object (e.g.
// "readToolCall"), or "" if there is none.
func firstKey(m map[string]json.RawMessage) string {
	for k := range m {
		return k
	}
	return ""
}

// classifyTool buckets a Cursor tool key for the watchdog and derives a short
// label. Shell/terminal/command tools may legitimately run long (command kind);
// everything else is a quick tool call.
func classifyTool(key string) (agent.ItemKind, string) {
	label := strings.TrimSuffix(key, "ToolCall")
	if label == "" {
		label = "tool"
	}
	lower := strings.ToLower(label)
	for _, kw := range []string{"shell", "command", "terminal", "run", "bash", "exec"} {
		if strings.Contains(lower, kw) {
			return agent.KindCommand, label
		}
	}
	return agent.KindTool, label
}
