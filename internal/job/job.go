// Package job owns the on-disk representation of a monitored Codex run.
//
// Every run gets a directory under the codexmon home (default ~/.codexmon/jobs/<id>):
//
//	spec.json     immutable launch spec (args, cwd, thresholds) read by the worker
//	status.json   live status, rewritten by the monitor at least once per second
//	events.jsonl  raw `codex exec --json` event lines (when JSON monitoring is on)
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
	"strings"
	"time"

	"github.com/tigercosmos/codexmon/internal/events"
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

// Thresholds are the watchdog limits. A zero duration disables that check.
type Thresholds struct {
	HeartbeatSec float64 `json:"heartbeat_sec"`
	SlowAfterSec float64 `json:"slow_after_sec"`
	StalledSec   float64 `json:"stalled_sec"`
	WallSec      float64 `json:"wall_sec"`
}

// Status is the full, serialized state of a job. It is both the live status
// file and the structure emitted by `--json`.
type Status struct {
	ID     string `json:"id"`
	State  State  `json:"state"`
	Health Health `json:"health"`
	Phase  string `json:"phase"`

	CodexBin string   `json:"codex_bin"`
	Args     []string `json:"args"` // args passed to codex
	Cwd      string   `json:"cwd"`
	JSONMode bool     `json:"json_mode"` // true when monitoring the --json event stream

	WorkerPID int `json:"worker_pid"` // process that owns the codex child
	CodexPID  int `json:"codex_pid"`  // codex process group leader

	StartedAt   time.Time  `json:"started_at"`
	UpdatedAt   time.Time  `json:"updated_at"`
	EndedAt     *time.Time `json:"ended_at,omitempty"`      // nil until terminal
	LastEventAt *time.Time `json:"last_event_at,omitempty"` // nil until first event

	ElapsedSec float64 `json:"elapsed_sec"`
	IdleSec    float64 `json:"idle_sec"`

	EventCount int           `json:"event_count"`
	LastEvent  string        `json:"last_event"`
	ThreadID   string        `json:"thread_id,omitempty"`
	Usage      *events.Usage `json:"usage,omitempty"`

	ExitCode *int   `json:"exit_code,omitempty"`
	Error    string `json:"error,omitempty"`

	// ResultPreview is a truncated copy of the final output for at-a-glance
	// status; the full text lives in result.txt (ResultFile).
	ResultPreview string `json:"result_preview,omitempty"`

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
	CodexBin     string     `json:"codex_bin"`
	Args         []string   `json:"args"`
	Cwd          string     `json:"cwd"`
	JSONMode     bool       `json:"json_mode"`
	ForwardStdin bool       `json:"forward_stdin"`
	Thresholds   Thresholds `json:"thresholds"`
	Title        string     `json:"title"`
	Env          []string   `json:"env,omitempty"`
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
	reconcileLiveness(&s)
	return &s, nil
}

// workerStaleLimit is how long status.json may go un-updated before an active
// job is treated as dead. The monitor rewrites status every watchdog tick (~1s),
// so a gap this large means the writer is gone or wedged.
const workerStaleLimit = 15 * time.Second

// reconcileLiveness downgrades an active status whose worker can no longer be
// updating it. The worker is the sole writer of status.json, so if it is gone —
// or its pid was reused and the file has gone stale — the status can never
// change again and must not be reported as still running.
func reconcileLiveness(s *Status) {
	if s == nil || !s.State.Active() || s.WorkerPID <= 0 {
		return
	}
	alive := proc.Alive(s.WorkerPID)
	stale := !s.UpdatedAt.IsZero() && time.Since(s.UpdatedAt) > workerStaleLimit
	if alive && !stale {
		return
	}
	s.State = StateFailed
	s.Health = HealthDead
	if s.Error == "" {
		if !alive {
			s.Error = fmt.Sprintf("worker process %d is no longer running; the job ended without recording a result", s.WorkerPID)
		} else {
			s.Error = fmt.Sprintf("status has not updated in %s; the worker appears wedged (or pid %d was reused)",
				time.Since(s.UpdatedAt).Round(time.Second), s.WorkerPID)
		}
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
