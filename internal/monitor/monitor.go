// Package monitor supervises a single codex child process: it streams and
// parses output, maintains a live status file, and enforces the watchdog
// policy (heartbeat, stall ceiling, wall-clock timeout, cancellation).
//
// The design deliberately avoids the codex app-server JSON-RPC path, whose
// turn-completion notifications can silently never arrive. `codex exec` is a
// one-shot process, so its OS-level exit is the authoritative completion
// signal. To make that literally true, the monitor waits on the process (not
// on pipe EOF) and force-closes its own pipe read-ends if a lingering
// grandchild keeps them open — so the monitor can never itself hang silently.
package monitor

import (
	"bufio"
	"io"
	"os"
	"os/exec"
	"strings"
	"sync"
	"time"

	"github.com/tigercosmos/codexmon/internal/events"
	"github.com/tigercosmos/codexmon/internal/job"
	"github.com/tigercosmos/codexmon/internal/proc"
)

// drainGrace is how long, after the process exits, we let the reader goroutines
// finish draining buffered output before force-closing the pipes.
const drainGrace = 5 * time.Second

// maxLogBytes caps each of output.log and events.jsonl so an unbounded run
// cannot fill the disk. Past the cap we stop appending (a notice is written).
const maxLogBytes = 64 << 20 // 64 MiB

// DefaultThresholds are the watchdog limits applied when a field is unset.
func DefaultThresholds() job.Thresholds {
	return job.Thresholds{
		HeartbeatSec: 10,
		SlowAfterSec: 30,
		StalledSec:   180,
		WallSec:      600,
	}
}

// Options tune a foreground run; Progress may be nil for a detached worker.
type Options struct {
	// Progress, if set, receives the same human-readable, timestamped lines
	// written to the job log (typically os.Stderr in foreground mode).
	Progress io.Writer
}

type runner struct {
	dir  string
	spec *job.Spec
	opts Options

	logMu     sync.Mutex
	logF      *os.File
	logBytes  int64
	logCapped bool
	evF       *os.File
	evBytes   int64
	evCapped  bool

	mu           sync.Mutex
	st           *job.Status
	lastActivity time.Time
	start        time.Time

	resultText      string          // most recent agent_message (final answer)
	resultBuf       strings.Builder // accumulated stdout for non-JSON mode
	stderrTail      []string        // last few meaningful stderr lines
	inFlightCmds    map[string]bool // command_execution items started but not completed
	sawFailureEvent bool            // an error / turn.failed event was observed

	// killState is set by the watchdog when it forcibly stops codex, so the
	// finalizer reports the right terminal state instead of a raw exit code.
	killState job.State
	finalized bool
}

const stderrTailMax = 12

