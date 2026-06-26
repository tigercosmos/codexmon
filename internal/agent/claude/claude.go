// Package claude adapts Anthropic's Claude Code CLI (`claude`) to codexmon's
// agent contract. A headless review runs `claude -p "<prompt>"`; to make it
// observable, codexmon adds `--output-format stream-json --verbose`, then parses
// the resulting newline-delimited event stream:
//
//	{"type":"system","subtype":"init","session_id":"...","model":"..."}
//	{"type":"assistant","message":{"role":"assistant","content":[{"type":"tool_use","id":"toolu_1","name":"Bash","input":{...}}]}}
//	{"type":"user","message":{"role":"user","content":[{"type":"tool_result","tool_use_id":"toolu_1",...}]}}
//	{"type":"assistant","message":{"role":"assistant","content":[{"type":"text","text":"Findings: ..."}]}}
//	{"type":"result","subtype":"success","result":"Findings: ...","is_error":false,"usage":{"input_tokens":...,"output_tokens":...}}
//
// Claude Code has no `--output-last-message`-style file flag, so the final
// answer is captured from the `result` event's `result` field (the streamed
// assistant text serves as a progressive fallback if a run is cut short).
package claude

import (
	"encoding/json"
	"errors"
	"strings"
	"time"

	"github.com/tigercosmos/codexmon/internal/agent"
)

// Provider drives the Claude Code CLI.
type Provider struct{}

func init() { agent.Register(Provider{}) }

func (Provider) Name() string            { return "claude" }
func (Provider) BinEnv() string          { return "CODEXMON_CLAUDE" }
func (Provider) BinCandidates() []string { return []string{"claude"} }

// Analyze adds the streaming-JSON flags for a headless (`-p`) run so codexmon
// can monitor the event stream and capture the result. It only does so for a
// print-mode run (interactive `claude` cannot be event-monitored), and respects
// an output format the caller already chose.
func (Provider) Analyze(args []string, _ string, allowJSON bool) agent.Analysis {
	out := append([]string(nil), args...)
	jsonMode := false
	if allowJSON && hasFlag(args, "-p", "--print") {
		val, present := flagValue(args, "--output-format")
		switch {
		case !present:
			out = append(out, "--output-format", "stream-json")
			jsonMode = true
		case val == "stream-json":
			jsonMode = true
		}
		// stream-json in print mode requires --verbose to emit per-turn events.
		if jsonMode && !hasFlag(args, "--verbose") {
			out = append(out, "--verbose")
		}
	}
	return agent.Analysis{JSONMode: jsonMode, Args: out, Title: "claude"}
}

// ReviewArgs runs a headless review with a constructed read-only prompt.
// bypassPermissions is required for headless tool use (codexmon pins stdin to
// /dev/null, so any permission prompt would stall, and Claude Code's other modes
// won't run tools without a TTY). To keep "review" from mutating the tree we
// additionally deny the dedicated file-editing tools; Read/Grep/Glob and Bash
// (for git) remain available, and the prompt forbids edits. This is defense in
// depth, not a sandbox — a determined Bash write is still possible.
func (Provider) ReviewArgs(spec agent.ReviewSpec) ([]string, error) {
	return []string{
		"-p", agent.ReviewPrompt(spec),
		"--permission-mode", "bypassPermissions",
		"--disallowedTools", "Edit,Write,MultiEdit,NotebookEdit",
	}, nil
}

// ParseLine normalizes one Claude Code stream-json line into an agent.Event.
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
	case "user":
		return userEvent(ln), true
	case "result":
		return resultEvent(ln), true
	default:
		// Unknown but valid line (e.g. partial stream_event deltas): count it as
		// activity, but log nothing.
		return agent.Event{}, true
	}
}

func assistantEvent(ln streamLine) agent.Event {
	var out agent.Event
	var texts, names []string
	for _, b := range decodeBlocks(ln.Message) {
		switch b.Type {
		case "text":
			if t := strings.TrimSpace(b.Text); t != "" {
				texts = append(texts, t)
			}
		case "tool_use":
			if b.ID == "" {
				continue // the watchdog tracks in-flight work by id; an id-less
				// tool can't be tracked, so don't escalate the phase for it either.
			}
			kind := agent.KindTool
			if b.Name == "Bash" { // only Bash runs arbitrary, possibly-long commands
				kind = agent.KindCommand
			}
			label := toolLabel(b.Name)
			out.Started = append(out.Started, agent.ActiveItem{ID: b.ID, Kind: kind, Label: label})
			names = append(names, label)
		}
	}
	if len(texts) > 0 {
		out.Result = strings.Join(texts, " ") // progressive fallback for the final answer
	}
	switch {
	case len(out.Started) > 0:
		out.Phase = agent.PhaseInvestigate
		for _, it := range out.Started {
			if it.Kind == agent.KindCommand {
				out.Phase = agent.PhaseRunning
			}
		}
		out.Summary = "tool " + strings.Join(names, ", ")
	case len(texts) > 0:
		out.Phase = agent.PhaseWriting
		out.Summary = "message: " + agent.Shorten(strings.Join(texts, " "), 120)
	}
	return out
}

