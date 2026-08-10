# Changelog

All notable changes to codexmon are documented here. This project adheres to
[Semantic Versioning](https://semver.org/) (pre-1.0: minor versions may change
behavior).

## Unreleased

Reliability fixes to process lifecycle, agent fallback, and retention.

### Fixed

- **Ctrl+C no longer orphans the agent.** The agent runs in its own process
  group, so an interrupt at the terminal reached codexmon alone: codexmon died
  by the default disposition and left the agent running unsupervised — still
  spending tokens, with nothing left watching it. SIGINT and SIGTERM now stop
  the agent's process group and record the job as `cancelled`, exactly as
  `codexmon cancel` does. A second Ctrl+C still force-quits.
- **A recycled pid can no longer be killed.** Before signalling an agent pid read
  out of a job file, codexmon now verifies the process actually started inside
  that job's launch window (plus the cheaper checks that it is alive and still
  leads its own process group), so an unrelated process that inherited the pid
  is never terminated. Because identity is verified rather than assumed,
  `codexmon cancel` can still stop a genuinely orphaned agent at any age: a job
  that already reconciled to a terminal state no longer reports "already
  finished" and walks away leaving its agent running. The same identity check
  applies to the recorded worker pid, so a recycled worker number can neither
  fake a live supervisor nor cause a real orphan to be abandoned; when the worker
  really is alive, cancel asks it to stop rather than killing its child.
- **A fast-failing background job keeps its real error.** The launcher recorded
  the worker pid by rewriting the whole status file, which could overwrite a
  terminal status the worker had already written (a bad agent binary, say) with
  a stale `queued`. The error then surfaced as the far vaguer "worker ended
  without recording a result". The worker is now the only writer of `status.json`
  from the moment it starts; it records that pid itself.
- **Suspend/resume no longer reports a healthy run as failed.** Status writes
  stop while a machine sleeps, which looked identical to a wedged worker. A live
  worker now has 10 minutes of silence before it is judged dead, and that verdict
  is no longer persisted, so a resumed run keeps its own record. `clean` also
  leaves a job alone while its worker process is still alive.
- **A crashed launch no longer shadows every later job.** A job seeded as
  `queued` whose worker never took over stayed active forever — never
  reconciled, never pruned — and `status`/`wait` with no id resolved to it
  indefinitely. It now ages out after a minute.
- **Agent fallback sees the whole failure message.** For codex, the text scanned
  for usage-limit phrases was the same 120-byte string used for the one-line
  status summary, so a rate limit wrapped in a verbose provider payload was
  invisible and the run failed instead of handing off to the next agent. The
  match now runs against the full message (up to 4 KiB, so `error` in a status
  block can be longer than before), and a nested `{"message": ...}` error is
  unwrapped rather than shown as raw JSON.
- **A chatty agent cannot exhaust the worker's memory.** Output is now read in
  bounded chunks — previously a single huge blob with no newline in it grew
  without limit — and the non-JSON run's accumulated stdout is capped at 8 MiB
  (`output.log` still holds up to 64 MiB). Either could previously OOM-kill the
  worker, which then surfaced as the much vaguer "worker died without recording
  a result". Memory now stays flat regardless of how much the agent emits, and
  anything dropped is reported in both the log and the result rather than
  disappearing silently.
- Long runs report an hours unit — `2h05m03s`, not `125m03s` — and the `list`
  columns are wide enough to keep the table aligned past an hour.
- `doctor` no longer reports a probe that succeeded at the deadline boundary as
  a timeout, and truncated summaries, result previews, and printed results no
  longer cut a multi-byte character in half.
- `clean` never removes a job whose worker process is still running, even under
  `--all`. Such a job is not finished whatever its record says — a status
  reconciles to `failed` while a merely-suspended worker is very much alive — and
  deleting the directory would strand that worker writing into removed files with
  no status, log, or cancel marker.
- An interrupt that arrives in the same instant the agent exits no longer
  nondeterministically relabels a completed run as `cancelled`.

### Changed

- **Cursor's default `--model` is applied only to print-mode (`-p`) runs**, the
  ones codexmon monitors — matching how the codex adapter gates on `exec` and
  the claude adapter on `-p`. Management subcommands (`cursor-agent status`,
  `login`, `ls`, …) are now forwarded exactly as typed rather than receiving a
  `--model` flag they never asked for and may reject.
- The flag-scanning and message-decoding helpers the three agent adapters had
  each copied now live in one place, so they cannot drift apart.

## v0.7.0

New default model for monitored Cursor runs.

### Changed

- **Cursor runs default to Cursor Grok 4.5** (`--model cursor-grok-4.5-high`)
  instead of Composer 2.5, unless you pass your own `--model`.

## v0.6.0

Automatic agent fallback when no backend is specified.

### Added

- **Fallback chain for an unspecified backend.** When neither `--agent` nor
  `CODEXMON_AGENT` is set, codexmon now tries agents in order — **codex → claude
  → cursor** — instead of only ever running codex. It skips any agent whose
  binary is not installed, and (for a foreground `run`/`review`) hands off to the
  next agent when the current one fails because it hit a usage/rate limit. A
  non-limit failure is surfaced as-is, never masked by a retry, and naming an
  agent explicitly disables fallback entirely (the run uses exactly that agent).
  When codexmon is invoked *by* Claude Code — detected via the `CLAUDECODE`
  environment variable — `claude` is dropped from the chain so codexmon never
  falls back to the agent already calling it, leaving **codex → cursor**.

