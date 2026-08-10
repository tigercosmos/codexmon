package claude

import (
	"strings"
	"testing"
	"time"

	"github.com/tigercosmos/codexmon/internal/agent"
)

func TestParseSystemInit(t *testing.T) {
	ev, ok := Provider{}.ParseLine(`{"type":"system","subtype":"init","session_id":"s1","model":"opus"}`)
	if !ok || ev.Phase != agent.PhaseStarting || ev.ThreadID != "s1" {
		t.Fatalf("init = %+v ok=%v", ev, ok)
	}
}

func TestParseAssistantToolUse(t *testing.T) {
	line := `{"type":"assistant","message":{"role":"assistant","content":[` +
		`{"type":"tool_use","id":"t1","name":"Bash","input":{"command":"git diff"}},` +
		`{"type":"tool_use","id":"t2","name":"Read"},` +
		`{"type":"tool_use","id":"t3","name":"mcp__sentry__search"}]}}`
	ev, ok := Provider{}.ParseLine(line)
	if !ok || len(ev.Started) != 3 {
		t.Fatalf("started = %+v ok=%v", ev.Started, ok)
	}
	if ev.Started[0].Kind != agent.KindCommand {
		t.Errorf("Bash should be a command, got %v", ev.Started[0].Kind)
	}
	if ev.Started[1].Kind != agent.KindTool {
		t.Errorf("Read should be a tool, got %v", ev.Started[1].Kind)
	}
	if ev.Started[2].Label != "sentry/search" {
		t.Errorf("mcp label = %q, want sentry/search", ev.Started[2].Label)
	}
	// A Bash command in the batch escalates the phase to running.
	if ev.Phase != agent.PhaseRunning {
		t.Errorf("phase = %q, want running", ev.Phase)
	}
}

func TestAssistantToolUseEmptyIDSkipped(t *testing.T) {
	// An id-less tool_use can't be tracked by the watchdog, so it must neither
	// enter Started nor escalate the phase (keeps phase/watchdog state in sync).
	ev, _ := (Provider{}).ParseLine(`{"type":"assistant","message":{"content":[{"type":"tool_use","name":"Bash"}]}}`)
	if len(ev.Started) != 0 {
		t.Errorf("empty-id tool_use should not be tracked: %+v", ev.Started)
	}
	if ev.Phase == agent.PhaseRunning {
		t.Errorf("empty-id tool_use should not escalate to running: %q", ev.Phase)
	}
}

func TestParseAssistantTextAndUserResult(t *testing.T) {
	ev, _ := Provider{}.ParseLine(`{"type":"assistant","message":{"content":[{"type":"text","text":"the findings"}]}}`)
	if ev.Phase != agent.PhaseWriting || ev.Result != "the findings" {
		t.Errorf("assistant text = %+v", ev)
	}
	ev, _ = Provider{}.ParseLine(`{"type":"user","message":{"content":[{"type":"tool_result","tool_use_id":"t1"}]}}`)
	if len(ev.Finished) != 1 || ev.Finished[0] != "t1" {
		t.Errorf("tool_result finished = %+v", ev.Finished)
	}
}

func TestParseResultSuccessAndError(t *testing.T) {
	ev, _ := Provider{}.ParseLine(`{"type":"result","subtype":"success","result":"done","is_error":false,"session_id":"s1","usage":{"input_tokens":10,"output_tokens":2,"cache_read_input_tokens":4,"cache_creation_input_tokens":7}}`)
	if ev.Phase != agent.PhaseCompleted || ev.Result != "done" {
		t.Errorf("result success = %+v", ev)
	}
	if ev.Usage == nil || ev.Usage.InputTokens != 10 || ev.Usage.CachedInputTokens != 4 || ev.Usage.CacheCreationTokens != 7 {
		t.Errorf("usage = %+v", ev.Usage)
	}

	ev, _ = Provider{}.ParseLine(`{"type":"result","subtype":"error_during_execution","is_error":true,"result":""}`)
	if !ev.Failure || ev.Phase != agent.PhaseFailed {
		t.Errorf("result error = %+v", ev)
	}
}

