#!/bin/bash
# Install (or remove) the ClashGO LaunchAgent.
#
# The keepalive wrapper already respawns the bot when it exits; what it cannot
# do is survive a closed terminal, a logout, or a reboot, and nothing kept the
# Mac awake. This installs a per-user LaunchAgent that runs the wrapper under
# `caffeinate -dimsu`, so the account farms across reboots and sleep.
#
# Usage:
#   bash tools/install_daemon.sh              # install + start
#   bash tools/install_daemon.sh --uninstall  # stop + remove
#   bash tools/install_daemon.sh --status     # print current state
#
# Env:
#   CLASHGO_BIN   bot binary to run (default: build/bin/bot_cli in this repo,
#                 else ~/.local/bin/bot_cli)
#
# No sudo: everything lives under the invoking user's HOME.
set -euo pipefail

LABEL="com.clashgo.agent"
PLIST="$HOME/Library/LaunchAgents/$LABEL.plist"
LOG_DIR="$HOME/Library/Application Support/ClashGO/logs"
SCRIPT_DIR="$(cd "$(dirname "$0")" && pwd)"
REPO_DIR="$(cd "$SCRIPT_DIR/.." && pwd)"
KEEPALIVE="$SCRIPT_DIR/run_bot_keepalive.sh"

resolve_bin() {
  if [ -n "${CLASHGO_BIN:-}" ]; then
    printf '%s\n' "$CLASHGO_BIN"
  elif [ -x "$REPO_DIR/build/bin/bot_cli" ]; then
    printf '%s\n' "$REPO_DIR/build/bin/bot_cli"
  elif [ -x "$HOME/.local/bin/bot_cli" ]; then
    printf '%s\n' "$HOME/.local/bin/bot_cli"
  else
    printf '%s\n' ""
  fi
}

DRY_RUN=0
case "${1:-}" in
  --dry-run)
    # Print the plist without installing it, so the file can be linted and
    # reviewed (plutil -lint) without starting a bot on a live account.
    DRY_RUN=1
    ;;
  --uninstall)
    launchctl bootout "gui/$(id -u)/$LABEL" 2>/dev/null || true
    rm -f "$PLIST"
    echo "removed $LABEL (log files kept in $LOG_DIR)"
    exit 0
    ;;
  --status)
    launchctl print "gui/$(id -u)/$LABEL" 2>/dev/null | sed -n '1,25p' || echo "$LABEL is not loaded"
    exit 0
    ;;
  "" ) ;;
  * )
    echo "usage: $0 [--uninstall|--status]" >&2
    exit 2
    ;;
esac

BIN="$(resolve_bin)"
if [ -z "$BIN" ] || [ ! -x "$BIN" ]; then
  echo "error: no bot binary found. Build one with 'make build-cli' or set CLASHGO_BIN." >&2
  exit 1
fi
if [ ! -x "$KEEPALIVE" ]; then
  echo "error: $KEEPALIVE is missing or not executable." >&2
  exit 1
fi

mkdir -p "$HOME/Library/LaunchAgents" "$LOG_DIR"

# caffeinate keeps the Mac awake while the bot runs; -dimsu covers display,
# idle, disk, system and user assertions. The wrapper respawns the bot, so this
# agent only has to keep the wrapper alive.
cat > "$PLIST" <<PLIST
<?xml version="1.0" encoding="UTF-8"?>
<!DOCTYPE plist PUBLIC "-//Apple//DTD PLIST 1.0//EN" "http://www.apple.com/DTDs/PropertyList-1.0.dtd">
<plist version="1.0">
<dict>
  <key>Label</key><string>$LABEL</string>
  <key>ProgramArguments</key>
  <array>
    <string>/usr/bin/caffeinate</string>
    <string>-dimsu</string>
    <string>$KEEPALIVE</string>
  </array>
  <key>EnvironmentVariables</key>
  <dict>
    <key>CLASHGO_BIN</key><string>$BIN</string>
    <key>CLASHGO_ASSETS_DIR</key><string>$REPO_DIR/assets</string>
    <key>PATH</key><string>/usr/local/bin:/opt/homebrew/bin:/usr/bin:/bin:/usr/sbin:/sbin</string>
  </dict>
  <key>WorkingDirectory</key><string>$REPO_DIR</string>
  <key>RunAtLoad</key><true/>
  <key>KeepAlive</key><true/>
  <key>ThrottleInterval</key><integer>30</integer>
  <key>StandardOutPath</key><string>$LOG_DIR/daemon.out.log</string>
  <key>StandardErrorPath</key><string>$LOG_DIR/daemon.err.log</string>
  <key>ProcessType</key><string>Background</string>
</dict>
</plist>
PLIST

if [ "$DRY_RUN" = "1" ]; then
  cat "$PLIST"
  rm -f "$PLIST"
  exit 0
fi

launchctl bootout "gui/$(id -u)/$LABEL" 2>/dev/null || true
launchctl bootstrap "gui/$(id -u)" "$PLIST"
launchctl enable "gui/$(id -u)/$LABEL" 2>/dev/null || true

echo "installed $LABEL"
echo "  binary : $BIN"
echo "  wrapper: $KEEPALIVE"
echo "  logs   : $LOG_DIR (app.log, bot_run.log, daemon.*.log)"
echo "Stop it with: bash tools/install_daemon.sh --uninstall"
