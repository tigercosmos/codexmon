// Package cli implements the codexmon command-line surface.
//
// codexmon is a transparent front-end for an AI coding CLI — codex (the
// default), Claude Code, or the Cursor agent, selected with --agent or
// CODEXMON_AGENT. Management subcommands (status/list/wait/tail/cancel/doctor/
// run/start/review) are handled locally; everything else is forwarded to the
// selected agent verbatim, wrapped in a liveness monitor so a watcher can always
// tell whether the agent is healthy, slow, stalled, or done.
package cli

import (
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"github.com/tigercosmos/codexmon/internal/agent"
	"github.com/tigercosmos/codexmon/internal/job"
	"github.com/tigercosmos/codexmon/internal/monitor"
	"github.com/tigercosmos/codexmon/internal/render"
)

// Version is the codexmon build version (overridable via -ldflags).
var Version = "0.2.0"

var usage = `codexmon ` + Version + ` — a health-monitoring wrapper around AI coding CLIs (codex/claude/cursor).

USAGE
  codexmon <agent args...>                 Run the agent in the foreground with monitoring
  codexmon run [flags] [--] <agent args>   Same, with explicit monitor flags
  codexmon start [flags] [--] <agent args> Launch detached; prints a job id to poll
  codexmon review [flags]                   Monitored code review across any agent (--uncommitted | --base REF)
  codexmon status [id] [--json]            Show health/status of a job (latest if id omitted)
  codexmon wait [id] [--timeout S] [--json] Block until a job finishes, then print the result
  codexmon tail [id] [-f] [-n N]           Show (or follow) a job's log
  codexmon list [--json]                   List recent jobs
  codexmon cancel [id]                     Stop a running job
  codexmon doctor [--agent A] [--json]     Check that the agent is installed and usable
  codexmon version [--agent A]             Print versions

AGENT SELECTION (run/start/review/doctor/version)
      --agent NAME           codex (default), claude, or cursor; or set CODEXMON_AGENT

MONITOR FLAGS (run/start/review)
  -b, --background           Detach and return a job id immediately
      --wall-timeout S       Hard wall-clock limit in seconds (0=off, default 600)
      --idle-timeout S       Kill after S idle seconds when nothing is in flight (0=off, default 180)
      --tool-timeout S       Kill if one MCP/tool call runs longer than S seconds (0=off, default 120)
      --slow-after S         Mark "slow" after S idle seconds (default 30)
      --heartbeat S          Heartbeat cadence in seconds (default 10)
  -C, --cwd DIR              Working directory for the agent
      --stdin                Forward codexmon's stdin to the agent (default: /dev/null)
      --no-json              Do not inject JSON event-stream monitoring
      --agent-bin PATH       Path to the agent binary (or set the agent's CODEXMON_* env var)
      --json                 Emit machine-readable JSON instead of human text

EXAMPLES
  codexmon exec review --uncommitted               # codex review (foreground, default agent)
  codexmon review --agent claude --uncommitted     # same review, run by Claude Code
  codexmon review --agent cursor --base main        # review vs a base branch, run by Cursor
  codexmon start --agent claude -- -p "Review X"    # detached raw passthrough; poll with status
  codexmon wait cdx-... --timeout 600 --json        # block, then emit final JSON
`

// Run executes codexmon with the given args (os.Args[1:]) and returns an exit code.
func Run(args []string) int {
	if len(args) == 0 {
		fmt.Print(usage)
		return 0
	}
	switch args[0] {
	case "help", "-h", "--help":
		fmt.Print(usage)
		return 0
	case "version", "-V", "--version":
		return cmdVersion(args[1:])
	case "run":
		return cmdRun(args[1:], false)
	case "start":
		return cmdRun(args[1:], true)
	case "review":
		return cmdReview(args[1:])
	case "status":
		return cmdStatus(args[1:])
	case "wait":
		return cmdWait(args[1:])
	case "tail":
		return cmdTail(args[1:])
	case "list", "ls":
		return cmdList(args[1:])
	case "cancel", "stop":
		return cmdCancel(args[1:])
	case "doctor":
		return cmdDoctor(args[1:])
	case "__worker":
		return cmdWorker(args[1:])
	default:
		// A leading monitor flag (incl. --agent) means "implicit run": parse the
		// flags, then forward the rest to the agent. Otherwise pass the whole
		// argv to the agent verbatim, so agent-native flags are never swallowed.
		if name, _ := splitFlag(args[0]); isMonitorFlag(name) {
			return cmdRun(args, false)
		}
		return cmdRun(prepend("--", args), false)
	}
}

