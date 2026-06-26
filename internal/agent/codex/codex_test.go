package codex

import (
	"strings"
	"testing"
	"time"

	"github.com/tigercosmos/codexmon/internal/agent"
)

func TestProviderMeta(t *testing.T) {
	p := Provider{}
	if p.Name() != "codex" || p.BinEnv() != "CODEXMON_CODEX" {
		t.Errorf("meta = %q / %q", p.Name(), p.BinEnv())
	}
	if got := p.BinCandidates(); len(got) != 1 || got[0] != "codex" {
		t.Errorf("candidates = %v", got)
	}
}

func TestParseLineCommandLifecycle(t *testing.T) {
	p := Provider{}
	ev, ok := p.ParseLine(`{"type":"item.started","item":{"id":"c0","type":"command_execution","command":"go test ./..."}}`)
	if !ok || len(ev.Started) != 1 {
		t.Fatalf("started: %+v ok=%v", ev, ok)
	}
	if ev.Started[0].ID != "c0" || ev.Started[0].Kind != agent.KindCommand {
		t.Errorf("started item = %+v", ev.Started[0])
	}
	if ev.Phase != agent.PhaseVerifying {
		t.Errorf("phase = %q, want verifying (go test)", ev.Phase)
	}

	ev, ok = p.ParseLine(`{"type":"item.completed","item":{"id":"c0","type":"command_execution","command":"go test ./...","exit_code":0}}`)
	if !ok || len(ev.Finished) != 1 || ev.Finished[0] != "c0" {
		t.Errorf("finished = %+v", ev.Finished)
	}
}

func TestParseLineToolIsKindTool(t *testing.T) {
	ev, ok := Provider{}.ParseLine(`{"type":"item.started","item":{"id":"t0","type":"mcp_tool_call","server":"s","tool":"slow"}}`)
	if !ok || len(ev.Started) != 1 || ev.Started[0].Kind != agent.KindTool {
		t.Fatalf("tool item = %+v ok=%v", ev, ok)
	}
	if ev.Started[0].Label != "s/slow" {
		t.Errorf("label = %q, want s/slow", ev.Started[0].Label)
	}
}

func TestParseLineResultAndReview(t *testing.T) {
	p := Provider{}
	ev, _ := p.ParseLine(`{"type":"item.completed","item":{"id":"m0","type":"agent_message","text":"the answer"}}`)
	if ev.Result != "the answer" {
		t.Errorf("agent_message result = %q", ev.Result)
	}
	ev, _ = p.ParseLine(`{"type":"item.completed","item":{"id":"r0","type":"exited_review_mode","review":"found 1 issue"}}`)
	if ev.Result != "found 1 issue" {
		t.Errorf("review result = %q", ev.Result)
	}
	if len(ev.Finished) != 1 || ev.Finished[0] != "r0" {
		t.Errorf("review item should also finish: %+v", ev.Finished)
	}
}

func TestParseLineFailureAndUsage(t *testing.T) {
	p := Provider{}
	ev, _ := p.ParseLine(`{"type":"error","message":"boom"}`)
	if !ev.Failure || !strings.Contains(ev.FailMsg, "boom") {
		t.Errorf("error event = %+v", ev)
	}
	ev, _ = p.ParseLine(`{"type":"turn.completed","usage":{"input_tokens":12,"output_tokens":3}}`)
	if ev.Usage == nil || ev.Usage.InputTokens != 12 {
		t.Errorf("usage = %+v", ev.Usage)
	}
	ev, _ = p.ParseLine(`{"type":"thread.started","thread_id":"thr"}`)
	if ev.ThreadID != "thr" {
		t.Errorf("thread = %q", ev.ThreadID)
	}
	if _, ok := p.ParseLine("not json"); ok {
		t.Error("non-json should not parse")
	}
}

func TestProviderReviewArgs(t *testing.T) {
	p := Provider{}
	args, err := p.ReviewArgs(agent.ReviewSpec{Scope: agent.ScopeUncommitted})
	if err != nil || strings.Join(args, " ") != "exec review --uncommitted" {
		t.Errorf("uncommitted = %v (%v)", args, err)
	}
	args, err = p.ReviewArgs(agent.ReviewSpec{Scope: agent.ScopeBase, Base: "main"})
	if err != nil || strings.Join(args, " ") != "exec review --base main" {
		t.Errorf("base = %v (%v)", args, err)
	}
	if _, err := p.ReviewArgs(agent.ReviewSpec{Scope: agent.ScopeBase}); err == nil {
		t.Error("base with no ref should error")
	}
}

func TestProviderDoctor(t *testing.T) {
	run := func(_ time.Duration, _ string, args ...string) (string, error) {
		switch {
		case len(args) == 1 && args[0] == "--version":
			return "codex-cli 1.2.3\n", nil
		case len(args) >= 1 && args[0] == "doctor":
			return `{"ok":true}`, nil
		}
		return "", nil
	}
	rep := Provider{}.Doctor("/bin/codex", run)
	if !rep.Ready || rep.Version != "codex-cli 1.2.3" || !rep.HealthOK {
		t.Errorf("doctor report = %+v", rep)
	}
	if string(rep.Detail) != `{"ok":true}` {
		t.Errorf("detail = %s", rep.Detail)
	}

	// A timed-out doctor must not report ready.
	runTimeout := func(_ time.Duration, _ string, args ...string) (string, error) {
		if args[0] == "--version" {
			return "codex-cli 1.2.3", nil
		}
		return "", agent.ErrTimeout
	}
	if rep := (Provider{}).Doctor("/bin/codex", runTimeout); rep.Ready {
		t.Error("timed-out doctor should not be ready")
	}
}
