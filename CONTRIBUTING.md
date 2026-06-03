# Contributing to codexmon

Thanks for your interest! codexmon is a small, focused Go CLI, and contributions
that keep it that way — robust, well-tested, and dependency-light — are very
welcome.

## Prerequisites

- **Go 1.24+**
- The **`codex`** CLI for manual testing (not needed for the automated tests).

## Develop

```sh
git clone https://github.com/tigercosmos/codexmon
cd codexmon
make build        # → ./codexmon
./codexmon help
```

Common tasks (see the [`Makefile`](Makefile)):

| Command | What it does |
|---|---|
| `make build` | Build `./codexmon` |
| `make test` | `go test ./...` |
| `make race` | `go test -race ./...` (concurrency — the monitor is multi-goroutine) |
| `make lint` | `gofmt` check + `go vet` + `staticcheck` |
| `make cover` | Coverage summary |
| `make all` | fmt-check + vet + build + test |

## Before you open a PR

CI runs on Ubuntu and macOS and must pass. Reproduce it locally:

```sh
make lint          # gofmt, go vet, staticcheck must all be clean
make race          # the full suite, including e2e, under the race detector
```

Please:

- **Keep it gofmt-clean** and free of `go vet` / `staticcheck` findings.
- **Add tests** for behavior changes. Tests run against a fake `codex` (a shell
  script), so the whole suite — including the end-to-end tests in [`e2e/`](e2e)
  that build the real binary and drive detached background workers — needs no
  network or Codex auth. Follow the existing patterns:
  - unit tests live beside the code (`internal/<pkg>/*_test.go`);
  - the monitor and e2e tests embed a fake codex whose behavior is driven by
    `FAKE_MODE` / `FAKE_SLEEP` env vars — extend those when you add a scenario.
- **Avoid new dependencies.** codexmon intentionally uses only the standard
  library. Open an issue first if you think one is unavoidable.
- **Keep the standard library only** for the binary; `staticcheck` is the only
  dev tool, fetched on demand.

## Architecture (where things live)

See the **How it works** and **Layout** sections of the [README](README.md).
In short:

| Package | Responsibility |
|---|---|
| `cmd/codexmon` | entrypoint |
| `internal/cli` | argument routing & subcommands |
| `internal/monitor` | the supervisor: spawn, stream, watchdog, status |
| `internal/events` | `codex exec --json` event parsing |
| `internal/job` | on-disk job records (spec/status/log/result) + liveness |
| `internal/codexcli` | locate codex; analyze args; inject `--json` |
| `internal/proc` | process-group lifecycle (stdin guard, group kill) |
| `internal/render` | human-readable status/result formatting |

Two design invariants worth preserving:

1. **Process exit is the authoritative completion signal.** The monitor waits on
   the process (not pipe EOF) and force-closes its pipes if a grandchild keeps
   them open, so codexmon can never hang on the thing it exists to detect.
2. **`status.json` is the contract.** It's written atomically (temp + rename),
   at least once per watchdog tick, and read paths reconcile a dead/stale worker
   to a terminal state. Don't introduce a path that leaves it stale while active.

## Recording the demo

The README's demo GIF is produced from [`docs/demo.tape`](docs/demo.tape) with
[VHS](https://github.com/charmbracelet/vhs):

```sh
make build
PATH="$PWD:$PATH" vhs docs/demo.tape     # → docs/demo.gif
```

The tape points codexmon at the bundled fake codex
([`docs/fakecodex.sh`](docs/fakecodex.sh)) and a throwaway `CODEXMON_HOME`, so it
records cleanly with no Codex install, network, or auth. After regenerating,
uncomment the `![codexmon demo](docs/demo.gif)` line at the top of the README.

## Commit & PR conventions

- Use clear, imperative commit subjects. Conventional-commit prefixes
  (`feat:`, `fix:`, `docs:`, `test:`, `refactor:`) are appreciated.
- Keep each PR focused; describe the change and how you verified it.
- Link any related issue.

## Reporting bugs & security

- **Bugs / features:** open a GitHub issue with steps to reproduce. A
  `codexmon status <id> --json` dump and the relevant `output.log` are gold.
- **Security:** codexmon runs another process and writes prompts/output under
  `~/.codexmon` (dirs `0700`, files `0600`). If you find a sensitive issue,
  please report it privately to the maintainer rather than opening a public
  issue.

## License

By contributing, you agree that your contributions are licensed under the
[MIT License](LICENSE).
