#!/bin/bash
# Supervised live-run wrapper for the ClashGO CLI bot.
#
# Respawns build/bin/clashgo whenever it exits for ANY reason (panic,
# OOM kill, crash, accidental kill), so the bot keeps farming
# unattended. The CLI's structured logs still go to app.log via the
# logger; this wrapper only captures the human console output into
# bot_run.log so you can see what happened right before a respawn.
#
# Usage:
#   ./run_bot_keepalive.sh              # run forever, respawn on exit
#
# Stop it with Ctrl-C, `pkill -f run_bot_keepalive`, or if launched
# via launchctl: `launchctl remove clashgo-keepalive`.
#
# Env knobs:
#   CLASHGO_BIN        path to the bot binary (default ./build/bin/clashgo)
#   CLASHGO_CONFIG_DIR override the writable-state dir (default: the
#                       standard macOS config dir)
set -u

# Resolve the bot binary. Prefer the absolute CLASHGO_BIN (the script
# may be run from a neutral location outside the project tree to dodge
# macOS TCC file-read restrictions on launchd-spawned processes).
# Fall back to the project root derived from this script's location.
# Note: `make build-cli` emits build/bin/bot_cli; the script lives in
# tools/, so the project root is one level up.
SCRIPT_DIR="$(cd "$(dirname "$0")" && pwd)"
PROJECT_DIR="$(cd "$SCRIPT_DIR/.." && pwd)"
if [ -n "${CLASHGO_BIN:-}" ]; then
  BIN="$CLASHGO_BIN"
elif [ -x "$PROJECT_DIR/build/bin/bot_cli" ]; then
  BIN="$PROJECT_DIR/build/bin/bot_cli"
else
  BIN="$PROJECT_DIR/build/bin/clashgo"
fi

# launchd runs jobs from cwd=/ by default. The bot resolves its
# assets/ and strategies/ relative to its working directory, so run it
# from the project root and pin CLASHGO_ASSETS_DIR explicitly. Without
# this, assets resolve to /assets and the template store fails, which
# previously left the bot running template-less (degraded OCR) or, on
# older binaries, panicking on a nil template store mid-attack.
if [ -d "$PROJECT_DIR/assets" ]; then
  cd "$PROJECT_DIR" || exit 1
  export CLASHGO_ASSETS_DIR="$PROJECT_DIR/assets"
fi

LOG_DIR="$HOME/Library/Application Support/ClashGO/dev/logs"
if [ -n "${CLASHGO_CONFIG_DIR:-}" ]; then
  LOG_DIR="$CLASHGO_CONFIG_DIR/logs"
fi
mkdir -p "$LOG_DIR"
RUNLOG="$LOG_DIR/bot_run.log"

BACKOFF=10
while true; do
  if [ ! -x "$BIN" ]; then
    echo "$(date '+%Y-%m-%d %H:%M:%S') ERROR: $BIN not found/executable — rebuild it (make build-cli) and re-run" | tee -a "$RUNLOG"
    exit 1
  fi
  echo "$(date '+%Y-%m-%d %H:%M:%S') starting bot: $BIN" >> "$RUNLOG"
  "$BIN"
  code=$?
  echo "$(date '+%Y-%m-%d %H:%M:%S') bot exited (code=$code); respawning in ${BACKOFF}s" >> "$RUNLOG"
  sleep "$BACKOFF"
done
