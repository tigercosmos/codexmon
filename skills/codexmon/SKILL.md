---
name: codexmon
description: >-
  Run the OpenAI Codex CLI from an AI agent without it hanging silently. Use
  whenever you want Codex to do a code review or an `exec` task and you need to
  monitor its health and get the result reliably. Triggers: "run codex",
  "codex review", "review the diff/PR with codex", "use codex to ...", or any
  time `codex exec` / `codex review` would otherwise block with no feedback.
---

# Using codexmon (monitored Codex)

`codexmon` wraps the `codex` CLI so you can launch it, watch its health, and
collect the result without ever blocking on a silent hang. Prefer it over
calling `codex` directly whenever you drive Codex non-interactively.

## Prerequisites (check once)

```sh
codexmon doctor --json
```
`ready:true` means Codex is installed and responding. If `codexmon` is not found,
install it (`go install github.com/tigercosmos/codexmon/cmd/codexmon@latest`, or
download a release binary). If `ready:false`, surface the `problems` to the user
and stop — Codex itself is not usable.

## The loop (never blocks you)

Launch detached, poll, then block for the result:

```sh
# 1. start a monitored run; capture the job id (first token of stdout)
ID=$(codexmon start -- exec review --uncommitted | head -1 | awk '{print $1}')

# 2. poll health whenever you want — returns immediately, never blocks
codexmon status "$ID" --json

# 3. (optional) watch progress
codexmon tail "$ID" -f

# 4. block until it finishes, then read the result as JSON
codexmon wait "$ID" --timeout 600 --json

# stop it early if needed
codexmon cancel "$ID"
```

Anything that is not a codexmon subcommand is passed to `codex` verbatim, so use
real Codex commands after `--`:
- **Code review of uncommitted changes:** `codexmon start -- exec review --uncommitted`
- **Review against a base branch:** `codexmon start -- exec review --base main`
- **Review one commit:** `codexmon start -- exec review --commit <sha>`
- **Arbitrary task / question:** `codexmon start -- exec "explain internal/foo.go and list risks"`

(You can also run in the foreground — `codexmon exec review --uncommitted` —
which streams heartbeats to stderr and prints the result to stdout. Background +
poll is preferred for agents because it never ties up your shell.)

## Reading `status --json` / `wait --json`

Key fields to branch on:

| Field | Meaning |
|---|---|
| `state` | `queued` `running` (active) → `completed` `failed` `stalled` `timeout` `cancelled` (terminal) |
| `health` | `starting` `healthy` `slow` `stalled` `done` `dead` |
| `phase` | what Codex is doing (`reviewing`, `running`, `verifying`, `thinking`, …) |
| `elapsed_sec` / `idle_sec` | wall time, and seconds since last activity |
| `last_event` | most recent step (e.g. `ran: go test ./... (exit 0)`) |
| `usage` | input/output token counts (on completion) |
| `result_preview` | truncated final output; full text is in `result_file` |
| `error` | why it failed/stalled (names the stuck tool, etc.) |

Decision rule: keep polling while `state` is `queued`/`running`. When terminal:
`completed` → read `result_file` (or `result_preview`); anything else → report
`error` to the user.

## Exit codes (from `wait` and foreground runs)

`0` completed · `1` failed (or Codex's own code) · `124` stalled/timeout ·
`130` cancelled · `75` your `wait --timeout` elapsed while still running.

## Watchdog defaults (tune with flags on `run`/`start`)

`--idle-timeout 180` (idle ceiling when nothing is in flight) ·
`--tool-timeout 120` (a single MCP/tool call may not exceed this) ·
`--wall-timeout 600` (hard cap) · `0` disables any of them.
A long shell command (e.g. `go test`) is exempt from idle/tool limits and only
bounded by the wall timeout.

## Gotchas

- **Stalls are real signals.** If a run ends `stalled`/`timeout`, Codex was stuck
  (often a wedged MCP tool — `error` names it). Report it; don't silently retry.
- **MCP hangs:** if `codex review` stalls on an MCP tool configured in
  `~/.codex/config.toml`, retry MCP-free: add `--ignore-user-config` to the codex
  args, e.g. `codexmon start -- exec review --uncommitted --ignore-user-config`
  (Codex still uses your auth).
- **Don't pipe a prompt into codexmon's stdin** expecting Codex to read it; by
  design the child's stdin is `/dev/null` (this is what prevents the classic
  stdin hang). Pass prompts as arguments, or use `--stdin` to forward.
- `status`/`wait`/`tail`/`cancel` with no id act on the most recent job.
```
