#!/usr/bin/env bash
# install_update.sh — in-place ClashGO.app replacement, bundled into the
# .app at Contents/Resources/ and invoked DETACHED by the Go updater
# (internal/updater ApplyAuto) as:
#
#   bash install_update.sh <downloaded_zip> <current_bundle_path> <install_dir>
#
# The Go updater has already verified the zip's SHA256 before calling us.
# This script:
#   1. Waits for the parent ClashGO process to exit (it spawns us detached
#      and then calls os.Exit(0) immediately).
#   2. Unzips the verified download into a temp staging dir.
#   3. Strips quarantine + extended attributes (Gatekeeper "damaged"
#      avoidance on macOS).
#   4. Swaps the bundle at the install location.
#   5. Relaunches the new app.
set -euo pipefail

ZIP="${1:?usage: install_update.sh <zip> <bundle_path> <install_dir>}"
BUNDLE="${2:?usage: install_update.sh <zip> <bundle_path> <install_dir>}"
INSTALL_DIR="${3:?usage: install_update.sh <zip> <bundle_path> <install_dir>}"

APP_NAME="$(basename "$BUNDLE")" # ClashGO.app
LOG_DIR="$HOME/Library/Application Support/ClashGO"
LOG_FILE="$LOG_DIR/update_install.log"
mkdir -p "$LOG_DIR"

log() { printf '%s %s\n' "$(date '+%Y-%m-%d %H:%M:%S')" "$*" >>"$LOG_FILE" 2>/dev/null || true; }

log "install_update.sh: start zip=$ZIP bundle=$BUNDLE dir=$INSTALL_DIR"

# 1. Wait for the parent process to fully exit. The Go side detaches us
#    and calls os.Exit(0); swapping the bundle while the old Mach-O is
#    still mapped can fail with "text file busy".
for _ in $(seq 1 30); do
  if ! pgrep -f 'ClashGO.app/Contents/MacOS/ClashGO' >/dev/null 2>&1; then
    break
  fi
  sleep 1
done
sleep 1

# 2. Sanity-check the verified download is present.
if [[ ! -f "$ZIP" ]]; then
  log "install_update.sh: ERROR zip missing: $ZIP"
  exit 1
fi

# 3. Stage the new bundle in a temp dir (cleaned on any exit).
STAGE="$(mktemp -d -t clashgo-update-XXXXXX)"
trap 'rm -rf "$STAGE"' EXIT
unzip -q "$ZIP" -d "$STAGE" || { log "install_update.sh: ERROR unzip failed: $ZIP"; exit 1; }

NEW_APP="$STAGE/$APP_NAME"
if [[ ! -d "$NEW_APP" ]]; then
  NEW_APP="$(find "$STAGE" -maxdepth 1 -name '*.app' -print -quit)"
fi
if [[ -z "$NEW_APP" || ! -d "$NEW_APP" ]]; then
  log "install_update.sh: ERROR no .app bundle found in $ZIP"
  exit 1
fi

# 4. Strip quarantine + ACLs so Gatekeeper doesn't flag the fresh bundle
#    as "damaged".
xattr -dr com.apple.quarantine "$NEW_APP" 2>/dev/null || true
xattr -rc "$NEW_APP" 2>/dev/null || true
chmod -R u+rwX,go+rX "$NEW_APP"

# 5. Swap the bundle at the install location.
DEST="$INSTALL_DIR/$APP_NAME"
rm -rf "$DEST"
if ! mv "$NEW_APP" "$DEST"; then
  log "install_update.sh: ERROR mv to $DEST failed"
  exit 1
fi

# 6. Relaunch from the install location.
open "$DEST" 2>>"$LOG_FILE" || true
log "install_update.sh: done — installed $DEST and relaunched"
exit 0
