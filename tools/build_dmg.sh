#!/usr/bin/env bash
# build_dmg.sh — assemble a best-practices macOS installer DMG for ClashGO.
#
# Produces a UDZO (compressed read-only) disk image, internet-enabled
# (so download managers in browsers like Chrome / Safari can resume),
# with macOS auto-recognised License.txt shown on mount.
#
# Layout inside the volume:
#   ClashGO.app/           <- the Wails build output (passed in)
#   Applications/          <- symlink for drag-target
#   License.txt            <- macOS auto-shows this on mount
#   README.txt             <- install / remix / licence pointers
#
# Best practices we deliberately apply:
#   - UDZO compression (smallest read-only image, fastest mount)
#   - Internet-enabled (lets browsers resume partial downloads)
#   - Atomic staging dir under $TMPDIR so concurrent builds don't race
#   - Bundle the in-app install helper into the .app
#   - Files-mode=755 / dir-mode=755 on the staging dir so the volume mounts
#     cleanly under any user
#   - Final `hdiutil verify` to catch corruptions before we ship
#
# Not applied (out of scope without an Apple Developer ID):
#   - Code signing
#   - Notarization
#   - Custom .VolumeIcon.icns (FUTURE: wails generate icon set, then drop-in)
#   - Custom Finder background image (FUTURE: ship a `dmg-bg.tiff` asset)
#
# Usage:
#   tools/build_dmg.sh <app_path> <out_dmg> [<volname>]
#
# Example:
#   tools/build_dmg.sh build/bin/ClashGO.app build/bin/ClashGO.dmg "ClashGO v0.2.0-beta"

set -euo pipefail