// Run supervises codex to completion and returns the terminal status. The
// status file in dir is kept current throughout, so other processes can poll it.
func Run(dir string, spec *job.Spec, opts Options) (*job.Status, error) {
	_, _, eventsFile, logFile, resultFile, _ := job.Paths(dir)

	now := time.Now()
	r := &runner{
		dir:          dir,
		spec:         spec,
		opts:         opts,
		start:        now,
		lastActivity: now,
		inFlightCmds: map[string]bool{},
		st: &job.Status{
			ID:         spec.ID,
			State:      job.StateRunning,
			Health:     job.HealthStarting,
			Phase:      string(events.PhaseStarting),
			CodexBin:   spec.CodexBin,
			Args:       spec.Args,
			Cwd:        spec.Cwd,
			JSONMode:   spec.JSONMode,
			WorkerPID:  os.Getpid(),
			StartedAt:  now,
			UpdatedAt:  now,
			Thresholds: spec.Thresholds,
			Dir:        dir,
			LogFile:    logFile,
			ResultFile: resultFile,
			Title:      spec.Title,
		},
	}
	if spec.JSONMode {
		r.st.EventsFile = eventsFile
	}
	_ = job.WriteStatus(dir, r.st)

	logF, err := os.OpenFile(logFile, os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0o644)
	if err != nil {
		return r.fail("open log file: " + err.Error()), err
	}
	defer logF.Close()
	r.logF = logF

	if spec.JSONMode {
		evF, err := os.OpenFile(eventsFile, os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0o644)
		if err != nil {
			return r.fail("open events file: " + err.Error()), err
		}
		defer evF.Close()
		r.evF = evF
	}

	cmd := exec.Command(spec.CodexBin, spec.Args...)
	cmd.Dir = spec.Cwd
	if len(spec.Env) > 0 {
		cmd.Env = spec.Env
	}
	if spec.ForwardStdin {
		cmd.Stdin = os.Stdin
	} // else nil → exec connects /dev/null, preventing the stdin-read hang.
	proc.SetChildGroup(cmd)

	// We own the pipes (not cmd.StdoutPipe) so that completion is gated on the
	// process exiting, not on pipe EOF — and so we can force EOF if needed.
	rOut, wOut, err := os.Pipe()
	if err != nil {
		return r.fail("stdout pipe: " + err.Error()), err
	}
	rErr, wErr, err := os.Pipe()
	if err != nil {
		rOut.Close()
		wOut.Close()
		return r.fail("stderr pipe: " + err.Error()), err
	}
	cmd.Stdout = wOut
	cmd.Stderr = wErr

	if err := cmd.Start(); err != nil {
		rOut.Close()
		wOut.Close()
		rErr.Close()
		wErr.Close()
		return r.fail("start codex: " + err.Error()), err
	}
	// The child holds its own dup of the write ends; the parent must release its
	// copies or the read ends would never reach EOF.
	wOut.Close()
	wErr.Close()

	r.mu.Lock()
	r.st.CodexPID = cmd.Process.Pid
	r.persistLocked()
	r.mu.Unlock()
	r.emit("started codex pid " + itoa(cmd.Process.Pid) + " (" + spec.Title + ")")

	var readers sync.WaitGroup
	readers.Add(2)
	go func() { defer readers.Done(); r.readStdout(rOut) }()
	go func() { defer readers.Done(); r.readStderr(rErr) }()

	// Authoritative completion: wait on the process itself.
	procExited := make(chan struct{})
	var waitErr error
	go func() {
		waitErr = cmd.Wait()
		close(procExited)
	}()

	stopWatch := make(chan struct{})
	var watchDone sync.WaitGroup
	watchDone.Add(1)
	go func() { defer watchDone.Done(); r.watchdog(cmd, procExited, stopWatch) }()

	<-procExited // the process has exited; exit code is now available

	// Let readers drain buffered output, but never block forever: if a lingering
	// grandchild still holds a write end, terminate the group and force EOF.
	if !waitGroupTimeout(&readers, drainGrace) {
		r.emit("output pipe held open after exit; force-closing")
		proc.TerminateGroup(cmd.Process.Pid, 2*time.Second)
		rOut.Close()
		rErr.Close()
		readers.Wait()
	}
	close(stopWatch)
	watchDone.Wait()
	rOut.Close()
	rErr.Close()

	return r.finalize(cmd, waitErr, resultFile), nil
}

// waitGroupTimeout waits for wg, returning false if d elapses first.
func waitGroupTimeout(wg *sync.WaitGroup, d time.Duration) bool {
	done := make(chan struct{})
	go func() { wg.Wait(); close(done) }()
	select {
	case <-done:
		return true
	case <-time.After(d):
		return false
	}
}

// touch records activity (any event or output byte resets the idle clock).
func (r *runner) touch() {
	r.mu.Lock()
	r.lastActivity = time.Now()
	r.mu.Unlock()
}

func (r *runner) readStdout(stdout io.Reader) {
	br := bufio.NewReader(stdout)
	for {
		line, err := br.ReadString('\n')
		if len(line) > 0 {
			r.handleStdoutLine(line)
		}
		if err != nil {
			return
		}
	}
}

func (r *runner) handleStdoutLine(line string) {
	r.touch()
	if r.spec.JSONMode {
		r.writeEvents(line)
		ev, ok := events.Parse(line)
		if !ok {
			return
		}
		r.applyEvent(ev)
		return
	}
	// Non-JSON: tee raw output to the log and accumulate as the result.
	r.writeLogRaw(line)
	r.mu.Lock()
	r.resultBuf.WriteString(line)
	r.mu.Unlock()
}

