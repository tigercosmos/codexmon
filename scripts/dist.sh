#!/usr/bin/env bash
# Build cross-platform codexmon release archives into ./dist (no goreleaser
# needed). Mirrors .goreleaser.yaml so local artifacts match published ones.
#   VERSION=v0.1.0 ./scripts/dist.sh     # or let it derive from git
set -euo pipefail
cd "$(dirname "$0")/.."

VERSION="${VERSION:-$(git describe --tags --always --dirty 2>/dev/null || echo dev)}"
# Match goreleaser: it strips a leading "v" from the version for both the binary
# string and the archive name, and archives are flat (no wrapping directory).
VER="${VERSION#v}"
LDFLAGS="-s -w -X github.com/tigercosmos/codexmon/internal/cli.Version=${VER}"
PLATFORMS=(darwin/amd64 darwin/arm64 linux/amd64 linux/arm64)
DIST="dist"

rm -rf "$DIST"
mkdir -p "$DIST"

for p in "${PLATFORMS[@]}"; do
  os="${p%/*}"; arch="${p#*/}"
  name="codexmon_${VER}_${os}_${arch}"
  stage="$DIST/.stage"
  echo "building ${os}/${arch} -> ${name}.tar.gz"
  rm -rf "$stage"; mkdir -p "$stage"
  GOOS="$os" GOARCH="$arch" CGO_ENABLED=0 \
    go build -trimpath -ldflags "$LDFLAGS" -o "$stage/codexmon" ./cmd/codexmon
  cp README.md LICENSE "$stage/"
  tar -C "$stage" -czf "$DIST/${name}.tar.gz" codexmon README.md LICENSE
  rm -rf "$stage"
done

(
  cd "$DIST"
  if command -v sha256sum >/dev/null 2>&1; then
    sha256sum ./*.tar.gz > SHA256SUMS
  else
    shasum -a 256 ./*.tar.gz > SHA256SUMS
  fi
)

echo "---"
echo "codexmon ${VERSION} artifacts in ./${DIST}:"
ls -1 "$DIST"