func prepend(s string, rest []string) []string {
	return append([]string{s}, rest...)
}

// isMonitorFlag reports whether name is a codexmon monitor/agent flag (as
// opposed to an agent-native flag). It gates whether a leading flag triggers an
// implicit `run` (flag parsing) or verbatim passthrough.
func isMonitorFlag(name string) bool {
	switch name {
	case "-b", "--background", "--json", "--stdin", "--no-json",
		"--agent", "--agent-bin", "--codex-bin", "-C", "--cwd",
		"--wall-timeout", "--idle-timeout", "--stall", "--tool-timeout",
		"--slow-after", "--heartbeat":
		return true
	default:
		return false
	}
}

// ---- run / start -----------------------------------------------------------

type runConfig struct {
	background   bool
	jsonOut      bool
	forwardStdin bool
	noJSON       bool
	cwd          string
	agent        string
	agentBin     string
	thresholds   job.Thresholds
}

// flagScan walks an arg slice, letting a flag optionally consume the next token
// as its value. It is shared by the run/start and review parsers.
type flagScan struct {
	args []string
	i    int
}

func (s *flagScan) value(name, attached string) (string, error) {
	if attached != "" {
		return attached, nil
	}
	if s.i+1 >= len(s.args) {
		return "", fmt.Errorf("flag %s needs a value", name)
	}
	// Don't swallow the `--` separator or a following flag as this flag's value:
	// none of codexmon's value flags take a flag-shaped value, and consuming `--`
	// would silently break passthrough. So `codexmon review --base --uncommitted`
	// errors instead of quietly treating "--uncommitted" as the base ref.
	if next := s.args[s.i+1]; next == "--" || (strings.HasPrefix(next, "-") && next != "-") {
		return "", fmt.Errorf("flag %s needs a value (got %q)", name, next)
	}
	s.i++
	return s.args[s.i], nil
}

func (s *flagScan) sec(dst *float64, name, attached string) error {
	v, err := s.value(name, attached)
	if err != nil {
		return err
	}
	f, err := strconv.ParseFloat(strings.TrimSpace(v), 64)
	if err != nil || f < 0 {
		return fmt.Errorf("flag %s needs a non-negative number of seconds, got %q", name, v)
	}
	*dst = f
	return nil
}

// applyCommonFlag handles a monitor/agent flag shared by run/start/review.
// handled is false when name is not one of them (the caller decides what to do
// — passthrough for run/start, an error for review).
func (cfg *runConfig) applyCommonFlag(s *flagScan, name, attached string) (handled bool, err error) {
	switch name {
	case "-b", "--background":
		if err = noValue(name, attached); err == nil {
			cfg.background = true
		}
	case "--json":
		if err = noValue(name, attached); err == nil {
			cfg.jsonOut = true
		}
	case "--stdin":
		if err = noValue(name, attached); err == nil {
			cfg.forwardStdin = true
		}
	case "--no-json":
		if err = noValue(name, attached); err == nil {
			cfg.noJSON = true
		}
	case "--agent":
		cfg.agent, err = s.value(name, attached)
	case "--agent-bin", "--codex-bin":
		cfg.agentBin, err = s.value(name, attached)
	case "-C", "--cwd":
		cfg.cwd, err = s.value(name, attached)
	case "--wall-timeout":
		err = s.sec(&cfg.thresholds.WallSec, name, attached)
	case "--idle-timeout", "--stall":
		err = s.sec(&cfg.thresholds.StalledSec, name, attached)
	case "--tool-timeout":
		err = s.sec(&cfg.thresholds.ToolStuckSec, name, attached)
	case "--slow-after":
		err = s.sec(&cfg.thresholds.SlowAfterSec, name, attached)
	case "--heartbeat":
		err = s.sec(&cfg.thresholds.HeartbeatSec, name, attached)
	default:
		return false, nil
	}
	return true, err
}

