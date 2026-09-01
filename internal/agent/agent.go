// Package agent defines the provider-agnostic contract codexmon uses to drive
// and monitor an AI coding CLI — codex, Claude Code, or the Cursor agent.
//
// Each concrete agent lives in a subpackage (internal/agent/codex, .../claude,
// .../cursor) that registers a Provider via Register from an init function. The
// supervisor and CLI then work only against the shared types here, so adding a
// new agent never touches the monitor: it just implements Provider and
// registers itself.
//
// The central idea is normalization. Every agent emits a different event stream
// — codex's JSONL item/turn events, Claude Code's stream-json system/assistant/
// result events, Cursor's tool_call lifecycle — but codexmon only cares about a
// handful of liveness facts: what phase the run is in, which units of work are
// in flight (so the watchdog can time them), the final answer, and token usage.
// A Provider's ParseLine collapses its native stream into the common Event here.
package agent

import (
	"encoding/json"
	"errors"
	"fmt"
	"time"
)

// Phase is a coarse, human-meaningful stage label derived from an agent's event
// stream. It is what `codexmon status` shows so a watcher can tell *what* the
// agent is doing — regardless of which agent it is.
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

// Usage is normalized token accounting. Agents leave fields they do not report
// at zero (Cursor, for instance, reports no token usage at all).
type Usage struct {
	InputTokens           int `json:"input_tokens"`
	CachedInputTokens     int `json:"cached_input_tokens"`
	CacheCreationTokens   int `json:"cache_creation_tokens,omitempty"`
	OutputTokens          int `json:"output_tokens"`
	ReasoningOutputTokens int `json:"reasoning_output_tokens"`
}

// ItemKind buckets an in-flight unit of work so the watchdog can apply the right
// liveness rule: a shell command may legitimately run for minutes (wall-clock
// only), an MCP/tool call should be quick (its own stuck-timeout), and anything
// else is generic activity governed by the idle ceiling.
type ItemKind int

const (
	KindOther   ItemKind = iota // generic activity
	KindCommand                 // a shell command — may legitimately run long
	KindTool                    // an MCP / tool / web call — should return quickly
)

// ActiveItem is a unit of work an agent reported starting. The watchdog tracks
// it by ID until a matching completion arrives, timing it per its Kind.
type ActiveItem struct {
	ID    string
	Kind  ItemKind
	Label string // short human label, used in stuck-tool kill reasons
}

// Event is the normalized interpretation of one line of an agent's output
// stream. A Provider's ParseLine fills only the fields a given line implies; the
// monitor merges them into the live status. Two conventions keep parsers simple:
// Phase == "" means "keep the previous phase", and Summary == "" means "log
// nothing for this line" (the line still counts as activity).
type Event struct {
	Phase    Phase
	Summary  string
	Started  []ActiveItem // units of work that began on this line
	Finished []string     // IDs of units that completed or failed on this line
	Result   string       // final/most-recent answer text carried by this line
	ThreadID string       // session/thread id, when this line establishes one
	Usage    *Usage       // token usage, when this line reports it
	Failure  bool         // a hard error / failed-turn line
	FailMsg  string       // failure detail (used when Failure is true)
}

// Analysis is the outcome of inspecting the args destined for one agent: the
// (possibly flag-augmented) args to actually run, whether codexmon will monitor
// the run as a JSON event stream, and a short human title.
type Analysis struct {
	JSONMode bool
	Args     []string
	Title    string
}

// ReviewScope selects which changes `codexmon review` asks the agent to review.
type ReviewScope string

const (
	ScopeUncommitted ReviewScope = "uncommitted" // the working tree (default)
	ScopeBase        ReviewScope = "base"        // current branch vs a base ref
)

// ReviewSpec is the high-level, agent-independent description of a review that
// the `codexmon review` command turns into native args via Provider.ReviewArgs.
type ReviewSpec struct {
	Scope ReviewScope
	Base  string // base ref when Scope == ScopeBase
}

// Provider adapts one AI coding CLI to codexmon's monitor.
type Provider interface {
	// Name is the canonical id used by --agent and CODEXMON_AGENT.
	Name() string

	// BinEnv is the environment variable that overrides the binary path
	// (e.g. CODEXMON_CODEX). It is also used in "not found" diagnostics.
	BinEnv() string

	// BinCandidates lists the executables to look up on PATH, in preference
	// order (Cursor ships both "cursor-agent" and "agent", for instance).
	BinCandidates() []string

	// Analyze inspects the args headed for the agent, optionally injects the
	// flags codexmon needs to monitor a JSON event stream and capture the final
	// answer, and reports whether the run will be JSON-monitored.
	Analyze(args []string, resultFile string, allowJSON bool) Analysis

	// ReviewArgs builds the agent-native args for a code review from a
	// high-level ReviewSpec. The result is then fed through Analyze.
	ReviewArgs(spec ReviewSpec) ([]string, error)

	// ParseLine normalizes one line of the agent's stdout stream into an Event.
	// ok is false for blank or non-event lines.
	ParseLine(line string) (Event, bool)

	// Doctor reports whether the agent is installed and usable, using run for
	// bounded version/health probes.
	Doctor(bin string, run RunFunc) DoctorReport
}

// EffortProvider is an optional provider capability for agents that expose a
// reasoning-effort setting. The CLI uses it only when the caller passes
// --effort; providers without this capability remain unchanged.
type EffortProvider interface {
	ApplyEffort(args []string, effort string) ([]string, error)
}

// ReviewPrompt builds a provider-neutral code-review instruction for agents that
// have no purpose-built reviewer (Claude Code, Cursor). It tells the agent which
// diff to look at and to stay strictly read-only.
func ReviewPrompt(spec ReviewSpec) string {
	var what string
	switch spec.Scope {
	case ScopeBase:
		base := spec.Base
		what = fmt.Sprintf("the changes on the current branch relative to `%s` "+
			"(inspect `git diff %s...HEAD` and any new files)", base, base)
	default:
		what = "the uncommitted changes in this repository " +
			"(inspect `git status` and `git diff`, including untracked files)"
	}
	return "Perform a focused code review of " + what + ". " +
		"Report correctness bugs, security issues, and risky changes — most important first. " +
		"For each finding give `path:line — problem — suggested fix`; be concise and concrete, " +
		"and skip praise and pure style nits. " +
		"This is a strictly read-only review: do not modify, stage, or commit any files."
}

// ErrTimeout is returned by a RunFunc when the probed command exceeded its
// deadline — the very hang codexmon exists to surface — so a Provider can report
// it distinctly from an ordinary command failure.
var ErrTimeout = errors.New("timed out")

// RunFunc runs a short, bounded command and returns its combined output. On a
// deadline it returns ErrTimeout. Providers use it for version/health probes in
// Doctor; the CLI supplies an implementation that kills the whole process group
// on timeout so a wedged agent can never hang `codexmon doctor`.
type RunFunc func(timeout time.Duration, name string, args ...string) (string, error)

// DoctorReport is the normalized result of a Provider's readiness check.
type DoctorReport struct {
	Agent      string          `json:"agent"`
	Ready      bool            `json:"ready"`
	Bin        string          `json:"bin"`
	Version    string          `json:"version,omitempty"`
	HealthName string          `json:"health_name,omitempty"` // probe used, e.g. "codex doctor"
	HealthOK   bool            `json:"health_ok"`
	Detail     json.RawMessage `json:"detail,omitempty"`      // structured probe output, if any
	DetailText string          `json:"detail_text,omitempty"` // textual probe output, if any
	Problems   []string        `json:"problems,omitempty"`
}
