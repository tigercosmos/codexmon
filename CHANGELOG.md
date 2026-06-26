# Changelog

All notable changes to codexmon are documented here. This project adheres to
[Semantic Versioning](https://semver.org/) (pre-1.0: minor versions may change
behavior).

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