// parseRunArgs parses monitor flags for run/start and returns the remaining
// tokens (the agent's own args). Flag parsing stops at `--` or at the first
// token that is not a monitor flag, which begins the passthrough args.
func parseRunArgs(args []string) (runConfig, []string, error) {
	cfg := runConfig{thresholds: monitor.DefaultThresholds()}
	s := &flagScan{args: args}
	for s.i = 0; s.i < len(args); s.i++ {
		a := args[s.i]
		if a == "--" {
			return cfg, args[s.i+1:], nil
		}
		name, attached := splitFlag(a)
		handled, err := cfg.applyCommonFlag(s, name, attached)
		if err != nil {
			return cfg, nil, err
		}
		if !handled {
			return cfg, args[s.i:], nil
		}
	}
	return cfg, nil, nil
}

func splitFlag(a string) (name, attached string) {
	if !strings.HasPrefix(a, "-") {
		return a, ""
	}
	if eq := strings.IndexByte(a, '='); eq > 0 {
		return a[:eq], a[eq+1:]
	}
	return a, ""
}

// noValue rejects an attached value on a boolean flag, so a footgun like
// `--background=false` errors instead of silently enabling the flag.
func noValue(name, attached string) error {
	if attached != "" {
		return fmt.Errorf("flag %s does not take a value (got %q)", name, attached)
	}
	return nil
}

// isSafeRef reports whether s is a plausible, injection-safe git ref: it permits
// the characters real refs use (alnum and . _ / - @ ^ ~ +) and rejects anything
// that could smuggle prose into a review prompt (spaces, quotes, shell/control
// characters).
func isSafeRef(s string) bool {
	if s == "" {
		return false
	}
	for _, r := range s {
		switch {
		case r >= 'a' && r <= 'z', r >= 'A' && r <= 'Z', r >= '0' && r <= '9':
		case r == '.' || r == '_' || r == '/' || r == '-' || r == '@' || r == '^' || r == '~' || r == '+':
		default:
			return false
		}
	}
	return true
}

// resolveAgent picks the agent provider from an explicit --agent value, else
// CODEXMON_AGENT, else the default (codex).
func resolveAgent(sel string) (agent.Provider, string, error) {
	name := strings.TrimSpace(sel)
	if name == "" {
		name = strings.TrimSpace(os.Getenv("CODEXMON_AGENT"))
	}
	if name == "" {
		name = agent.DefaultName
	}
	p, err := agent.Get(name)
	if err != nil {
		return nil, "", err
	}
	return p, name, nil
}

func cmdRun(args []string, forceBackground bool) int {
	cfg, passthrough, err := parseRunArgs(args)
	if err != nil {
		return fail(err)
	}
	if forceBackground {
		cfg.background = true
	}
	prov, agentName, err := resolveAgent(cfg.agent)
	if err != nil {
		return fail(err)
	}
	if len(passthrough) == 0 {
		return fail(fmt.Errorf("no %s arguments given; e.g. `codexmon exec review --uncommitted`", agentName))
	}
	return launchAgent(cfg, prov, agentName, passthrough, "")
}

// cmdReview turns a high-level review request into the selected agent's native
// args and launches it under monitoring.
func cmdReview(args []string) int {
	cfg, rspec, err := parseReviewArgs(args)
	if err != nil {
		return fail(err)
	}
	prov, agentName, err := resolveAgent(cfg.agent)
	if err != nil {
		return fail(err)
	}
	reviewArgs, err := prov.ReviewArgs(rspec)
	if err != nil {
		return fail(err)
	}
	scope := "uncommitted"
	if rspec.Scope == agent.ScopeBase {
		scope = "base " + rspec.Base
	}
	title := agentName + " review (" + scope + ")"
	return launchAgent(cfg, prov, agentName, reviewArgs, title)
}

