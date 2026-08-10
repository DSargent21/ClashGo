#!/bin/bash
# tools/observe.sh — one-shot "bot's eye view" of the live emulator.
#
#   ./tools/observe.sh [--watch] [--no-ocr]
#
# Captures the current emulator frame, classifies it with the bot's own
# classifier, renders a color map, OCRs on-screen text (Apple Vision),
# and saves the raw PNG + OCR text to ./obs/<timestamp>/ for later
# inspection. Drop --watch to loop every 3 seconds.
#
# Requirements: adb on PATH, go, swiftc (macOS).

set -u
cd "$(dirname "$0")/.."

WATCH=0
OCR=1
for a in "$@"; do
  case "$a" in
    --watch) WATCH=1 ;;
    --no-ocr) OCR=0 ;;
    *) echo "unknown arg: $a" >&2; exit 2 ;;
  esac
done

loop() {
  while true; do
    run_once
    sleep 3
  done
}

run_once() {
  local ts dir png
  ts=$(date +%Y%m%d_%H%M%S)
  dir="obs/$ts"
  mkdir -p "$dir"
  png="$dir/screen.png"

  adb exec-out screencap -p > "$png" 2>/dev/null
  if [ ! -s "$png" ]; then
    echo "[observe] screencap failed"
    return 1
  fi

  local extra=""
  [ "$OCR" = 1 ] && extra="-ocr"
  go run ./cmd/screendump -img "$png" $extra -save "$dir/annotated.png" 2>/dev/null |
    tee "$dir/report.txt"

  # Stash a copy under obs/latest for convenience.
  rm -f obs/latest
  ln -sfn "$ts" obs/latest
  echo
  echo "[observe] artifacts in $dir (obs/latest -> $ts)"
}

if [ "$WATCH" = 1 ]; then
  echo "[observe] watching every 3s (Ctrl-C to stop)"
  loop
else
  run_once
fi
