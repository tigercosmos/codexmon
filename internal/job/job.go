// Package job owns the on-disk representation of a monitored agent run.
//
// Every run gets a directory under the codexmon home (default ~/.codexmon/jobs/<id>):
//
//	spec.json     immutable launch spec (agent, args, cwd, thresholds) read by the worker
//	status.json   live status, rewritten by the monitor at least once per second
//	events.jsonl  raw agent event-stream lines (when JSON monitoring is on)
//	output.log    merged human-readable stdout/stderr log
//	result.txt    final agent message / review output
//	cancel        marker file; its presence asks the monitor to stop
//
// status.json is the contract `codexmon status/wait/list/tail` reads, so it is
// written atomically (temp file + rename) to avoid torn reads by a poller.
package job

import (
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/tigercosmos/codexmon/internal/agent"
	"github.com/tigercosmos/codexmon/internal/proc"
)

// State is the lifecycle of a job. queued/running are active; the rest terminal.
type State string

const (
	StateQueued    State = "queued"
	StateRunning   State = "running"
	StateCompleted State = "completed"
	StateFailed    State = "failed"
	StateStalled   State = "stalled"
	StateTimeout   State = "timeout"
	StateCancelled State = "cancelled"
)

// Active reports whether the state is non-terminal.
func (s State) Active() bool { return s == StateQueued || s == StateRunning }

// Health is the liveness verdict derived from idle time while running.
type Health string

const (
	HealthStarting Health = "starting"
	HealthHealthy  Health = "healthy"
	HealthSlow     Health = "slow"
	HealthStalled  Health = "stalled"
	HealthDone     Health = "done"
	HealthDead     Health = "dead"
)

// Thresholds are the watchdog limits, in seconds. A zero value disables that check.
type Thresholds struct {
	HeartbeatSec float64 `json:"heartbeat_sec"`
	SlowAfterSec float64 `json:"slow_after_sec"`
	StalledSec   float64 `json:"stalled_sec"`    // idle ceiling when nothing is in flight
	ToolStuckSec float64 `json:"tool_stuck_sec"` // max time a single MCP/tool call may run
	WallSec      float64 `json:"wall_sec"`
}

// Status is the full, serialized state of a job. It is both the live status
// file and the structure emitted by `--json`.
type Status struct {
	ID     string `json:"id"`
	State  State  `json:"state"`
	Health Health `json:"health"`
	Phase  string `json:"phase"`

	Agent    string   `json:"agent"`     // which agent ran (codex/claude/cursor)
	AgentBin string   `json:"agent_bin"` // resolved path to the agent binary
	Args     []string `json:"args"`      // args passed to the agent
	Cwd      string   `json:"cwd"`
	JSONMode bool     `json:"json_mode"` // true when monitoring the JSON event stream

	WorkerPID int `json:"worker_pid"` // process that owns the agent child
	AgentPID  int `json:"agent_pid"`  // agent process group leader

	StartedAt   time.Time  `json:"started_at"`
	UpdatedAt   time.Time  `json:"updated_at"`
	EndedAt     *time.Time `json:"ended_at,omitempty"`      // nil until terminal
	LastEventAt *time.Time `json:"last_event_at,omitempty"` // nil until first event

	ElapsedSec float64 `json:"elapsed_sec"`
	IdleSec    float64 `json:"idle_sec"`

	EventCount int          `json:"event_count"`
	LastEvent  string       `json:"last_event"`
	ThreadID   string       `json:"thread_id,omitempty"`
	Usage      *agent.Usage `json:"usage,omitempty"`

	ExitCode *int   `json:"exit_code,omitempty"`
	Error    string `json:"error,omitempty"`

	// ResultPreview is a truncated copy of the final output for at-a-glance
	// status; the full text lives in result.txt (ResultFile).
	ResultPreview string `json:"result_preview,omitempty"`

	// Result is the final output, capped at 4 MiB — past the cap it ends with
	// a truncation notice naming ResultFile, which always holds the full text.
	// It is never persisted in status.json (the file is rewritten every
	// second; ResultFile holds the text once) — the CLI fills it from
	// ResultFile when emitting terminal JSON for wait/run, so a consumer gets
	// the whole answer in one call.
	Result string `json:"result,omitempty"`

	Thresholds Thresholds `json:"thresholds"`

	Dir        string `json:"dir"`
	EventsFile string `json:"events_file,omitempty"`
	LogFile    string `json:"log_file"`
	ResultFile string `json:"result_file"`

	// Title is a short human label (e.g. "codex exec review").
	Title string `json:"title"`
}

