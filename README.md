# codexmon

**A health-monitoring wrapper around the [Codex](https://github.com/openai/codex) CLI.**

`codexmon` forwards arguments straight to `codex`, but supervises the process so
a caller — a human, or an agent like Claude Code — can *always* tell whether
Codex is **healthy, slow, stalled, or done**. It exists to fix the failure mode
where `codex review` / `codex exec` appears to hang silently with no way to know
if it is still working or wedged.

```
$ codexmon start -- exec review --uncommitted
cdx-20260603-024602-8f2560 started (worker pid 84130) — codex exec review
  poll:   codexmon status cdx-20260603-024602-8f2560
  follow: codexmon tail cdx-20260603-024602-8f2560 -f
  block:  codexmon wait cdx-20260603-024602-8f2560

$ codexmon status cdx-20260603-024602-8f2560
✅  cdx-20260603-024602-8f2560  [codex exec review]
  state:    running (healthy)
  phase:    reviewing
  elapsed:  47s   idle: 3s
  events:   12
  last:     ran: go test ./... (exit 0)
  limits:   slow>30s stall>3m00s wall>10m00s
```

## Why

Two things make Codex look "stuck silently":

1. **A piped, never-closing stdin.** When `codex exec` is launched with a pipe on
   stdin that never reaches EOF, it blocks forever on
   `Reading additional input from stdin...`. codexmon connects the child's stdin
   to `/dev/null` by default, so this can't happen.
2. **No liveness signal.** Long model reasoning produces no output for a while,
   and there's no easy way to distinguish "thinking hard" from "dead." codexmon
   parses the `codex exec --json` event stream, tracks the time since the last
   activity, and exposes a continuously-updated status file plus heartbeats.

codexmon deliberately drives `codex exec` (a one-shot process) rather than the
`app-server` JSON-RPC path, so the **process exit is the authoritative
completion signal** — there is no completion event that can fail to arrive.

## Install

Requires Go 1.24+ and the `codex` CLI on your `PATH`.

```sh
go install github.com/tigercosmos/codexmon/cmd/codexmon@latest
# or, from a clone:
make install      # or: make build  -> ./codexmon
```

Verify your environment:

```sh
codexmon doctor
```

## Usage

codexmon is a transparent front-end: **anything that isn't a codexmon
subcommand is passed to `codex` verbatim**, wrapped in monitoring.

```
codexmon <codex args...>                 Run codex in the foreground with monitoring
codexmon run [flags] [--] <codex args>   Same, with explicit monitor flags
codexmon start [flags] [--] <codex args> Launch detached; prints a job id to poll
codexmon status [id] [--json]            Health/status of a job (latest if id omitted)
codexmon wait [id] [--timeout S] [--json] Block until a job finishes, then print the result
codexmon tail [id] [-f] [-n N]           Show (or follow) a job's log
codexmon list [--json]                   List recent jobs
codexmon cancel [id]                     Stop a running job
codexmon doctor [--json]                 Check that codex itself is installed and usable
codexmon version                         Print versions
```

When the codex subcommand is `exec` (including `exec review`), codexmon
auto-injects `--json` to monitor the event stream and `--output-last-message`
to reliably capture the final answer. Use `--no-json` to disable.

### Monitor flags (`run` / `start`)

| Flag | Default | Meaning |
|------|---------|---------|
| `-b, --background` | off | Detach and return a job id immediately |
| `--wall-timeout S` | 600 | Hard wall-clock limit, seconds (`0` = off) |
| `--idle-timeout S` | 180 | Kill after S idle seconds **when nothing is in flight** (`0` = off) |
| `--tool-timeout S` | 120 | Kill if a single MCP/tool call runs longer than S seconds (`0` = off) |
| `--slow-after S` | 30 | Flag health as `slow` after S idle seconds |
| `--heartbeat S` | 10 | Heartbeat cadence, seconds |
| `-C, --cwd DIR` | cwd | Working directory for codex |
| `--stdin` | off | Forward codexmon's stdin to codex (default: `/dev/null`) |
| `--no-json` | off | Don't inject `exec --json` event monitoring |
| `--codex-bin PATH` | `codex` | Path to the codex binary |
| `--json` | off | Emit machine-readable JSON instead of human text |

The watchdog applies the right rule to **what Codex is actually doing**, so a
slow-but-working step isn't mistaken for a hang:

- **An MCP/tool call in flight** is judged against `--tool-timeout` (tool calls
  should be quick; a stuck one is caught precisely and faster than the idle
  ceiling). A slow-but-returning tool is not killed.
- **A shell command in flight** (e.g. `go test`) is exempt from idle/stall — it
  may legitimately run for minutes; only `--wall-timeout` bounds it.
- **Nothing in flight** (quiet model reasoning) is governed by `--idle-timeout`.
- `--wall-timeout` is the absolute backstop in every case.

Set `--tool-timeout 0` to instead let a slow MCP tool run until the wall timeout.

### Health model

| Health | Meaning |
|--------|---------|
| `starting` | launched, no events yet |
| `healthy` ✅ | producing events, or a command/tool actively in flight within budget |
| `slow` ⚠️ | idle past `--slow-after`, or a tool call past half `--tool-timeout` |
| `stalled` ❌ | idle past `--idle-timeout`, or a tool call past `--tool-timeout`; will be terminated |
| `done` ✅ / `dead` ❌ | terminal: completed, or failed/stalled/timeout/cancelled |

### Exit codes

`0` completed · `1` failed (or codex's own exit code) · `124` stalled/timeout ·
`130` cancelled · `75` (from `wait`) the wait's own `--timeout` elapsed while the
job was still running. A forwarded codex exit code is never allowed to collide
with the `124`/`130`/`75` sentinels.

## Using codexmon from Claude Code

The intended loop never blocks Claude and is always observable:

```sh
codexmon doctor --json                       # 1. confirm codex is usable
ID=$(codexmon start -- exec review --uncommitted | head -1 | awk '{print $1}')
codexmon status "$ID" --json                  # 2. poll health any time
codexmon tail "$ID" -f                         # 3. (optional) follow progress
codexmon wait "$ID" --timeout 600 --json       # 4. block, then read the result
codexmon cancel "$ID"                          # if it needs to be stopped
```

`status --json` and `wait --json` emit the full job record (state, health,
phase, elapsed/idle seconds, last event, token usage, result preview), so an
agent can decide what to do without parsing prose.

## Configuration

| Env var | Purpose |
|---------|---------|
| `CODEXMON_HOME` | State directory (default `~/.codexmon`) |
| `CODEXMON_CODEX` | Path to the codex binary (overrides `PATH`) |

Each run gets `~/.codexmon/jobs/<id>/` containing `spec.json`, `status.json`,
`events.jsonl`, `output.log`, and `result.txt`.

## Development

```sh
make build         # build ./codexmon
make test          # go test ./...
make race          # go test -race ./...
make lint          # gofmt + go vet + staticcheck
make cover         # coverage summary
```

Tests use a fake `codex` shell script, so the full suite (including the
end-to-end tests in `e2e/`) runs without network access or Codex auth.

## Layout

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

MIT — see [LICENSE](LICENSE).
