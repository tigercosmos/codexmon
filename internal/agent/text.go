package agent

import (
	"strconv"
	"strings"
)

// Shorten collapses runs of whitespace in s and truncates it to limit bytes,
// appending an ellipsis when it had to cut. Providers use it to keep one-line
// status summaries tidy.
//
// The cut lands on a UTF-8 rune boundary: slicing mid-rune would leave a
// mangled byte that renders as U+FFFD in status output and in status.json.
func Shorten(s string, limit int) string {
	s = strings.TrimSpace(strings.Join(strings.Fields(s), " "))
	if len(s) <= limit {
		return s
	}
	if limit <= 3 {
		return truncRunes(s, limit)
	}
	return truncRunes(s, limit-3) + "..."
}

// CutBytes returns the longest prefix of s that is at most limit bytes and does
// not split a UTF-8 rune. Unlike Shorten it leaves whitespace and line structure
// alone, so it suits truncating raw output (previews, result text) rather than
// one-line summaries.
func CutBytes(s string, limit int) string { return truncRunes(s, limit) }

// truncRunes returns the longest prefix of s that is at most n bytes and does
// not split a rune.
func truncRunes(s string, n int) string {
	if n <= 0 {
		return ""
	}
	if n >= len(s) {
		return s
	}
	// Back off the cut point while it sits on a UTF-8 continuation byte
	// (0b10xxxxxx), which can only be the interior of a multi-byte rune.
	for n > 0 && s[n]&0xC0 == 0x80 {
		n--
	}
	return s[:n]
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