// Spec is the immutable launch description persisted for the detached worker.
type Spec struct {
	ID           string     `json:"id"`
	Agent        string     `json:"agent"`     // which agent to run (codex/claude/cursor)
	AgentBin     string     `json:"agent_bin"` // resolved path to the agent binary
	Args         []string   `json:"args"`
	Cwd          string     `json:"cwd"`
	JSONMode     bool       `json:"json_mode"`
	ForwardStdin bool       `json:"forward_stdin"`
	Thresholds   Thresholds `json:"thresholds"`
	Title        string     `json:"title"`
	// Env, when non-empty, fully REPLACES the agent's environment (it is not
	// merged with the parent's). codexmon never sets it — the agent inherits the
	// launcher's environment — so it exists only for tests/advanced use; a
	// hand-edited spec.json that sets it must include PATH/HOME/etc.
	Env []string `json:"env,omitempty"`
}

// Home returns the codexmon home directory ($CODEXMON_HOME or ~/.codexmon).
func Home() (string, error) {
	if h := strings.TrimSpace(os.Getenv("CODEXMON_HOME")); h != "" {
		return h, nil
	}
	home, err := os.UserHomeDir()
	if err != nil {
		return "", err
	}
	return filepath.Join(home, ".codexmon"), nil
}

func jobsRoot() (string, error) {
	h, err := Home()
	if err != nil {
		return "", err
	}
	return filepath.Join(h, "jobs"), nil
}

// idPattern is the canonical job id shape; it also gates ids that reach the
// filesystem so a caller-supplied id can never traverse outside the jobs root.
var idPattern = regexp.MustCompile(`^cdx-[0-9]{8}-[0-9]{6}-[0-9a-f]{6}$`)

// NewID returns a sortable, unique job id like "cdx-20260603-150405-9f3a1c".
func NewID() string {
	var b [3]byte
	if _, err := rand.Read(b[:]); err != nil {
		// crypto/rand should never fail here, but never emit a predictable or
		// colliding suffix if it does — mix in the nanosecond clock.
		n := time.Now().UnixNano()
		b[0], b[1], b[2] = byte(n), byte(n>>8), byte(n>>16)
	}
	return fmt.Sprintf("cdx-%s-%s", time.Now().Format("20060102-150405"), hex.EncodeToString(b[:]))
}

// ValidID reports whether id is a well-formed, traversal-safe job id.
func ValidID(id string) bool {
	return idPattern.MatchString(id)
}

// Dir returns (and creates) the directory for a job id. Directories are 0700:
// codex prompts, output, and review text may contain secrets.
func Dir(id string) (string, error) {
	if !ValidID(id) {
		return "", fmt.Errorf("invalid job id %q", id)
	}
	root, err := jobsRoot()
	if err != nil {
		return "", err
	}
	dir := filepath.Join(root, id)
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return "", err
	}
	return dir, nil
}

// Paths returns the standard file paths within a job directory.
func Paths(dir string) (spec, status, eventsFile, logFile, resultFile, cancel string) {
	return filepath.Join(dir, "spec.json"),
		filepath.Join(dir, "status.json"),
		filepath.Join(dir, "events.jsonl"),
		filepath.Join(dir, "output.log"),
		filepath.Join(dir, "result.txt"),
		filepath.Join(dir, "cancel")
}