func parseReviewArgs(args []string) (runConfig, agent.ReviewSpec, error) {
	cfg := runConfig{thresholds: monitor.DefaultThresholds()}
	rspec := agent.ReviewSpec{Scope: agent.ScopeUncommitted}
	s := &flagScan{args: args}
	for s.i = 0; s.i < len(args); s.i++ {
		a := args[s.i]
		name, attached := splitFlag(a)
		switch name {
		case "--uncommitted":
			if err := noValue(name, attached); err != nil {
				return cfg, rspec, err
			}
			rspec.Scope = agent.ScopeUncommitted
		case "--base":
			v, err := s.value(name, attached)
			if err != nil {
				return cfg, rspec, err
			}
			base := strings.TrimSpace(v)
			if base == "" {
				return cfg, rspec, errors.New("--base requires a base ref, e.g. --base main")
			}
			// The ref is interpolated into review prompts (claude/cursor) and
			// passed to codex; constrain it to ref-shaped characters so a crafted
			// value can't smuggle instructions into the prompt.
			if !isSafeRef(base) {
				return cfg, rspec, fmt.Errorf("--base %q is not a valid ref (allowed: letters, digits, and . _ / - @ ^ ~ +)", base)
			}
			rspec.Scope = agent.ScopeBase
			rspec.Base = base
		default:
			handled, err := cfg.applyCommonFlag(s, name, attached)
			if err != nil {
				return cfg, rspec, err
			}
			if !handled {
				return cfg, rspec, fmt.Errorf("unknown flag %q for review", a)
			}
		}
	}
	return cfg, rspec, nil
}

// launchAgent resolves the binary, builds the job spec for the given agent and
// args (injecting any monitoring flags via the provider), and runs it foreground
// or detached per cfg. A non-empty titleOverride replaces the analyzed title.
func launchAgent(cfg runConfig, prov agent.Provider, agentName string, args []string, titleOverride string) int {
	// A detached worker's stdin is /dev/null, so forwarding can't reach a real
	// terminal — reject the combination instead of silently ignoring it.
	if cfg.background && cfg.forwardStdin {
		return fail(errors.New("--stdin cannot be combined with -b/start: a detached worker has no terminal to forward"))
	}

	bin, err := agent.ResolveBin(prov, cfg.agentBin)
	if err != nil {
		return fail(fmt.Errorf("%s CLI not found (install it or set %s): %w", agentName, prov.BinEnv(), err))
	}

	cwd := cfg.cwd
	if cwd == "" {
		cwd, _ = os.Getwd()
	} else {
		abs, aerr := filepath.Abs(cwd)
		if aerr != nil {
			return fail(aerr)
		}
		cwd = abs
	}

	id := job.NewID()
	dir, err := job.Dir(id)
	if err != nil {
		return fail(err)
	}
	_, _, _, _, resultFile, _ := job.Paths(dir)

	analysis := prov.Analyze(args, resultFile, !cfg.noJSON)
	title := analysis.Title
	if titleOverride != "" {
		title = titleOverride
	}
	spec := &job.Spec{
		ID:           id,
		Agent:        agentName,
		AgentBin:     bin,
		Args:         analysis.Args,
		Cwd:          cwd,
		JSONMode:     analysis.JSONMode,
		ForwardStdin: cfg.forwardStdin,
		Thresholds:   cfg.thresholds,
		Title:        title,
	}
	if err := job.WriteSpec(dir, spec); err != nil {
		return fail(err)
	}

	if cfg.background {
		return launchBackground(spec, dir, cfg.jsonOut)
	}
	return runForeground(spec, dir, cfg.jsonOut)
}

func runForeground(spec *job.Spec, dir string, jsonOut bool) int {
	if !jsonOut {
		fmt.Fprintf(os.Stderr, "codexmon: %s (job %s)\n", spec.Title, spec.ID)
	}
	st, _ := monitor.Run(dir, spec, monitor.Options{Progress: os.Stderr})

	result := readResult(st)
	if jsonOut {
		printJSON(st)
	} else {
		fmt.Println()
		fmt.Print(render.Result(st, result))
	}
	return exitCodeFor(st)
}

