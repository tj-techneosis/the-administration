#!/bin/sh
# Builds the binaries a release carries, into dist/: one per machine the
# installer picks from (macOS and Linux, Apple silicon/ARM and Intel/AMD).
set -eu
here=$(cd "$(dirname "$0")/.." && pwd)
ver=$(git -C "$here" describe --tags --always --dirty 2>/dev/null || echo dev)
mkdir -p "$here/dist"
for target in darwin/arm64 darwin/amd64 linux/amd64 linux/arm64; do
  os=${target%/*} arch=${target#*/}
  (cd "$here" && CGO_ENABLED=0 GOOS=$os GOARCH=$arch go build -trimpath -ldflags "-X main.version=$ver" \
    -o "dist/administration-$os-$arch" ./cmd/administration)
  echo "dist/administration-$os-$arch ($ver)"
done
