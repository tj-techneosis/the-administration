#!/bin/sh
# Builds and packages every release target: scripts/package.sh VERSION OUTDIR
#
# Each archive, administration-<os>-<arch>.tar.gz, unpacks to a folder that
# install.sh recognises as a release (a VERSION file and build/administration),
# so the installer inside it installs that binary rather than building one.
# SHA256SUMS covers every archive. The release workflow runs this; so can you.
set -eu
version=${1:?usage: scripts/package.sh VERSION OUTDIR}
out=${2:?usage: scripts/package.sh VERSION OUTDIR}
root=$(cd "$(dirname "$0")/.." && pwd)
mkdir -p "$out"
out=$(cd "$out" && pwd)
work=$(mktemp -d)
trap 'rm -rf "$work"' EXIT

for target in darwin/arm64 darwin/amd64 linux/amd64 linux/arm64; do
  os=${target%/*} arch=${target#*/}
  name=administration-$os-$arch
  dir=$work/$name
  mkdir -p "$dir/build"
  (cd "$root" && CGO_ENABLED=0 GOOS=$os GOARCH=$arch go build -trimpath \
    -ldflags "-X main.version=$version" -o "$dir/build/administration" ./cmd/administration)
  cp "$root/install.sh" "$root/LICENSE" "$root/README.md" "$dir/"
  echo "$version" >"$dir/VERSION"
  tar -C "$work" -czf "$out/$name.tar.gz" "$name"
  echo "packaged $name.tar.gz"
done
(cd "$out" && { if command -v sha256sum >/dev/null 2>&1; then sha256sum administration-*.tar.gz; else shasum -a 256 administration-*.tar.gz; fi; } >SHA256SUMS)
echo "wrote $out/SHA256SUMS"
