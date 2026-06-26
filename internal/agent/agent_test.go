package agent

import (
	"strings"
	"testing"
	"time"
)

// fakeProvider is a minimal Provider for registry/resolve tests.
type fakeProvider struct {
	name       string
	candidates []string
}

func (f fakeProvider) Name() string            { return f.name }
func (f fakeProvider) BinEnv() string          { return "CODEXMON_FAKE" }
func (f fakeProvider) BinCandidates() []string { return f.candidates }
func (f fakeProvider) Analyze(args []string, _ string, _ bool) Analysis {
	return Analysis{Args: args, Title: f.name}
}
func (f fakeProvider) ReviewArgs(ReviewSpec) ([]string, error) { return []string{"review"}, nil }
func (f fakeProvider) ParseLine(string) (Event, bool)          { return Event{}, false }
func (f fakeProvider) Doctor(string, RunFunc) DoctorReport     { return DoctorReport{Agent: f.name} }

func TestRegistryGetAndNames(t *testing.T) {
	Register(fakeProvider{name: "testfake", candidates: []string{"go"}})

	p, err := Get("testfake")
	if err != nil || p.Name() != "testfake" {
		t.Fatalf("Get(testfake) = %v, %v", p, err)
	}
	if _, err := Get("nope"); err == nil {
		t.Error("Get(nope) should error")
	}
	found := false
	for _, n := range Names() {
		if n == "testfake" {
			found = true
		}
	}
	if !found {
		t.Errorf("Names() = %v, missing testfake", Names())
	}
}

func TestRegisterDuplicatePanics(t *testing.T) {
	Register(fakeProvider{name: "dupe"})
	defer func() {
		if recover() == nil {
			t.Error("registering a duplicate name should panic")
		}
	}()
	Register(fakeProvider{name: "dupe"})
}

func TestResolveBinOverrideAndEnv(t *testing.T) {
	p := fakeProvider{name: "rb", candidates: []string{"definitely-not-a-real-binary-xyz"}}

	if got, err := ResolveBin(p, "/explicit/path"); err != nil || got != "/explicit/path" {
		t.Errorf("override: got %q, %v", got, err)
	}

	t.Setenv("CODEXMON_FAKE", "/from/env")
	if got, err := ResolveBin(p, ""); err != nil || got != "/from/env" {
		t.Errorf("env: got %q, %v", got, err)
	}
	// An explicit override still beats the env var.
	if got, _ := ResolveBin(p, "/override"); got != "/override" {
		t.Errorf("override should beat env, got %q", got)
	}
}

func TestResolveBinPathLookup(t *testing.T) {
	p := fakeProvider{name: "rb2", candidates: []string{"definitely-missing-xyz", "go"}}
	got, err := ResolveBin(p, "")
	if err != nil {
		t.Fatalf("ResolveBin should find `go` on PATH: %v", err)
	}
	if !strings.HasSuffix(got, "go") {
		t.Errorf("resolved %q, want a path ending in `go`", got)
	}
}

func TestResolveBinNotFound(t *testing.T) {
	p := fakeProvider{name: "rb3", candidates: []string{"definitely-missing-xyz"}}
	_, err := ResolveBin(p, "")
	if err == nil {
		t.Fatal("ResolveBin should error when no candidate is on PATH")
	}
	// The error should name the candidates tried and the env override.
	if !strings.Contains(err.Error(), "definitely-missing-xyz") || !strings.Contains(err.Error(), p.BinEnv()) {
		t.Errorf("error should name candidates and env var: %v", err)
	}
}

func TestReviewPrompt(t *testing.T) {
	unc := ReviewPrompt(ReviewSpec{Scope: ScopeUncommitted})
	if !strings.Contains(unc, "uncommitted") || !strings.Contains(unc, "read-only") {
		t.Errorf("uncommitted prompt = %q", unc)
	}
	base := ReviewPrompt(ReviewSpec{Scope: ScopeBase, Base: "main"})
	if !strings.Contains(base, "main") || !strings.Contains(base, "git diff main...HEAD") {
		t.Errorf("base prompt = %q", base)
	}
}

func TestShortenAndUsage(t *testing.T) {
	if Shorten("a   b\tc", 100) != "a b c" {
		t.Errorf("Shorten should collapse whitespace: %q", Shorten("a   b\tc", 100))
	}
	long := strings.Repeat("x", 50)
	if got := Shorten(long, 10); len(got) != 10 || !strings.HasSuffix(got, "...") {
		t.Errorf("Shorten truncation = %q", got)
	}
	if UsageSummary(nil) != "" {
		t.Error("nil usage should render empty")
	}
	if got := UsageSummary(&Usage{InputTokens: 5, OutputTokens: 2}); !strings.Contains(got, "5 in") || !strings.Contains(got, "2 out") {
		t.Errorf("UsageSummary = %q", got)
	}
	if FirstNonEmpty("", "  ", "x") != "x" {
		t.Error("FirstNonEmpty should skip blanks")
	}
}

// Ensure the RunFunc type is usable as a value (compile-time sanity for the
// Doctor contract).
var _ RunFunc = func(time.Duration, string, ...string) (string, error) { return "", nil }
