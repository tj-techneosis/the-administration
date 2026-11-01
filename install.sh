#!/bin/sh
# Installs `administration` into ~/.local/bin, and the browser the rooms use.
# Run it again to upgrade; contracts and rosters carry over.
#
#   curl -fsSL https://raw.githubusercontent.com/tj-techneosis/the-administration/main/install.sh | sh
#
# Piped like that (or run as a lone downloaded file), it fetches the latest
# release for this machine, checks its SHA-256, unpacks it into
# ~/.local/share/administration/releases/<version>/ and installs from there.
# Run from a release it installs that release's binary; run from a checkout
# it always builds from source (Go 1.26 or newer, or mise). Options go after
# `sh -s --` when piped.
#
#   ./install.sh                 install (and the browser)
#   ./install.sh --no-browser    skip the browser (critics only, or already installed)
#   ./install.sh --version v0.5.0   that release rather than the latest (download only)
#
# A checkout with no Go (or mise) installs the latest release instead.
set -eu
repo=tj-techneosis/the-administration
releases=${ADMINISTRATION_RELEASES:-https://github.com/$repo/releases}
data=${XDG_DATA_HOME:-$HOME/.local/share}/administration
bindir="${ADMINISTRATION_BIN_DIR:-$HOME/.local/bin}"
# Keep in step with playwrightMCP in cmd/administration/work.go.
mcp="@playwright/mcp@0.0.83"

# Where this script runs from decides what it installs. Piped into sh it has
# no file of its own, and the working directory says nothing about it.
here=""
case "$0" in
  */install.sh|install.sh) [ -f "$0" ] && here=$(cd "$(dirname "$0")" && pwd) ;;
esac
if [ -n "$here" ] && [ -f "$here/cmd/administration/main.go" ]; then
  mode=checkout
elif [ -n "$here" ] && [ -f "$here/VERSION" ] && [ -x "$here/build/administration" ]; then
  mode=release
else
  mode=download
fi

browser=1 want=latest pass=""
while [ $# -gt 0 ]; do
  case $1 in
    --no-browser) browser=0; pass="$pass --no-browser" ;;
    --version) want=${2:?--version needs a release, e.g. v0.4.0}; shift ;;
    --version=*) want=${1#*=} ;;
    -h|--help)
      if [ -n "$here" ]; then sed -n '2,18p' "$0" | sed 's/^# \{0,1\}//'
      else echo "Installs the Administration's latest release. Options (after sh -s --): --no-browser, --version vX.Y.Z. See https://github.com/$repo#readme"; fi
      exit 0 ;;
    *) echo "install.sh: unknown option $1 (try --help)" >&2; exit 2 ;;
  esac
  shift
done
case "$want" in
  latest|v[0-9]*.[0-9]*.[0-9]*) ;;
  *) echo "install.sh: --version wants a release tag like v0.4.0, not \"$want\"" >&2; exit 2 ;;
esac
# A checkout with no Go installs a release, so --version means something there too.
if [ "$mode" = checkout ] && [ "$want" != latest ] && ! command -v go >/dev/null 2>&1 && ! command -v mise >/dev/null 2>&1; then
  mode=download
fi
if [ "$mode" != download ] && [ "$want" != latest ]; then
  echo "install.sh: --version is for downloading a release; this is a $mode install" >&2
  exit 2
fi

os=$(uname -s | tr '[:upper:]' '[:lower:]')
arch=$(uname -m); [ "$arch" = x86_64 ] && arch=amd64; [ "$arch" = aarch64 ] && arch=arm64

# A checkout builds; without Go it falls back to the latest release (an old
# clone that used to install a committed binary still upgrades).
if [ "$mode" = checkout ] && ! command -v go >/dev/null 2>&1 && ! command -v mise >/dev/null 2>&1; then
  echo "install.sh: no Go (or mise) to build this checkout, so installing the latest release instead"
  mode=download
fi

