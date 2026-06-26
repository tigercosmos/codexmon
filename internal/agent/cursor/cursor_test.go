package cursor

import (
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/tigercosmos/codexmon/internal/agent"
)

func TestProviderMeta(t *testing.T) {
	p := Provider{}
	if p.Name() != "cursor" || p.BinEnv() != "CODEXMON_CURSOR" {
		t.Errorf("meta = %q / %q", p.Name(), p.BinEnv())
	}
	got := p.BinCandidates()
	if len(got) != 1 || got[0] != "cursor-agent" {
		t.Errorf("candidates = %v, want [cursor-agent] only (no ambiguous `agent` fallback)", got)
	}
}

func TestParseSystemAndAssistant(t *testing.T) {
	ev, ok := Provider{}.ParseLine(`{"type":"system","subtype":"init","session_id":"s1","cwd":"/r","model":"m"}`)
	if !ok || ev.Phase != agent.PhaseStarting || ev.ThreadID != "s1" {
		t.Fatalf("init = %+v ok=%v", ev, ok)
	}
	ev, _ = Provider{}.ParseLine(`{"type":"assistant","message":{"role":"assistant","content":[{"type":"text","text":"the findings"}]}}`)
	if ev.Phase != agent.PhaseWriting || ev.Result != "the findings" {
		t.Errorf("assistant = %+v", ev)
	}
}

func TestParseToolLifecycle(t *testing.T) {
	p := Provider{}
	ev, ok := p.ParseLine(`{"type":"tool_call","subtype":"started","call_id":"c1","tool_call":{"readToolCall":{"args":{"path":"a.ts"}}}}`)
	if !ok || len(ev.Started) != 1 {
		t.Fatalf("started = %+v ok=%v", ev, ok)
	}
	if ev.Started[0].ID != "c1" || ev.Started[0].Kind != agent.KindTool || ev.Started[0].Label != "read" {
		t.Errorf("started item = %+v", ev.Started[0])
	}

	ev, _ = p.ParseLine(`{"type":"tool_call","subtype":"started","call_id":"c2","tool_call":{"shellToolCall":{"args":{}}}}`)
	if ev.Started[0].Kind != agent.KindCommand || ev.Started[0].Label != "shell" {
		t.Errorf("shell tool should be a command: %+v", ev.Started[0])
	}

	ev, _ = p.ParseLine(`{"type":"tool_call","subtype":"completed","call_id":"c1","tool_call":{"readToolCall":{"result":{}}}}`)
	if len(ev.Finished) != 1 || ev.Finished[0] != "c1" {
		t.Errorf("completed = %+v", ev.Finished)
	}

	// A failed tool call still clears in-flight tracking, and says so.
	ev, _ = p.ParseLine(`{"type":"tool_call","subtype":"failed","call_id":"c1","tool_call":{"readToolCall":{}}}`)
	if len(ev.Finished) != 1 || !strings.Contains(ev.Summary, "failed") {
		t.Errorf("failed tool = %+v", ev)
	}
}

func TestParseResult(t *testing.T) {
	p := Provider{}
	ev, _ := p.ParseLine(`{"type":"result","subtype":"success","is_error":false,"result":"all good","session_id":"s1","duration_ms":12}`)
	if ev.Phase != agent.PhaseCompleted || ev.Result != "all good" {
		t.Errorf("result = %+v", ev)
	}
	if ev.Usage != nil {
		t.Errorf("cursor reports no usage, got %+v", ev.Usage)
	}
	ev, _ = p.ParseLine(`{"type":"result","subtype":"error","is_error":true,"result":"nope"}`)
	if !ev.Failure || ev.Phase != agent.PhaseFailed {
		t.Errorf("error result = %+v", ev)
	}
	if _, ok := p.ParseLine("banner"); ok {
		t.Error("non-json should not parse")
	}
}

func TestAnalyzeAndReview(t *testing.T) {
	a := Provider{}.Analyze([]string{"-p", "review"}, "", true)
	if !a.JSONMode {
		t.Fatalf("print run should be json-monitored: %+v", a)
	}
	v, present := flagValue(a.Args, "--output-format")
	if !present || v != "stream-json" {
		t.Errorf("output-format = %q present=%v", v, present)
	}
	if a := (Provider{}).Analyze([]string{"status"}, "", true); a.JSONMode {
		t.Errorf("non-print run must not be json-monitored: %+v", a)
	}

	args, err := Provider{}.ReviewArgs(agent.ReviewSpec{Scope: agent.ScopeBase, Base: "main"})
	if err != nil || !hasFlag(args, "-p") {
		t.Fatalf("review args = %v (%v)", args, err)
	}
	if !strings.Contains(strings.Join(args, " "), "main") {
		t.Errorf("base ref should reach the prompt: %v", args)
	}
}

func TestDoctorNeedsStatus(t *testing.T) {
	good := func(_ time.Duration, _ string, args ...string) (string, error) {
		if args[0] == "--version" {
			return "2026.1.1", nil
		}
		return "Logged in as a@b.com", nil
	}
	if rep := (Provider{}).Doctor("/bin/cursor-agent", good); !rep.Ready || !rep.HealthOK {
		t.Errorf("doctor (authed) = %+v", rep)
	}

	unauth := func(_ time.Duration, _ string, args ...string) (string, error) {
		if args[0] == "--version" {
			return "2026.1.1", nil
		}
		return "not logged in", errors.New("exit 1")
	}
	if rep := (Provider{}).Doctor("/bin/cursor-agent", unauth); rep.Ready {
		t.Error("doctor should not be ready when status fails")
	}
}
