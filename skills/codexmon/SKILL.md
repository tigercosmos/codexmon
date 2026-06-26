---
name: codexmon
description: >-
  Run an AI coding CLI (codex, claude, or cursor) for a code review or task
  without it hanging silently. Use whenever you want one of these agents to do a
  code review or an exec task and you need to monitor its health and get the
  result reliably. Triggers: "run codex/claude/cursor", "codex/claude review",
  "review the diff/PR with <agent>", "use <agent> to ...", or any time the agent
  CLI would otherwise block with no feedback.
---

# Using codexmon (monitored AI coding agents)

`codexmon` wraps an AI coding CLI — **codex** (default), **claude** (Claude
Code), or **cursor** (Cursor agent) — so you can launch it, watch its health, and
collect the result without ever blocking on a silent hang. Prefer it over calling
those CLIs directly whenever you drive them non-interactively.

Pick the agent with `--agent codex|claude|cursor` (on `run`/`start`/`review`/
`doctor`/`version`) or the `CODEXMON_AGENT` env var. The default is `codex`.

## Prerequisites (check once)

```sh
codexmon doctor --json                 # or: codexmon doctor --agent claude --json
```
`ready:true` means that agent is installed and responding. If `codexmon` is not
found, install it (`go install github.com/tigercosmos/codexmon/cmd/codexmon@latest`,
or download a release binary). If `ready:false`, surface the `problems` to the
user and stop — that agent is not usable.

## The loop (never blocks you)

Launch detached, poll, then block for the result:

```sh
# 1. start a monitored run; capture the job id (first token of stdout)
ID=$(codexmon review --agent claude --uncommitted -b | head -1 | awk '{print $1}')

# 2. poll health whenever you want — returns immediately, never blocks
codexmon status "$ID" --json

# 3. (optional) watch progress
codexmon tail "$ID" -f

# 4. block until it finishes, then read the result as JSON
codexmon wait "$ID" --timeout 600 --json

# stop it early if needed
codexmon cancel "$ID"
```

### Two ways to run a review

- **`codexmon review` (recommended, agent-neutral):** codexmon builds the right
  native invocation for whichever agent you pick.
  - Uncommitted changes: `codexmon review --agent <name> --uncommitted`
  - Against a base branch: `codexmon review --agent <name> --base main`
  - Add `-b` to run detached and poll (as above).
- **Raw passthrough (full control):** anything that isn't a codexmon subcommand
  is passed to the selected agent verbatim, after `--`:
  - codex: `codexmon start -- exec review --uncommitted`
  - codex one commit: `codexmon start -- exec review --commit <sha>`
  - claude: `codexmon start --agent claude -- -p "Review the uncommitted changes"`
  - cursor: `codexmon start --agent cursor -- -p "Review the uncommitted changes"`

(You can also run in the foreground — `codexmon review --agent claude --uncommitted`
— which streams heartbeats to stderr and prints the result to stdout. Background
+ poll is preferred for agents because it never ties up your shell.)

## Reading `status --json` / `wait --json`

The JSON contract is identical for every agent. Key fields to branch on:

| Field | Meaning |
|---|---|
| `agent` | which agent ran (`codex`/`claude`/`cursor`) |
| `state` | `queued` `running` (active) → `completed` `failed` `stalled` `timeout` `cancelled` (terminal) |
| `health` | `starting` `healthy` `slow` `stalled` `done` `dead` |
| `phase` | what the agent is doing (`reviewing`, `running`, `verifying`, `thinking`, …) |
| `elapsed_sec` / `idle_sec` | wall time, and seconds since last activity |
| `last_event` | most recent step (e.g. `ran: go test ./... (exit 0)`) |
| `usage` | input/output token counts (codex/claude; cursor reports none) |
| `result_preview` | truncated final output; full text is in `result_file` |
| `error` | why it failed/stalled (names the stuck tool, etc.) |

Decision rule: keep polling while `state` is `queued`/`running`. When terminal:
`completed` → read `result_file` (or `result_preview`); anything else → report
`error` to the user.

## Exit codes (from `wait` and foreground runs)

`0` completed · `1` failed (the agent's own non-zero exit is forwarded verbatim) · `124` stalled/timeout ·
`130` cancelled · `75` your `wait --timeout` elapsed while still running.

## Watchdog defaults (tune with flags on `run`/`start`/`review`)

`--idle-timeout 180` (idle ceiling when nothing is in flight) ·
`--tool-timeout 120` (a single MCP/tool call may not exceed this) ·
`--wall-timeout 600` (hard cap) · `0` disables any of them.
A long shell command (e.g. `go test`) is exempt from idle/tool limits and only
bounded by the wall timeout.

## Gotchas

- **Stalls are real signals.** If a run ends `stalled`/`timeout`, the agent was
  stuck (often a wedged MCP tool — `error` names it). Report it; don't silently
  retry.
- **MCP hangs (codex):** if `codex review` stalls on an MCP tool configured in
  `~/.codex/config.toml`, retry MCP-free: add `--ignore-user-config` to the codex
  args, e.g. `codexmon start -- exec review --uncommitted --ignore-user-config`.
- **Cursor auth:** `cursor` needs `CURSOR_API_KEY` (or a prior `cursor-agent
  login`) for non-interactive use; check with `codexmon doctor --agent cursor`.
- **claude/cursor reviews are hardened but not sandboxed.** claude runs with
  permissions bypassed (the only mode that uses tools headlessly) but with the
  file-editing tools denied and a prompt forbidding edits; cursor runs in print
  mode without auto-approving mutations; codex's reviewer is natively read-only.
  A determined shell write is still possible, so prefer a clean working tree if an
  accidental edit would matter.
- **Don't pipe a prompt into codexmon's stdin** expecting the agent to read it;
  by design the child's stdin is `/dev/null` (this is what prevents the classic
  stdin hang). Pass prompts as arguments, or use `--stdin` to forward.
- `status`/`wait`/`tail`/`cancel` with no id act on the most recent job.