func (r *runner) applyEvent(ev events.Event) {
	phase, summary := ev.Describe()
	r.mu.Lock()
	r.st.EventCount++
	now := time.Now()
	r.st.LastEventAt = &now
	if summary != "" {
		r.st.LastEvent = summary
	}
	if phase != "" {
		r.st.Phase = string(phase)
	}
	if ev.ThreadID != "" {
		r.st.ThreadID = ev.ThreadID
	}
	if ev.Usage != nil {
		r.st.Usage = ev.Usage
	}
	if ev.Item != nil && ev.Item.Type == "command_execution" {
		switch ev.Type {
		case "item.started":
			r.inFlightCmds[ev.Item.ID] = true
		case "item.completed":
			delete(r.inFlightCmds, ev.Item.ID)
		}
	}
	if ev.Item != nil && ev.Item.Type == "agent_message" && ev.Type == "item.completed" && ev.Item.Text != "" {
		r.resultText = ev.Item.Text
	}
	// `codex exec review` delivers its findings as the review payload of an
	// exited-review-mode item rather than a plain agent_message; capture it so
	// the review output is surfaced as the result.
	if ev.Item != nil && ev.Type == "item.completed" && ev.Item.Review != "" {
		r.resultText = ev.Item.Review
	}
	if ev.Type == "error" || ev.Type == "turn.failed" {
		r.sawFailureEvent = true
		if r.st.Error == "" {
			r.st.Error = summary
		}
	}
	r.mu.Unlock()
	if summary != "" {
		r.emit(summary)
	}
}

func (r *runner) readStderr(stderr io.Reader) {
	br := bufio.NewReader(stderr)
	for {
		line, err := br.ReadString('\n')
		if len(line) > 0 {
			trimmed := strings.TrimRight(line, "\r\n")
			r.touch()
			r.writeLogRaw("stderr: " + line)
			if isMeaningfulStderr(trimmed) {
				r.mu.Lock()
				r.stderrTail = append(r.stderrTail, trimmed)
				if len(r.stderrTail) > stderrTailMax {
					r.stderrTail = r.stderrTail[len(r.stderrTail)-stderrTailMax:]
				}
				r.mu.Unlock()
			}
		}
		if err != nil {
			return
		}
	}
}

func isMeaningfulStderr(line string) bool {
	t := strings.TrimSpace(line)
	if t == "" {
		return false
	}
	// Benign noise codex always prints; not a real error.
	if strings.HasPrefix(t, "Reading additional input from stdin") {
		return false
	}
	if strings.HasPrefix(t, "WARNING: proceeding, even though we could not update PATH") {
		return false
	}
	return true
}

// watchdog ticks once per second, recomputing health, persisting status,
// emitting heartbeats, and enforcing the cancel/stall/timeout policy. It stops
// as soon as the process exits — a natural exit always wins over a threshold.
func (r *runner) watchdog(cmd *exec.Cmd, procExited <-chan struct{}, stop <-chan struct{}) {
	ticker := time.NewTicker(time.Second)
	defer ticker.Stop()

	hb := r.spec.Thresholds.HeartbeatSec
	var lastBeat time.Time
	for {
		select {
		case <-procExited:
			return
		case <-stop:
			return
		case <-ticker.C:
		}
		// A buffered tick must not beat a just-exited process.
		select {
		case <-procExited:
			return
		default:
		}

		now := time.Now()
		r.mu.Lock()
		elapsed := now.Sub(r.start).Seconds()
		idle := now.Sub(r.lastActivity).Seconds()
		inFlight := len(r.inFlightCmds)
		r.st.ElapsedSec = round1(elapsed)
		r.st.IdleSec = round1(idle)
		r.st.UpdatedAt = now
		r.st.Health = classifyHealth(r.st, idle, inFlight)
		killReason := r.decideKillLocked(idle, elapsed, inFlight)
		r.persistLocked()
		stderrTail := append([]string(nil), r.stderrTail...)
		r.mu.Unlock()

		if killReason != "" {
			r.emit("terminating codex: " + killReason)
			proc.TerminateGroup(cmd.Process.Pid, 3*time.Second)
			return
		}

		if hb > 0 && (lastBeat.IsZero() || now.Sub(lastBeat).Seconds() >= hb) {
			lastBeat = now
			r.emit(r.heartbeatLine(elapsed, idle, stderrTail))
		}
	}
}