func writeJSONAtomic(path string, v any) error {
	data, err := json.MarshalIndent(v, "", "  ")
	if err != nil {
		return err
	}
	data = append(data, '\n')
	tmp, err := os.CreateTemp(filepath.Dir(path), ".tmp-*")
	if err != nil {
		return err
	}
	tmpName := tmp.Name()
	// os.CreateTemp already uses 0600, but pin it explicitly: job records can
	// hold prompts and review output, which must not be briefly world-readable.
	if err := tmp.Chmod(0o600); err != nil {
		tmp.Close()
		os.Remove(tmpName)
		return err
	}
	if _, err := tmp.Write(data); err != nil {
		tmp.Close()
		os.Remove(tmpName)
		return err
	}
	if err := tmp.Close(); err != nil {
		os.Remove(tmpName)
		return err
	}
	if err := os.Rename(tmpName, path); err != nil {
		os.Remove(tmpName)
		return err
	}
	return nil
}

// WriteSpec persists the immutable launch spec.
func WriteSpec(dir string, s *Spec) error {
	specPath, _, _, _, _, _ := Paths(dir)
	return writeJSONAtomic(specPath, s)
}

// ReadSpec loads a launch spec.
func ReadSpec(dir string) (*Spec, error) {
	specPath, _, _, _, _, _ := Paths(dir)
	data, err := os.ReadFile(specPath)
	if err != nil {
		return nil, err
	}
	var s Spec
	if err := json.Unmarshal(data, &s); err != nil {
		return nil, err
	}
	return &s, nil
}

// WriteStatus atomically writes status.json.
func WriteStatus(dir string, s *Status) error {
	_, statusPath, _, _, _, _ := Paths(dir)
	return writeJSONAtomic(statusPath, s)
}

// ReadStatus reads status.json for a job directory. If the status still claims
// to be active but the owning worker process is gone, it is reconciled to a
// terminal failed state — otherwise a crashed worker would leave the job
// reported as "running"/"healthy" forever, the exact misread codexmon must
// avoid. Reconciliation is applied to the returned value only; callers that
// want it persisted should WriteStatus it back.
func ReadStatus(dir string) (*Status, error) {
	_, statusPath, _, _, _, _ := Paths(dir)
	data, err := os.ReadFile(statusPath)
	if err != nil {
		return nil, err
	}
	var s Status
	if err := json.Unmarshal(data, &s); err != nil {
		return nil, err
	}
	wasActive := s.State.Active()
	workerDead := s.WorkerPID > 0 && !proc.Alive(s.WorkerPID)
	fresh := !s.UpdatedAt.IsZero() && time.Since(s.UpdatedAt) <= orphanReapLimit
	reconcileLiveness(&s)
	if wasActive && !s.State.Active() {
		// Reap the agent process group the dead worker orphaned. Four guards
		// stand between us and killing an innocent process that inherited a
		// recycled pid: the status must be recent (a worker rewrites it every
		// second, so a live orphan is always found within seconds of being
		// abandoned — anything older is not worth the risk), the pid must still
		// be alive, it must still lead its own group the way every agent child
		// does, and — the decisive one — its process must have started inside
		// this job's own launch window.
		if workerDead && fresh && s.AgentPID > 0 && proc.Alive(s.AgentPID) &&
			proc.IsGroupLeader(s.AgentPID) && proc.StartedBefore(s.AgentPID, s.StartedAt.Add(agentStartSlack)) {
			proc.TerminateGroup(s.AgentPID, 2*time.Second)
		}
		// Persist the verdict so anything reading status.json directly — not
		// just through this function — sees the real state. The one exception is
		// a worker that is alive but has stopped updating: it may merely have
		// been suspended, and writing "failed" under a run that then resumes
		// would leave the record flip-flopping. That verdict stays in the value
		// we return.
		workerUnresponsive := !workerDead && s.WorkerPID > 0
		if !workerUnresponsive {
			_ = writeJSONAtomic(statusPath, &s) // best effort; a read must still return
		}
	}
	return &s, nil
}

