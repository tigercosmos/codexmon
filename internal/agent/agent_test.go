package agent

import (
	"strings"
	"testing"
	"time"
	"unicode/utf8"
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

// Shorten must cut on a rune boundary: slicing mid-rune leaves a mangled byte
// that renders as U+FFFD in status output and in status.json.
func TestShortenDoesNotSplitRunes(t *testing.T) {
	// Each "é" is two bytes, so most limits land mid-rune.
	s := strings.Repeat("é", 40)
	for limit := 1; limit < len(s); limit++ {
		got := Shorten(s, limit)
		if len(got) > limit {
			t.Fatalf("Shorten(limit=%d) returned %d bytes", limit, len(got))
		}
		if !utf8.ValidString(got) {
			t.Fatalf("Shorten(limit=%d) produced invalid UTF-8: %q", limit, got)
		}
	}
	// A multi-byte string that fits is returned untouched.
	if got := Shorten("héllo", 32); got != "héllo" {
		t.Errorf("Shorten should leave a short string alone, got %q", got)
	}
}

// The first occurrence wins, so a prompt operand that merely mentions a flag
// cannot override the real one the caller passed ahead of it.
func TestFlagValueFirstOccurrenceWins(t *testing.T) {
	args := []string{"-p", "--output-format", "stream-json", "explain --output-format=json to me"}
	if v, present := FlagValue(args, "--output-format"); !present || v != "stream-json" {
		t.Errorf("FlagValue = (%q, %v), want (stream-json, true)", v, present)
	}
	if v, present := FlagValue([]string{"--model=a", "--model=b"}, "--model"); !present || v != "a" {
		t.Errorf("attached form should also first-win, got %q", v)
	}
	if _, present := FlagValue([]string{"-p"}, "--model"); present {
		t.Error("absent flag should report not present")
	}
	// A trailing flag with no value is present but empty.
	if v, present := FlagValue([]string{"-p", "--model"}, "--model"); !present || v != "" {
		t.Errorf("trailing flag = (%q, %v), want (\"\", true)", v, present)
	}
}

func TestHasFlagFormsAndDecodeBlocks(t *testing.T) {
	if !HasFlag([]string{"--output-format=json"}, "--output-format") {
		t.Error("attached form should count as present")
	}
	if !HasFlag([]string{"-p"}, "-p", "--print") {
		t.Error("any listed alias should count as present")
	}
	if HasFlag([]string{"--printer"}, "--print") {
		t.Error("a longer flag with the same prefix is a different flag")
	}
	blocks := DecodeBlocks([]byte(`{"content":[{"type":"tool_use","id":"t1","name":"Bash"}]}`))
	if len(blocks) != 1 || blocks[0].ID != "t1" || blocks[0].Name != "Bash" {
		t.Errorf("tool_use block not decoded: %+v", blocks)
	}
	if got := DecodeBlocks([]byte(`{"content":"bare"}`)); len(got) != 1 || got[0].Text != "bare" {
		t.Errorf("bare string content should decode as one text block, got %+v", got)
	}
	if got := DecodeBlocks([]byte(`{"content":7}`)); got != nil {
		t.Errorf("unusable content should decode to nothing, got %+v", got)
	}
	if got := DecodeBlocks([]byte(`not json`)); got != nil {
		t.Errorf("invalid JSON should decode to nothing, got %+v", got)
	}
}
