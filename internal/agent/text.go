package agent

import (
	"strconv"
	"strings"
)

// Shorten collapses runs of whitespace in s and truncates it to limit bytes,
// appending an ellipsis when it had to cut. Providers use it to keep one-line
// status summaries tidy.
func Shorten(s string, limit int) string {
	s = strings.TrimSpace(strings.Join(strings.Fields(s), " "))
	if len(s) <= limit {
		return s
	}
	if limit <= 3 {
		return s[:limit]
	}
	return s[:limit-3] + "..."
}

// UsageSummary renders token usage as " (N in / M out tokens)", or "" when nil,
// for appending to a completion summary.
func UsageSummary(u *Usage) string {
	if u == nil {
		return ""
	}
	return " (" + strconv.Itoa(u.InputTokens) + " in / " + strconv.Itoa(u.OutputTokens) + " out tokens)"
}

// FirstNonEmpty returns the first argument that is not empty after trimming, or
// "" if all are empty.
func FirstNonEmpty(vals ...string) string {
	for _, v := range vals {
		if strings.TrimSpace(v) != "" {
			return v
		}
	}
	return ""
}