const (
	// workerWedgedLimit is how long an ALIVE worker's status.json may go
	// un-updated before the job is reported dead. The monitor rewrites status
	// every watchdog tick (~1s), so any gap means something is wrong — but a
	// suspended laptop produces exactly the same signal, and both the worker and
	// its poller resume together. The limit is therefore generous: a wedged run
	// is reported minutes late, whereas a tight limit would report a perfectly
	// healthy run as failed every time the machine woke up.
	workerWedgedLimit = 10 * time.Minute

	// queuedStaleLimit bounds how long a job may sit "queued" with no worker pid
	// recorded. The worker takes ownership of status.json as its first act, so a
	// job still in the launcher's seed state past this window is debris — either
	// a launcher killed mid-launch or a worker that died before it could write.
	// It must age out, since Latest() prefers active jobs and such a job would
	// otherwise shadow every later one forever. The window is wide enough to
	// cover a worker that is merely slow to start on a loaded machine.
	queuedStaleLimit = time.Minute

	// agentStartSlack is how long after a job's recorded start its agent may have
	// begun and still be considered that job's agent. codexmon spawns the agent
	// within a second; the margin only absorbs a slow launch or a clock nudge.
	agentStartSlack = 10 * time.Minute

	// orphanReapLimit bounds how stale a status may be before ReadStatus stops
	// signalling the agent pid recorded in it. It is generous on purpose:
	// declining to reap leaves an agent running unwatched, which is the failure
	// codexmon exists to prevent, so the bound only has to exclude records old
	// enough that pid reuse is a genuine worry. `codexmon cancel` reaps an
	// orphan regardless of age, since there the user has asked for it directly.
	orphanReapLimit = time.Hour
)

// reconcileLiveness downgrades an active status that can no longer be advancing.
// The worker is the sole writer of status.json, so if it is gone — or was never
// spawned, or has stopped writing for long enough to be considered wedged — the
// status can never change again and must not be reported as still running.
//
// Reconciliation is applied to the passed value; only the definitively-dead case
// is persisted by ReadStatus, so a merely-suspended worker that wakes up keeps
// its own record.
func reconcileLiveness(s *Status) {
	if s == nil || !s.State.Active() {
		return
	}
	if s.WorkerPID <= 0 {
		// No worker was ever recorded. Only a queued job can legitimately be in
		// this state, and only briefly, between the launcher's seed write and
		// its pid write.
		if s.State == StateQueued && stalerThan(progressTime(s), queuedStaleLimit) {
			markDead(s, "no worker process was ever recorded; the launch did not complete")
		}
		return
	}
	alive := proc.Alive(s.WorkerPID)
	switch {
	case !alive:
		markDead(s, fmt.Sprintf("worker process %d is no longer running; the job ended without recording a result", s.WorkerPID))
	case stalerThan(s.UpdatedAt, workerWedgedLimit):
		markDead(s, fmt.Sprintf("status has not updated in %s; the worker appears wedged (or pid %d was reused)",
			time.Since(s.UpdatedAt).Round(time.Second), s.WorkerPID))
	}
}

// stalerThan reports whether t is set and older than d.
func stalerThan(t time.Time, d time.Duration) bool {
	return !t.IsZero() && time.Since(t) > d
}

// progressTime is the most recent moment a job is known to have made progress,
// falling back to its start for a record written without an UpdatedAt.
func progressTime(s *Status) time.Time {
	if !s.UpdatedAt.IsZero() {
		return s.UpdatedAt
	}
	return s.StartedAt
}

// markDead moves a status to failed/dead, keeping any error it already carries.
func markDead(s *Status, reason string) {
	s.State = StateFailed
	s.Health = HealthDead
	if s.Error == "" {
		s.Error = reason
	}
	if s.EndedAt == nil {
		now := time.Now()
		s.EndedAt = &now
	}
}

// ReadStatusByID resolves a job id to its status.
func ReadStatusByID(id string) (*Status, error) {
	if !ValidID(id) {
		return nil, fmt.Errorf("invalid job id %q", id)
	}
	root, err := jobsRoot()
	if err != nil {
		return nil, err
	}
	return ReadStatus(filepath.Join(root, id))
}