## v0.5.0

Opinionated model defaults for monitored `codex exec` runs.

### Changed

- **`codex exec` now defaults to `--model gpt-5.6-sol` at `high` reasoning
  effort.** codexmon injects these as global Codex options (before the `exec`
  token) so long-running, monitored reviews and tasks use a strong model
  without the caller repeating the flags. Every explicit override wins:
  `-m/--model`, `-c model=…`, or a `-p/--profile` suppresses the model default,
  and the reasoning-effort default is imposed only when codexmon also picks the
  model — so a caller-chosen model is never forced into a setting it may not
  support. An explicit `-c model_reasoning_effort=…` is always respected. Only
  the exec option region and global flags are inspected, so a prompt word that
  happens to look like a flag can't suppress the defaults.

## v0.4.0

Retention for the jobs directory, a snappier `wait`, and a one-call JSON
result.

### Added

- **Automatic job retention.** Finished jobs are pruned best-effort on every
  launch — older than 7 days or beyond the newest 200 by default, tunable via
  `CODEXMON_KEEP_DAYS` / `CODEXMON_KEEP_JOBS` (`0` disables that limit).
  Previously `~/.codexmon/jobs/` grew without bound (each job can hold up to
  128 MiB of logs) and every `list`/`status`/`wait` scanned all of it. Active
  jobs are never pruned; a status that claims active but whose worker is dead
  reconciles to terminal first, so it ages out too.
- **`codexmon clean [--keep-days N] [--keep N] [--all]`** — apply retention on
  demand. `--all` removes every finished job; active jobs always survive.
- **`wait --json` / `run --json` embed the final output as `result`** on
  completion, so a JSON consumer no longer needs a second read of
  `result_file`. Capped at 4 MiB; a larger output ends with a truncation
  notice naming `result_file`, which always holds the full text.
  (`status --json` still carries only `result_preview` — it is the cheap
  1 Hz poll.)

### Changed

- **`wait` polls adaptively**: it starts at 150 ms and backs off to
  `--interval` (still 2 s by default), so a short job returns in milliseconds
  instead of a full first interval; sleeps are also clamped to `--timeout` so
  the deadline is honored precisely instead of overshooting by up to one
  interval.
- The monitor's stdout/stderr readers start with 64 KiB buffers (agent JSON
  event lines routinely exceed bufio's 4 KiB default), and `tail -f` reuses its
  read buffer across polls.

## v0.3.0

Cursor reviews now run on a known model by default instead of Cursor's
server-side `auto` selection.

### Changed

- Cursor runs default to **Composer 2.5** (`--model composer-2.5`) unless the
  caller passes their own `--model`. The flag is injected before any `--` prompt
  terminator, and an existing `--model` is detected only among the real options
  (not inside the prompt), so a dash-prefixed prompt is never polluted.
  Previously codexmon set no model and left Cursor on `auto`.

## v0.2.0

Multi-agent support: codexmon now monitors **codex** (default), **Claude Code**,
and the **Cursor agent** behind one interface, with the same status contract and
watchdog for all three.

### Added

- **`--agent codex|claude|cursor`** (and `CODEXMON_AGENT`) on
  `run`/`start`/`review`/`doctor`/`version`. Defaults to `codex`, so existing
  commands keep working unchanged.
- **`codexmon review [--uncommitted | --base REF]`** — an agent-neutral code
  review that builds each agent's native invocation (codex's `exec review`; a
  read-only review prompt for Claude Code / Cursor).
- A provider abstraction (`internal/agent`): a normalized event/phase/usage model
  plus a `Provider` registry, so adding an agent is one self-contained package.
- Per-agent binary env overrides `CODEXMON_CLAUDE` and `CODEXMON_CURSOR`
  (alongside the existing `CODEXMON_CODEX`), and `--agent-bin` (`--codex-bin`
  stays as an alias).
- `claude review` denies the file-editing tools (`Edit`/`Write`/`MultiEdit`/
  `NotebookEdit`) in addition to its read-only prompt.

### Changed

- Job records use agent-neutral fields: `--json` now reports `agent`,
  `agent_bin`, and `agent_pid` (previously `codex_bin`/`codex_pid`).
- A leading monitor flag (e.g. `--agent`, `--wall-timeout`) is now parsed as an
  implicit `run`; a leading agent-native flag is still passed through verbatim.

### Fixed

- The watchdog no longer falsely kills a healthy run when an agent batches a long
  shell command with quick tool calls (a command in flight suppresses the
  tool-stuck timeout).
- A dead background worker's orphaned agent process group is now reaped, and the
  reconciled terminal state is persisted to `status.json`.
- A panic in a monitor goroutine tears the agent down and finalizes `failed`
  instead of crashing the worker with the child orphaned.
- A failed background-worker spawn records the job `failed` instead of leaving it
  stuck `queued`; `cancel` reports the job's actual final state; `--stdin` is
  rejected with `start`/`-b`.
- `tail` bounds its initial read; `readResult` is capped; `events.jsonl` writes a
  truncation notice at its cap; atomic status writes pin the temp file to `0600`.

## v0.1.0

Initial release: a health-monitoring wrapper around the `codex` CLI.
