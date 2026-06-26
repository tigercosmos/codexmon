// Command codexmon is a health-monitoring wrapper around AI coding CLIs —
// codex, Claude Code, and the Cursor agent.
//
// It forwards arbitrary arguments to the selected agent while supervising the
// process so a caller (a human or an agent like Claude) can always tell whether
// the agent is healthy, slow, stalled, or finished — solving the "the review
// hangs silently" problem. See `codexmon help` for usage.
package main

import (
	"os"

	// Register the built-in agent providers regardless of which internal
	// packages the CLI happens to pull in. The monitor also imports this, but
	// anchoring it at the entry point keeps registration robust if that ever
	// changes.
	_ "github.com/tigercosmos/codexmon/internal/agent/all"
	"github.com/tigercosmos/codexmon/internal/cli"
)

func main() {
	os.Exit(cli.Run(os.Args[1:]))
}
