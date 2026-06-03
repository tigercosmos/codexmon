package codexcli

import (
	"reflect"
	"testing"
)

func TestSubcommand(t *testing.T) {
	cases := []struct {
		args []string
		want string
	}{
		{[]string{"exec", "review", "--uncommitted"}, "exec"},
		{[]string{"review", "--base", "main"}, "review"},
		{[]string{"-c", "model=o3", "exec", "hi"}, "exec"},  // -c consumes its value
		{[]string{"-C", "/tmp", "exec"}, "exec"},            // -C consumes its value
		{[]string{"--config=foo", "exec"}, "exec"},          // attached value, no skip
		{[]string{"-m", "gpt-5", "exec", "review"}, "exec"}, // -m consumes value
		{[]string{}, ""},                 // bare codex
		{[]string{"--help"}, "--help"},   // boolean flag stops? no: --help isn't positional
		{[]string{"--", "exec"}, "exec"}, // after --
		{[]string{"login"}, "login"},
	}
	for _, c := range cases {
		got := Subcommand(c.args)
		// --help is a flag, not a positional; Subcommand should skip it and find none.
		if reflect.DeepEqual(c.args, []string{"--help"}) {
			if got != "" {
				t.Errorf("Subcommand(--help) = %q, want empty", got)
			}
			continue
		}
		if got != c.want {
			t.Errorf("Subcommand(%v) = %q, want %q", c.args, got, c.want)
		}
	}
}

func TestAnalyzeExecInjectsJSON(t *testing.T) {
	a := Analyze([]string{"exec", "review", "--uncommitted"}, "/tmp/result.txt", true)
	if !a.IsExec || !a.JSONMode {
		t.Fatalf("expected exec+json, got %+v", a)
	}
	if !hasFlag(a.Args, "--json") {
		t.Errorf("expected --json injected, got %v", a.Args)
	}
	if !hasFlag(a.Args, "--output-last-message") {
		t.Errorf("expected --output-last-message injected, got %v", a.Args)
	}
	if a.Title != "codex exec review" {
		t.Errorf("title = %q, want %q", a.Title, "codex exec review")
	}
	// --json must be placed right after exec, before the review sub-subcommand.
	if a.Args[0] != "exec" || a.Args[1] != "--json" {
		t.Errorf("--json not injected right after exec: %v", a.Args)
	}
}

func TestAnalyzeExecPromptTitle(t *testing.T) {
	a := Analyze([]string{"exec", "Reply with PONG"}, "/tmp/r", true)
	if a.Title != "codex exec" {
		t.Errorf("a free-text prompt should not become a subcommand; title=%q", a.Title)
	}
}

func TestAnalyzeDoesNotDuplicateFlags(t *testing.T) {
	a := Analyze([]string{"exec", "--json", "-o", "/my/out", "hi"}, "/tmp/result.txt", true)
	count := 0
	for _, x := range a.Args {
		if x == "--json" {
			count++
		}
	}
	if count != 1 {
		t.Errorf("--json duplicated: %v", a.Args)
	}
	if hasFlag(a.Args, "--output-last-message") {
		t.Errorf("should not inject -o when -o already present: %v", a.Args)
	}
}

func TestAnalyzeInjectionPositionWithExecValue(t *testing.T) {
	// A flag value equal to "exec" before the real subcommand must not fool the
	// injector: --json must land after the actual exec subcommand token.
	a := Analyze([]string{"-c", "exec", "exec", "prompt"}, "/tmp/r", true)
	if !a.IsExec {
		t.Fatalf("should detect exec subcommand, got %+v", a)
	}
	// args: [-c exec exec --json --output-last-message /tmp/r prompt]
	if a.Args[2] != "exec" || a.Args[3] != "--json" {
		t.Errorf("--json injected at wrong position: %v", a.Args)
	}
	if a.Title != "codex exec" {
		t.Errorf("title = %q, want 'codex exec'", a.Title)
	}
}

func TestSubcommandSkipsApprovalValue(t *testing.T) {
	// -a/--ask-for-approval takes a value; "never" must not be read as the subcommand.
	if got := Subcommand([]string{"-a", "never", "exec", "hi"}); got != "exec" {
		t.Errorf("Subcommand with -a = %q, want exec", got)
	}
	if got := Subcommand([]string{"--ask-for-approval", "on-request", "exec"}); got != "exec" {
		t.Errorf("Subcommand with --ask-for-approval = %q, want exec", got)
	}
}

func TestAnalyzeApprovalFlagStillDetectsExec(t *testing.T) {
	a := Analyze([]string{"-a", "never", "exec", "hi"}, "/tmp/r", true)
	if !a.IsExec || !a.JSONMode || !hasFlag(a.Args, "--json") {
		t.Fatalf("-a value should not break exec detection: %+v", a)
	}
}

func TestAnalyzePromptTokenDoesNotSuppressInjection(t *testing.T) {
	// A standalone "--json" token belonging to the prompt must NOT stop codexmon
	// from injecting its own --json (which would leave JSONMode on with no stream).
	a := Analyze([]string{"exec", "explain", "--json"}, "/tmp/r", true)
	if a.Args[0] != "exec" || a.Args[1] != "--json" {
		t.Errorf("--json should still be injected after exec: %v", a.Args)
	}
	if !a.JSONMode {
		t.Error("JSONMode should be true")
	}
}

func TestAnalyzeExecAlias(t *testing.T) {
	a := Analyze([]string{"e", "review", "--uncommitted"}, "/tmp/r", true)
	if !a.IsExec || !a.JSONMode {
		t.Fatalf("`e` alias should be treated as exec: %+v", a)
	}
	if a.Args[0] != "e" || a.Args[1] != "--json" {
		t.Errorf("--json not injected after `e`: %v", a.Args)
	}
	if a.Title != "codex exec review" {
		t.Errorf("title = %q, want 'codex exec review' (alias normalized)", a.Title)
	}
}

func TestAnalyzeNonExecNoJSON(t *testing.T) {
	a := Analyze([]string{"review", "--base", "main"}, "/tmp/result.txt", true)
	if a.IsExec || a.JSONMode {
		t.Fatalf("review is not exec; got %+v", a)
	}
	if hasFlag(a.Args, "--json") {
		t.Errorf("must not inject --json for non-exec: %v", a.Args)
	}
	if a.Title != "codex review" {
		t.Errorf("title = %q", a.Title)
	}
}

func TestAnalyzeRespectsNoJSON(t *testing.T) {
	a := Analyze([]string{"exec", "hi"}, "/tmp/result.txt", false)
	if a.JSONMode {
		t.Errorf("allowJSON=false must disable json mode")
	}
	if hasFlag(a.Args, "--json") {
		t.Errorf("must not inject --json when disabled: %v", a.Args)
	}
}

func TestIsExecSubcommand(t *testing.T) {
	for _, s := range []string{"review", "resume", "help"} {
		if !isExecSubcommand(s) {
			t.Errorf("%q should be an exec subcommand", s)
		}
	}
	for _, s := range []string{"", "Reply with PONG", "ls"} {
		if isExecSubcommand(s) {
			t.Errorf("%q should not be an exec subcommand", s)
		}
	}
}