if [ "$mode" = download ]; then
  case "$os-$arch" in
    darwin-arm64|darwin-amd64|linux-amd64|linux-arm64) ;;
    *) echo "install.sh: no release for $os-$arch; build from a checkout of the repo instead, with Go" >&2; exit 1 ;;
  esac
  asset=administration-$os-$arch.tar.gz
  if [ "$want" = latest ]; then url=$releases/latest/download; else url=$releases/download/$want; fi
  tmp=$(mktemp -d)
  trap 'rm -rf "$tmp"' EXIT
  echo "fetching $asset ($want)"
  # Time limits, so a stalled connection fails instead of hanging the
  # one-liner (and administration upgrade).
  get() { curl -fsSL --connect-timeout 15 --max-time 300 "$1" -o "$2"; }
  get "$url/$asset" "$tmp/$asset" || { echo "install.sh: could not download $url/$asset (is $want a release?)" >&2; exit 1; }
  get "$url/SHA256SUMS" "$tmp/SHA256SUMS" || { echo "install.sh: could not download $url/SHA256SUMS" >&2; exit 1; }
  expect=$(awk -v a="$asset" '$2 == a || $2 == "*"a {print $1}' "$tmp/SHA256SUMS")
  if command -v sha256sum >/dev/null 2>&1; then got=$(sha256sum "$tmp/$asset"); else got=$(shasum -a 256 "$tmp/$asset"); fi
  got=${got%% *}
  if [ -z "$expect" ] || [ "$expect" != "$got" ]; then
    echo "install.sh: checksum mismatch for $asset: expected ${expect:-nothing}, got $got. Not installing." >&2
    exit 1
  fi
  echo "checksum ok"
  tar -xzf "$tmp/$asset" -C "$tmp"
  ver=$(cat "$tmp/administration-$os-$arch/VERSION")
  # It names a folder about to be replaced: a release tag and nothing else.
  case "$ver" in
    v[0-9]*) case "$ver" in */*|*..*|*" "*) ver="" ;; esac ;;
    *) ver="" ;;
  esac
  [ -n "$ver" ] || { echo "install.sh: the release's VERSION isn't a release tag; not installing" >&2; exit 1; }
  mkdir -p "$data/releases"
  rm -rf "${data:?}/releases/$ver"
  mv "$tmp/administration-$os-$arch" "$data/releases/$ver"
  # Keep this release and the one installed before it; older ones go.
  prev=$("$bindir/administration" version 2>/dev/null | awk '{print $NF}' || true)
  for d in "$data"/releases/*/; do
    d=$(basename "$d")
    [ "$d" = "$ver" ] || [ "$d" = "$prev" ] || rm -rf "${data:?}/releases/$d"
  done
  rm -rf "$tmp"
  trap - EXIT
  # shellcheck disable=SC2086 # pass is a list of plain flags
  exec sh "$data/releases/$ver/install.sh" $pass
fi

mkdir -p "$bindir"
if [ "$mode" = release ]; then
  ver=$(cat "$here/VERSION")
  cp "$here/build/administration" "$bindir/administration.new" && mv "$bindir/administration.new" "$bindir/administration"
else
  if ! command -v go >/dev/null 2>&1; then
    go() { mise exec -- go "$@"; }
  fi
  ver=$(git -C "$here" describe --tags --always --dirty 2>/dev/null || echo dev)
  (cd "$here" && CGO_ENABLED=0 go build -trimpath -ldflags "-X main.version=$ver" -o "$bindir/administration.new" ./cmd/administration)
  mv "$bindir/administration.new" "$bindir/administration"
fi
echo "installed $bindir/administration ($ver)"

# The browser server goes in a folder of the Administration's own, pinned,
# so nothing in a project (an .npmrc, a node_modules) can choose the code
# that enforces the wall.
tools="${ADMINISTRATION_TOOLS:-$HOME/.local/share/administration/tools}"
if [ "$browser" = 1 ]; then
  if command -v npm >/dev/null 2>&1; then
    mkdir -p "$tools"
    (cd "$tools" && npm install --no-audit --no-fund --silent --prefix "$tools" "$mcp")
    "$tools/node_modules/.bin/playwright" install chromium
    echo "installed $mcp and Chromium in $tools"
  else
    echo "install.sh: no npm, so no browser; browser professionals need Node" >&2
  fi
fi

case ":$PATH:" in
  *":$bindir:"*) ;;
  *) echo "note: $bindir is not on PATH. The plugin finds the binary anyway; for your own shell:"
     echo "  echo 'export PATH=\"$bindir:\$PATH\"' >> ~/.zprofile" ;;
esac
if ! command -v claude >/dev/null 2>&1; then
  echo "note: professionals run the standalone Claude Code CLI (claude), which isn't on PATH."
  echo "  The VS Code extension doesn't provide it. Install it: https://code.claude.com/docs/en/setup"
fi
# The first time only: an upgrade has the plugin already, and its skill reads
# the new binary's instructions from the next task.
if ! grep -q '"the-administration@' "$HOME/.claude/plugins/installed_plugins.json" 2>/dev/null; then
  cat <<MSG

Next, in Claude Code (CLI or VS Code chat):
  /plugin marketplace add tj-techneosis/the-administration
  /plugin install the-administration@tj-techneosis
Then quit and reopen VS Code (Cmd+Q) so the extension picks it up.
MSG
fi
