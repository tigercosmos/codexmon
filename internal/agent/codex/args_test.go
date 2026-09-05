package codex

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
	if !a.JSONMode {
		t.Fatalf("expected json mode, got %+v", a)
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
	if !hasFlag(a.Args, "--model") {
		t.Errorf("expected default --model injected, got %v", a.Args)
	}
	if !optionRegionsHaveConfig(a.Args, subcommandIndex(a.Args), "model_reasoning_effort") {
		t.Errorf("expected default reasoning effort injected, got %v", a.Args)
	}
	// Global defaults precede exec; --json follows exec before the sub-subcommand.
	if a.Args[4] != "exec" || a.Args[5] != "--json" {
		t.Errorf("--json not injected right after exec: %v", a.Args)
	}
}

func TestAnalyzeExecDefaultsModelAndReasoningEffort(t *testing.T) {
	a := Analyze([]string{"exec", "hi"}, "", false)
	wantPrefix := []string{
		"--model", "gpt-6-astra",
		"--config", "model_reasoning_effort=high",
		"exec",
	}
	if !reflect.DeepEqual(a.Args[:len(wantPrefix)], wantPrefix) {
		t.Fatalf("default args = %v, want prefix %v", a.Args, wantPrefix)
	}
}

func TestApplyEffortSupportsEveryCodexLevel(t *testing.T) {
	for _, effort := range []string{"low", "medium", "high", "xhigh", "max", "ultra"} {
		t.Run(effort, func(t *testing.T) {
			args, err := ApplyEffort([]string{"exec", "hi"}, effort)
			if err != nil {
				t.Fatal(err)
			}
			a := Analyze(args, "", false)
			want := "model_reasoning_effort=" + effort
			if !reflect.DeepEqual(a.Args[:4], []string{"--config", want, "--model", defaultModel}) {
				t.Errorf("configured args = %v, want effort %q and default model", a.Args, effort)
			}
		})
	}
}

func TestApplyEffortRejectsUnsupportedAndDuplicateValues(t *testing.T) {
	if _, err := ApplyEffort([]string{"exec", "hi"}, "none"); err == nil {
		t.Error("unsupported effort should fail")
	}
	if _, err := ApplyEffort([]string{
		"exec", "--config", "model_reasoning_effort=low", "hi",
	}, "max"); err == nil {
		t.Error("duplicate reasoning-effort settings should fail")
	}
	// Config-shaped prompt text is not a real duplicate.
	if _, err := ApplyEffort([]string{
		"exec", "explain", "--config", "model_reasoning_effort=low",
	}, "max"); err != nil {
		t.Errorf("prompt literal was treated as config: %v", err)
	}
}

func TestAnalyzeExecRespectsModelAndReasoningOverrides(t *testing.T) {
	a := Analyze([]string{
		"--model", "custom-model",
		"exec", "--config", "model_reasoning_effort=low", "hi",
	}, "", false)
	if !reflect.DeepEqual(a.Args, []string{
		"--model", "custom-model",
		"exec", "--config", "model_reasoning_effort=low", "hi",
	}) {
		t.Fatalf("caller overrides changed: %v", a.Args)
	}
}

func TestAnalyzeExecRespectsModelConfigOverride(t *testing.T) {
	a := Analyze([]string{"-c", "model=custom-model", "exec", "hi"}, "", false)
	if hasFlag(a.Args, "-m", "--model") {
		t.Fatalf("model config should suppress default --model: %v", a.Args)
	}
	// A caller-chosen model suppresses the reasoning-effort default too, so we
	// never force a setting the chosen model may not support.
	if !reflect.DeepEqual(a.Args, []string{
		"-c", "model=custom-model",
		"exec", "hi",
	}) {
		t.Fatalf("model config override changed: %v", a.Args)
	}
}

func TestAnalyzeExecModelFlagSuppressesReasoningDefault(t *testing.T) {
	a := Analyze([]string{"--model", "custom-model", "exec", "hi"}, "", false)
	if optionRegionsHaveConfig(a.Args, subcommandIndex(a.Args), "model_reasoning_effort") {
		t.Fatalf("explicit model should suppress reasoning-effort default: %v", a.Args)
	}
	if !reflect.DeepEqual(a.Args, []string{"--model", "custom-model", "exec", "hi"}) {
		t.Fatalf("model flag override changed: %v", a.Args)
	}
}

func TestAnalyzeExecProfileSuppressesDefaults(t *testing.T) {
	a := Analyze([]string{"-p", "myprofile", "exec", "hi"}, "", false)
	if hasFlag(a.Args, "-m", "--model") {
		t.Fatalf("profile should suppress default --model: %v", a.Args)
	}
	if optionRegionsHaveConfig(a.Args, subcommandIndex(a.Args), "model_reasoning_effort") {
		t.Fatalf("profile should suppress reasoning-effort default: %v", a.Args)
	}
	if !reflect.DeepEqual(a.Args, []string{"-p", "myprofile", "exec", "hi"}) {
		t.Fatalf("profile run changed: %v", a.Args)
	}
}

func TestAnalyzePromptLiteralsDoNotSuppressDefaults(t *testing.T) {
	a := Analyze([]string{"exec", "explain", "--model", "fake", "--config", "model_reasoning_effort=low"}, "", false)
	if len(a.Args) < 4 || a.Args[0] != "--model" || a.Args[1] != defaultModel ||
		a.Args[2] != "--config" || a.Args[3] != "model_reasoning_effort=high" {
		t.Fatalf("prompt literals suppressed defaults: %v", a.Args)
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
	if !a.JSONMode {
		t.Fatalf("should detect exec subcommand, got %+v", a)
	}
	// Global defaults are inserted before the real exec subcommand.
	if a.Args[6] != "exec" || a.Args[7] != "--json" {
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
	if !a.JSONMode || !hasFlag(a.Args, "--json") {
		t.Fatalf("-a value should not break exec detection: %+v", a)
	}
}

func TestAnalyzePromptTokenDoesNotSuppressInjection(t *testing.T) {
	// A standalone "--json" token belonging to the prompt must NOT stop codexmon
	// from injecting its own --json (which would leave JSONMode on with no stream).
	a := Analyze([]string{"exec", "explain", "--json"}, "/tmp/r", true)
	if a.Args[4] != "exec" || a.Args[5] != "--json" {
		t.Errorf("--json should still be injected after exec: %v", a.Args)
	}
	if !a.JSONMode {
		t.Error("JSONMode should be true")
	}
}

func TestAnalyzeExecAlias(t *testing.T) {
	a := Analyze([]string{"e", "review", "--uncommitted"}, "/tmp/r", true)
	if !a.JSONMode {
		t.Fatalf("`e` alias should be treated as exec: %+v", a)
	}
	if a.Args[4] != "e" || a.Args[5] != "--json" {
		t.Errorf("--json not injected after `e`: %v", a.Args)
	}
	if a.Title != "codex exec review" {
		t.Errorf("title = %q, want 'codex exec review' (alias normalized)", a.Title)
	}
}

func TestAnalyzeNonExecNoJSON(t *testing.T) {
	a := Analyze([]string{"review", "--base", "main"}, "/tmp/result.txt", true)
	if a.JSONMode {
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
