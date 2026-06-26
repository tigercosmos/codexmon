// Package all registers every built-in agent provider as an import side effect.
// Import it for its effects (`import _ ".../internal/agent/all"`) anywhere a
// provider must be resolvable by name — the monitor pulls it in, which also
// covers the CLI that drives the monitor.
package all

import (
	_ "github.com/tigercosmos/codexmon/internal/agent/claude"
	_ "github.com/tigercosmos/codexmon/internal/agent/codex"
	_ "github.com/tigercosmos/codexmon/internal/agent/cursor"
)
