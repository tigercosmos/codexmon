package agent

import (
	"os"
	"strings"
)

// fallbackOrder is the ordered chain codexmon walks when no agent is explicitly
// selected (no --agent flag and no CODEXMON_AGENT): codex first, then Claude
// Code, then the Cursor agent. Each is tried in turn when the one before it is
// unavailable or hits a usage limit. It intentionally mirrors DefaultName —
// codex stays the default — and only adds what to do when codex can't serve. It
// is unexported so no caller can mutate the global order; FallbackChain returns
// a fresh slice built from it.
var fallbackOrder = []string{"codex", "claude", "cursor"}

// Caller reports which coding agent invoked codexmon, inferred from the
// environment, or "" when unknown (a human shell, CI, an unrecognized agent).
// It exists so codexmon can avoid falling back to the very agent that is
// calling it. Today only Claude Code is detected: it exports CLAUDECODE=1 into
// the environment of every process it launches, so a codexmon run spawned from
// Claude Code sees it.
func Caller() string {
	if v := strings.TrimSpace(os.Getenv("CLAUDECODE")); v != "" && v != "0" {
		return "claude"
	}
	return ""
}

// FallbackChain is FallbackOrder with the calling agent removed: codexmon must
// not fall back to the agent that invoked it, which is presumably the one
// already rate-limited or busy. With a Claude Code caller, for instance, the
// chain collapses from codex→claude→cursor to codex→cursor (Cursor becomes the
// second and last fallback). An unknown caller leaves the full chain intact.
func FallbackChain(caller string) []string {
	out := make([]string, 0, len(fallbackOrder))
	for _, name := range fallbackOrder {
		if caller != "" && name == caller {
			continue
		}
		out = append(out, name)
	}
	return out
}

// limitSignals are lowercased substrings that mark a run that stopped because a
// usage / rate / quota / credit limit was hit, as opposed to a genuine task
// failure. Every phrase is multi-word and distinctive: bare tokens like "quota",
// "429", or "rate limit" are intentionally excluded because a false positive is
// worse than a miss here. A false positive retries on another agent AND masks the
// real failure; a miss merely surfaces the original error. Distinctiveness
// matters especially because some agents (claude, cursor) derive the failure
// message from the model's own output — so a run that merely *discusses* rate
// limiting must not be mistaken for codexmon hitting one. It is a package
// variable so a new agent's phrasing can be appended without touching callers.
//
// The phrases cover the real messages observed in practice: codex's
// "Your workspace is out of credits" and "429 Too Many Requests", and Claude
// Code's "usage limit reached".
var limitSignals = []string{
	"usage limit",
	"too many requests",
	"rate limit exceeded",
	"rate_limit_exceeded",
	"rate-limited",
	"quota exceeded",
	"resource_exhausted",
	"insufficient_quota",
	"insufficient credit",
	"out of credit",
}

// IsLimitFailure reports whether any of the given texts (typically a failed
// run's error message and final output) indicates the agent stopped because it
// ran out of a usage, rate, or quota limit — the cue codexmon uses to fall back
// to the next agent rather than surfacing the failure.
func IsLimitFailure(texts ...string) bool {
	for _, t := range texts {
		if t == "" {
			continue
		}
		low := strings.ToLower(t)
		for _, sig := range limitSignals {
			if strings.Contains(low, sig) {
				return true
			}
		}
	}
	return false
}
