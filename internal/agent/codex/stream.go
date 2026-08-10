// Package codex adapts the OpenAI Codex CLI (`codex`) to codexmon's agent
// contract: it locates the binary, injects the `exec --json` /
// `--output-last-message` flags that make a run observable, and parses codex's
// JSONL event stream into the normalized agent.Event the monitor consumes.
package codex

import (
	"encoding/json"
	"strconv"
	"strings"

	"github.com/tigercosmos/codexmon/internal/agent"
)

// A real `codex exec --json` stream looks like:
//
//	{"type":"thread.started","thread_id":"019e..."}
//	{"type":"turn.started"}
//	{"type":"item.started","item":{"id":"item_0","type":"command_execution","command":"...","status":"in_progress"}}
//	{"type":"item.completed","item":{"id":"item_0","type":"command_execution","command":"...","exit_code":0,"status":"completed"}}
//	{"type":"item.completed","item":{"id":"item_1","type":"agent_message","text":"..."}}
//	{"type":"turn.completed","usage":{"input_tokens":13159,...}}
//
// The parser is deliberately lenient: unknown event or item types are kept as
// "activity" so the monitor never mistakes a still-working Codex for a dead one.

// Event is a single line of the `codex exec --json` stream.
type Event struct {
	Type     string          `json:"type"`
	ThreadID string          `json:"thread_id,omitempty"`
	Item     *Item           `json:"item,omitempty"`
	Usage    *agent.Usage    `json:"usage,omitempty"`
	Error    json.RawMessage `json:"error,omitempty"`
	Message  string          `json:"message,omitempty"`
}

// Item is the payload of item.started / item.updated / item.completed events.
type Item struct {
	ID               string          `json:"id,omitempty"`
	Type             string          `json:"type,omitempty"`
	Text             string          `json:"text,omitempty"`
	Command          string          `json:"command,omitempty"`
	AggregatedOutput string          `json:"aggregated_output,omitempty"`
	ExitCode         *int            `json:"exit_code,omitempty"`
	Status           string          `json:"status,omitempty"`
	Server           string          `json:"server,omitempty"`
	Tool             string          `json:"tool,omitempty"`
	Query            string          `json:"query,omitempty"`
	Review           string          `json:"review,omitempty"`
	Changes          []FileChange    `json:"changes,omitempty"`
	Summary          json.RawMessage `json:"summary,omitempty"`
}

// FileChange describes one edited path inside a file_change item.
type FileChange struct {
	Path string `json:"path,omitempty"`
	Kind string `json:"kind,omitempty"`
}

// Parse decodes a single JSONL line. ok is false for blank lines or lines that
// are not valid JSON objects (Codex occasionally prints stray banner text).
func Parse(line string) (Event, bool) {
	trimmed := strings.TrimSpace(line)
	if trimmed == "" || trimmed[0] != '{' {
		return Event{}, false
	}
	var ev Event
	if err := json.Unmarshal([]byte(trimmed), &ev); err != nil {
		return Event{}, false
	}
	if ev.Type == "" {
		return Event{}, false
	}
	return ev, true
}

// Describe returns the phase implied by an event plus a one-line, human-readable
// summary. phase is empty when the event does not change the phase (the caller
// keeps the previous phase).
func (ev Event) Describe() (phase agent.Phase, summary string) {
	switch ev.Type {
	case "thread.started":
		return agent.PhaseStarting, "thread started"
	case "turn.started":
		return agent.PhaseStarting, "turn started"
	case "turn.completed":
		return agent.PhaseCompleted, "turn completed" + agent.UsageSummary(ev.Usage)
	case "turn.failed":
		return agent.PhaseFailed, "turn failed" + rawSuffix(ev.Error)
	case "error":
		return agent.PhaseFailed, "error: " + agent.Shorten(agent.FirstNonEmpty(ev.Message, errorText(ev.Error)), 120)
	case "item.started", "item.updated", "item.completed":
		if ev.Item == nil {
			return "", ev.Type
		}
		return ev.Item.describe(ev.Type)
	default:
		// Unknown but non-empty event type: treat as generic activity.
		return "", ev.Type
	}
}

