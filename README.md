<div align="center">

# codexmon

**A health-monitoring wrapper around AI coding CLIs — [Codex](https://github.com/openai/codex), [Claude Code](https://github.com/anthropics/claude-code), and the [Cursor agent](https://cursor.com/docs/cli).**

*Run any of them and always know whether it's healthy, slow, stalled, or done —
so a code review can never hang silently again.*

[![CI](https://github.com/tigercosmos/codexmon/actions/workflows/ci.yml/badge.svg)](https://github.com/tigercosmos/codexmon/actions/workflows/ci.yml)
[![Go Reference](https://pkg.go.dev/badge/github.com/tigercosmos/codexmon.svg)](https://pkg.go.dev/github.com/tigercosmos/codexmon)
[![Go Report Card](https://goreportcard.com/badge/github.com/tigercosmos/codexmon)](https://goreportcard.com/report/github.com/tigercosmos/codexmon)
[![Go 1.24+](https://img.shields.io/badge/Go-1.24%2B-00ADD8?logo=go&logoColor=white)](https://go.dev/dl/)
[![License: MIT](https://img.shields.io/badge/License-MIT-blue.svg)](LICENSE)

<!-- Demo GIF: regenerate with `make build && PATH="$PWD:$PATH" vhs docs/demo.tape`
     (see CONTRIBUTING.md → "Recording the demo"), then uncomment the line below. -->
<!-- ![codexmon demo](docs/demo.gif) -->
<sub>📹 <i>Demo: <code>vhs docs/demo.tape</code> → <code>docs/demo.gif</code> (runs against a bundled fake codex — no auth needed).</i></sub>

</div>

---

`codexmon` forwards your arguments straight through to the selected agent
(`codex` by default; `--agent claude` or `--agent cursor` to switch), but
supervises the process so a caller — a human, or an agent like Claude Code — can
observe its liveness at any moment and bound how long it may run. It turns the
opaque "the review is just sitting there… is it working or wedged?" experience
into a continuously-updated status you can poll, with heartbeats, structured
JSON, and a watchdog that stops a genuinely stuck run and tells you *why*.

It speaks each agent's own event stream — codex's `exec --json` items, Claude
Code's `stream-json`, Cursor's `tool_call` lifecycle — and normalizes them to a
single status model, so `codexmon review --agent <name>` looks identical no
matter who does the reviewing.

```console
$ codexmon start -- exec review --uncommitted
cdx-20260603-024602-8f2560 started (worker pid 84130) — codex exec review
  poll:   codexmon status cdx-20260603-024602-8f2560
  follow: codexmon tail   cdx-20260603-024602-8f2560 -f
  block:  codexmon wait   cdx-20260603-024602-8f2560

$ codexmon status cdx-20260603-024602-8f2560
✅  cdx-20260603-024602-8f2560  [codex exec review]
  state:    running (healthy)
  phase:    reviewing
  elapsed:  47s   idle: 3s
  pid:      codex=84132 worker=84130 (codex alive: true)
  events:   12
  last:     ran: go test ./... (exit 0)
  limits:   slow>30s stall>3m00s tool>2m00s wall>10m00s
  log:      ~/.codexmon/jobs/cdx-20260603-024602-8f2560/output.log
  hint:     codexmon wait … | codexmon tail … -f | codexmon cancel …
```

## Why

Two things make a non-interactive agent run look like it has hung — codexmon
neutralizes both, for every agent:

1. **A piped, never-closing stdin.** Launched with a pipe on stdin that never
   reaches EOF, a CLI like `codex exec` blocks forever on
   `Reading additional input from stdin…`. codexmon connects the child's stdin
   to `/dev/null` by default, so this simply can't happen.
2. **No liveness signal.** Long model reasoning — or a wedged MCP tool — produces
   no output for a while, and nothing distinguishes "thinking hard" from "dead."
   codexmon parses each agent's event stream (codex's `exec --json`, Claude
   Code's and Cursor's `stream-json`), tracks time-since-last-activity,
   classifies it by *what the agent is doing*, and writes it all to a status
   file you can poll.

It deliberately drives each agent in its one-shot, non-interactive mode rather
than a long-lived JSON-RPC / app-server path, so the **OS process exit is the
authoritative completion signal** — there is no completion event that can fail to
arrive. And it owns its output pipes, so even a lingering grandchild can never
hang the monitor itself.

## Install

codexmon runs on **macOS and Linux** (arm64 / amd64) and needs at least one
supported agent CLI on your `PATH`: **`codex`** (default), **`claude`**
(Claude Code), and/or **`cursor-agent`** (Cursor).

**Prebuilt binary** — grab the archive for your OS/arch from the
[latest release](https://github.com/tigercosmos/codexmon/releases/latest)
(`darwin`/`linux` × `arm64`/`amd64`), then extract just the binary onto your PATH:

```sh
# example: macOS (Apple Silicon) — substitute your version/os/arch
curl -sSL https://github.com/tigercosmos/codexmon/releases/download/v0.7.0/codexmon_0.7.0_darwin_arm64.tar.gz \
  | tar -xz codexmon && sudo mv codexmon /usr/local/bin/
codexmon version
```

Verify the download against the release's `SHA256SUMS` if you like
(`sha256sum -c SHA256SUMS`).

**With Go** (1.24+):

```sh
go install github.com/tigercosmos/codexmon/cmd/codexmon@latest   # → $GOBIN
# or from a clone:
make build                 # → ./codexmon
sudo make install          # → /usr/local/bin/codexmon  +  agent skill into ~/.claude/skills
#   make install PREFIX=$HOME/.local   # install the binary elsewhere, no sudo
#   make install-go                    # install the binary into $GOBIN instead
#   make install-skill                 # install just the agent skill
```

`make install` also drops the [agent skill](#using-codexmon-from-claude-code)
into `~/.claude/skills/codexmon` (the invoking user's home, even under `sudo`).

Confirm your environment is ready:

```sh
codexmon doctor
```

## Quickstart

```sh
# Foreground: run codex with live heartbeats on stderr, result on stdout
codexmon exec review --uncommitted

# Same review, but run by a different agent — identical command, just --agent:
codexmon review --agent claude --uncommitted
codexmon review --agent cursor --base main

# Background: launch detached, then poll — never blocks your shell
ID=$(codexmon start -- exec review --base main | head -1 | awk '{print $1}')
codexmon status "$ID"          # health at a glance
codexmon tail   "$ID" -f       # follow the log
codexmon wait   "$ID"          # block until done, print the result
```

> **Picking the agent.** `--agent codex|claude|cursor` (on `run`/`start`/
> `review`/`doctor`/`version`) or the `CODEXMON_AGENT` env var selects who runs;
> the default is `codex`, so every existing command keeps working unchanged.
>
> **Fallback when no backend is specified.** With neither `--agent` nor
> `CODEXMON_AGENT` set, codexmon walks a fallback chain — **codex → claude →
> cursor** — so an unspecified backend still gets the work done: it skips any
> agent that is not installed, and (for a foreground run) hands off to the next
> agent when the current one fails because it hit a usage/rate limit. A non-limit
> failure is surfaced as-is, never masked by a retry. When codexmon is invoked
> *by* Claude Code (`CLAUDECODE` is set), `claude` is dropped from the chain, so
> it becomes **codex → cursor**. Naming an agent explicitly disables all of this
> and runs exactly that one.

## Commands

codexmon is a transparent front-end: **anything that isn't a codexmon
subcommand is passed to the selected agent verbatim**, wrapped in monitoring.

| Command | Description |
|---|---|
| `codexmon <agent args…>` | Run the agent in the **foreground** with monitoring (implicit `run`) |
| `codexmon run [flags] [--] <agent args>` | Foreground run, with explicit monitor flags |
| `codexmon start [flags] [--] <agent args>` | Launch **detached**; prints a job id to poll |
| `codexmon review [flags]` | Monitored **code review** on any agent: `--uncommitted` (default) or `--base REF` |
| `codexmon status [id] [--json]` | Health/status of a job (latest if `id` omitted) |
| `codexmon wait [id] [--timeout S] [--interval S] [--json]` | Block until a job finishes, then print the result (`--interval` caps the adaptive poll) |
| `codexmon tail [id] [-f] [-n N]` | Show (or follow) a job's log |
| `codexmon list [--json]` | List recent jobs |
| `codexmon cancel [id]` | Stop a running job |
| `codexmon clean [--keep-days N] [--keep N] [--all]` | Remove old finished jobs (active jobs are never touched) |
| `codexmon doctor [--agent A] [--json]` | Check that the agent is installed and responding |
| `codexmon version [--agent A]` | Print codexmon and agent versions |

`codexmon review` is the agent-neutral path: it builds each agent's native review
invocation for you (codex's `exec review`, a read-only review prompt for Claude
Code / Cursor) so the same command works everywhere. For full control, pass
agent-native args yourself via `run`/`start`.

> **claude/cursor reviews are hardened but not sandboxed.** codex has a native
> read-only reviewer. Claude Code needs `bypassPermissions` to use tools
> headlessly, so `review` additionally **denies the file-editing tools**
> (`Edit`/`Write`/`MultiEdit`/`NotebookEdit`) and prompts it to stay read-only;
> Cursor runs in print mode without auto-approving mutations. A determined shell
> write is still possible, so run on a clean working tree if that matters.

codexmon auto-injects each agent's event-stream and result-capture flags so the
run is observable: `--json` + `--output-last-message` for `codex exec`,
`--output-format stream-json --verbose` for `claude -p`, and
`--output-format stream-json` for `cursor-agent -p`. Use `--no-json` to opt out;
without a parseable stream, codexmon falls back to monitoring raw stdout/stderr
activity. For a Cursor print-mode run, codexmon also defaults
`--model cursor-grok-4.5-high` (Cursor Grok 4.5) unless you pass your own
`--model`; management subcommands such as `cursor-agent status` are forwarded
untouched.

Interrupting a foreground run stops the agent too: Ctrl+C (or a `SIGTERM` to a
detached worker) terminates the agent's process group and records the job as
`cancelled`, so an interrupted run never leaves an agent working unwatched.
Press Ctrl+C again to force-quit without waiting for the shutdown.

### Monitor flags (`run` / `start` / `review`)

| Flag | Default | Meaning |
|---|---|---|
| `--agent NAME` | `codex` | Which agent runs: `codex`, `claude`, or `cursor` (or set `CODEXMON_AGENT`) |
| `-b, --background` | off | Detach and return a job id immediately |
| `--wall-timeout S` | `600` | Hard wall-clock limit, seconds (`0` = off) |
| `--idle-timeout S` | `180` | Kill after S idle seconds **when nothing is in flight** (`0` = off) |
| `--tool-timeout S` | `120` | Kill if a single MCP/tool call runs longer than S seconds (`0` = off) |
| `--slow-after S` | `30` | Flag health as `slow` after S idle seconds |
| `--heartbeat S` | `10` | Heartbeat cadence, seconds |
| `-C, --cwd DIR` | cwd | Working directory for the agent |
| `--stdin` | off | Forward codexmon's stdin to the agent (default: `/dev/null`) |
| `--no-json` | off | Don't inject JSON event-stream monitoring |
| `--agent-bin PATH` | agent default | Path to the agent binary (or set its `CODEXMON_*` var; `--codex-bin` still accepted) |
| `--json` | off | Emit machine-readable JSON instead of human text |

> For `run`/`start`, monitor flags must come **before** the agent subcommand, or
> after a `--` separator; everything from the subcommand onward is passed to the
> agent untouched: `codexmon start --agent claude --wall-timeout 900 -- -p "Review the diff"`.
> `review` takes only the flags above plus `--uncommitted` / `--base REF`.

## The watchdog (what makes it "monitoring")

codexmon doesn't use one blunt timeout. Each second it classifies **what the
agent is doing** and applies the matching rule, so a slow-but-working step is
never mistaken for a hang:

| The agent is… | Governed by | Rationale |
|---|---|---|
| running an **MCP / tool call** | `--tool-timeout` (120s) | tools should be quick — a stuck one is caught precisely *and by name*, sooner than the idle ceiling |
| running a **shell command** (`go test`, build) | `--wall-timeout` only | commands legitimately run for minutes; idle is expected |
| **idle, nothing in flight** (model reasoning) | `--idle-timeout` (180s) | the only case the idle clock should govern |
| anything | `--wall-timeout` (600s) | absolute backstop |
| — | the cancel marker | `codexmon cancel` stops it gracefully |

When the watchdog stops a run it records a precise reason, e.g.
`tool call codebase-memory-mcp/list_projects stuck for 120s (tool timeout 120s)`.
Set `--tool-timeout 0` to instead let a slow tool run until the wall timeout.

### Health

| Health | Meaning |
|---|---|
| `starting` | launched, no events yet |
| `healthy` ✅ | producing events, or a command/tool actively in flight within budget |
| `slow` ⚠️ | idle past `--slow-after`, or a tool call past half `--tool-timeout` |
| `stalled` ❌ | idle past `--idle-timeout`, or a tool call past `--tool-timeout` — being terminated |
| `done` ✅ / `dead` ❌ | terminal: completed, or failed/stalled/timeout/cancelled |

If a background worker dies without recording a result (crash, OOM, reboot) or
its status file goes stale, `status`/`wait`/`list` **reconcile** the job to
`failed` instead of reporting it `running` forever.

### Exit codes

| Code | Meaning |
|---|---|
| `0` | completed |
| `1` | failed (the agent's own non-zero exit is forwarded verbatim unless it would collide with a sentinel below, in which case it becomes `1`) |
| `124` | stalled or wall-clock timeout |
| `130` | cancelled |
| `75` | `wait`'s own `--timeout` elapsed while the job was still running |

A forwarded agent exit code is never allowed to collide with the `124`/`130`/`75`
sentinels.

## Using codexmon from Claude Code

This is the headline use case: a loop that **never blocks** the agent and is
**always observable**.

```sh
codexmon doctor --json                          # 1. confirm codex is usable
ID=$(codexmon start -- exec review --uncommitted | head -1 | awk '{print $1}')
codexmon status "$ID" --json                    # 2. poll health any time
codexmon tail   "$ID" -f                         # 3. (optional) follow progress
codexmon wait   "$ID" --timeout 600 --json       # 4. block, then read the result
codexmon cancel "$ID"                            # stop it if needed
```

`status --json` and `wait --json` emit the full job record — state, health,
phase, elapsed/idle seconds, last event, token usage, result preview — so an
agent can branch on the outcome without parsing prose. On completion,
`wait --json` (and a foreground `run --json`) additionally embeds the final
output as `result` (capped at 4 MiB — a larger output ends with a truncation
notice naming `result_file`, which always holds the full text), so one call
yields everything; `wait` also polls adaptively (fast at first, backing off to
`--interval`), so short jobs return in milliseconds. To skip permission
prompts, allow `Bash(codexmon:*)` in `.claude/settings.json`.

The contract is identical whichever agent does the work, so a Claude Code loop
can delegate a review to a *different* model with one flag — e.g.
`codexmon review --agent cursor --uncommitted` — and read back the same JSON.

**Drop-in agent skill.** [`skills/codexmon/SKILL.md`](skills/codexmon/SKILL.md)
is a ready-made skill that teaches an agent the whole loop above. `make install`
installs it automatically; to (re)install just the skill:

```sh
make install-skill                             # → ~/.claude/skills/codexmon
# or by hand:
cp -r skills/codexmon ~/.claude/skills/        # user-wide
cp -r skills/codexmon .claude/skills/          # project-local
```

> **Tip (codex):** if a review stalls on an MCP tool that's configured in
> `~/.codex/config.toml`, you can run it MCP-free with
> `codexmon exec review --uncommitted --ignore-user-config` (codex still uses
> your auth). Without that, codexmon will correctly report `stalled` (exit 124)
> rather than hang.

## Configuration

| Env var | Purpose |
|---|---|
| `CODEXMON_HOME` | State directory (default `~/.codexmon`) |
| `CODEXMON_AGENT` | Default agent when `--agent` is omitted (`codex`/`claude`/`cursor`) |
| `CODEXMON_CODEX` | Path to the `codex` binary (overrides `PATH`) |
| `CODEXMON_CLAUDE` | Path to the `claude` binary (overrides `PATH`) |
| `CODEXMON_CURSOR` | Path to the `cursor-agent` binary (overrides `PATH`) |
| `CODEXMON_KEEP_DAYS` | Retention: prune finished jobs older than this many days (default `7`; `0` = no age limit) |
| `CODEXMON_KEEP_JOBS` | Retention: keep at most this many finished jobs (default `200`; `0` = no count limit) |

(Cursor also needs `CURSOR_API_KEY` for non-interactive use — codexmon passes
your environment through to the agent.)

> `CODEXMON_CODEX`/`CLAUDE`/`CURSOR` and `--agent-bin` run whatever executable you
> point them at — only set them to a binary you trust.

Each run gets `~/.codexmon/jobs/<id>/` (created `0700`; files `0600`, since
prompts and output can be sensitive). Finished jobs are pruned automatically on
each launch — by default after 7 days or beyond the newest 200 (tune with
`CODEXMON_KEEP_DAYS` / `CODEXMON_KEEP_JOBS`, or run `codexmon clean` yourself);
active jobs are never touched:

```
spec.json      immutable launch spec (agent, args, cwd, thresholds)
status.json    live status — rewritten ~1×/second; the contract pollers read
events.jsonl   raw agent event-stream lines (when JSON monitoring is on)
output.log     merged human-readable log (events, stderr, heartbeats)
result.txt     final agent message / review output
```

## How it works

```
┌─ codexmon run/start/review ──────────────────────────────────────────┐
│  pick agent.Provider (codex/claude/cursor)   (internal/agent)        │
│  analyze args → inject the agent's stream/result flags  (provider)   │
│  spawn the agent in its own process group    (internal/proc)         │
│  ├─ read stdout: provider.ParseLine → normalized Event (provider)    │
│  ├─ read stderr: capture diagnostics                                 │
│  ├─ watchdog 1Hz: health + tool/idle/wall/cancel                     │
│  └─ wait on the *process* (not pipe EOF) ── authoritative exit       │
│  write status.json atomically each tick      (internal/job)          │
└──────────────────────────────────────────────────────────────────────┘
        status / wait / tail / list / cancel  poll those files
```

`start` re-execs codexmon as a detached `__worker` (its own session) so the run
survives the launching shell; the worker runs the exact same monitor and writes
to the same job files.

Adding a new agent is one self-contained package: implement `agent.Provider`
(locate the binary, shape the args, parse the stream, health-check) and register
it. The supervisor, CLI, and watchdog never change.

## Development

```sh
make build         # build ./codexmon
make test          # go test ./...
make race          # go test -race ./...   (concurrency)
make lint          # gofmt + go vet + staticcheck
make cover         # coverage summary
```

Tests use **fake agent shell scripts** (codex, claude, and cursor), so the whole
suite — including the end-to-end tests in `e2e/` that build the real binary and
drive detached background workers across every agent — runs with no network
access or any agent's auth.

### Layout

```
cmd/codexmon              entrypoint
internal/cli              argument routing & subcommands (incl. review, --agent)
internal/monitor          the supervisor: spawn, stream, watchdog, status
internal/agent            the Provider contract + normalized Event/Phase/Usage + registry
internal/agent/codex      codex provider: locate, analyze args, parse `exec --json`
internal/agent/claude     Claude Code provider: -p + stream-json parsing
internal/agent/cursor     Cursor provider: -p + stream-json parsing
internal/agent/all        blank-imported to register the built-in providers
internal/job              on-disk job records (spec/status/log/result)
internal/proc             process-group lifecycle (stdin guard, group kill)
internal/render           human-readable status/result formatting
e2e                       end-to-end tests against fake codex/claude/cursor
```

### Releasing

Notable changes per version are in [`CHANGELOG.md`](CHANGELOG.md). Releases are
cut by GoReleaser from a version tag, via
[`.github/workflows/release.yml`](.github/workflows/release.yml):

```sh
git tag v0.7.0 && git push origin v0.7.0     # CI builds + publishes the release
```

To build the same cross-platform archives locally (no goreleaser required):

```sh
make dist          # → dist/codexmon_<version>_<os>_<arch>.tar.gz + SHA256SUMS
```

## License

[MIT](LICENSE) © tigercosmos
