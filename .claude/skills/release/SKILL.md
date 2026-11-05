---
name: release
description: >-
  Cut a codexmon release: bump the version, update the changelog, README, and
  other docs, commit and tag, then publish the GitHub release and verify its
  assets. Use when the maintainer asks to release, cut, or ship a new version.
argument-hint: "[X.Y.Z | major | minor | patch]"
disable-model-invocation: true
---

# Release codexmon

Requested version: `$ARGUMENTS` (empty means infer it in step 2).

Pushing a `v*` tag runs `.github/workflows/release.yml`. That workflow runs
`go vet` and `go test`, then GoReleaser builds the archives and creates the
GitHub release. This skill prepares the commit, pushes the tag, and replaces the
generated release notes.

Stop and report on any failed step. Do not skip a check to continue.

## 1. Check the starting state

```sh
git fetch origin --tags
git status -sb
gh auth status
git tag --sort=-v:refname | head -1
git log "$(git tag --sort=-v:refname | head -1)"..HEAD --oneline
```

Stop if any of these is true:

- The branch is not `main`, or it is behind `origin/main`.
- The working tree is dirty. Ask the maintainer to commit or stash the changes.
- There are no commits since the latest tag.
- The latest tag disagrees with `Version` in `internal/cli/cli.go`.

## 2. Choose the version

- `X.Y.Z` or `vX.Y.Z`: use it. It must be higher than the latest tag.
- `major`, `minor`, or `patch`: bump the latest tag.
- Empty: infer the bump from the commits since the latest tag. Use `minor` for
  features and behavior changes, including breaking changes while pre-1.0, and
  `patch` when there are only fixes. Never infer `major`.

Below, `OLD` and `NEW` are bare versions such as `0.12.0`.

## 3. Update the version and docs

Read `git log vOLD..HEAD` and `git diff vOLD..HEAD` first. Base all the text on
what actually changed.

| File | Change |
|---|---|
| `internal/cli/cli.go` | Set `var Version = "NEW"`. |
| `CHANGELOG.md` | Rename `## Unreleased` to `## vNEW` if it exists, otherwise add `## vNEW` above the previous version. Make sure it covers every user-visible change. Use `### Added`, `### Changed`, and `### Fixed`. |
| `README.md` | Update the install URL, which has both `vOLD` and `_OLD_`, and the `git tag` example under **Releasing**. Update any section that the changes made stale. |
| `skills/codexmon/SKILL.md` | Update it if commands, flags, defaults, or models changed. |
| `CONTRIBUTING.md` | Update it if the development workflow or package layout changed. |
| Usage text in `internal/cli/cli.go` | Update it if commands, flags, or defaults changed. |

Do not rewrite the history of earlier versions in `CHANGELOG.md`.

Then look for missed version references. Review each match:

```sh
git grep -nF "OLD" -- ':!CHANGELOG.md'
```

## 4. Verify locally

```sh
make lint
make race
go run ./cmd/codexmon version    # the first line must be: codexmon NEW
```

## 5. Commit and tag

```sh
git add -A
git diff --cached --stat
git commit -m "chore: release vNEW"
git tag -a vNEW -m "codexmon vNEW"
```

Check that the staged files are only the ones you meant to change. Follow the
commit attribution rules of the session.

## 6. Confirm, then push

The release is public once the tag is pushed. If you inferred the version, or
the maintainer did not name one, show the version and the changelog section and
ask for approval first. If the maintainer named the version, continue.

```sh
git push --atomic origin main vNEW
```

## 7. Wait for the release workflow

The run can take a few seconds to appear. Retry the list until it returns an ID.

```sh
gh run list --workflow release.yml --branch vNEW --limit 1 --json databaseId -q '.[0].databaseId'
gh run watch <id> --exit-status
```

If the run fails, read `gh run view <id> --log-failed` and report the cause. Do
not move or delete the pushed tag unless the maintainer approves it. The normal
fix is a new patch release.

## 8. Publish the release notes

GoReleaser generates the notes from commit subjects and filters out `chore:`
commits, so its notes can be empty. Replace them with the changelog section:

```sh
notes="$(mktemp)"
awk -v h="## vNEW" '$0==h{f=1;next} /^## /{f=0} f' CHANGELOG.md > "$notes"
test -s "$notes"
gh release edit vNEW --notes-file "$notes"
```

## 9. Verify the published release

```sh
gh release view vNEW --json isDraft,isPrerelease,assets,url,body
```

Check that:

- The release is not a draft or a prerelease.
- The assets are `SHA256SUMS` and `codexmon_NEW_<os>_<arch>.tar.gz` for
  `linux` and `darwin` on `amd64` and `arm64`, which is five files.
- The body is the changelog section.

Then check the archive for this machine:

```sh
dir="$(mktemp -d)"
archive="codexmon_NEW_$(go env GOOS)_$(go env GOARCH).tar.gz"
gh release download vNEW --dir "$dir" --pattern "$archive" --pattern SHA256SUMS
(cd "$dir" && grep -F "$archive" SHA256SUMS | shasum -a 256 -c - && tar -xzf "$archive" codexmon && ./codexmon version)
```

The first line of the version output must be `codexmon NEW`.

Also read the `ci.yml` result for the release commit:

```sh
gh run list --workflow ci.yml --commit "$(git rev-parse HEAD)" --limit 1
```

## 10. Report

Give the release URL, the version, the files changed, and the results of the
workflow, the archive check, and CI. State anything that was skipped or is still
running.