func (it *Item) describe(lifecycle string) (agent.Phase, string) {
	done := lifecycle == "item.completed"
	switch it.Type {
	case "agent_message":
		if done {
			return agent.PhaseWriting, "message: " + agent.Shorten(it.Text, 120)
		}
		return agent.PhaseWriting, "drafting message"
	case "reasoning":
		return agent.PhaseThinking, "reasoning"
	case "command_execution":
		phase := agent.PhaseRunning
		if looksLikeVerification(it.Command) {
			phase = agent.PhaseVerifying
		}
		if done {
			ec := "?"
			if it.ExitCode != nil {
				ec = strconv.Itoa(*it.ExitCode)
			}
			return phase, "ran: " + agent.Shorten(it.Command, 80) + " (exit " + ec + ")"
		}
		return phase, "running: " + agent.Shorten(it.Command, 96)
	case "file_change":
		if done {
			return agent.PhaseEditing, "edited " + strconv.Itoa(len(it.Changes)) + " file(s)"
		}
		return agent.PhaseEditing, "editing files"
	case "mcp_tool_call":
		return agent.PhaseInvestigate, "tool " + it.Server + "/" + it.Tool
	case "dynamic_tool_call":
		return agent.PhaseInvestigate, "tool " + it.Tool
	case "web_search":
		return agent.PhaseSearching, "search: " + agent.Shorten(it.Query, 96)
	case "entered_review_mode", "enteredReviewMode":
		return agent.PhaseReviewing, "reviewer started"
	case "exited_review_mode", "exitedReviewMode":
		return agent.PhaseFinalizing, "reviewer finished"
	default:
		return "", lifecycle + " " + it.Type
	}
}

// classifyItem buckets an item type for the watchdog's liveness rules.
func classifyItem(itemType string) agent.ItemKind {
	switch itemType {
	case "command_execution":
		return agent.KindCommand
	case "mcp_tool_call", "dynamic_tool_call", "web_search":
		return agent.KindTool
	default:
		return agent.KindOther
	}
}

// itemLabel is a short human label for an in-flight item, used in kill reasons.
func itemLabel(it *Item) string {
	switch it.Type {
	case "command_execution":
		return agent.Shorten(it.Command, 60)
	case "mcp_tool_call":
		return it.Server + "/" + it.Tool
	case "dynamic_tool_call":
		return it.Tool
	case "web_search":
		return agent.Shorten(it.Query, 60)
	default:
		return it.Type
	}
}

func looksLikeVerification(cmd string) bool {
	c := strings.ToLower(cmd)
	for _, kw := range []string{
		"test", "lint", "build", "typecheck", "type-check", "tsc", "eslint",
		"ruff", "pytest", "jest", "vitest", "cargo test", "go test", "go vet",
		"mvn test", "gradle test", "make check", "verify", "validate",
	} {
		if strings.Contains(c, kw) {
			return true
		}
	}
	return false
}

func rawSuffix(raw json.RawMessage) string {
	s := errorText(raw)
	if s == "" {
		return ""
	}
	return ": " + agent.Shorten(s, 120)
}

// maxFailBytes caps FailureText. It is far wider than a one-line summary because
// this text is what agent.IsLimitFailure scans to decide whether to fall back to
// the next agent, and a provider can bury "Too Many Requests" behind a verbose
// JSON envelope — but it is still bounded, since the text lands in status.json.
const maxFailBytes = 4096

// FailureText returns the fullest available failure detail for an `error` or
// `turn.failed` event.
//
// It is deliberately NOT cut to the display width: the summary from Describe is
// for humans, while this feeds the usage-limit matching that drives agent
// fallback. Truncating both at 120 bytes (as this package once did) meant a
// rate-limit phrase sitting past the first line of a wrapped provider error was
// silently invisible to the fallback chain.
func (ev Event) FailureText() string {
	msg := agent.FirstNonEmpty(ev.Message, errorText(ev.Error))
	return agent.Shorten(msg, maxFailBytes)
}

// errorText renders an event's `error` payload as human text: a JSON object
// carrying a `message` (the usual shape) yields just that message, anything else
// its raw JSON.
func errorText(raw json.RawMessage) string {
	s := strings.TrimSpace(string(raw))
	if s == "" || s == "null" {
		return ""
	}
	var obj struct {
		Message string `json:"message"`
	}
	if err := json.Unmarshal(raw, &obj); err == nil && strings.TrimSpace(obj.Message) != "" {
		return obj.Message
	}
	return s
}
