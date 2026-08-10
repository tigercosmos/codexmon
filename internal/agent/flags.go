package agent

import "strings"

// HasFlag reports whether args contains any of the given flag names, in either
// the bare (`--flag`) or attached (`--flag=value`) form. Providers use it to
// decide whether a flag codexmon wants to inject is already present.
func HasFlag(args []string, names ...string) bool {
	for _, a := range args {
		for _, n := range names {
			if a == n || strings.HasPrefix(a, n+"=") {
				return true
			}
		}
	}
	return false
}

// FlagValue returns the value of a `--name value` or `--name=value` flag and
// whether the flag was present at all.
//
// The FIRST occurrence wins. That differs from how a CLI parses its own argv
// (last wins there), and deliberately so: callers scan the whole arg slice,
// including free-text prompt operands, and a prompt is far more likely to be the
// last argument than a genuinely repeated flag. Preferring the last match would
// let a prompt that merely mentions `--output-format=json` flip codexmon out of
// stream monitoring for a run that really is streaming.
func FlagValue(args []string, name string) (string, bool) {
	for i, a := range args {
		if a == name {
			if i+1 < len(args) {
				return args[i+1], true
			}
			return "", true
		}
		if v, ok := strings.CutPrefix(a, name+"="); ok {
			return v, true
		}
	}
	return "", false
}
