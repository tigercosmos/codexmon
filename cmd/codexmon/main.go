// Command codexmon is a health-monitoring wrapper around the codex CLI.
//
// It forwards arbitrary arguments to `codex` while supervising the process so a
// caller (a human or an agent like Claude) can always tell whether Codex is
// healthy, slow, stalled, or finished — solving the "codex review hangs
// silently" problem. See `codexmon help` for usage.
package main

import (
	"os"

	"github.com/tigercosmos/codexmon/internal/cli"
)

func main() {
	os.Exit(cli.Run(os.Args[1:]))
}
