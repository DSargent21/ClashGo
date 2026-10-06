#!/usr/bin/env bash
#
# wall_pick.sh — capture the frames the wall-upgrade flow needs, let a human drag
# the boxes on them, and write the asset files.
#
# Why a capture-then-pick session rather than picking live: a box only means
# something against the frame it was drawn on. Every box is recorded as an 860x732
# reference coordinate that the bot re-maps onto the device, so the frames have to
# come from one run at one screen size, and the person dragging has to be able to
# see the control they are drawing around. Both are easier when the frames are
# captured first and picked afterwards in a browser.
#
# It also has to be the same interpreter all the way through: a drag is physical
# pixels, the asset is reference pixels, and the only correct conversion is
# cmd/refmap (see internal/game/calibration.go — the law is piecewise and not
# surjective, so dividing by a scale factor is wrong).
#
# Usage:
#
#   ./tools/wall_pick.sh          guided session: capture all seven states, then
#                                 build and open the picking page
#   ./tools/wall_pick.sh serve    run the local picking server (cmd/wallserve):
#                                 the page in a browser with a Save button, and
#                                 captures driven from the same page
#   ./tools/wall_pick.sh 3        capture just step 3 now, rebuild, open
#   ./tools/wall_pick.sh capture 3   capture step 3 and nothing else
#   ./tools/wall_pick.sh page     rebuild and open the page from what is captured
#   ./tools/wall_pick.sh apply    read `key x1 y1 x2 y2` lines on stdin and write
#                                 the asset files
#
# WALL_PICK_NO_OPEN=1 suppresses opening a browser, which is how cmd/wallserve
# reuses this script without a new tab per capture.
#
#
# The page shows each frame with a note saying what has to be on screen for it, so
# the game only has to be in the right state at capture time, not during picking.
set -euo pipefail

cd "$(dirname "$0")/.."
# WALL_PICK_DIR lets cmd/wallserve point the whole session somewhere else, so a
# server started for a probe does not overwrite the real captures.
OUT="${WALL_PICK_DIR:-output/wall_pick}"
PAGE="$OUT/pick.html"

# step|key|what must be on screen|asset file it lands in
STEPS=(
  "1|builder_button|the village, upgrades menu CLOSED — drag the builder-head button in the top bar|assets/builder_button.json"
  "2|physical|the upgrades menu OPEN (tap the builder head) — drag the whole menu panel|assets/builder_menu_roi.json"
  "3|gold|the wall-upgrade tray OPEN — drag the GOLD upgrade chip|assets/wall_upgrade_buttons.json"
  "4|elixir|the same tray still open — drag the ELIXIR upgrade chip|assets/wall_upgrade_buttons.json"
  "5|confirm_button|the confirm dialog after tapping an upgrade chip — drag its Confirm button|assets/wall_upgrade_confirm.json"
  "6|x_popup_roi|the buy-with-gems popup — drag its X|assets/wall_upgrade_x_roi.json"
  "7|x_popup_roi_alt|the SECOND popup, if this client chains one — else skip it|assets/wall_upgrade_x_roi.json"
)

die() { printf 'wall_pick: %s\n' "$*" >&2; exit 1; }

find_adb() {
  if [ -n "${ADB:-}" ]; then echo "$ADB"; return; fi
  if command -v adb >/dev/null 2>&1; then command -v adb; return; fi
  local c
  for c in "${ANDROID_HOME:-}/platform-tools/adb" "${ANDROID_SDK_ROOT:-}/platform-tools/adb" \
           "$HOME/sdk/platform-tools/adb" "$HOME/Library/Android/sdk/platform-tools/adb" \
           "/usr/local/bin/adb" "/opt/homebrew/bin/adb"; do
    [ -x "$c" ] && { echo "$c"; return; }
  done
  return 1
}

# screen_size prints the live frame size, which is the size the frames are
# captured at and therefore the size refmap has to be told.
screen_size() {
  local raw
  raw="$("$ADB_BIN" shell wm size 2>/dev/null | tr -d '\r')" || die "adb shell wm size failed"
  # "Physical size: 1280x720", possibly after an "Override size:" line
  raw="$(printf '%s\n' "$raw" | sed -n 's/.*size: *\([0-9][0-9]*\)x\([0-9][0-9]*\).*/\1x\2/p' | tail -1)"
  [ -n "$raw" ] || die "could not read the screen size from adb: $(printf '%q' "${raw:-}")"
  printf '%s' "$raw"
}

# frame_size prints WxH from a captured frame's PNG header, so applying picks
# needs no device: the frames already fixed the geometry they were dragged on.
frame_size() {
  local n path
  for n in 1 2 3 4 5 6 7; do
    path="$OUT/step$n.png"
    [ -s "$path" ] || continue
    # od wraps its output across lines, so normalise the whitespace before
    # indexing: the width is IHDR bytes 16..19, the height 20..23, big-endian.
    local bytes
    bytes=$(od -An -tu1 -j16 -N8 "$path" | tr -s '[:space:]' ' ')
    # shellcheck disable=SC2086
    set -- $bytes
    local w h
    w=$(( $1 * 16777216 + $2 * 65536 + $3 * 256 + $4 ))
    h=$(( $5 * 16777216 + $6 * 65536 + $7 * 256 + $8 ))
    if [ "$w" -gt 0 ] && [ "$h" -gt 0 ]; then
      printf '%sx%s' "$w" "$h"
      return
    fi
  done
  printf ''
}

