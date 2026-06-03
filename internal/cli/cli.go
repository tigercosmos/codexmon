// Package cli implements the codexmon command-line surface.
//
// codexmon is a transparent front-end for the `codex` CLI: management
// subcommands (status/list/wait/tail/cancel/doctor/run/start) are handled
// locally; everything else is forwarded to codex verbatim, wrapped in a
// liveness monitor so a watcher can always tell whether Codex is healthy,
// slow, stalled, or done.
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

	"github.com/tigercosmos/codexmon/internal/codexcli"
	"github.com/tigercosmos/codexmon/internal/job"
	"github.com/tigercosmos/codexmon/internal/monitor"
	"github.com/tigercosmos/codexmon/internal/render"
)

// Version is the codexmon build version (overridable via -ldflags).
var Version = "0.1.0"

var usage = `codexmon ` + Version + ` — a health-monitoring wrapper around the codex CLI.

USAGE
  codexmon <codex args...>                 Run codex in the foreground with monitoring
  codexmon run [flags] [--] <codex args>   Same, with explicit monitor flags
  codexmon start [flags] [--] <codex args> Launch detached; prints a job id to poll
  codexmon status [id] [--json]            Show health/status of a job (latest if id omitted)
  codexmon wait [id] [--timeout S] [--json] Block until a job finishes, then print the result
  codexmon tail [id] [-f] [-n N]           Show (or follow) a job's log
  codexmon list [--json]                   List recent jobs
  codexmon cancel [id]                     Stop a running job
  codexmon doctor [--json]                 Check that codex itself is installed and usable
  codexmon version                         Print versions

MONITOR FLAGS (run/start)
  -b, --background           Detach and return a job id immediately
      --wall-timeout S       Hard wall-clock limit in seconds (0=off, default 600)
      --idle-timeout S       Kill after S idle seconds when nothing is in flight (0=off, default 180)
      --tool-timeout S       Kill if one MCP/tool call runs longer than S seconds (0=off, default 120)
      --slow-after S         Mark "slow" after S idle seconds (default 30)
      --heartbeat S          Heartbeat cadence in seconds (default 10)
  -C, --cwd DIR              Working directory for codex
      --stdin                Forward codexmon's stdin to codex (default: /dev/null)
      --no-json              Do not inject 'exec --json' event monitoring
      --codex-bin PATH       Path to the codex binary (or set CODEXMON_CODEX)
      --json                 Emit machine-readable JSON instead of human text

EXAMPLES
  codexmon exec review --uncommitted              # monitored code review (foreground)
  codexmon start -- exec review --base main       # detached review; poll with status
  codexmon status                                 # latest job's health
  codexmon wait cdx-... --timeout 600 --json      # block, then emit final JSON
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
		return cmdVersion()
	case "run":
		return cmdRun(args[1:], false)
	case "start":
		return cmdRun(args[1:], true)
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
		// Implicit passthrough: treat the whole argv as codex args, foreground.
		return cmdRun(prepend("--", args), false)
	}
}

func prepend(s string, rest []string) []string {
	return append([]string{s}, rest...)
}

// ---- run / start -----------------------------------------------------------

type runConfig struct {
	background   bool
	jsonOut      bool
	forwardStdin bool
	noJSON       bool
	cwd          string
	codexBin     string
	thresholds   job.Thresholds
}

func parseRunArgs(args []string) (runConfig, []string, error) {
	cfg := runConfig{thresholds: monitor.DefaultThresholds()}
	i := 0
	val := func(name, attached string) (string, error) {
		if attached != "" {
			return attached, nil
		}
		if i+1 >= len(args) {
			return "", fmt.Errorf("flag %s needs a value", name)
		}
		i++
		return args[i], nil
	}
	for i = 0; i < len(args); i++ {
		a := args[i]
		name, attached := splitFlag(a)
		switch {
		case a == "--":
			return cfg, args[i+1:], nil
		case name == "-b" || name == "--background":
			if err := noValue(name, attached); err != nil {
				return cfg, nil, err
			}
			cfg.background = true
		case name == "--json":
			if err := noValue(name, attached); err != nil {
				return cfg, nil, err
			}
			cfg.jsonOut = true
		case name == "--stdin":
			if err := noValue(name, attached); err != nil {
				return cfg, nil, err
			}
			cfg.forwardStdin = true
		case name == "--no-json":
			if err := noValue(name, attached); err != nil {
				return cfg, nil, err
			}
			cfg.noJSON = true
		case name == "--wall-timeout":
			if err := setSec(&cfg.thresholds.WallSec, val, name, attached); err != nil {
				return cfg, nil, err
			}
		case name == "--idle-timeout" || name == "--stall":
			if err := setSec(&cfg.thresholds.StalledSec, val, name, attached); err != nil {
				return cfg, nil, err
			}
		case name == "--tool-timeout":
			if err := setSec(&cfg.thresholds.ToolStuckSec, val, name, attached); err != nil {
				return cfg, nil, err
			}
		case name == "--slow-after":
			if err := setSec(&cfg.thresholds.SlowAfterSec, val, name, attached); err != nil {
				return cfg, nil, err
			}
		case name == "--heartbeat":
			if err := setSec(&cfg.thresholds.HeartbeatSec, val, name, attached); err != nil {
				return cfg, nil, err
			}
		case name == "-C" || name == "--cwd":
			v, err := val(name, attached)
			if err != nil {
				return cfg, nil, err
			}
			cfg.cwd = v
		case name == "--codex-bin":
			v, err := val(name, attached)
			if err != nil {
				return cfg, nil, err
			}
			cfg.codexBin = v
		default:
			// First token that isn't a known monitor flag begins the codex args.
			return cfg, args[i:], nil
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

func setSec(dst *float64, val func(string, string) (string, error), name, attached string) error {
	v, err := val(name, attached)
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

func cmdRun(args []string, forceBackground bool) int {
	cfg, codexArgs, err := parseRunArgs(args)
	if err != nil {
		return fail(err)
	}
	if forceBackground {
		cfg.background = true
	}
	if len(codexArgs) == 0 {
		return fail(errors.New("no codex arguments given; e.g. `codexmon exec review --uncommitted`"))
	}

	codexBin := cfg.codexBin
	if codexBin == "" {
		codexBin, err = codexcli.Resolve()
		if err != nil {
			return fail(fmt.Errorf("codex CLI not found (install it or set CODEXMON_CODEX): %w", err))
		}
	}

	cwd := cfg.cwd
	if cwd == "" {
		cwd, _ = os.Getwd()
	} else {
		abs, err := filepath.Abs(cwd)
		if err != nil {
			return fail(err)
		}
		cwd = abs
	}

	id := job.NewID()
	dir, err := job.Dir(id)
	if err != nil {
		return fail(err)
	}
	_, _, _, _, resultFile, _ := job.Paths(dir)

	analysis := codexcli.Analyze(codexArgs, resultFile, !cfg.noJSON)
	spec := &job.Spec{
		ID:           id,
		CodexBin:     codexBin,
		Args:         analysis.Args,
		Cwd:          cwd,
		JSONMode:     analysis.JSONMode,
		ForwardStdin: cfg.forwardStdin,
		Thresholds:   cfg.thresholds,
		Title:        analysis.Title,
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
		Phase: "queued", CodexBin: spec.CodexBin, Args: spec.Args, Cwd: spec.Cwd,
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

	data, err := io.ReadAll(f)
	if err != nil {
		return fail(fmt.Errorf("read log: %w", err))
	}
	lines := strings.Split(strings.TrimRight(string(data), "\n"), "\n")
	if len(data) == 0 {
		lines = nil
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

	offset := int64(len(data))
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
	// Worker is gone (or unresponsive): stop codex ourselves and finalize. Only
	// signal codex if it is genuinely still alive, so we never kill a PID that
	// has since been reaped and reused by an unrelated process.
	cur, err := job.ReadStatus(st.Dir)
	if err == nil && cur.CodexPID > 0 && cur.State.Active() && alive(cur.CodexPID) {
		killGroup(cur.CodexPID)
	}
	if cur, err := job.ReadStatus(st.Dir); err == nil && cur.State.Active() {
		now := time.Now()
		cur.State = job.StateCancelled
		cur.Health = job.HealthDead
		cur.Error = "cancelled by request (worker not responding)"
		cur.EndedAt = &now
		cur.UpdatedAt = now
		_ = job.WriteStatus(cur.Dir, cur)
	}
	fmt.Printf("%s cancelled.\n", st.ID)
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

func readResult(st *job.Status) string {
	if st.ResultFile == "" {
		return st.ResultPreview
	}
	data, err := os.ReadFile(st.ResultFile)
	if err != nil {
		return st.ResultPreview
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

func cmdVersion() int {
	fmt.Printf("codexmon %s\n", Version)
	if bin, err := codexcli.Resolve(); err == nil {
		if out, err := runCapture(5*time.Second, bin, "--version"); err == nil {
			fmt.Printf("codex   %s (%s)\n", strings.TrimSpace(out), bin)
		} else {
			fmt.Printf("codex   found at %s but `--version` failed: %v\n", bin, err)
		}
	} else {
		fmt.Println("codex   not found on PATH (set CODEXMON_CODEX)")
	}
	return 0
}