func launchBackground(spec *job.Spec, dir string, jsonOut bool) int {
	// Seed a status so an immediate poll finds the job.
	now := time.Now()
	_, _, _, logFile, resultFile, _ := job.Paths(dir)
	seed := &job.Status{
		ID: spec.ID, State: job.StateQueued, Health: job.HealthStarting,
		Phase: "queued", Agent: spec.Agent, AgentBin: spec.AgentBin, Args: spec.Args, Cwd: spec.Cwd,
		JSONMode: spec.JSONMode, StartedAt: now, UpdatedAt: now,
		Thresholds: spec.Thresholds, Dir: dir, LogFile: logFile,
		ResultFile: resultFile, Title: spec.Title,
	}
	// The status file is the whole contract for "the job started"; if we can't
	// even write the seed, fail loudly instead of launching an unobservable job.
	if err := job.WriteStatus(dir, seed); err != nil {
		return fail(fmt.Errorf("write initial status: %w", err))
	}

	self, err := os.Executable()
	if err != nil {
		return fail(err)
	}
	pid, err := spawnWorker(self, spec.ID, spec.Cwd, logFile)
	if err != nil {
		// Don't leave the seeded job stuck as "queued" forever: with no worker
		// pid, reconciliation can never age it out. Record it as failed.
		now := time.Now()
		seed.State = job.StateFailed
		seed.Health = job.HealthDead
		seed.Error = "failed to spawn background worker: " + err.Error()
		seed.EndedAt = &now
		seed.UpdatedAt = now
		_ = job.WriteStatus(dir, seed)
		return fail(fmt.Errorf("spawn background worker: %w", err))
	}
	seed.WorkerPID = pid
	_ = job.WriteStatus(dir, seed)

	if jsonOut {
		printJSON(seed)
	} else {
		fmt.Printf("%s started (worker pid %d) — %s\n", spec.ID, pid, spec.Title)
		fmt.Printf("  poll:   codexmon status %s\n", spec.ID)
		fmt.Printf("  follow: codexmon tail %s -f\n", spec.ID)
		fmt.Printf("  block:  codexmon wait %s\n", spec.ID)
	}
	return 0
}

func cmdWorker(args []string) int {
	var id string
	for i := 0; i < len(args); i++ {
		if args[i] == "--job" && i+1 < len(args) {
			id = args[i+1]
			i++
		}
	}
	if id == "" {
		return fail(errors.New("__worker requires --job <id>"))
	}
	dir, err := job.Dir(id)
	if err != nil {
		return fail(err)
	}
	spec, err := job.ReadSpec(dir)
	if err != nil {
		return fail(fmt.Errorf("read spec for %s: %w", id, err))
	}
	st, _ := monitor.Run(dir, spec, monitor.Options{}) // file-only; no terminal
	return exitCodeFor(st)
}

// ---- status / list ---------------------------------------------------------

func cmdStatus(args []string) int {
	id, flags := splitIDAndFlags(args)
	jsonOut := flags["--json"]
	st, err := job.Resolve(id)
	if err != nil {
		return fail(err)
	}
	if jsonOut {
		printJSON(st)
	} else {
		fmt.Print(render.Status(st))
	}
	return 0
}

func cmdList(args []string) int {
	_, flags := splitIDAndFlags(args)
	jobs, err := job.List()
	if err != nil {
		return fail(err)
	}
	if flags["--json"] {
		printJSON(jobs)
		return 0
	}
	fmt.Print(render.List(jobs))
	return 0
}

// ---- wait -------------------------------------------------------------------

