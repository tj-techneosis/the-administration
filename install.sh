#!/bin/sh
# Installs `administration` into ~/.local/bin (the release's binary for this
# machine, or built from source) and the browser the rooms use. Run it again to upgrade; contracts and rosters carry over.
#   ./install.sh               build and install
#   ./install.sh --no-browser  skip the browser (critics only, or already installed)
set -eu
here=$(cd "$(dirname "$0")" && pwd)
bindir="${ADMINISTRATION_BIN_DIR:-$HOME/.local/bin}"
# Keep in step with playwrightMCP in cmd/administration/work.go.
mcp="@playwright/mcp@0.0.83"

mkdir -p "$bindir"
# A release carries a binary for each machine (scripts/release.sh builds them
# into dist/); a checkout without one builds from source.
os=$(uname -s | tr '[:upper:]' '[:lower:]')
arch=$(uname -m); [ "$arch" = x86_64 ] && arch=amd64; [ "$arch" = aarch64 ] && arch=arm64
prebuilt="$here/dist/administration-$os-$arch"
if [ -x "$prebuilt" ]; then
  cp "$prebuilt" "$bindir/administration.new" && mv "$bindir/administration.new" "$bindir/administration"
  echo "installed $bindir/administration ($("$bindir/administration" version 2>/dev/null || echo prebuilt))"
else
  if ! command -v go >/dev/null 2>&1; then
    if command -v mise >/dev/null 2>&1; then
      go() { mise exec -- go "$@"; }
    else
      echo "install.sh: no prebuilt binary for $os-$arch in dist/, and building needs Go 1.26 or newer (or mise, which reads mise.toml here)" >&2
      exit 1
    fi
  fi
  ver=$(git -C "$here" describe --tags --always --dirty 2>/dev/null || echo dev)
  (cd "$here" && CGO_ENABLED=0 go build -trimpath -ldflags "-X main.version=$ver" -o "$bindir/administration" ./cmd/administration)
  echo "installed $bindir/administration ($ver)"
fi

# The browser server goes in a folder of the Administration's own, pinned,
# so nothing in a project (an .npmrc, a node_modules) can choose the code
# that enforces the wall.
tools="${ADMINISTRATION_TOOLS:-$HOME/.local/share/administration/tools}"
if [ "${1:-}" != "--no-browser" ]; then
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
