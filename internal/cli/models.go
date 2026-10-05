package cli

import (
	"fmt"
	"os"
	"text/tabwriter"

	"github.com/tigercosmos/codexmon/internal/agent"
)

func cmdModels(args []string) int {
	jsonOut := false
	sel := ""
	nativeArgs := []string{"models"}
	s := &flagScan{args: args}
	for s.i = 0; s.i < len(args); s.i++ {
		if args[s.i] == "--" {
			nativeArgs = append(nativeArgs, args[s.i+1:]...)
			break
		}
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
			nativeArgs = append(nativeArgs, args[s.i])
		}
	}
	prov, agentName, err := resolveAgent(sel)
	if err != nil {
		return fail(err)
	}
	provider, ok := prov.(agent.ModelProvider)
	if !ok {
		// Preserve native model discovery for agents without a curated catalog.
		// Pin the selected agent so its command cannot fall back to another CLI.
		runArgs := []string{"--agent", agentName}
		if jsonOut {
			runArgs = append(runArgs, "--json")
		}
		runArgs = append(runArgs, "--")
		return cmdRun(append(runArgs, nativeArgs...), false)
	}
	if len(nativeArgs) > 1 {
		return fail(fmt.Errorf("unknown flag %q for models", nativeArgs[1]))
	}
	models := provider.Models()
	if jsonOut {
		printJSON(struct {
			Agent  string        `json:"agent"`
			Models []agent.Model `json:"models"`
		}{agentName, models})
		return 0
	}
	w := tabwriter.NewWriter(os.Stdout, 0, 0, 2, ' ', 0)
	fmt.Fprintln(w, "MODEL\tNAME\tDEFAULT\tDESCRIPTION")
	for _, m := range models {
		marker := ""
		if m.Default {
			marker = "yes"
		}
		fmt.Fprintf(w, "%s\t%s\t%s\t%s\n", m.ID, m.Name, marker, m.Description)
	}
	if err := w.Flush(); err != nil {
		return fail(err)
	}
	fmt.Fprintln(os.Stderr, "Curated choices; availability depends on your account and CLI. Pass a native --model to select a model.")
	return 0
}