capture() {
  # Two statements, not one: `local n="$1" path="$OUT/step$n.png"` expands $n
  # before the same `local` has bound it, and `set -u` turns that into
  # "n: unbound variable" the moment the capture path is used for real.
  local n="$1"
  local path="$OUT/step$n.png"
  mkdir -p "$OUT"
  "$ADB_BIN" exec-out screencap -p > "$path" || die "screencap failed"
  # A wedged adbd or an unplugged device writes an empty or error file, and a
  # broken frame that reaches the page is worse than no frame: it looks pickable.
  if [ ! -s "$path" ] || ! head -c 8 "$path" | grep -q PNG; then
    rm -f "$path"
    die "step $n: the device did not return a PNG (is the game on screen?)"
  fi
  printf 'step %d: captured %s (%s bytes)\n' "$n" "$path" "$(wc -c < "$path" | tr -d ' ')"
}

build_page() {
  local n have=0
  for n in 1 2 3 4 5 6 7; do
    [ -s "$OUT/step$n.png" ] && have=$((have + 1))
  done
  if [ "$have" -eq 0 ]; then
    die "no frames captured yet — run the guided session first"
  fi
  mkdir -p "$OUT"
  go run ./cmd/pickpage -dir "$OUT" -out "$PAGE"
}

open_page() {
  [ -n "${WALL_PICK_NO_OPEN:-}" ] && return
  case "$(uname -s)" in
    Darwin) open "$PAGE" ;;
    *) xdg-open "$PAGE" >/dev/null 2>&1 || printf 'open %s in a browser\n' "$PAGE" ;;
  esac
}

hint_for() {
  local n="$1" row
  for row in "${STEPS[@]}"; do
    [ "${row%%|*}" = "$n" ] && { printf '%s\n' "$row"; return; }
  done
}

guided() {
  printf 'wall_pick: guided capture. For each step, put the game in the state shown,\nthen press Enter to capture, s to skip, q to quit.\n\n'
  local row n key what file reply
  for row in "${STEPS[@]}"; do
    IFS='|' read -r n key what file <<<"$row"
    printf -- '--- step %s/7: %s -> %s\n    %s\n' "$n" "$key" "$file" "$what"
    read -r -p '    [Enter]=capture  s=skip  q=quit: ' reply || reply=q
    case "$reply" in
      q|Q) break ;;
      s|S) printf '    skipped\n\n'; continue ;;
    esac
    printf '    capturing in 2s (leave the game alone)...\n'
    sleep 2
    capture "$n"
    printf '\n'
  done
}

apply() {
  local size w h
  size="$(frame_size)"
  if [ -z "$size" ]; then
    ADB_BIN="$(find_adb)" || die "no captured frames and no adb found; set ADB=/path/to/adb"
    size="$(screen_size)"
  fi
  w="${size%x*}"; h="${size#*x}"
  printf 'wall_pick: applying picks for a %s frame; read the boxes from the page and paste them here.\n' "$size" >&2
  # -k is left to refmap's default: it uses the display scale the bot pins for
  # this device, so the recorded boxes describe what the bot will actually do.
  # Built through the Makefile so the OpenCV rpath comes from one place.
  make -s refmap ARGS="-schema wall -write -w $w -h $h -optional x_popup_roi_alt"
}

case "${1:-}" in
  "")
    ADB_BIN="$(find_adb)" || die "no adb found; set ADB=/path/to/adb"
    "$ADB_BIN" get-state >/dev/null 2>&1 || die "no device visible to adb ($ADB_BIN)"
    guided
    build_page
    open_page
    printf '\nwall_pick: drag a box over each control, then paste the text from the page into:\n  ./tools/wall_pick.sh apply\n'
    ;;
  page)
    build_page
    open_page
    ;;
  apply)
    apply
    ;;
  serve)
    exec go run ./cmd/wallserve "${@:2}"
    ;;
  capture)
    step="${2:-}"
    case "$step" in
      1|2|3|4|5|6|7) ;;
      *) die "want a step 1..7 after capture (got '${step:-nothing}')" ;;
    esac
    ADB_BIN="$(find_adb)" || die "no adb found; set ADB=/path/to/adb"
    capture "$step"
    ;;
  [1-7])
    ADB_BIN="$(find_adb)" || die "no adb found; set ADB=/path/to/adb"
    IFS='|' read -r _ key what file <<<"$(hint_for "$1")"
    printf 'wall_pick: step %s (%s -> %s)\n    %s\n' "$1" "$key" "$file" "$what"
    capture "$1"
    build_page
    open_page
    ;;
  -h|--help|help)
    sed -n '2,38p' "$0" | sed 's/^# \{0,1\}//'
    ;;
  *)
    die "unknown argument $1 (want a step 1..7, capture N, page, apply, serve, or nothing)"
    ;;
esac
