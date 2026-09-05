package codex

import (
	"errors"
	"fmt"
	"strings"

	"github.com/tigercosmos/codexmon/internal/agent"
)

const (
	defaultModel           = "gpt-6-astra"
	defaultReasoningEffort = "high"
)

// flagsTakingValue is the set of codex flags whose following token is a value
// (so the subcommand scanner must skip it). Covers global + common exec flags.
var flagsTakingValue = map[string]bool{
	"-c": true, "--config": true,
	"-C": true, "--cd": true,
	"--add-dir":        true,
	"--local-provider": true,
	"-p":               true, "--profile": true,
	"--profile-v2": true,
	"-a":           true, "--ask-for-approval": true,
	"--enable": true, "--disable": true,
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

// ApplyEffort adds a caller-selected reasoning effort as a global Codex config
// option. The values match the effort levels in the Codex model catalog.
func ApplyEffort(args []string, effort string) ([]string, error) {
	effort = strings.TrimSpace(effort)
	if !isSupportedEffort(effort) {
		return nil, fmt.Errorf("unsupported Codex effort %q (want low, medium, high, xhigh, max, or ultra)", effort)
	}
	if configIsSet(args, "model_reasoning_effort") {
		return nil, errors.New("--effort cannot be combined with -c/--config model_reasoning_effort")
	}

	out := append([]string(nil), args...)
	idx := subcommandIndex(out)
	if idx < 0 {
		idx = len(out)
	}
	return injectAt(out, idx, []string{"--config", "model_reasoning_effort=" + effort}), nil
}

func isSupportedEffort(effort string) bool {
	switch effort {
	case "low", "medium", "high", "xhigh", "max", "ultra":
		return true
	default:
		return false
	}
}

func configIsSet(args []string, key string) bool {
	subIdx := subcommandIndex(args)
	if subIdx < 0 {
		return optionsHaveConfig(args, key, false)
	}
	if optionsHaveConfig(args[:subIdx], key, false) {
		return true
	}
	return optionsHaveConfig(args[subIdx+1:], key, isExecToken(args[subIdx]))
}

// Analyze decides how to run codex. For `exec`, it defaults the model (and,
// only when it also picks the model, the reasoning effort) unless the caller
// supplied that setting via -m/--model, -c/--config, or a -p/--profile. When JSON is not
// disabled, it also injects `--json` (for event monitoring) and, unless the
// caller already set one, `--output-last-message <resultFile>` as a reliable
// final-answer backup. Exec-specific flags are placed right after the `exec`
// token, where they apply to exec and any of its sub-subcommands (review/resume).
func Analyze(args []string, resultFile string, allowJSON bool) agent.Analysis {
	subIdx := subcommandIndex(args)
	sub := ""
	if subIdx >= 0 {
		sub = args[subIdx]
	}
	isExec := isExecToken(sub)
	title := "codex"
	if sub != "" {
		label := sub
		if isExec {
			label = "exec" // normalize the `e` alias to `exec` in the title
		}
		title = "codex " + label
		// `exec` has its own sub-subcommands; surface them in the title, but
		// don't mistake a free-text prompt for one.
		if isExec {
			if next := Subcommand(args[subIdx+1:]); isExecSubcommand(next) {
				title = "codex exec " + next
			}
		}
	}

	out := append([]string(nil), args...)
	if isExec {
		var defaults []string
		// A profile (-p) layers a caller-chosen config (model, reasoning effort,
		// and more) on top of the base config, so treat its presence as the
		// caller managing both settings and inject nothing.
		hasProfile := optionRegionsHaveFlag(args, subIdx, "-p", "--profile", "--profile-v2")
		modelSet := hasProfile ||
			optionRegionsHaveFlag(args, subIdx, "-m", "--model") ||
			optionRegionsHaveConfig(args, subIdx, "model")
		if !modelSet {
			defaults = append(defaults, "--model", defaultModel)
		}
		// Only impose the reasoning-effort default when we also pick the model:
		// a caller-chosen model may not support this setting, and an explicit
		// reasoning config (or profile) should win.
		if !modelSet && !optionRegionsHaveConfig(args, subIdx, "model_reasoning_effort") {
			defaults = append(defaults, "--config", "model_reasoning_effort="+defaultReasoningEffort)
		}
		if len(defaults) > 0 {
			// Model and config are global Codex options, so keep them before exec.
			out = injectAt(out, subIdx, defaults)
			subIdx += len(defaults)
		}
	}
	if isExec && allowJSON {
		var inject []string
		// Scope the "already present?" check to the exec option region so a
		// prompt or flag value that merely equals "--json"/"-o" can't suppress
		// real injection (which would leave JSONMode on but no JSON stream).
		if !execOptionsHaveFlag(out, subIdx, "--json") {
			inject = append(inject, "--json")
		}
		if resultFile != "" && !execOptionsHaveFlag(out, subIdx, "-o", "--output-last-message") {
			inject = append(inject, "--output-last-message", resultFile)
		}
		if len(inject) > 0 {
			// Inject right after the (flag-aware) exec token, not the first
			// literal "exec", which could be a flag value or earlier positional.
			out = injectAt(out, subIdx+1, inject)
		}
	}

	// JSONMode reflects reality: monitor the event stream only when --json is
	// actually present in the final args (whether we injected it or it was
	// already there), never merely because the subcommand is exec.
	jsonMode := isExec && allowJSON && hasFlag(out, "--json")
	return agent.Analysis{JSONMode: jsonMode, Args: out, Title: title}
}

// optionRegionsHaveFlag checks the global option region before the subcommand
// and, for exec, its option region after the subcommand. Prompt text is ignored.
func optionRegionsHaveFlag(args []string, subIdx int, names ...string) bool {
	if hasFlag(args[:subIdx], names...) {
		return true
	}
	return execOptionsHaveFlag(args, subIdx, names...)
}

// optionRegionsHaveConfig reports whether a real -c/--config option sets key.
// It checks both global and exec option regions without treating prompt text as
// configuration.
func optionRegionsHaveConfig(args []string, subIdx int, key string) bool {
	if optionsHaveConfig(args[:subIdx], key, false) {
		return true
	}
	return optionsHaveConfig(args[subIdx+1:], key, true)
}

func optionsHaveConfig(args []string, key string, allowExecSubcommand bool) bool {
	for i := 0; i < len(args); i++ {
		a := args[i]
		if a == "--" {
			return false
		}
		if !strings.HasPrefix(a, "-") || a == "-" {
			if allowExecSubcommand && isExecSubcommand(a) {
				continue
			}
			return false
		}

		name, value, attached := a, "", false
		if eq := strings.IndexByte(a, '='); eq > 0 {
			name, value, attached = a[:eq], a[eq+1:], true
		}
		if name == "-c" || name == "--config" {
			if !attached && i+1 < len(args) {
				i++
				value = args[i]
			}
			if configKey(value) == key {
				return true
			}
			continue
		}
		if !attached && flagsTakingValue[name] {
			i++
		}
	}
	return false
}

func configKey(value string) string {
	key, _, _ := strings.Cut(value, "=")
	return strings.TrimSpace(key)
}

// execOptionsHaveFlag reports whether any of names appears as a real flag in the
// exec option region (after the exec token). It skips flag values and stops at a
// free-text prompt, so prompt words and option values are never mistaken for
// flags. Exec sub-subcommands (review/resume) are stepped over so their flags
// are still considered.
func execOptionsHaveFlag(args []string, subIdx int, names ...string) bool {
	want := make(map[string]bool, len(names))
	for _, n := range names {
		want[n] = true
	}
	for i := subIdx + 1; i < len(args); i++ {
		a := args[i]
		if a == "--" {
			return false // everything after -- is positional
		}
		if !strings.HasPrefix(a, "-") || a == "-" {
			if isExecSubcommand(a) {
				continue // a sub-subcommand; its flags follow
			}
			return false // reached the free-text prompt
		}
		name := a
		if eq := strings.IndexByte(a, '='); eq > 0 {
			name = a[:eq]
		}
		if want[name] {
			return true
		}
		if !strings.Contains(a, "=") && flagsTakingValue[name] {
			i++ // skip this flag's value
		}
	}
	return false
}

// isExecToken reports whether tok is the codex `exec` subcommand or its alias `e`.
func isExecToken(tok string) bool {
	return tok == "exec" || tok == "e"
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