func userEvent(ln streamLine) agent.Event {
	var out agent.Event
	for _, b := range decodeBlocks(ln.Message) {
		if b.Type == "tool_result" && b.ToolUseID != "" {
			out.Finished = append(out.Finished, b.ToolUseID)
		}
	}
	if len(out.Finished) > 0 {
		out.Summary = "tool result"
	}
	return out
}

func resultEvent(ln streamLine) agent.Event {
	out := agent.Event{ThreadID: ln.SessionID, Usage: ln.Usage.normalized()}
	if ln.IsError || (ln.Subtype != "" && ln.Subtype != "success") {
		out.Phase = agent.PhaseFailed
		out.Failure = true
		out.FailMsg = agent.FirstNonEmpty(strings.TrimSpace(ln.Result), "run ended: "+ln.Subtype)
		out.Summary = "error: " + agent.Shorten(out.FailMsg, 120)
		return out
	}
	out.Phase = agent.PhaseCompleted
	out.Result = ln.Result
	out.Summary = "result" + agent.UsageSummary(out.Usage)
	return out
}

// Doctor checks the install with `claude --version` and runs `claude doctor` as
// a best-effort health probe. Readiness hinges on `--version` responding: that
// is the strongest scriptable signal Claude Code exposes that a headless run
// can launch.
func (Provider) Doctor(bin string, run agent.RunFunc) agent.DoctorReport {
	rep := agent.DoctorReport{Agent: "claude", Bin: bin, HealthName: "claude doctor"}

	out, err := run(5*time.Second, bin, "--version")
	switch {
	case errors.Is(err, agent.ErrTimeout):
		rep.Problems = append(rep.Problems, "`claude --version` timed out — the Claude Code install may be wedged")
	case err != nil:
		rep.Problems = append(rep.Problems, "`claude --version` failed: "+strings.TrimSpace(out))
	default:
		rep.Version = strings.TrimSpace(out)
	}

	dout, derr := run(20*time.Second, bin, "doctor")
	switch {
	case errors.Is(derr, agent.ErrTimeout):
		rep.Problems = append(rep.Problems, "`claude doctor` timed out after 20s")
	case derr != nil:
		rep.DetailText = strings.TrimSpace(dout)
	default:
		rep.HealthOK = true
		rep.DetailText = strings.TrimSpace(dout)
	}

	rep.Ready = rep.Version != ""
	return rep
}

// ---- stream types -----------------------------------------------------------

type streamLine struct {
	Type      string          `json:"type"`
	Subtype   string          `json:"subtype"`
	SessionID string          `json:"session_id"`
	Message   json.RawMessage `json:"message"`
	Result    string          `json:"result"`
	IsError   bool            `json:"is_error"`
	Usage     *claudeUsage    `json:"usage"`
}

type block struct {
	Type      string `json:"type"`
	Text      string `json:"text"`
	ID        string `json:"id"`          // tool_use id
	Name      string `json:"name"`        // tool name (for tool_use)
	ToolUseID string `json:"tool_use_id"` // matching id on a tool_result
}

type claudeUsage struct {
	InputTokens           int `json:"input_tokens"`
	OutputTokens          int `json:"output_tokens"`
	CacheReadInputTokens  int `json:"cache_read_input_tokens"`
	CacheCreateInputToken int `json:"cache_creation_input_tokens"`
}

func (u *claudeUsage) normalized() *agent.Usage {
	if u == nil {
		return nil
	}
	return &agent.Usage{
		InputTokens:         u.InputTokens,
		OutputTokens:        u.OutputTokens,
		CachedInputTokens:   u.CacheReadInputTokens,
		CacheCreationTokens: u.CacheCreateInputToken,
	}
}

// decodeBlocks pulls the content blocks out of a Claude message. The message's
// `content` is normally an array of blocks, but a plain user message may carry a
// bare string; both are tolerated.
func decodeBlocks(raw json.RawMessage) []block {
	var msg struct {
		Content json.RawMessage `json:"content"`
	}
	if err := json.Unmarshal(raw, &msg); err != nil || len(msg.Content) == 0 {
		return nil
	}
	var blocks []block
	if err := json.Unmarshal(msg.Content, &blocks); err == nil {
		return blocks
	}
	var s string
	if err := json.Unmarshal(msg.Content, &s); err == nil {
		return []block{{Type: "text", Text: s}}
	}
	return nil
}

// toolLabel renders an MCP tool name (`mcp__server__tool`) as "server/tool" and
// otherwise returns the tool name unchanged.
func toolLabel(name string) string {
	if rest, ok := strings.CutPrefix(name, "mcp__"); ok {
		return strings.Replace(rest, "__", "/", 1)
	}
	if name == "" {
		return "tool"
	}
	return name
}

func hasFlag(args []string, names ...string) bool {
	for _, a := range args {
		for _, n := range names {
			if a == n || strings.HasPrefix(a, n+"=") {
				return true
			}
		}
	}
	return false
}

// flagValue returns the value of a `--name value` or `--name=value` flag and
// whether the flag was present at all.
func flagValue(args []string, name string) (string, bool) {
	for i, a := range args {
		if a == name {
			if i+1 < len(args) {
				return args[i+1], true
			}
			return "", true
		}
		if v, ok := strings.CutPrefix(a, name+"="); ok {
			return v, true
		}
	}
	return "", false
}
