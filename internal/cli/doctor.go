package cli

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"time"

	"github.com/tigercosmos/codexmon/internal/codexcli"
)

// doctorReport summarizes whether codex is installed and usable.
type doctorReport struct {
	Ready      bool            `json:"ready"`
	CodexBin   string          `json:"codex_bin"`
	Version    string          `json:"version,omitempty"`
	DoctorOK   bool            `json:"doctor_ok"`
	Doctor     json.RawMessage `json:"doctor,omitempty"`
	DoctorText string          `json:"doctor_text,omitempty"`
	Problems   []string        `json:"problems,omitempty"`
}

func cmdDoctor(args []string) int {
	_, flags := splitIDAndFlags(args)
	jsonOut := flags["--json"]

	rep := doctorReport{}
	bin, err := codexcli.Resolve()
	if err != nil {
		rep.Problems = append(rep.Problems, "codex CLI not found on PATH (install it or set CODEXMON_CODEX)")
		return emitDoctor(rep, jsonOut)
	}
	rep.CodexBin = bin

	if out, err := runCapture(5*time.Second, bin, "--version"); err == nil {
		rep.Version = strings.TrimSpace(out)
	} else if err == context.DeadlineExceeded {
		rep.Problems = append(rep.Problems, "`codex --version` timed out — the codex install may be wedged")
	} else {
		rep.Problems = append(rep.Problems, "`codex --version` failed: "+strings.TrimSpace(out))
	}

	// `codex doctor --json` covers install/config/auth/runtime health. We bound
	// it with a timeout because a stuck environment is exactly what we report.
	out, derr := runCapture(30*time.Second, bin, "doctor", "--json")
	switch {
	case derr == context.DeadlineExceeded:
		rep.Problems = append(rep.Problems, "`codex doctor` timed out after 30s — Codex is not responding")
		rep.DoctorText = strings.TrimSpace(out)
	case derr != nil:
		rep.Problems = append(rep.Problems, "`codex doctor` failed")
		rep.DoctorText = strings.TrimSpace(out)
	default:
		rep.DoctorOK = true
		if trimmed := strings.TrimSpace(out); strings.HasPrefix(trimmed, "{") || strings.HasPrefix(trimmed, "[") {
			rep.Doctor = json.RawMessage(trimmed)
		} else {
			rep.DoctorText = trimmed
		}
	}

	rep.Ready = rep.Version != "" && rep.DoctorOK && len(rep.Problems) == 0
	return emitDoctor(rep, jsonOut)
}

func emitDoctor(rep doctorReport, jsonOut bool) int {
	if jsonOut {
		printJSON(rep)
	} else {
		icon := "✅"
		if !rep.Ready {
			icon = "❌"
		}
		fmt.Printf("%s codex doctor\n", icon)
		if rep.CodexBin != "" {
			fmt.Printf("  binary:  %s\n", rep.CodexBin)
		}
		if rep.Version != "" {
			fmt.Printf("  version: %s\n", rep.Version)
		}
		fmt.Printf("  doctor:  %s\n", boolWord(rep.DoctorOK, "ok", "not ok"))
		for _, p := range rep.Problems {
			fmt.Printf("  ⚠ %s\n", p)
		}
		if rep.Ready {
			fmt.Println("  → Codex is installed and responding. Safe to run reviews.")
		} else {
			fmt.Println("  → Resolve the issues above before relying on Codex.")
		}
	}
	if rep.Ready {
		return 0
	}
	return 1
}

func boolWord(b bool, yes, no string) string {
	if b {
		return yes
	}
	return no
}
