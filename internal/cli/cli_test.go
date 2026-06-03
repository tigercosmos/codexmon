package cli

import (
	"reflect"
	"testing"

	"github.com/tigercosmos/codexmon/internal/job"
)

func TestParseRunArgsSeparator(t *testing.T) {
	cfg, codexArgs, err := parseRunArgs([]string{"--background", "--wall-timeout", "900", "--", "exec", "review", "--uncommitted"})
	if err != nil {
		t.Fatal(err)
	}
	if !cfg.background {
		t.Error("background not set")
	}
	if cfg.thresholds.WallSec != 900 {
		t.Errorf("wall = %v, want 900", cfg.thresholds.WallSec)
	}
	if !reflect.DeepEqual(codexArgs, []string{"exec", "review", "--uncommitted"}) {
		t.Errorf("codexArgs = %v", codexArgs)
	}
}

func TestParseRunArgsImplicitStop(t *testing.T) {
	// First non-monitor token (the codex subcommand) ends flag parsing.
	cfg, codexArgs, err := parseRunArgs([]string{"--heartbeat", "2", "exec", "--json", "hi"})
	if err != nil {
		t.Fatal(err)
	}
	if cfg.thresholds.HeartbeatSec != 2 {
		t.Errorf("heartbeat = %v", cfg.thresholds.HeartbeatSec)
	}
	if !reflect.DeepEqual(codexArgs, []string{"exec", "--json", "hi"}) {
		t.Errorf("codexArgs = %v", codexArgs)
	}
}

func TestParseRunArgsAttachedValue(t *testing.T) {
	cfg, _, err := parseRunArgs([]string{"--wall-timeout=120", "--idle-timeout=30", "--", "exec"})
	if err != nil {
		t.Fatal(err)
	}
	if cfg.thresholds.WallSec != 120 || cfg.thresholds.StalledSec != 30 {
		t.Errorf("attached values not parsed: %+v", cfg.thresholds)
	}
}

func TestParseRunArgsDefaults(t *testing.T) {
	cfg, _, err := parseRunArgs([]string{"--", "exec"})
	if err != nil {
		t.Fatal(err)
	}
	if cfg.thresholds.WallSec != 600 || cfg.thresholds.StalledSec != 180 {
		t.Errorf("defaults not applied: %+v", cfg.thresholds)
	}
}

func TestParseRunArgsZeroDisables(t *testing.T) {
	cfg, _, err := parseRunArgs([]string{"--idle-timeout", "0", "--", "exec"})
	if err != nil {
		t.Fatal(err)
	}
	if cfg.thresholds.StalledSec != 0 {
		t.Errorf("idle-timeout 0 should disable, got %v", cfg.thresholds.StalledSec)
	}
}

func TestParseRunArgsBadValue(t *testing.T) {
	if _, _, err := parseRunArgs([]string{"--wall-timeout", "abc", "--", "exec"}); err == nil {
		t.Error("expected error for non-numeric timeout")
	}
	if _, _, err := parseRunArgs([]string{"--wall-timeout"}); err == nil {
		t.Error("expected error for missing value")
	}
}

func TestParseRunArgsBooleanRejectsValue(t *testing.T) {
	for _, a := range []string{"--background=false", "--json=0", "--no-json=x", "--stdin=1"} {
		if _, _, err := parseRunArgs([]string{a, "--", "exec"}); err == nil {
			t.Errorf("%q should be rejected (boolean flag with a value)", a)
		}
	}
}

func TestParseWaitArgsJSONRejectsValue(t *testing.T) {
	if _, _, _, _, err := parseWaitArgs([]string{"--json=1"}); err == nil {
		t.Error("--json=1 should be rejected")
	}
}

func TestParseWaitArgsNegativeTimeout(t *testing.T) {
	if _, _, _, _, err := parseWaitArgs([]string{"--timeout", "-5"}); err == nil {
		t.Error("negative --timeout should be rejected")
	}
}

func TestSplitFlag(t *testing.T) {
	n, v := splitFlag("--wall-timeout=90")
	if n != "--wall-timeout" || v != "90" {
		t.Errorf("splitFlag attached = %q,%q", n, v)
	}
	n, v = splitFlag("--background")
	if n != "--background" || v != "" {
		t.Errorf("splitFlag bare = %q,%q", n, v)
	}
}

func TestExitCodeFor(t *testing.T) {
	ec := 7
	cases := []struct {
		st   *job.Status
		want int
	}{
		{&job.Status{State: job.StateCompleted}, 0},
		{&job.Status{State: job.StateCancelled}, 130},
		{&job.Status{State: job.StateStalled}, 124},
		{&job.Status{State: job.StateTimeout}, 124},
		{&job.Status{State: job.StateFailed, ExitCode: &ec}, 7},
		{&job.Status{State: job.StateFailed}, 1},
		{&job.Status{State: job.StateRunning}, 1},
	}
	// A forwarded codex exit code must never collide with our sentinels.
	for _, code := range []int{124, 130, exitWaitTimeout} {
		c := code
		if got := exitCodeFor(&job.Status{State: job.StateFailed, ExitCode: &c}); got != 1 {
			t.Errorf("failed with codex exit %d should remap to 1, got %d", code, got)
		}
	}
	for _, c := range cases {
		if got := exitCodeFor(c.st); got != c.want {
			t.Errorf("exitCodeFor(%s) = %d, want %d", c.st.State, got, c.want)
		}
	}
}

func TestSplitIDAndFlags(t *testing.T) {
	id, flags := splitIDAndFlags([]string{"cdx-123", "--json"})
	if id != "cdx-123" || !flags["--json"] {
		t.Errorf("id=%q flags=%v", id, flags)
	}
	id, flags = splitIDAndFlags([]string{"--json"})
	if id != "" || !flags["--json"] {
		t.Errorf("flags-only: id=%q flags=%v", id, flags)
	}
}

func TestParseWaitArgs(t *testing.T) {
	id, timeout, interval, jsonOut, err := parseWaitArgs([]string{"cdx-9", "--timeout", "600", "--json"})
	if err != nil {
		t.Fatal(err)
	}
	if id != "cdx-9" || timeout != 600 || !jsonOut || interval != 2 {
		t.Errorf("id=%q timeout=%v interval=%v json=%v", id, timeout, interval, jsonOut)
	}
	if _, _, _, _, err := parseWaitArgs([]string{"--interval", "0"}); err == nil {
		t.Error("interval 0 should error")
	}
	if _, _, _, _, err := parseWaitArgs([]string{"--bogus"}); err == nil {
		t.Error("unknown flag should error")
	}
}

func TestPrepend(t *testing.T) {
	got := prepend("--", []string{"exec", "review"})
	if !reflect.DeepEqual(got, []string{"--", "exec", "review"}) {
		t.Errorf("prepend = %v", got)
	}
}
