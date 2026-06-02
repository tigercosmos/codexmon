// Package codexcli locates the codex binary and analyzes the arguments handed
// through to it, deciding whether the run can be monitored via the structured
// `--json` event stream.
package codexcli

import (
	"os"
	"os/exec"
	"strings"
)

// Resolve finds the codex executable. Order: $CODEXMON_CODEX, then PATH.
func Resolve() (string, error) {
	if bin := strings.TrimSpace(os.Getenv("CODEXMON_CODEX")); bin != "" {
		return bin, nil
	}
	return exec.LookPath("codex")
}

// flagsTakingValue is the set of codex flags whose following token is a value
// (so the subcommand scanner must skip it). Covers global + common exec flags.
var flagsTakingValue = map[string]bool{
	"-c": true, "--config": true,
	"-C": true, "--cd": true,
	"--add-dir":        true,
	"--local-provider": true,
	"-p":               true, "--profile": true,
	"--profile-v2": true,
	"--enable":     true, "--disable": true,
	"--remote": true, "--remote-auth-token-env": true,
	"-m": true, "--model": true,
	"-i": true, "--image": true,
	"-s": true, "--sandbox": true,
	"--output-schema": true,
	"--color":         true,
	"-o":              true, "--output-last-message": true,
}

// Subcommand returns the first positional token in args — codex's subcommand
// (e.g. "exec", "review", "login") — skipping flags and their values. Returns
// "" if there is no positional (bare `codex`, which opens the TUI).
func Subcommand(args []string) string {
	idx := subcommandIndex(args)
	if idx < 0 {
		return ""
	}
	return args[idx]
}

// subcommandIndex returns the index of the first positional token (the codex
// subcommand), skipping flags and their values, or -1 if there is none.
func subcommandIndex(args []string) int {
	for i := 0; i < len(args); i++ {
		a := args[i]
		if a == "--" {
			if i+1 < len(args) {
				return i + 1
			}
			return -1
		}
		if strings.HasPrefix(a, "-") && a != "-" {
			// `--flag=value` is self-contained; otherwise it may consume next token.
			if !strings.Contains(a, "=") && flagsTakingValue[a] {
				i++
			}
			continue
		}
		return i
	}
	return -1
}

// Analysis is the outcome of inspecting the passthrough args.
type Analysis struct {
	IsExec   bool     // the codex subcommand is `exec` (supports --json)
	JSONMode bool     // codexmon will parse a JSON event stream
	Args     []string // possibly-augmented args to pass to codex
	Title    string   // short human label, e.g. "codex exec review"
}

func hasFlag(args []string, names ...string) bool {
	want := map[string]bool{}
	for _, n := range names {
		want[n] = true
	}
	for _, a := range args {
		if want[a] {
			return true
		}
		if eq := strings.IndexByte(a, '='); eq > 0 && want[a[:eq]] {
			return true
		}
	}
	return false
}

// Analyze decides how to run codex. When the subcommand is `exec` and JSON is
// not disabled, it injects `--json` (for event monitoring) and, unless the
// caller already set one, `--output-last-message <resultFile>` as a reliable
// final-answer backup. Injected flags are placed right after the `exec` token,
// where they apply to exec and any of its sub-subcommands (review/resume).
func Analyze(args []string, resultFile string, allowJSON bool) Analysis {
	subIdx := subcommandIndex(args)
	sub := ""
	if subIdx >= 0 {
		sub = args[subIdx]
	}
	isExec := sub == "exec"
	title := "codex"
	if sub != "" {
		title = "codex " + sub
		// `exec` has its own sub-subcommands; surface them in the title, but
		// don't mistake a free-text prompt for one.
		if isExec {
			if next := Subcommand(args[subIdx+1:]); isExecSubcommand(next) {
				title = "codex exec " + next
			}
		}
	}

	out := append([]string(nil), args...)
	jsonMode := false
	if isExec && allowJSON {
		var inject []string
		if !hasFlag(args, "--json") {
			inject = append(inject, "--json")
		}
		if resultFile != "" && !hasFlag(args, "-o", "--output-last-message") {
			inject = append(inject, "--output-last-message", resultFile)
		}
		if len(inject) > 0 {
			// Inject right after the (flag-aware) exec token, not the first
			// literal "exec", which could be a flag value or earlier positional.
			out = injectAt(out, subIdx+1, inject)
		}
		jsonMode = true
	}

	return Analysis{IsExec: isExec, JSONMode: jsonMode, Args: out, Title: title}
}

// isExecSubcommand reports whether tok is one of `codex exec`'s sub-subcommands
// (as opposed to a free-text prompt).
func isExecSubcommand(tok string) bool {
	switch tok {
	case "review", "resume", "help":
		return true
	default:
		return false
	}
}

// injectAt inserts extra at index pos (clamped to the slice bounds).
func injectAt(args []string, pos int, extra []string) []string {
	if pos < 0 {
		pos = 0
	}
	if pos > len(args) {
		pos = len(args)
	}
	out := make([]string, 0, len(args)+len(extra))
	out = append(out, args[:pos]...)
	out = append(out, extra...)
	out = append(out, args[pos:]...)
	return out
}
