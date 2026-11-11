#!/usr/bin/env bash
# Robust adb wrapper for BlueStacks: the TCP ADB bridge drops connections
# randomly ("error: closed"), so each call reconnects and retries.
# ponytail: retry shim only; real fix is a stable emulator ADB bridge.
set -uo pipefail

HD="${HD_ADB:-/Applications/BlueStacks.app/Contents/MacOS/hd-adb}"
SERIAL="${SERIAL:-127.0.0.1:5555}"

connect() {
  "$HD" connect "$SERIAL" >/dev/null 2>&1
}

# adbx <args...>  -- retries until the command stops returning "error: closed"
adbx() {
  local i out rc
  for i in $(seq 1 "${ADBX_TRIES:-8}"); do
    connect
    out=$("$HD" -s "$SERIAL" "$@" 2>&1)
    rc=$?
    if ! grep -qi "error: closed\|device .* not found\|device offline" <<<"$out"; then
      printf '%s' "$out"
      return $rc
    fi
    sleep 1
  done
  printf '%s' "$out"
  return 1
}

case "${1:-}" in
  shell) shift; adbx shell "$@" ;;
  *) adbx "$@" ;;
esac