func TestParseUnknownAndNonJSON(t *testing.T) {
	if _, ok := (Provider{}).ParseLine(`{"type":"stream_event","event":{"type":"content_block_delta"}}`); !ok {
		t.Error("unknown but valid line should count as activity (ok=true)")
	}
	if _, ok := (Provider{}).ParseLine("hello banner"); ok {
		t.Error("non-json should not parse")
	}
}

func TestAnalyzeInjectsStreamJSON(t *testing.T) {
	a := Provider{}.Analyze([]string{"-p", "review my diff"}, "", true)
	if !a.JSONMode {
		t.Fatalf("print run should be json-monitored: %+v", a)
	}
	if !agent.HasFlag(a.Args, "--output-format") || !agent.HasFlag(a.Args, "--verbose") {
		t.Errorf("expected stream-json + verbose injected: %v", a.Args)
	}
	v, _ := agent.FlagValue(a.Args, "--output-format")
	if v != "stream-json" {
		t.Errorf("output-format = %q", v)
	}
}

func TestAnalyzeNonPrintNoInject(t *testing.T) {
	a := Provider{}.Analyze([]string{"mcp", "list"}, "", true)
	if a.JSONMode || agent.HasFlag(a.Args, "--output-format") {
		t.Errorf("non-print run must not be json-monitored: %+v", a)
	}
}

func TestAnalyzeRespectsExistingFormat(t *testing.T) {
	// Caller asked for text/json output: don't override, don't claim JSON mode.
	a := Provider{}.Analyze([]string{"-p", "x", "--output-format", "json"}, "", true)
	if a.JSONMode {
		t.Errorf("explicit non-stream format should disable json mode: %+v", a)
	}
	// Caller already chose stream-json: keep it, add verbose, json mode on.
	a = Provider{}.Analyze([]string{"-p", "x", "--output-format", "stream-json"}, "", true)
	if !a.JSONMode || !agent.HasFlag(a.Args, "--verbose") {
		t.Errorf("stream-json should stay json-monitored with verbose: %+v", a)
	}
	count := 0
	for _, x := range a.Args {
		if x == "--output-format" {
			count++
		}
	}
	if count != 1 {
		t.Errorf("--output-format duplicated: %v", a.Args)
	}
}

func TestReviewArgs(t *testing.T) {
	args, err := Provider{}.ReviewArgs(agent.ReviewSpec{Scope: agent.ScopeUncommitted})
	if err != nil {
		t.Fatal(err)
	}
	if !agent.HasFlag(args, "-p") || !agent.HasFlag(args, "--permission-mode") {
		t.Errorf("review args = %v", args)
	}
	if !strings.Contains(strings.Join(args, " "), "read-only") {
		t.Errorf("review prompt should be read-only: %v", args)
	}
	// The dedicated file-editing tools must be denied so a review can't mutate.
	dis, ok := agent.FlagValue(args, "--disallowedTools")
	if !ok || !strings.Contains(dis, "Edit") || !strings.Contains(dis, "Write") {
		t.Errorf("review should deny edit tools, got --disallowedTools=%q", dis)
	}
}

func TestDoctorReadyOnVersion(t *testing.T) {
	run := func(_ time.Duration, _ string, args ...string) (string, error) {
		if args[0] == "--version" {
			return "claude 2.1.0", nil
		}
		return "ok", nil
	}
	rep := Provider{}.Doctor("/bin/claude", run)
	if !rep.Ready || rep.Version != "claude 2.1.0" {
		t.Errorf("doctor = %+v", rep)
	}
}

// A message whose content is a bare string (rather than a block array) must
// still yield its text.
func TestAssistantEventAcceptsBareStringContent(t *testing.T) {
	ev, ok := Provider{}.ParseLine(`{"type":"assistant","message":{"role":"assistant","content":"just a string"}}`)
	if !ok {
		t.Fatal("line should parse")
	}
	if ev.Result != "just a string" {
		t.Errorf("result = %q, want the bare string content", ev.Result)
	}
}
