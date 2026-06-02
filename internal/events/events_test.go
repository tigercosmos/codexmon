package events

import "testing"

func TestParseValidEvents(t *testing.T) {
	cases := []struct {
		line     string
		wantType string
	}{
		{`{"type":"thread.started","thread_id":"abc"}`, "thread.started"},
		{`{"type":"turn.started"}`, "turn.started"},
		{`{"type":"item.completed","item":{"id":"i0","type":"agent_message","text":"hi"}}`, "item.completed"},
		{`{"type":"turn.completed","usage":{"input_tokens":10,"output_tokens":3}}`, "turn.completed"},
	}
	for _, c := range cases {
		ev, ok := Parse(c.line)
		if !ok {
			t.Fatalf("Parse(%q) returned ok=false", c.line)
		}
		if ev.Type != c.wantType {
			t.Errorf("Parse(%q) type = %q, want %q", c.line, ev.Type, c.wantType)
		}
	}
}

func TestParseRejectsNonEvents(t *testing.T) {
	for _, line := range []string{"", "   ", "not json", "plain banner text", `{"no":"type"}`, `[1,2,3]`} {
		if _, ok := Parse(line); ok {
			t.Errorf("Parse(%q) = ok, want rejected", line)
		}
	}
}

func TestParseExtractsFields(t *testing.T) {
	ev, ok := Parse(`{"type":"thread.started","thread_id":"019e-thread"}`)
	if !ok || ev.ThreadID != "019e-thread" {
		t.Fatalf("thread id not parsed: %+v ok=%v", ev, ok)
	}
	ev, ok = Parse(`{"type":"item.completed","item":{"type":"command_execution","command":"go test ./...","exit_code":0,"status":"completed"}}`)
	if !ok || ev.Item == nil || ev.Item.Command != "go test ./..." || ev.Item.ExitCode == nil || *ev.Item.ExitCode != 0 {
		t.Fatalf("command_execution not parsed: %+v", ev.Item)
	}
	ev, ok = Parse(`{"type":"turn.completed","usage":{"input_tokens":100,"cached_input_tokens":40,"output_tokens":7,"reasoning_output_tokens":3}}`)
	if !ok || ev.Usage == nil || ev.Usage.InputTokens != 100 || ev.Usage.OutputTokens != 7 {
		t.Fatalf("usage not parsed: %+v", ev.Usage)
	}
}

func TestDescribePhases(t *testing.T) {
	cases := []struct {
		line      string
		wantPhase Phase
		wantSub   string // substring expected in summary
	}{
		{`{"type":"thread.started","thread_id":"t"}`, PhaseStarting, "thread"},
		{`{"type":"turn.started"}`, PhaseStarting, "turn"},
		{`{"type":"turn.completed","usage":{"input_tokens":5,"output_tokens":2}}`, PhaseCompleted, "completed"},
		{`{"type":"turn.failed","error":{"message":"x"}}`, PhaseFailed, "failed"},
		{`{"type":"error","message":"boom"}`, PhaseFailed, "boom"},
		{`{"type":"item.completed","item":{"type":"agent_message","text":"the answer"}}`, PhaseWriting, "the answer"},
		{`{"type":"item.started","item":{"type":"reasoning"}}`, PhaseThinking, "reasoning"},
		{`{"type":"item.started","item":{"type":"command_execution","command":"ls -a"}}`, PhaseRunning, "ls -a"},
		{`{"type":"item.started","item":{"type":"command_execution","command":"go test ./..."}}`, PhaseVerifying, "go test"},
		{`{"type":"item.completed","item":{"type":"file_change","changes":[{"path":"a.go"},{"path":"b.go"}]}}`, PhaseEditing, "2 file"},
		{`{"type":"item.started","item":{"type":"web_search","query":"golang context"}}`, PhaseSearching, "golang context"},
		{`{"type":"item.started","item":{"type":"mcp_tool_call","server":"s","tool":"t"}}`, PhaseInvestigate, "s/t"},
	}
	for _, c := range cases {
		ev, ok := Parse(c.line)
		if !ok {
			t.Fatalf("Parse(%q) failed", c.line)
		}
		phase, summary := ev.Describe()
		if phase != c.wantPhase {
			t.Errorf("Describe(%q) phase = %q, want %q", c.line, phase, c.wantPhase)
		}
		if c.wantSub != "" && !contains(summary, c.wantSub) {
			t.Errorf("Describe(%q) summary = %q, want substring %q", c.line, summary, c.wantSub)
		}
	}
}

func TestDescribeUnknownKeepsActivity(t *testing.T) {
	// Unknown event types must still register as activity (non-empty summary),
	// so the monitor never treats a live-but-novel Codex as dead.
	ev, ok := Parse(`{"type":"some.future.event"}`)
	if !ok {
		t.Fatal("future event should parse")
	}
	phase, summary := ev.Describe()
	if phase != "" {
		t.Errorf("unknown event phase = %q, want empty", phase)
	}
	if summary == "" {
		t.Error("unknown event summary should be non-empty")
	}
}

func contains(s, sub string) bool {
	return len(sub) == 0 || (len(s) >= len(sub) && indexOf(s, sub) >= 0)
}

func indexOf(s, sub string) int {
	for i := 0; i+len(sub) <= len(s); i++ {
		if s[i:i+len(sub)] == sub {
			return i
		}
	}
	return -1
}
