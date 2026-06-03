<div align="center">

# codexmon

**A health-monitoring wrapper around the [Codex](https://github.com/openai/codex) CLI.**

*Run `codex` and always know whether it's healthy, slow, stalled, or done —
so a review can never hang silently again.*

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

`codexmon` forwards your arguments straight through to `codex`, but supervises
the process so a caller — a human, or an agent like Claude Code — can observe
its liveness at any moment and bound how long it may run. It turns the opaque
"`codex review` is just sitting there… is it working or wedged?" experience into
a continuously-updated status you can poll, with heartbeats, structured JSON,
and a watchdog that stops a genuinely stuck run and tells you *why*.

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

Two things make `codex` look like it has hung:

1. **A piped, never-closing stdin.** Launched with a pipe on stdin that never
   reaches EOF, `codex exec` blocks forever on
   `Reading additional input from stdin…`. codexmon connects the child's stdin
   to `/dev/null` by default, so this simply can't happen.
2. **No liveness signal.** Long model reasoning — or a wedged MCP tool — produces
   no output for a while, and nothing distinguishes "thinking hard" from "dead."
   codexmon parses the `codex exec --json` event stream, tracks time-since-last-
   activity, classifies it by *what Codex is doing*, and writes it all to a
   status file you can poll.

It deliberately drives `codex exec` (a one-shot process) rather than the
`app-server` JSON-RPC path, so the **OS process exit is the authoritative
completion signal** — there is no completion event that can fail to arrive. And
it owns its output pipes, so even a lingering grandchild can never hang the
monitor itself.

## Install

Requires **Go 1.24+** and the **`codex`** CLI on your `PATH`.

```sh
go install github.com/tigercosmos/codexmon/cmd/codexmon@latest
# or from a clone:
make install         # → $GOBIN/codexmon   (ensure it's on PATH)
make build           # → ./codexmon
```

Confirm your environment is ready:

```sh
codexmon doctor
```

## Quickstart

```sh
# Foreground: run codex with live heartbeats on stderr, result on stdout
codexmon exec review --uncommitted

# Background: launch detached, then poll — never blocks your shell
ID=$(codexmon start -- exec review --base main | head -1 | awk '{print $1}')
codexmon status "$ID"          # health at a glance
codexmon tail   "$ID" -f       # follow the log
codexmon wait   "$ID"          # block until done, print the result
```

## Commands

codexmon is a transparent front-end: **anything that isn't a codexmon
subcommand is passed to `codex` verbatim**, wrapped in monitoring.

| Command | Description |
|---|---|
| `codexmon <codex args…>` | Run codex in the **foreground** with monitoring (implicit `run`) |
| `codexmon run [flags] [--] <codex args>` | Foreground run, with explicit monitor flags |
| `codexmon start [flags] [--] <codex args>` | Launch **detached**; prints a job id to poll |
| `codexmon status [id] [--json]` | Health/status of a job (latest if `id` omitted) |
| `codexmon wait [id] [--timeout S] [--json]` | Block until a job finishes, then print the result |
| `codexmon tail [id] [-f] [-n N]` | Show (or follow) a job's log |
| `codexmon list [--json]` | List recent jobs |
| `codexmon cancel [id]` | Stop a running job |
| `codexmon doctor [--json]` | Check that codex itself is installed and responding |
| `codexmon version` | Print codexmon and codex versions |

When the codex subcommand is `exec` (or its alias `e`, including `exec review`),
codexmon auto-injects `--json` to monitor the event stream and
`--output-last-message` to reliably capture the final answer. Use `--no-json`
to opt out. For any other codex subcommand it falls back to monitoring raw
stdout/stderr activity.

### Monitor flags (`run` / `start`)

| Flag | Default | Meaning |
|---|---|---|
| `-b, --background` | off | Detach and return a job id immediately |
| `--wall-timeout S` | `600` | Hard wall-clock limit, seconds (`0` = off) |
| `--idle-timeout S` | `180` | Kill after S idle seconds **when nothing is in flight** (`0` = off) |
| `--tool-timeout S` | `120` | Kill if a single MCP/tool call runs longer than S seconds (`0` = off) |
| `--slow-after S` | `30` | Flag health as `slow` after S idle seconds |
| `--heartbeat S` | `10` | Heartbeat cadence, seconds |
| `-C, --cwd DIR` | cwd | Working directory for codex |
| `--stdin` | off | Forward codexmon's stdin to codex (default: `/dev/null`) |
| `--no-json` | off | Don't inject `exec --json` event monitoring |
| `--codex-bin PATH` | `codex` | Path to the codex binary (or set `CODEXMON_CODEX`) |
| `--json` | off | Emit machine-readable JSON instead of human text |

> Monitor flags must come **before** the codex subcommand, or after a `--`
> separator. Everything from the codex subcommand onward is passed to codex
> untouched: `codexmon start --wall-timeout 900 -- exec review --uncommitted`.

## The watchdog (what makes it "monitoring")

codexmon doesn't use one blunt timeout. Each second it classifies **what Codex
is doing** and applies the matching rule, so a slow-but-working step is never
mistaken for a hang:

| Codex is… | Governed by | Rationale |
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
| `1` | failed (or codex's own non-zero exit) |
| `124` | stalled or wall-clock timeout |
| `130` | cancelled |
| `75` | `wait`'s own `--timeout` elapsed while the job was still running |

A forwarded codex exit code is never allowed to collide with the `124`/`130`/`75`
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
agent can branch on the outcome without parsing prose. To skip permission
prompts, allow `Bash(codexmon:*)` in `.claude/settings.json`.

> **Tip:** if a review stalls on an MCP tool that's configured in
> `~/.codex/config.toml`, you can run it MCP-free with
> `codexmon exec review --uncommitted --ignore-user-config` (codex still uses
> your auth). Without that, codexmon will correctly report `stalled` (exit 124)
> rather than hang.

## Configuration

| Env var | Purpose |
|---|---|
| `CODEXMON_HOME` | State directory (default `~/.codexmon`) |
| `CODEXMON_CODEX` | Path to the codex binary (overrides `PATH`) |

Each run gets `~/.codexmon/jobs/<id>/` (created `0700`; files `0600`, since
prompts and output can be sensitive):

```
spec.json      immutable launch spec (args, cwd, thresholds)
status.json    live status — rewritten ~1×/second; the contract pollers read
events.jsonl   raw `codex exec --json` events
output.log     merged human-readable log (events, stderr, heartbeats)
result.txt     final agent message / review output
```

## How it works

```
┌─ codexmon run/start ─────────────────────────────────────────────┐
│  analyze args → inject `exec --json` / `-o`  (internal/codexcli)  │
│  spawn codex in its own process group        (internal/proc)     │
│  ├─ read stdout: parse JSONL events           (internal/events)  │
│  ├─ read stderr: capture diagnostics                             │
│  ├─ watchdog 1Hz: health + tool/idle/wall/cancel                 │
│  └─ wait on the *process* (not pipe EOF) ── authoritative exit   │
│  write status.json atomically each tick      (internal/job)      │
└──────────────────────────────────────────────────────────────────┘
        status / wait / tail / list / cancel  poll those files
```

`start` re-execs codexmon as a detached `__worker` (its own session) so the run
survives the launching shell; the worker runs the exact same monitor and writes
to the same job files.

## Development

```sh
make build         # build ./codexmon
make test          # go test ./...
make race          # go test -race ./...   (concurrency)
make lint          # gofmt + go vet + staticcheck
make cover         # coverage summary
```

Tests use a **fake `codex` shell script**, so the whole suite — including the
end-to-end tests in `e2e/` that build the real binary and drive detached
background workers — runs with no network access or Codex auth.

### Layout

```
cmd/codexmon          entrypoint
internal/cli          argument routing & subcommands
internal/monitor      the supervisor: spawn, stream, watchdog, status
internal/events       codex `exec --json` event parsing
internal/job          on-disk job records (spec/status/log/result)
internal/codexcli     locate codex; analyze args; inject --json
internal/proc         process-group lifecycle (stdin guard, group kill)
internal/render       human-readable status/result formatting
e2e                   end-to-end tests against a fake codex
```

## License

[MIT](LICENSE) © tigercosmos
