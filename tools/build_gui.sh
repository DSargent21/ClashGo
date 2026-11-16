#!/usr/bin/env bash
# build_gui.sh — build the Wails GUI against the pruned gocv staged by
# tools/slim_build.sh, then restore the module file.
#
# Why the swap: `go build` takes -modfile, and `make build-cli` passes it
# through GOFLAGS. The wails CLI drives `go build` itself and rejects that
# variable ("build flag -modfile only valid when using modules"), so the local
# replace has to sit in go.mod while wails runs. A trap restores the checked-in
# go.mod/go.sum on every exit path, including Ctrl-C and a failed build, and a
# byte comparison afterwards turns a missed restore into a build failure rather
# than a silently rewritten module file.
#
# Usage: tools/build_gui.sh [wails build flags...]
set -euo pipefail

ROOT=$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)
SLIM="$ROOT/build/bin/slim"

if [ ! -f "$SLIM/go.slim.mod" ] || [ ! -d "$SLIM/gocv" ]; then
  echo "build_gui: run tools/slim_build.sh first (or 'make build-gui')" >&2
  exit 1
fi

cp "$ROOT/go.mod" "$SLIM/go.mod.orig"
cp "$ROOT/go.sum" "$SLIM/go.sum.orig"

restore() {
  cp -f "$SLIM/go.mod.orig" "$ROOT/go.mod"
  cp -f "$SLIM/go.sum.orig" "$ROOT/go.sum"
}
trap restore EXIT INT TERM

printf '\nreplace gocv.io/x/gocv => %s\n' "$SLIM/gocv" >> "$ROOT/go.mod"

PKG_CONFIG_PATH="$SLIM/opencv" wails build "$@"

# The trap restores; verify it did (a killed process is the only way past it).
restore
if ! cmp -s "$ROOT/go.mod" "$SLIM/go.mod.orig" || ! cmp -s "$ROOT/go.sum" "$SLIM/go.sum.orig"; then
  echo "build_gui: go.mod/go.sum not restored from the backup in $SLIM" >&2
  exit 1
fi
