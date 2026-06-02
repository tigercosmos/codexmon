// Package events parses the JSONL event stream emitted by `codex exec --json`.
//
// A real stream looks like:
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
package events

import (
	"encoding/json"
	"strings"
)

// Event is a single line of the `codex exec --json` stream.
type Event struct {
	Type     string          `json:"type"`
	ThreadID string          `json:"thread_id,omitempty"`
	Item     *Item           `json:"item,omitempty"`
	Usage    *Usage          `json:"usage,omitempty"`
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

// Usage is the token accounting reported on turn.completed.
type Usage struct {
	InputTokens           int `json:"input_tokens"`
	CachedInputTokens     int `json:"cached_input_tokens"`
	OutputTokens          int `json:"output_tokens"`
	ReasoningOutputTokens int `json:"reasoning_output_tokens"`
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

// Phase is a coarse, human-meaningful stage label derived from an event. It is
// what `codexmon status` shows so a watcher can tell *what* Codex is doing.
type Phase string

const (
	PhaseStarting    Phase = "starting"
	PhaseThinking    Phase = "thinking"
	PhaseRunning     Phase = "running"
	PhaseVerifying   Phase = "verifying"
	PhaseEditing     Phase = "editing"
	PhaseInvestigate Phase = "investigating"
	PhaseSearching   Phase = "searching"
	PhaseReviewing   Phase = "reviewing"
	PhaseWriting     Phase = "writing"
	PhaseFinalizing  Phase = "finalizing"
	PhaseCompleted   Phase = "completed"
	PhaseFailed      Phase = "failed"
)

// Describe returns the phase implied by an event plus a one-line, human-readable
// summary suitable for a status line or log. phase is empty when the event does
// not change the phase (the caller keeps the previous phase).
func (ev Event) Describe() (phase Phase, summary string) {
	switch ev.Type {
	case "thread.started":
		return PhaseStarting, "thread started"
	case "turn.started":
		return PhaseStarting, "turn started"
	case "turn.completed":
		return PhaseCompleted, "turn completed" + usageSuffix(ev.Usage)
	case "turn.failed":
		return PhaseFailed, "turn failed" + rawSuffix(ev.Error)
	case "error":
		msg := ev.Message
		if msg == "" {
			msg = string(ev.Error)
		}
		return PhaseFailed, "error: " + shorten(msg, 120)
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

func (it *Item) describe(lifecycle string) (Phase, string) {
	done := lifecycle == "item.completed"
	switch it.Type {
	case "agent_message":
		if done {
			return PhaseWriting, "message: " + shorten(it.Text, 120)
		}
		return PhaseWriting, "drafting message"
	case "reasoning":
		return PhaseThinking, "reasoning"
	case "command_execution":
		phase := PhaseRunning
		if looksLikeVerification(it.Command) {
			phase = PhaseVerifying
		}
		if done {
			ec := "?"
			if it.ExitCode != nil {
				ec = itoa(*it.ExitCode)
			}
			return phase, "ran: " + shorten(it.Command, 80) + " (exit " + ec + ")"
		}
		return phase, "running: " + shorten(it.Command, 96)
	case "file_change":
		if done {
			return PhaseEditing, "edited " + itoa(len(it.Changes)) + " file(s)"
		}
		return PhaseEditing, "editing files"
	case "mcp_tool_call":
		return PhaseInvestigate, "tool " + it.Server + "/" + it.Tool
	case "dynamic_tool_call":
		return PhaseInvestigate, "tool " + it.Tool
	case "web_search":
		return PhaseSearching, "search: " + shorten(it.Query, 96)
	case "entered_review_mode", "enteredReviewMode":
		return PhaseReviewing, "reviewer started"
	case "exited_review_mode", "exitedReviewMode":
		return PhaseFinalizing, "reviewer finished"
	default:
		return "", lifecycle + " " + it.Type
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

func usageSuffix(u *Usage) string {
	if u == nil {
		return ""
	}
	return " (" + itoa(u.InputTokens) + " in / " + itoa(u.OutputTokens) + " out tokens)"
}

func rawSuffix(raw json.RawMessage) string {
	s := strings.TrimSpace(string(raw))
	if s == "" || s == "null" {
		return ""
	}
	return ": " + shorten(s, 120)
}

func shorten(s string, limit int) string {
	s = strings.TrimSpace(strings.Join(strings.Fields(s), " "))
	if len(s) <= limit {
		return s
	}
	if limit <= 3 {
		return s[:limit]
	}
	return s[:limit-3] + "..."
}

func itoa(n int) string {
	// Tiny dependency-free int formatter to keep this package import-light.
	if n == 0 {
		return "0"
	}
	neg := n < 0
	if neg {
		n = -n
	}
	var buf [20]byte
	i := len(buf)
	for n > 0 {
		i--
		buf[i] = byte('0' + n%10)
		n /= 10
	}
	if neg {
		i--
		buf[i] = '-'
	}
	return string(buf[i:])
}
