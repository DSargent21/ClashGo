#!/bin/bash
# run_designed_attack.sh — single-command workflow:
#
#   1. Capture screenshot from the running emulator while you're at the
#      attack screen (no game relaunch required).
#   2. Open cmd/design_attack: walk through every unit (troops, heroes,
#      spells) — you click the placement point(s) for each. A formula.json
#      is saved next to your strategy YAML.
#   3. Run `clashgo_cli --once` with that formula. The bot deploys in
#      the order your strategy specifies, using YOUR pinned coordinates
#      instead of red-zone auto-detection. Exits cleanly after one attack.
#
# Result: a single command runs the entire "design your attack, then
# tap it" loop. Game stays on the attack screen the whole time.
#
# Usage:
#   ./run_designed_attack.sh
#   ./run_designed_attack.sh --strategy assets/strategies/auto_edrag_rush.yaml
#   ./run_designed_attack.sh --device emulator-5556 --out ./tmp/my_attack
#
# Env overrides:
#   DEVICE, STRATEGY, OUT_DIR, GO_BIN, CLASHGO_BIN
#
set -euo pipefail

# ---- defaults --------------------------------------------------------------
DEVICE="${DEVICE:-emulator-5554}"
STRATEGY="${STRATEGY:-assets/strategies/auto_edrag_rush.yaml}"
OUT_DIR="${OUT_DIR:-tmp/last_designed_attack}"
GO_BIN="${GO_BIN:-go}"
CLASHGO_BIN="${CLASHGO_BIN:-./clashgo_cli}"
AUTO_MODE="${AUTO_MODE:-false}"
TARGET_EDGE="${TARGET_EDGE:-}"
SCREEN_PNG=""

while [[ $# -gt 0 ]]; do
  case "$1" in
    --device)      DEVICE="$2"; shift 2 ;;
    --strategy)    STRATEGY="$2"; shift 2 ;;
    --out)         OUT_DIR="$2"; shift 2 ;;
    --clashgo)     CLASHGO_BIN="$2"; shift 2 ;;
    --auto)        AUTO_MODE=true; shift ;;
    --target-edge) TARGET_EDGE="$2"; shift 2 ;;
    -h|--help)
      sed -n '2,30p' "$0"
      exit 0
      ;;
    *)
      echo "unknown flag: $1" >&2
      exit 2
      ;;
  esac
done

# ---- preflight: refuse to proceed if prereqs are missing ------------------
if ! command -v adb >/dev/null 2>&1; then
  echo "FATAL: 'adb' not in PATH. Install Android Platform Tools." >&2
  exit 1
fi
if ! adb devices | awk -v dev="${DEVICE}" '$1==dev && $2=="device" {found=1} END{exit !found}'; then
  echo "FATAL: device '${DEVICE}' not connected. Try: adb devices" >&2
  exit 1
fi
if [[ ! -f "$STRATEGY" ]]; then
  echo "FATAL: strategy not found: $STRATEGY" >&2
  exit 1
fi
if ! command -v "$GO_BIN" >/dev/null 2>&1; then
  echo "FATAL: '${GO_BIN}' not in PATH." >&2
  exit 1
fi

mkdir -p "$OUT_DIR"
SCREEN_PNG="${SCREEN_PNG:-$OUT_DIR/pre_attack.png}"
STAGE1_LOG="$OUT_DIR/01_design.log"
STAGE2_LOG="$OUT_DIR/02_deploy.log"

# formula.json lands next to the YAML so the orchestrator's auto-load
# (pkg/formula.candidatePaths) finds it without a flag.
case "$STRATEGY" in
  *.yaml|*.yml) ;;
  *)
    echo "FATAL: strategy must be .yaml or .yml: $STRATEGY" >&2
    exit 1
    ;;
esac
STRATEGY_DIR="$(dirname "$STRATEGY")"
STRATEGY_BASE="$(basename "${STRATEGY%.*}")"
FORMULA_PATH="${STRATEGY_DIR}/${STRATEGY_BASE}_formula.json"

# ---- stage 1: capture + design placement -----------------------------------
echo "===================================================================="
echo "[1/3] Capturing pre-attack screen from ${DEVICE}"
echo "      output: ${SCREEN_PNG}"
echo "===================================================================="