// decideKillLocked checks cancel/stall/timeout and, if firing, records the
// terminal kill-state. The stall check is suppressed while a command is in
// flight (codex emits no events during a long shell command, but it is working,
// not hung — the wall-clock timeout still backstops that case). Caller holds r.mu.
func (r *runner) decideKillLocked(idle, elapsed float64, inFlight int) (reason string) {
	if job.CancelRequested(r.dir) {
		r.killState = job.StateCancelled
		return "cancel requested"
	}
	if t := r.spec.Thresholds.StalledSec; t > 0 && inFlight == 0 && idle >= t {
		r.killState = job.StateStalled
		return "no activity for " + itoa(int(idle)) + "s (stall ceiling " + itoa(int(t)) + "s)"
	}
	if t := r.spec.Thresholds.WallSec; t > 0 && elapsed >= t {
		r.killState = job.StateTimeout
		return "wall-clock timeout after " + itoa(int(elapsed)) + "s (limit " + itoa(int(t)) + "s)"
	}
	return ""
}

func classifyHealth(st *job.Status, idle float64, inFlight int) job.Health {
	slow := st.Thresholds.SlowAfterSec
	stalled := st.Thresholds.StalledSec
	// Genuinely early (no events yet) only counts as "starting" until it has
	// been idle long enough to be worth flagging; after that it must escalate.
	if st.EventCount == 0 && st.JSONMode && (slow <= 0 || idle < slow) {
		return job.HealthStarting
	}
	// A command in flight means codex is actively working even while quiet.
	if inFlight > 0 {
		if slow > 0 && idle >= slow {
			return job.HealthSlow
		}
		return job.HealthHealthy
	}
	switch {
	case stalled > 0 && idle >= stalled:
		return job.HealthStalled
	case slow > 0 && idle >= slow:
		return job.HealthSlow
	default:
		return job.HealthHealthy
	}
}

func (r *runner) heartbeatLine(elapsed, idle float64, stderrTail []string) string {
	r.mu.Lock()
	phase := r.st.Phase
	health := r.st.Health
	last := r.st.LastEvent
	r.mu.Unlock()
	line := "♥ " + string(health) + " phase=" + phase +
		" elapsed=" + itoa(int(elapsed)) + "s idle=" + itoa(int(idle)) + "s"
	if last != "" {
		line += " last=\"" + last + "\""
	}
	if health == job.HealthStalled && len(stderrTail) > 0 {
		line += " stderr=\"" + stderrTail[len(stderrTail)-1] + "\""
	}
	return line
}

// finalize computes the terminal state, captures the result, and writes the
// final status.
func (r *runner) finalize(cmd *exec.Cmd, waitErr error, resultFile string) *job.Status {
	now := time.Now()
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.finalized {
		return r.st
	}
	r.finalized = true

	exitCode := 0
	if cmd.ProcessState != nil {
		exitCode = cmd.ProcessState.ExitCode()
	}
	if exitCode >= 0 {
		ec := exitCode
		r.st.ExitCode = &ec
	}
	cleanExit := waitErr == nil && exitCode == 0

	switch {
	case r.killState != "":
		// The watchdog deliberately terminated codex (cancel/stall/timeout).
		// Honor that verdict regardless of the exit code: codex may exit 0 in
		// response to our SIGTERM, and reporting "completed" would mask a real
		// hang — the exact failure codexmon exists to surface. A genuinely
		// natural exit never reaches here with killState set, because the
		// watchdog checks procExited before deciding to kill.
		r.st.State = r.killState
		r.st.Health = job.HealthDead
		if r.st.Error == "" {
			r.st.Error = killMessage(r.killState)
		}
	case cleanExit && !r.sawFailureEvent:
		r.st.State = job.StateCompleted
		r.st.Health = job.HealthDone
	default:
		r.st.State = job.StateFailed
		r.st.Health = job.HealthDead
		if r.st.Error == "" {
			r.st.Error = r.failureMessageLocked(exitCode)
		}
	}

	result := r.captureResultLocked(resultFile)
	if result != "" {
		_ = os.WriteFile(resultFile, []byte(result), 0o644)
		r.st.ResultPreview = preview(result, 600)
	}

	r.st.EndedAt = &now
	r.st.UpdatedAt = now
	r.st.ElapsedSec = round1(now.Sub(r.start).Seconds())
	r.st.IdleSec = round1(now.Sub(r.lastActivity).Seconds())
	r.persistLocked()
	r.emit("done: state=" + string(r.st.State) + " elapsed=" + itoa(int(r.st.ElapsedSec)) + "s")
	return r.st
}