if [[ $# -lt 2 ]]; then
  cat >&2 <<EOF
usage: $(basename "$0") <app_path> <out_dmg> [<volname>]

  <app_path>  path to ClashGO.app (built by 'make build-gui')
  <out_dmg>   absolute or relative path where the .dmg will be written
  <volname>   optional volume label (defaults to "ClashGO Installer")
EOF
  exit 64
fi

APP_SRC="$1"
OUT_DMG="$2"
VOLNAME="${3:-ClashGO Installer}"

# Validate inputs.
if [[ ! -d "$APP_SRC" ]]; then
  echo "error: app bundle not found: $APP_SRC" >&2
  exit 66
fi
if [[ ! -f "$APP_SRC/Contents/Info.plist" ]]; then
  echo "error: missing Info.plist in $APP_SRC (not a valid .app bundle)" >&2
  exit 66
fi

SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
PROJECT_ROOT="$(cd "$SCRIPT_DIR/.." && pwd)"
DMG_RESOURCES="$PROJECT_ROOT/build/darwin/dmg-resources"

# Build a temp staging dir under $TMPDIR so mktemp / hdiutil don't
# freak out about long paths. We clean it on any exit.
WORK="$(mktemp -d -t clashgo-dmg-XXXXXX)"
trap 'rm -rf "$WORK"' EXIT

log()  { printf "  \033[1;32m\u25b6\033[0m %s\n" "$*" >&2; }
warn() { printf "  \033[1;33m\u26a0\033[0m %s\n" "$*" >&2; }
fail() { printf "  \033[1;31m\u2716\033[0m %s\n" "$*" >&2; exit "${2:-1}"; }

echo "Building $OUT_DMG ...\n" >&2

# Stage 1 — copy the .app into the staging dir.
log "staging app bundle"
cp -R "$APP_SRC" "$WORK/$(basename "$APP_SRC")"
APP_STAGED="$WORK/$(basename "$APP_SRC")"

# Stage 1.5 — fail-fast guard against shipping a "damaged" bundle.
# Wails builds can abort partway through `npm install`, leaving
# Contents/MacOS/ empty even though Info.plist still declares the
# CFBundleExecutable path. macOS launches the app via that exact path
# and raises "damaged or incomplete" if the file is missing — so we
# verify the Mach-O is present, non-empty, and actually a Mach-O
# binary before we ever write the DMG out. This was added in
# response to a user bug report where the .app was empty and the
# DMG shipped anyway.
log "validating Mach-O executable"
EXEC_NAME="$(/usr/libexec/PlistBuddy -c 'Print :CFBundleExecutable' "$APP_STAGED/Contents/Info.plist" 2>/dev/null || true)"
# Rely solely on the empty check: the `2>/dev/null || true` already
# forces an empty stdout on every PlistBuddy failure mode, so any
# non-empty result here was a real value from the Info.plist.
if [[ -z "$EXEC_NAME" ]]; then
  fail "Info.plist is missing CFBundleExecutable in $APP_STAGED/Contents/Info.plist" 66
fi
EXEC_PATH="$APP_STAGED/Contents/MacOS/$EXEC_NAME"
if [[ ! -s "$EXEC_PATH" ]]; then
  fail "Executable missing or empty at $EXEC_PATH (was the wails build complete?)" 66
fi
# A wails build that aborts between writing the Mach-O and the
# chmod +x would pass the size + `file` checks but still fail with
# "permission denied" on launch — same root-cause class, different
# surface error. Catch it here.
if [[ ! -x "$EXEC_PATH" ]]; then
  fail "$EXEC_PATH is not executable (chmod +x missing?) — aborting" 66
fi
if ! file "$EXEC_PATH" | grep -q "Mach-O"; then
  fail "$EXEC_PATH is not a valid Mach-O executable — aborting" 66
fi

# Stage 2 — drop assets + the install helper into the .app.
# The Makefile's previous `package` target did this inline; we keep the
# duplication here so this script can run standalone (e.g. CI without
# butler / npm).
log "copying assets + install helper into the .app"
mkdir -p "$APP_STAGED/Contents/Resources/assets"
if [[ -d "$PROJECT_ROOT/assets" ]]; then
  cp -R "$PROJECT_ROOT/assets/." "$APP_STAGED/Contents/Resources/assets/"
fi
if [[ -f "$PROJECT_ROOT/build/darwin/install_update.sh" ]]; then
  cp "$PROJECT_ROOT/build/darwin/install_update.sh" "$APP_STAGED/Contents/Resources/install_update.sh"
  chmod +x "$APP_STAGED/Contents/Resources/install_update.sh"
else
  warn "no install_update.sh found at build/darwin/ — skipping"
fi

# Stage 3 — symlink Applications on the volume so drag-target works.
log "creating /Applications symlink"
ln -sf /Applications "$WORK/Applications"

# Stage 4 — License.txt (auto-shown by Finder on mount when at root and
# named exactly "License.txt" or "License.rtf"; we use plain text).
if [[ -f "$DMG_RESOURCES/License.txt" ]]; then
  log "copying License.txt"
  cp "$DMG_RESOURCES/License.txt" "$WORK/License.txt"
else
  warn "no License.txt at $DMG_RESOURCES — Finder won't auto-show licence"
fi

# Stage 5 — README.txt (read-on-mount instructions).
if [[ -f "$DMG_RESOURCES/README.txt" ]]; then
  log "copying README.txt"
  cp "$DMG_RESOURCES/README.txt" "$WORK/README.txt"
fi

# Stage 6 — set sane permissions so the mounted volume renders cleanly
# under any user account. Use capital X (rather than 0755) so plain
# text artefacts (License.txt, README.txt) keep their non-executable
# bit — strict macOS file-system auditors flag unexpected +x on text.
log "normalising permissions"
chmod -R u+rwX,go+rX "$WORK"

# Stage 7 — create the DMG.
# Flags picked for distribution:
#   -ov                       overwrite in place
#   -format UDZO              compressed read-only (smallest, fastest)
#   -volname "$VOLNAME"       the label Finder shows
#   -fs HFS+                  max-compat filesystem (also default)
log "running hdiutil create"
rm -f "$OUT_DMG"
hdiutil create \
  -ov \
  -format UDZO \
  -volname "$VOLNAME" \
  -fs HFS+ \
  -srcfolder "$WORK" \
  "$OUT_DMG" 2>&1 | sed 's/^/    /' >&2

# Stage 8 — internet-enable so browsers can resume partial downloads.
# `hdiutil internet-enable` requires HFS+ read-only (which UDZO is).
# On recent macOS the verb moved to diskutil on some images; if it
# fails we keep the DMG (still valid) but call it out loudly so the
# operator notices the regression instead of burying it.
log "internet-enable"
if ! hdiutil internet-enable -yes "$OUT_DMG" 2> >(sed 's/^/    /' >&2); then
  warn "hdiutil internet-enable failed \u2014 the DMG is still valid but"
  warn "browsers may not be able to resume partial downloads. Re-run"
  warn "with --verbose if you need to investigate the cause."
fi

# Stage 9 — quick mount + integrity probe. We attach readonly to /dev/null
# (read-only), do an `ls` on the volume to confirm the .app landed, then
# detach. This proves the DMG is browsable end-to-end, not just a valid
# on-disk container.
log "mount + integrity probe"
MOUNT_OUT="$(mktemp)"
# `hdiutil attach` on macOS 14+ appends deprecation / informational
# messages *after* the plist XML, so plistlib rejects the stream.
# Extract just the leading <?xml ... <plist>...</plist> block.
hdiutil attach -nobrowse -readonly -plist "$OUT_DMG" \
  | awk '/^<\?xml/{flag=1} flag{print} /^<\/plist>/{flag=0; exit}' \
  >"$MOUNT_OUT" 2>/dev/null || true
if [[ ! -s "$MOUNT_OUT" ]]; then
  # Fallback: keep the raw output but the awk below still tolerates
  # pre-plist warnings because it searches by line, not by position.
  hdiutil attach -nobrowse -readonly -plist "$OUT_DMG" >"$MOUNT_OUT" 2>&1 || {
    cat "$MOUNT_OUT" >&2
    fail "could not mount the produced DMG"
  }
fi
# Plist output looks like:
#   <key>mount-point</key>
#   <string>/Volumes/ClashGO Installer</string>
# (with TAB indentation on macOS, not spaces — must use [[:space:]],
#  not literal space, or a tab-indented line leaves leading \t in
#  MOUNT_POINT and the [[ -d ]] check below silently fails).
# Strip the <string> tags + leading whitespace so we get a real path.
MOUNT_POINT="$(awk '
  /<key>mount-point<\/key>/ {
    getline
    gsub(/<string>/, "")
    gsub(/<\/string>/, "")
    gsub(/^[[:space:]]+/, "")
    print
    exit
  }
' "$MOUNT_OUT")"
if [[ -z "$MOUNT_POINT" || ! -d "$MOUNT_POINT" ]]; then
  cat "$MOUNT_OUT" >&2
  fail "could not parse mount point out of hdiutil attach output"
fi

INSPECT_FAIL=0
[[ -d "$MOUNT_POINT/$(basename "$APP_SRC")" ]] || { warn "missing app bundle inside DMG"; INSPECT_FAIL=1; }
[[ -L "$MOUNT_POINT/Applications" ]]           || { warn "missing Applications symlink"; INSPECT_FAIL=1; }
[[ -f "$MOUNT_POINT/License.txt" ]]           || { warn "missing License.txt on mounted volume"; INSPECT_FAIL=1; }

hdiutil detach "$MOUNT_POINT" >/dev/null 2>&1 || warn "detach failed"
rm -f "$MOUNT_OUT"
[[ "$INSPECT_FAIL" -eq 0 ]] || fail "DMG contents probe failed" 70

# Stage 10 — final verify pass.
log "hdiutil verify"
if ! hdiutil verify "$OUT_DMG" >/dev/null 2>&1; then
  fail "hdiutil verify failed for $OUT_DMG"
fi

echo "" >&2
SIZE_BYTES="$(stat -f%z "$OUT_DMG" 2>/dev/null || stat -c%s "$OUT_DMG" 2>/dev/null || echo 0)"
SIZE_HUMAN="$(du -h "$OUT_DMG" | awk '{print $1}')"
echo "\033[1;32mDone.\033[0m $SIZE_HUMAN ($SIZE_BYTES bytes) \u2192 $OUT_DMG" >&2
