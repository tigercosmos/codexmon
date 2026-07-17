package agent

import (
	"reflect"
	"testing"
)

func TestCaller(t *testing.T) {
	t.Setenv("CLAUDECODE", "")
	if got := Caller(); got != "" {
		t.Errorf("no CLAUDECODE should be unknown caller, got %q", got)
	}
	t.Setenv("CLAUDECODE", "1")
	if got := Caller(); got != "claude" {
		t.Errorf("CLAUDECODE=1 should be claude, got %q", got)
	}
	// An explicit "0" is treated as unset, not as a Claude caller.
	t.Setenv("CLAUDECODE", "0")
	if got := Caller(); got != "" {
		t.Errorf("CLAUDECODE=0 should be unknown caller, got %q", got)
	}
}

func TestFallbackChain(t *testing.T) {
	if got := FallbackChain(""); !reflect.DeepEqual(got, []string{"codex", "claude", "cursor"}) {
		t.Errorf("unknown caller chain = %v", got)
	}
	// The caller is dropped so codexmon never falls back to itself.
	if got := FallbackChain("claude"); !reflect.DeepEqual(got, []string{"codex", "cursor"}) {
		t.Errorf("claude caller chain = %v, want codex,cursor", got)
	}
	if got := FallbackChain("codex"); !reflect.DeepEqual(got, []string{"claude", "cursor"}) {
		t.Errorf("codex caller chain = %v", got)
	}
	// A caller not in the order leaves the chain intact.
	if got := FallbackChain("zed"); !reflect.DeepEqual(got, []string{"codex", "claude", "cursor"}) {
		t.Errorf("unknown-agent caller chain = %v", got)
	}
}

func TestIsLimitFailure(t *testing.T) {
	limits := []string{
		"You've hit your usage limit.",
		"Claude AI usage limit reached|1730000000",
		"stream error: 429 Too Many Requests",
		"error: rate limit exceeded, retry later",
		"RESOURCE_EXHAUSTED: quota exceeded for requests",
		"insufficient_quota: you have run out of credit",
		// The exact message a genuinely limited codex emits (captured live).
		"error: Your workspace is out of credits. Ask your workspace owner to refill in order to continue.",
	}
	for _, s := range limits {
		if !IsLimitFailure(s) {
			t.Errorf("IsLimitFailure(%q) = false, want true", s)
		}
	}
	nonLimits := []string{
		"",
		"exit status 1",
		"panic: nil pointer dereference",
		"compile error in main.go: undefined: foo",
		"the review found a bug on line 429 of parser.go",
		// Review prose about rate/quota handling must not be read as a limit —
		// st.Error can be the model's own output for claude/cursor.
		"the quota check in pkg/quota looks correct",
		"consider adding a rate limit to the login endpoint",
		"this handler should return 429 when the client exceeds its budget",
	}
	for _, s := range nonLimits {
		if IsLimitFailure(s) {
			t.Errorf("IsLimitFailure(%q) = true, want false", s)
		}
	}
	// Any one of several texts matching is enough (multiple fields inspected).
	if !IsLimitFailure("exit status 1", "the model returned: usage limit reached") {
		t.Error("a match in any argument should count")
	}
}
