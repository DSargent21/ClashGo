#!/usr/bin/env bash
# Rebuild internal/adb/zoominject.jar, the on-device helper that injects a real
# multi-pointer pinch through the Android framework.
#
# Why this exists at all: a pinch written with `sendevent` is a no-op on the
# BlueStacks guest. `getevent` shows both slots, but the framework only ever
# sees one contact, so the gesture collapses to a tap on whatever sits under the
# fingers and the camera never zooms. Handing a two-pointer MotionEvent to
# InputManager (what the `input` command does) is the only path that works.
#
# Requirements (one-off, not needed to build the Go bot — the prebuilt jar is
# committed):
#   - a JDK (javac + java on PATH)
#   - android-33/android.jar           (SDK platform stubs, for compiling)
#   - r8.jar                           (provides d8, the .class -> dex compiler)
#
# The jar is pushed to /data/local/tmp/ClashGO-zoominject.jar by
# internal/adb/zoominject.go, which transfers it over the shell because adb
# file-sync is unreliable on this emulator.
set -euo pipefail

here="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
repo="$(cd "$here/../.." && pwd)"
work="$(mktemp -d)"
trap 'rm -rf "$work"' EXIT

ANDROID_JAR="${ANDROID_JAR:-/tmp/android33.jar}"
R8_JAR="${R8_JAR:-/tmp/r8.jar}"

for f in "$ANDROID_JAR" "$R8_JAR"; do
  if [ ! -f "$f" ]; then
    echo "missing $f (set ANDROID_JAR / R8_JAR)" >&2
    exit 1
  fi
done

javac -source 8 -target 8 -bootclasspath "$ANDROID_JAR" -classpath "$ANDROID_JAR" \
  -d "$work" "$here/ZoomInject.java"

mkdir -p "$work/dex"
java -cp "$R8_JAR" com.android.tools.r8.D8 --min-api 24 --lib "$ANDROID_JAR" \
  --output "$work/dex" "$work/ZoomInject.class"

(cd "$work/dex" && jar cf "$repo/internal/adb/zoominject.jar" classes.dex)
echo "wrote $repo/internal/adb/zoominject.jar"