// captureResultLocked returns the best available final output. Prefers the
// codex --output-last-message file, then the streamed agent_message, then the
// accumulated stdout (non-JSON mode). Caller holds r.mu.
func (r *runner) captureResultLocked(resultFile string) string {
	if data, err := os.ReadFile(resultFile); err == nil {
		if s := strings.TrimSpace(string(data)); s != "" {
			return s
		}
	}
	if s := strings.TrimSpace(r.resultText); s != "" {
		return s
	}
	return strings.TrimSpace(r.resultBuf.String())
}

func (r *runner) failureMessageLocked(exitCode int) string {
	if len(r.stderrTail) > 0 {
		return "codex exited " + itoa(exitCode) + ": " + r.stderrTail[len(r.stderrTail)-1]
	}
	return "codex exited with code " + itoa(exitCode)
}

func (r *runner) persistLocked() {
	_ = job.WriteStatus(r.dir, r.st)
}

// emit writes a human-readable, timestamped line to the job log and (if set)
// the foreground progress writer.
func (r *runner) emit(msg string) {
	if msg == "" {
		return
	}
	line := ts() + " " + msg + "\n"
	r.writeLogRaw(line)
	if r.opts.Progress != nil {
		_, _ = io.WriteString(r.opts.Progress, line)
	}
}

func (r *runner) writeLogRaw(s string) {
	if r.logF == nil {
		return
	}
	r.logMu.Lock()
	defer r.logMu.Unlock()
	if r.logCapped {
		return
	}
	if r.logBytes+int64(len(s)) > maxLogBytes {
		_, _ = r.logF.WriteString("\n[log truncated: exceeded " + itoa(maxLogBytes>>20) + " MiB]\n")
		r.logCapped = true
		return
	}
	n, _ := r.logF.WriteString(s)
	r.logBytes += int64(n)
}

func (r *runner) writeEvents(line string) {
	if r.evF == nil {
		return
	}
	r.logMu.Lock()
	defer r.logMu.Unlock()
	if r.evCapped {
		return
	}
	if r.evBytes+int64(len(line)) > maxLogBytes {
		r.evCapped = true
		return
	}
	n, _ := r.evF.WriteString(line)
	r.evBytes += int64(n)
}

// fail builds a terminal failed status for setup errors before codex starts.
func (r *runner) fail(msg string) *job.Status {
	now := time.Now()
	r.mu.Lock()
	r.st.State = job.StateFailed
	r.st.Health = job.HealthDead
	r.st.Error = msg
	r.st.EndedAt = &now
	r.st.UpdatedAt = now
	r.persistLocked()
	st := r.st
	r.mu.Unlock()
	r.emit("failed: " + msg) // emit locks logMu only, not r.mu
	return st
}

func killMessage(s job.State) string {
	switch s {
	case job.StateCancelled:
		return "cancelled by request"
	case job.StateStalled:
		return "terminated after stalling (no activity past the idle ceiling)"
	case job.StateTimeout:
		return "terminated after exceeding the wall-clock timeout"
	default:
		return string(s)
	}
}

func ts() string { return time.Now().Format("15:04:05") }

func round1(f float64) float64 {
	return float64(int64(f*10+0.5)) / 10
}

func preview(s string, limit int) string {
	s = strings.TrimSpace(s)
	if len(s) <= limit {
		return s
	}
	return s[:limit] + "…"
}

func itoa(n int) string {
	if n == 0 {
		return "0"
	}
	neg := n < 0
	if neg {
		n = -n
	}
	var buf [20]byte
	i := len(buf)
	for n > 0 {
		i--
		buf[i] = byte('0' + n%10)
		n /= 10
	}
	if neg {
		i--
		buf[i] = '-'
	}
	return string(buf[i:])
}