func cmdWait(args []string) int {
	id, timeout, interval, jsonOut, err := parseWaitArgs(args)
	if err != nil {
		return fail(err)
	}
	st, err := job.Resolve(id)
	if err != nil {
		return fail(err)
	}
	dir := st.Dir
	deadline := time.Time{}
	if timeout > 0 {
		deadline = time.Now().Add(time.Duration(timeout * float64(time.Second)))
	}
	readErrs := 0
	for st.State.Active() {
		if !deadline.IsZero() && time.Now().After(deadline) {
			if !jsonOut {
				fmt.Fprintf(os.Stderr, "codexmon: wait timed out after %.0fs; job %s still %s\n", timeout, st.ID, st.State)
			}
			if jsonOut {
				printJSON(st)
			} else {
				fmt.Print(render.Status(st))
			}
			return exitWaitTimeout
		}
		time.Sleep(time.Duration(interval * float64(time.Second)))
		// ReadStatus reconciles a dead worker to a terminal state, so this loop
		// also ends if the worker crashes without recording a result. If the
		// status file becomes persistently unreadable, don't spin forever.
		next, err := job.ReadStatus(dir)
		if err != nil {
			readErrs++
			if readErrs >= 5 {
				return fail(fmt.Errorf("cannot read status for %s after %d attempts: %w", st.ID, readErrs, err))
			}
			continue
		}
		readErrs = 0
		st = next
	}

	if jsonOut {
		printJSON(st)
	} else {
		fmt.Print(render.Result(st, readResult(st)))
	}
	return exitCodeFor(st)
}

func parseWaitArgs(args []string) (id string, timeout, interval float64, jsonOut bool, err error) {
	interval = 2
	for i := 0; i < len(args); i++ {
		name, attached := splitFlag(args[i])
		readVal := func() (string, error) {
			if attached != "" {
				return attached, nil
			}
			if i+1 >= len(args) {
				return "", fmt.Errorf("flag %s needs a value", name)
			}
			i++
			return args[i], nil
		}
		switch {
		case name == "--json":
			if attached != "" {
				return "", 0, 0, false, fmt.Errorf("flag --json does not take a value (got %q)", attached)
			}
			jsonOut = true
		case name == "--timeout":
			v, e := readVal()
			if e != nil {
				return "", 0, 0, false, e
			}
			timeout, err = strconv.ParseFloat(v, 64)
			if err != nil || timeout < 0 {
				return "", 0, 0, false, fmt.Errorf("--timeout needs a non-negative number of seconds, got %q", v)
			}
		case name == "--interval":
			v, e := readVal()
			if e != nil {
				return "", 0, 0, false, e
			}
			interval, err = strconv.ParseFloat(v, 64)
			if err != nil || interval <= 0 {
				return "", 0, 0, false, fmt.Errorf("--interval needs a positive number")
			}
		case strings.HasPrefix(args[i], "-"):
			return "", 0, 0, false, fmt.Errorf("unknown flag %q", args[i])
		default:
			id = args[i]
		}
	}
	return id, timeout, interval, jsonOut, nil
}

// ---- tail -------------------------------------------------------------------

func cmdTail(args []string) int {
	var id string
	follow := false
	n := 40
	for i := 0; i < len(args); i++ {
		a := args[i]
		switch {
		case a == "-f" || a == "--follow":
			follow = true
		case a == "-n":
			if i+1 >= len(args) {
				return fail(fmt.Errorf("flag -n needs a value"))
			}
			i++
			v, err := strconv.Atoi(args[i])
			if err != nil || v < 0 {
				return fail(fmt.Errorf("-n needs a non-negative integer, got %q", args[i]))
			}
			n = v
		case strings.HasPrefix(a, "-n"):
			v, err := strconv.Atoi(a[2:])
			if err != nil || v < 0 {
				return fail(fmt.Errorf("-n needs a non-negative integer, got %q", a[2:]))
			}
			n = v
		case strings.HasPrefix(a, "-"):
			return fail(fmt.Errorf("unknown flag %q", a))
		default:
			id = a
		}
	}
	st, err := job.Resolve(id)
	if err != nil {
		return fail(err)
	}
	return tailLog(st, n, follow)
}

