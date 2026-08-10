#!/bin/bash
# run_edrag_attack.sh — single-shot EDrag rush attack to verify the
# event-troop auto-deploy fix.
#
# You MUST be on the CoC attack screen yourself (base loaded, troops on
# the bar) before running this. The script deploys ONCE on the current
# screen and exits — no search loop, no game relaunch.
#
# Usage:
#   ./run_edrag_attack.sh
#   ./run_edrag_attack.sh --device emulator-5556
#
# Env overrides: DEVICE, STRATEGY, GO_BIN, CLASHGO_BIN
#
set -euo pipefail

DEVICE="${DEVICE:-emulator-5554}"
STRATEGY="${STRATEGY:-assets/strategies/auto_edrag_rush.yaml}"
GO_BIN="${GO_BIN:-go}"
CLASHGO_BIN="${CLASHGO_BIN:-./clashgo_cli}"
LOG_FILE="${LOG_FILE:-tmp/edrag_attack.log}"

while [[ $# -gt 0 ]]; do
  case "$1" in
    --device)    DEVICE="$2"; shift 2 ;;
    --strategy)  STRATEGY="$2"; shift 2 ;;
    --clashgo)   CLASHGO_BIN="$2"; shift 2 ;;
    --log)       LOG_FILE="$2"; shift 2 ;;
    -h|--help)
      sed -n '2,18p' "$0"
      exit 0
      ;;
    *)
      echo "unknown flag: $1" >&2
      exit 2
      ;;
  esac
done

# ---- preflight -------------------------------------------------------------
if ! command -v adb >/dev/null 2>&1; then
  echo "FATAL: 'adb' not in PATH. Install Android Platform Tools." >&2
  exit 1
fi

# Accept device as either `emulator-5555` OR `localhost:5555` — adb lists
# TCP devices under their host:port. Resolve the given id to the actual
# adb serial (exact match first, then host:port fallback) and use the
# resolved serial for every adb invocation below.
resolve_device() {
  local want="$1" serial=""
  if adb devices | awk -v dev="$want" '$1==dev && $2=="device" {found=1} END{exit !found}'; then
    serial="$want"
  else
    serial=$(adb devices | awk -v port="${want##*-}" \
      '$1 ~ /^localhost:/ && $2=="device" && $1 ~ ":"port"$" {print $1; exit}')
  fi
  if [[ -n "$serial" ]]; then
    echo "$serial"
    return 0
  fi
  return 1
}

if ! DEVICE="$(resolve_device "$DEVICE")"; then
  echo "FATAL: device '${DEVICE}' not connected. Try: adb devices" >&2
  echo "       Connected: $(adb devices | awk 'NR>1 && $2=="device" {print $1}' | tr '\n' ' ')" >&2
  exit 1
fi
if [[ ! -f "$STRATEGY" ]]; then
  echo "FATAL: strategy not found: $STRATEGY" >&2
  exit 1
fi

mkdir -p "$(dirname "$LOG_FILE")"

echo "===================================================================="
echo "EDrag rush single-shot deploy on ${DEVICE}"
echo "  strategy: ${STRATEGY}"
echo "  formula:  ${STRATEGY%.*}_formula.json (auto-loaded)"
echo ""
echo "  > Make sure you are on the CoC ATTACK screen with troops loaded."
echo "  > Deploy starts ~1s after you hit enter."
echo "===================================================================="

# WARN (not fatal) if CoC isn't the focused app — user may have a slow
# emulator; they confirmed they'll navigate to the attack screen manually.
if adb_out=$(adb -s "$DEVICE" shell dumpsys window 2>&1) && \
     ! printf '%s\n' "$adb_out" | grep -qF 'com.supercell.clashofclans'; then
  echo "WARN: Clash of Clans does not appear focused on ${DEVICE}." >&2
  echo "      If you're not on the attack screen, the deploy will no-op. Ctrl-C now to abort." >&2
  sleep 3
fi

# ---- build + deploy --------------------------------------------------------
if [[ ! -x "$CLASHGO_BIN" ]]; then
  echo "[build] compiling ${CLASHGO_BIN} (-tags cli)..."
  "$GO_BIN" build -tags cli -o "$CLASHGO_BIN" .
fi

echo "[deploy] starting attack (--once --deploy-only)..."
"$CLASHGO_BIN" \
  --once \
  --deploy-only \
  --strategy "$STRATEGY" \
  --device "$DEVICE" 2>&1 | tee -a "$LOG_FILE"

echo
echo "===================================================================="
echo "DONE. Full log: ${LOG_FILE}"
echo "Check it for event-troop lines:"
echo "  grep 'event troop' ${LOG_FILE}"
echo "===================================================================="
