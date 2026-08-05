#!/usr/bin/env bash
# install_update.sh — in-place ClashGO.app replacement, bundled into the
# .app at Contents/Resources/ and invoked DETACHED by the Go updater
# (internal/updater ApplyAuto) as:
#
#   bash install_update.sh <downloaded_zip> <current_bundle_path> <install_dir>
#
# The Go updater has already verified the zip's SHA256 before calling us.
# This script:
#   0. Re-execs itself from a stable temp copy (see below).
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

# 0. Re-exec from a stable temp copy. This script lives INSIDE the old
#    .app bundle, and step 4 removes that bundle mid-run. bash reads
#    scripts incrementally, so deleting the executing file can break late
#    commands on some systems. Copying ourselves to $TMPDIR first and
#    re-exec'ing guarantees the script's file is never the one removed.
if [[ "${CLASHGO_HELPER_COPY:-0}" != "1" ]]; then
  HELPER_COPY="$(mktemp -d -t clashgo-helper-XXXXXX)"
  cp "$0" "$HELPER_COPY/install_update.sh"
  chmod +x "$HELPER_COPY/install_update.sh"
  exec env CLASHGO_HELPER_COPY=1 HELPER_COPY_DIR="$HELPER_COPY" bash "$HELPER_COPY/install_update.sh" "$@"
fi
trap 'rm -rf "${HELPER_COPY_DIR:-}" "${STAGE:-}"' EXIT

log "install_update.sh: start zip=$ZIP bundle=$BUNDLE dir=$INSTALL_DIR"

# 1. Wait for the parent process to fully exit. The Go side detaches us
#    and calls os.Exit(0); swapping the bundle while the old Mach-O is
#    still mapped can fail with "text file busy". The match is scoped to
#    the exact bundle path (not a bare "ClashGO" match) so a second copy
#    of ClashGO elsewhere on the machine neither stalls nor races us.
EXEC_NAME="$(/usr/libexec/PlistBuddy -c 'Print :CFBundleExecutable' "$BUNDLE/Contents/Info.plist" 2>/dev/null || true)"
EXEC_NAME="${EXEC_NAME:-ClashGO}"
for _ in $(seq 1 30); do
  if ! pgrep -f "$BUNDLE/Contents/MacOS/$EXEC_NAME" >/dev/null 2>&1; then
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
