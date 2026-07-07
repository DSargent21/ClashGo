#!/bin/bash
# run_live_attack.sh — SINGLE-COMMAND ATTACK.
#
#   Use this when the bot has found a base and you're already on the
#   clash screen with troops loaded. This script:
#
#     1. Captures the attack screen from your emulator
#     2. Auto-picks a deployment line + spot for every unit in your
#        strategy (no clicks needed)
#     3. Deploys the whole attack in one shot and exits
#
# Usage:
#   ./run_live_attack.sh                                   # default: bluestacks + right edge
#   ./run_live_attack.sh --device emulator-5554            # custom device
#   ./run_live_attack.sh --strategy .../my_strat.yaml      # custom strategy
#   ./run_live_attack.sh --target-edge left                # attack from a different side
#   ./run_live_attack.sh --interactive                     # click placements for each unit
#   ./run_live_attack.sh --replay tmp/my_attack.json       # replay a recorded macro instead
#
# Defaults are tuned for BlueStacks on 127.0.0.1:5555 with the
# auto_edrag_rush strategy. Override any of them with flags.
#
# Env overrides (same as flags):
#   DEVICE, STRATEGY, OUT_DIR, TARGET_EDGE, REPLAY_FILE

set -euo pipefail

# ---- defaults --------------------------------------------------------------
DEVICE="${DEVICE:-127.0.0.1:5555}"
STRATEGY="${STRATEGY:-assets/strategies/auto_edrag_rush.yaml}"
OUT_DIR="${OUT_DIR:-tmp/last_live_attack}"
TARGET_EDGE="${TARGET_EDGE:-right}"
# Default to the binary `make build-cli` produces. The older
# `./clashgo_cli` is a leftover artifact (.gitignore'd, never rebuilt).
CLASHGO_BIN="${CLASHGO_BIN:-./build/bin/bot_cli}"
INTERACTIVE=false
REPLAY_FILE="${REPLAY_FILE:-}"

usage() {
    sed -n '2,30p' "$0"
    exit 0
}

while [[ $# -gt 0 ]]; do
    case "$1" in
        --device)        DEVICE="$2"; shift 2 ;;
        --strategy)      STRATEGY="$2"; shift 2 ;;
        --out)           OUT_DIR="$2"; shift 2 ;;
        --target-edge)   TARGET_EDGE="$2"; shift 2 ;;
        --interactive)   INTERACTIVE=true; shift ;;
        --replay)        REPLAY_FILE="$2"; shift 2 ;;
        -h|--help)       usage ;;
        *)               echo "unknown flag: $1" >&2; exit 2 ;;
    esac
done

# ---- preflight -------------------------------------------------------------
if ! command -v adb >/dev/null 2>&1; then
    echo "FATAL: 'adb' not in PATH. Install Android Platform Tools." >&2
    exit 1
fi
if ! adb devices | awk -v dev="${DEVICE}" '$1==dev && $2=="device" {found=1} END{exit !found}'; then
    echo "FATAL: device '${DEVICE}' not connected." >&2
    echo "  run: adb connect ${DEVICE}" >&2
    echo "  then: adb devices" >&2
    exit 1
fi

mkdir -p "$OUT_DIR"

# ---- branch A: replay a recorded macro -------------------------------------
# Cheapest path: use cmd/attack_record in replay mode. Skips design entirely.
if [[ -n "$REPLAY_FILE" ]]; then
    if [[ ! -f "$REPLAY_FILE" ]]; then
        echo "FATAL: replay file missing: $REPLAY_FILE" >&2
        exit 1
    fi
    if [[ ! -x ./build/bin/attack_record ]]; then
        echo "FATAL: ./build/bin/attack_record not built. Run \`make build-attack-record\` first." >&2
        exit 1
    fi
    echo "==> Replaying macro $REPLAY_FILE on $DEVICE"
    ./build/bin/attack_record --mode replay --in "$REPLAY_FILE" --device "$DEVICE"
    exit 0
fi

# ---- branch B: design + deploy via run_designed_attack.sh -------------------
# Auto-pick by default (no clicks). Pass --interactive to click placements
# for each unit (the old behavior in case auto-pick misses for your base).
case "$INTERACTIVE" in
    true)  AUTO_FLAG="";   TARGET_FLAG="" ;;
    *)     AUTO_FLAG="--auto"; TARGET_FLAG="--target-edge $TARGET_EDGE" ;;
esac

exec ./run_designed_attack.sh \
    --strategy "$STRATEGY" \
    --device "$DEVICE" \
    --out "$OUT_DIR" \
    --clashgo "$CLASHGO_BIN" \
    $AUTO_FLAG \
    $TARGET_FLAG
