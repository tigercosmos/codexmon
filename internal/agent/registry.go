package agent

import (
	"fmt"
	"os"
	"os/exec"
	"sort"
	"strings"
	"sync"
)

// DefaultName is the agent codexmon uses when none is selected via --agent or
// CODEXMON_AGENT. It is codex, so every command that worked before this tool
// learned other agents keeps working unchanged.
const DefaultName = "codex"

var (
	mu        sync.RWMutex
	providers = map[string]Provider{}
)

// Register adds a provider to the global registry. Providers call this from an
// init function; registering two providers under the same name panics, since
// that can only be a programming error.
func Register(p Provider) {
	mu.Lock()
	defer mu.Unlock()
	name := p.Name()
	if _, dup := providers[name]; dup {
		panic("agent: duplicate provider registered: " + name)
	}
	providers[name] = p
}

// Get returns the registered provider for name, or an error naming the agents
// that are available.
func Get(name string) (Provider, error) {
	mu.RLock()
	defer mu.RUnlock()
	if p, ok := providers[name]; ok {
		return p, nil
	}
	return nil, fmt.Errorf("unknown agent %q (available: %s)", name, strings.Join(namesLocked(), ", "))
}

// Names returns the registered agent names, sorted.
func Names() []string {
	mu.RLock()
	defer mu.RUnlock()
	return namesLocked()
}

func namesLocked() []string {
	out := make([]string, 0, len(providers))
	for n := range providers {
		out = append(out, n)
	}
	sort.Strings(out)
	return out
}

// ResolveBin locates a provider's executable. An explicit override (e.g.
// --agent-bin) wins; then the provider's BinEnv environment variable; then the
// first of its BinCandidates found on PATH.
func ResolveBin(p Provider, override string) (string, error) {
	if o := strings.TrimSpace(override); o != "" {
		return o, nil
	}
	if env := strings.TrimSpace(os.Getenv(p.BinEnv())); env != "" {
		return env, nil
	}
	candidates := p.BinCandidates()
	for _, c := range candidates {
		if path, err := exec.LookPath(c); err == nil {
			return path, nil
		}
	}
	return "", fmt.Errorf("none of %v found on PATH (set %s or pass --agent-bin)", candidates, p.BinEnv())
}