// ErrNoJobs is returned by Latest when no jobs exist.
var ErrNoJobs = errors.New("no codexmon jobs found")

// List returns all known job statuses, newest first.
func List() ([]*Status, error) {
	root, err := jobsRoot()
	if err != nil {
		return nil, err
	}
	entries, err := os.ReadDir(root)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return nil, nil
		}
		return nil, err
	}
	var out []*Status
	for _, e := range entries {
		if !e.IsDir() {
			continue
		}
		st, err := ReadStatus(filepath.Join(root, e.Name()))
		if err != nil {
			continue // skip half-initialized dirs
		}
		out = append(out, st)
	}
	sort.Slice(out, func(i, j int) bool {
		return out[i].StartedAt.After(out[j].StartedAt)
	})
	return out, nil
}

// Latest returns the most recently started job, preferring active ones.
func Latest() (*Status, error) {
	all, err := List()
	if err != nil {
		return nil, err
	}
	if len(all) == 0 {
		return nil, ErrNoJobs
	}
	for _, s := range all {
		if s.State.Active() {
			return s, nil
		}
	}
	return all[0], nil
}

// Resolve returns the status for an explicit id, or the latest job if id == "".
func Resolve(id string) (*Status, error) {
	if strings.TrimSpace(id) == "" {
		return Latest()
	}
	st, err := ReadStatusByID(id)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return nil, fmt.Errorf("no job %q found (try `codexmon list`)", id)
		}
		return nil, err
	}
	return st, nil
}

// ---- retention ---------------------------------------------------------------

// Default retention: terminal jobs older than this, or beyond this many, are
// pruned. Active jobs are never touched.
const (
	defaultKeepAge   = 7 * 24 * time.Hour
	defaultKeepCount = 200
)

// recentlyEndedGrace bounds how long after a job's recorded end its still-live
// worker protects it from pruning. See Prune.
const recentlyEndedGrace = 2 * workerWedgedLimit

// unreadableGrace is the minimum age (by dir mtime) before a job directory with
// no readable status may be pruned. A launch writes its seed status within
// milliseconds of creating the directory, so anything past this grace is
// crashed-launch debris, not a run in progress.
const unreadableGrace = 10 * time.Minute

// PruneOptions bound which terminal jobs Prune removes. A zero or negative
// limit disables that dimension; All removes every terminal job regardless.
type PruneOptions struct {
	MaxAge   time.Duration // remove terminal jobs that ended longer ago than this
	MaxCount int           // keep at most this many terminal jobs (newest first)
	All      bool          // remove all terminal jobs
}

// DefaultPruneOptions returns the retention policy: the defaults above,
// overridable via CODEXMON_KEEP_DAYS and CODEXMON_KEEP_JOBS (0 disables that
// limit; non-numeric or negative values are ignored).
func DefaultPruneOptions() PruneOptions {
	opts := PruneOptions{MaxAge: defaultKeepAge, MaxCount: defaultKeepCount}
	if v, ok := envInt("CODEXMON_KEEP_DAYS"); ok {
		opts.MaxAge = time.Duration(v) * 24 * time.Hour
	}
	if v, ok := envInt("CODEXMON_KEEP_JOBS"); ok {
		opts.MaxCount = v
	}
	return opts
}

func envInt(name string) (int, bool) {
	v := strings.TrimSpace(os.Getenv(name))
	if v == "" {
		return 0, false
	}
	n, err := strconv.Atoi(v)
	if err != nil || n < 0 {
		return 0, false
	}
	return n, true
}

// AutoPrune applies DefaultPruneOptions. Launch paths call it best-effort so
// the jobs directory stays bounded (logs are capped per job, but the number of
// jobs would otherwise grow forever, and every list/status scan reads them all).
func AutoPrune() (int, error) {
	opts := DefaultPruneOptions()
	if opts.MaxAge <= 0 && opts.MaxCount <= 0 {
		return 0, nil
	}
	return Prune(opts)
}

