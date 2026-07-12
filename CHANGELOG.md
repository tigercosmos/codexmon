# Changelog

All notable changes to codexmon are documented here. This project adheres to
[Semantic Versioning](https://semver.org/) (pre-1.0: minor versions may change
behavior).

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