func tailLog(st *job.Status, n int, follow bool) int {
	f, err := os.Open(st.LogFile)
	if err != nil {
		return fail(fmt.Errorf("open log: %w", err))
	}
	defer f.Close()

	fi, err := f.Stat()
	if err != nil {
		return fail(fmt.Errorf("stat log: %w", err))
	}
	size := fi.Size()

	// Bound the initial read: show the last n lines from at most the final
	// tailReadCap bytes instead of loading the whole (up to 64 MiB) log.
	const tailReadCap = 256 << 10
	readFrom := int64(0)
	if size > tailReadCap {
		readFrom = size - tailReadCap
	}
	if _, err := f.Seek(readFrom, io.SeekStart); err != nil {
		return fail(fmt.Errorf("seek log: %w", err))
	}
	data, err := io.ReadAll(f)
	if err != nil {
		return fail(fmt.Errorf("read log: %w", err))
	}
	chunk := strings.TrimRight(string(data), "\n")
	if readFrom > 0 {
		// We likely started mid-line; drop the first partial line.
		if i := strings.IndexByte(chunk, '\n'); i >= 0 {
			chunk = chunk[i+1:]
		} else {
			chunk = ""
		}
	}
	var lines []string
	if chunk != "" {
		lines = strings.Split(chunk, "\n")
	}
	start := 0
	if len(lines) > n {
		start = len(lines) - n
	}
	for _, ln := range lines[start:] {
		fmt.Println(ln)
	}
	if !follow {
		return 0
	}

	offset := size
	for {
		cur, err := job.ReadStatus(st.Dir)
		if err == nil {
			st = cur
		}
		buf := make([]byte, 64*1024)
		for {
			if _, err := f.Seek(offset, io.SeekStart); err != nil {
				return fail(fmt.Errorf("seek log: %w", err))
			}
			nr, rerr := f.Read(buf)
			if nr > 0 {
				if _, werr := os.Stdout.Write(buf[:nr]); werr != nil {
					return fail(fmt.Errorf("write: %w", werr))
				}
				offset += int64(nr)
			}
			if nr == 0 || rerr != nil {
				break
			}
		}
		if !st.State.Active() {
			return exitCodeFor(st)
		}
		time.Sleep(500 * time.Millisecond)
	}
}

// ---- cancel -----------------------------------------------------------------

func cmdCancel(args []string) int {
	id, _ := splitIDAndFlags(args)
	st, err := job.Resolve(id)
	if err != nil {
		return fail(err)
	}
	if !st.State.Active() {
		fmt.Printf("%s already finished (%s).\n", st.ID, st.State)
		return 0
	}
	// The marker is the graceful path: the owning worker's watchdog sees it,
	// stops codex, and records a clean `cancelled` status within ~1 tick.
	_ = job.RequestCancel(st.Dir)
	fmt.Printf("cancel requested for %s.\n", st.ID)

	workerLive := st.WorkerPID > 0 && alive(st.WorkerPID)
	if workerLive {
		for i := 0; i < 30; i++ { // up to ~3s for the worker to finalize
			time.Sleep(100 * time.Millisecond)
			if cur, err := job.ReadStatus(st.Dir); err == nil && !cur.State.Active() {
				fmt.Printf("%s is now %s.\n", cur.ID, cur.State)
				return 0
			}
		}
	}
	// Worker is gone (or unresponsive): stop the agent ourselves and finalize.
	// Only signal it if it is genuinely still alive, so we never kill a PID that
	// has since been reaped and reused by an unrelated process.
	cur, err := job.ReadStatus(st.Dir)
	if err == nil && cur.AgentPID > 0 && cur.State.Active() && alive(cur.AgentPID) {
		killGroup(cur.AgentPID)
	}
	if cur, err := job.ReadStatus(st.Dir); err == nil && cur.State.Active() {
		now := time.Now()
		cur.State = job.StateCancelled
		cur.Health = job.HealthDead
		cur.Error = "cancelled by request (worker not responding)"
		cur.EndedAt = &now
		cur.UpdatedAt = now
		if werr := job.WriteStatus(cur.Dir, cur); werr != nil {
			return fail(fmt.Errorf("agent signalled but could not record cancelled status for %s: %w", cur.ID, werr))
		}
	}
	// Report the job's actual final state rather than assuming success — the
	// agent may not have stopped yet, or the status write may have failed.
	final, ferr := job.ReadStatus(st.Dir)
	if ferr != nil {
		fmt.Printf("%s cancel requested; final state unknown: %v\n", st.ID, ferr)
		return 1
	}
	if final.State.Active() {
		fmt.Printf("%s: cancel signalled but still %s; it should stop shortly (poll `codexmon status %s`).\n", final.ID, final.State, final.ID)
		return 1
	}
	fmt.Printf("%s is now %s.\n", final.ID, final.State)
	return 0
}

