#!/bin/sh
# Runs the test suite where it can't touch your session: in a private PID
# namespace (Linux, no root needed), where the tool can only see and signal
# the processes the tests start. The suite refuses to run any other way
# unless ADMINISTRATION_TEST_UNSAFE=1, because it starts and kills real
# processes.
#   tests/isolated.sh [unittest args...]
set -eu
here=$(cd "$(dirname "$0")/.." && pwd)
if [ "$(uname)" != Linux ]; then
  echo "tests/isolated.sh: needs Linux (unshare). On macOS, run the suite in a throwaway account with ADMINISTRATION_TEST_UNSAFE=1." >&2
  exit 1
fi
bin=$(mktemp -d)/administration
(cd "$here" && CGO_ENABLED=0 mise exec -- go build -o "$bin" ./cmd/administration)
exec unshare --user --map-root-user --pid --fork --mount-proc --kill-child \
  env ADMINISTRATION_BIN="$bin" ADMINISTRATION_TEST_ISOLATED=1 \
  sh -c 'cd "$1"; shift; if [ $# -eq 0 ]; then mise exec -- go test ./... || exit 1; set -- discover tests; fi; exec python3 -W ignore -m unittest "$@"' sh "$here" "$@"