# Make sure the CoC attack screen is actually in the foreground. If the
# game is at home/village the captured PNG is useless for placement.
# `dumpsys window` reports the focused activity; mResumedActivity=true
# means the app is in the foreground. We accept "com.supercell.clashofclans"
# as the package and any activity name; the bot's classifier checks for
# troop-bar presence downstream regardless.
# Make sure the CoC attack screen is actually in the foreground. If the
# game is at home/village the captured PNG is useless for placement.
# Split adb unreachable vs wrong focus so the WARN tells the truth.
# Hard-stop on adb unreachable — every subsequent stage needs adb. Continue
# on wrong-focus because the user may want to overlay anyway.
if ! adb_out=$(adb -s "$DEVICE" shell dumpsys window 2>&1); then
  echo "FATAL: adb shell failed on ${DEVICE}; no further stage can run." >&2
  echo "       Check: adb devices   adb -s ${DEVICE} reconnect" >&2
  exit 1
elif ! printf '%s\n' "$adb_out" | grep -qF 'com.supercell.clashofclans'; then
  echo "WARN: Clash of Clans does not appear to be focused on ${DEVICE}." >&2
  echo "      Captured PNG may not show the attack screen. Continue at your own risk." >&2
fi

adb -s "$DEVICE" exec-out screencap -p > "$SCREEN_PNG"

echo
echo "===================================================================="
if [[ "$AUTO_MODE" == "true" ]]; then
  echo "[2/3] Auto-pick placements (no clicks required)."
  edge_msg="(strategy default)"
  [[ -n "$TARGET_EDGE" ]] && edge_msg="(forced: $TARGET_EDGE)"
  echo "      target_edge: $edge_msg"
  echo "      formula will be saved to: ${FORMULA_PATH}"
else
  echo "[2/3] Design placements for every unit."
  echo "      Click 1 point or 2 line-endpoints per unit. ENTER=commit, u=undo, s=save, q=quit (no save)."
  echo "      formula will be saved to: ${FORMULA_PATH}"
fi
echo "===================================================================="

declare -a design_args=(-screen "$SCREEN_PNG" -strategy "$STRATEGY" -out "$FORMULA_PATH")
if [[ "$AUTO_MODE" == "true" ]]; then
  design_args+=(-auto)
  [[ -n "$TARGET_EDGE" ]] && design_args+=(-target-edge "$TARGET_EDGE")
fi

"$GO_BIN" run ./cmd/design_attack "${design_args[@]}" 2>&1 | tee -a "$STAGE1_LOG"

echo
echo "===================================================================="
echo "[2/3] placement saved."
echo "===================================================================="
echo

# ---- stage 1.5: verify formula.json was actually saved ---------------------
# design_attack exits 0 even when the user quits with `q` (no save). The
# JSON then is stale from a prior run or absent, and stage 2 would deploy
# against the wrong geometry. Gate stage 2 on a non-empty, valid formula.
if [[ ! -s "$FORMULA_PATH" ]]; then
  echo "FATAL: formula not saved at $FORMULA_PATH (design_attack was abandoned)." >&2
  echo "       Re-run and either commit every unit or press 's' to save." >&2
  exit 1
fi
if ! command -v jq >/dev/null 2>&1; then
  echo "WARN: 'jq' not installed; skipping JSON-shape check on $FORMULA_PATH" >&2
elif ! jq -e '.units | length > 0' "$FORMULA_PATH" >/dev/null 2>&1; then
  echo "FATAL: saved formula has zero units: $FORMULA_PATH" >&2
  exit 1
fi
UNIT_COUNT=$(jq -r '.units | length' "$FORMULA_PATH" 2>/dev/null || echo "?")
echo "[stage guard] formula OK: ${UNIT_COUNT} units committed at $FORMULA_PATH"

# ---- stage 2: deploy using formula ----------------------------------------
echo "===================================================================="
echo "[3/3] Deploying via ${CLASHGO_BIN} --deploy-only --once (reads formula, single deploy, exits)."
echo "      --deploy-only: skips the search/attack-button pipeline; deploys on the base already loaded on the attack screen."
echo "===================================================================="
"$CLASHGO_BIN" \
    --once \
    --deploy-only \
    --strategy "$STRATEGY" \
    --device "$DEVICE" 2>&1 | tee -a "$STAGE2_LOG"

echo
echo "===================================================================="
echo "DONE."
echo "  formula:  ${FORMULA_PATH}"
echo "  screen:   ${SCREEN_PNG}"
echo "  logs:     ${STAGE1_LOG}"
echo "            ${STAGE2_LOG}"
echo "===================================================================="