// ---- helpers ----------------------------------------------------------------

func splitIDAndFlags(args []string) (id string, flags map[string]bool) {
	flags = map[string]bool{}
	for _, a := range args {
		if strings.HasPrefix(a, "-") {
			name, _ := splitFlag(a)
			flags[name] = true
			continue
		}
		if id == "" {
			id = a
		}
	}
	return id, flags
}

// maxResultBytes caps how much of result.txt we load into memory to print, so a
// pathologically large final message can't blow up `wait`/`run`.
const maxResultBytes = 4 << 20

func readResult(st *job.Status) string {
	if st.ResultFile == "" {
		return st.ResultPreview
	}
	f, err := os.Open(st.ResultFile)
	if err != nil {
		return st.ResultPreview
	}
	defer f.Close()
	data, err := io.ReadAll(io.LimitReader(f, maxResultBytes+1))
	if err != nil {
		return st.ResultPreview
	}
	if len(data) > maxResultBytes {
		return string(data[:maxResultBytes]) + "\n…[result truncated at " + strconv.Itoa(maxResultBytes>>20) + " MiB; full text in " + st.ResultFile + "]"
	}
	return string(data)
}

// exitWaitTimeout is returned by `wait` when its own --timeout elapses while
// the job is still running. It is intentionally distinct from any code
// exitCodeFor can produce (and from codex's own exit codes) so a caller can
// unambiguously tell "I gave up waiting" from "the job finished with code N".
const exitWaitTimeout = 75 // EX_TEMPFAIL

func exitCodeFor(st *job.Status) int {
	switch st.State {
	case job.StateCompleted:
		return 0
	case job.StateCancelled:
		return 130
	case job.StateStalled, job.StateTimeout:
		return 124
	case job.StateFailed:
		// Forward codex's own exit code, but never let it collide with our
		// reserved sentinels (124/130/75).
		if st.ExitCode != nil && *st.ExitCode > 0 {
			switch *st.ExitCode {
			case 124, 130, exitWaitTimeout:
				return 1
			default:
				return *st.ExitCode
			}
		}
		return 1
	default:
		return 1
	}
}

func printJSON(v any) {
	enc := json.NewEncoder(os.Stdout)
	enc.SetIndent("", "  ")
	_ = enc.Encode(v)
}

func fail(err error) int {
	fmt.Fprintf(os.Stderr, "codexmon: %v\n", err)
	return 1
}

func cmdVersion(args []string) int {
	fmt.Printf("codexmon %s\n", Version)

	sel := ""
	s := &flagScan{args: args}
	for s.i = 0; s.i < len(args); s.i++ {
		if name, attached := splitFlag(args[s.i]); name == "--agent" {
			v, err := s.value(name, attached)
			if err != nil {
				return fail(err)
			}
			sel = v
		}
	}

	prov, agentName, err := resolveAgent(sel)
	if err != nil {
		fmt.Printf("agent:  %v\n", err)
		return 0
	}
	bin, err := agent.ResolveBin(prov, "")
	if err != nil {
		fmt.Printf("%-7s not found on PATH (set %s)\n", agentName, prov.BinEnv())
		return 0
	}
	if out, err := runCapture(5*time.Second, bin, "--version"); err == nil {
		fmt.Printf("%-7s %s (%s)\n", agentName, strings.TrimSpace(out), bin)
	} else {
		fmt.Printf("%-7s found at %s but `--version` failed: %v\n", agentName, bin, err)
	}
	return 0
}
