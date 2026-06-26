package cli

import (
	"context"
	"fmt"
	"time"

	"github.com/tigercosmos/codexmon/internal/agent"
)

func cmdDoctor(args []string) int {
	jsonOut := false
	sel := ""
	s := &flagScan{args: args}
	for s.i = 0; s.i < len(args); s.i++ {
		name, attached := splitFlag(args[s.i])
		switch name {
		case "--json":
			if err := noValue(name, attached); err != nil {
				return fail(err)
			}
			jsonOut = true
		case "--agent":
			v, err := s.value(name, attached)
			if err != nil {
				return fail(err)
			}
			sel = v
		default:
			return fail(fmt.Errorf("unknown flag %q for doctor", args[s.i]))
		}
	}

	prov, agentName, err := resolveAgent(sel)
	if err != nil {
		return fail(err)
	}

	bin, berr := agent.ResolveBin(prov, "")
	if berr != nil {
		rep := agent.DoctorReport{
			Agent:    agentName,
			Problems: []string{fmt.Sprintf("%s CLI not found on PATH (install it or set %s)", agentName, prov.BinEnv())},
		}
		return emitDoctor(rep, jsonOut)
	}
	return emitDoctor(prov.Doctor(bin, doctorRun), jsonOut)
}

// doctorRun adapts runCapture to the agent.RunFunc contract, mapping the
// deadline error to agent.ErrTimeout so providers can report a wedged probe
// distinctly from an ordinary failure.
func doctorRun(timeout time.Duration, name string, args ...string) (string, error) {
	out, err := runCapture(timeout, name, args...)
	if err == context.DeadlineExceeded {
		return out, agent.ErrTimeout
	}
	return out, err
}

func emitDoctor(rep agent.DoctorReport, jsonOut bool) int {
	if jsonOut {
		printJSON(rep)
	} else {
		icon := "✅"
		if !rep.Ready {
			icon = "❌"
		}
		fmt.Printf("%s %s doctor\n", icon, rep.Agent)
		if rep.Bin != "" {
			fmt.Printf("  binary:  %s\n", rep.Bin)
		}
		if rep.Version != "" {
			fmt.Printf("  version: %s\n", rep.Version)
		}
		if rep.HealthName != "" {
			fmt.Printf("  %s: %s\n", rep.HealthName, boolWord(rep.HealthOK, "ok", "not ok"))
		}
		for _, p := range rep.Problems {
			fmt.Printf("  ⚠ %s\n", p)
		}
		if rep.Ready {
			fmt.Printf("  → %s is installed and responding. Safe to run reviews.\n", rep.Agent)
		} else {
			fmt.Println("  → Resolve the issues above before relying on this agent.")
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