// Prune deletes old terminal job directories and returns how many were
// removed. Active (queued/running) jobs are never removed — a status that
// *claims* active but whose worker is dead reconciles to terminal via
// ReadStatus first, so it ages out like any other finished job. A directory
// with no readable status (half-initialized or corrupt) is removed once its
// mtime is older than MaxAge, so a crashed launch cannot linger forever.
func Prune(opts PruneOptions) (removed int, err error) {
	root, err := jobsRoot()
	if err != nil {
		return 0, err
	}
	entries, err := os.ReadDir(root)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return 0, nil
		}
		return 0, err
	}
	now := time.Now()
	type ended struct {
		dir string
		at  time.Time
	}
	var terminal []ended
	for _, e := range entries {
		// Only touch well-formed job dirs, never a foreign file or directory
		// something else placed under the jobs root.
		if !e.IsDir() || !ValidID(e.Name()) {
			continue
		}
		dir := filepath.Join(root, e.Name())
		st, rerr := ReadStatus(dir)
		if rerr != nil {
			// No readable status — either a launch still inside its dir-created →
			// status-written window, or debris from a crashed one. Never remove it
			// younger than the grace age (not even for --all), so pruning cannot
			// race a concurrent launch.
			age := unreadableGrace
			if !opts.All {
				if opts.MaxAge <= 0 {
					continue
				}
				age = max(opts.MaxAge, unreadableGrace)
			}
			if dirOlderThan(dir, now, age) && os.RemoveAll(dir) == nil {
				removed++
			}
			continue
		}
		if st.State.Active() {
			continue
		}
		// A terminal verdict is not always proof the writer is gone: a worker
		// that stopped updating long enough to look wedged reconciles to failed
		// while its process is still there (a suspended machine does exactly
		// this). Removing the directory would pull the log, events, and result
		// files out from under a run that is about to resume writing them.
		//
		// The recency bound matters as much as the liveness probe: without it, an
		// ancient job whose pid had been recycled by an unrelated process would be
		// exempt from retention forever.
		//
		// This guard holds even for --all. A job whose worker is still running is
		// not finished, whatever its record currently says, and removing the
		// directory would strand that worker writing into deleted files with no
		// status, no log, and no cancel marker — an unobservable process, which is
		// the one outcome codexmon must never produce. "Remove every terminal job"
		// has always excluded jobs that are still going.
		if st.WorkerPID > 0 && !stalerThan(endedTime(st), recentlyEndedGrace) && proc.Alive(st.WorkerPID) {
			continue
		}
		terminal = append(terminal, ended{dir: dir, at: endedTime(st)})
	}
	sort.Slice(terminal, func(i, j int) bool { return terminal[i].at.After(terminal[j].at) })
	for i, t := range terminal {
		drop := opts.All ||
			(opts.MaxAge > 0 && now.Sub(t.at) > opts.MaxAge) ||
			(opts.MaxCount > 0 && i >= opts.MaxCount)
		if drop && os.RemoveAll(t.dir) == nil {
			removed++
		}
	}
	return removed, nil
}

// endedTime is when a terminal job finished, with fallbacks for statuses that
// predate EndedAt or were reconciled without one.
func endedTime(s *Status) time.Time {
	if s.EndedAt != nil {
		return *s.EndedAt
	}
	if !s.UpdatedAt.IsZero() {
		return s.UpdatedAt
	}
	return s.StartedAt
}

func dirOlderThan(dir string, now time.Time, age time.Duration) bool {
	fi, err := os.Stat(dir)
	if err != nil {
		return false
	}
	return now.Sub(fi.ModTime()) > age
}

// RequestCancel writes the cancel marker the monitor polls for.
func RequestCancel(dir string) error {
	_, _, _, _, _, cancel := Paths(dir)
	return os.WriteFile(cancel, []byte(time.Now().Format(time.RFC3339Nano)), 0o600)
}

// CancelRequested reports whether the cancel marker exists.
func CancelRequested(dir string) bool {
	_, _, _, _, _, cancel := Paths(dir)
	_, err := os.Stat(cancel)
	return err == nil
}
