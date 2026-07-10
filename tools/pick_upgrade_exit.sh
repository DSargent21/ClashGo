#!/usr/bin/env bash
# pick_upgrade_exit.sh — interactive wizard for the simplified
# wall-upgrade exit flow's two assets.
#
# Walks the user through two picker.py runs in sequence, prompting
# for screen navigation between them:
#
#   Step 1/2 — capture post-upgrade Confirm button as a POINT
#              (writes assets/wall_upgrade_confirm.json)
#   Step 2/2 — capture the X-button ROI on the unaffordable gem-buy
#              popup as a RECT (writes assets/wall_upgrade_x_roi.json)
#
# The two assets MUST be picked on different dialog screens — the
# post-upgrade Confirm dialog and the unaffordable gem-buy popup
# can't both be visible at once. The script therefore runs the
# picker twice, with an Enter-key prompt between runs so the user
# can navigate the BlueStacks window into the right screen state.
#
# Why two assets instead of one combined file:
#   • Clean ownership — each asset is owned by one picker run.
#   • Independent re-runs — you can re-pick the Confirm button
#     without re-picking the popup ROI, and vice versa.
#   • Two CLI invocations are cheaper than maintaining one custom
#     preset whose screen-frame spans two UI states.
#
# Usage:
#   tools/pick_upgrade_exit.sh                         # uses config.json device
#   tools/pick_upgrade_exit.sh --device=HOST:PORT         # one-off override (equals form)
#   tools/pick_upgrade_exit.sh --device=localhost:5555   # equals form
#   tools/pick_upgrade_exit.sh --device localhost:5555    # space-separated form (same)
#   tools/pick_upgrade_exit.sh --rect-confirm             # use rect-shaped confirm
# Both --device=VALUE and --device VALUE are accepted.
#
# If you'd rather pick the Confirm button as a RECT (more forgiving
# to UI shifts) instead of a POINT, pass --rect-confirm:
#   tools/pick_upgrade_exit.sh --rect-confirm
# That replaces the `--preset confirm` step with
# `-o assets/wall_upgrade_confirm.json --rect confirm_button`
# so the picker writes the rect schema instead of the point one.
# Note: the bot's wall_upgrade.go treats both as "tap the center";
# the rect variant just gives a tolerance band if the Confirm button
# shows up at slightly different coords across renders.
#
# SCHEMA WARNING: --preset confirm writes {confirm_button: {x,y}}
# (point) while --rect-confirm writes {confirm_button: {x1,y1,x2,y2}}
# (rect). Whatever Go loader consumes assets/wall_upgrade_confirm.json
# must branch on schema — pick the variant that's easiest to integrate
# with the rest of your internal/bot code. Both wall_upgrade.go
# versions would tap the rect's {cx, cy} = ((x1+x2)/2, (y1+y2)/2).
#
# Exit codes:
#   0 — both assets picked successfully
#   1 — pre-flight failed: device is configured but adb can't see it
#       RIGHT NOW. Distinguishable from 2 because the device id itself
#       is valid; only network/process state is wrong. See the FATAL
#       block above the picker invocations.
#   2 — argv or preflight precondition not met: unknown flag, missing
#       python3 on PATH, or failed tmpfile write. Distinct from 1:
#       the device id never even got resolved before exit.
#   non-zero otherwise — picker was cancelled, adb errored mid-pick,
#       user hit 'q' in the picker, etc.

set -e

# Resolve repo root: this script lives in tools/, repo root is one up.
SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
REPO_ROOT="$(cd "${SCRIPT_DIR}/.." && pwd)"
cd "${REPO_ROOT}"

DEVICE_FLAG=""
RECT_CONFIRM=0

# Manual while-loop flag parsing (instead of `for arg in "$@"`) so the
# script accepts BOTH `--device=VALUE` and `--device VALUE` cleanly.
# `for` iterates over already-tokenized args, which collapses
# `--device VALUE` into two separate args and rejects the first as
# an unknown flag.
while [ $# -gt 0 ]; do
  case "$1" in
    --device=*)
      DEVICE_FLAG="--device=${1#*=}"
      shift
      ;;
    --device)
      # Two-token form: consume both args.
      DEVICE_FLAG="--device=$2"
      shift 2
      ;;
    --rect-confirm)
      RECT_CONFIRM=1
      shift
      ;;
    -h|--help)
      sed -n '2,/^$/p' "$0"  # print the comment block above set -e
      exit 0
      ;;
    *)
      echo "pick_upgrade_exit.sh: unknown flag: $1" >&2
      echo "  (try --device=HOST:PORT, --device HOST:PORT, --rect-confirm, or no args for defaults)" >&2
      exit 2
      ;;
  esac
done

# --- Pre-flight: device reachable? --------------------------------------
# picker.py just calls `adb -s X exec-out screencap -p` and surfaces the
# raw adb error if the device isn't registered with the local adb-server.
# That's a hostile UX — the user sees a cryptic "device 'localhost:5555'
# not found" instead of "is BlueStacks running?". Surface the real cause
# here with a checklist the user can hit before any picker runs.
#
# Resolve the device id in EXACTLY the same order picker.py does:
#   1. --device flag (highest priority)
#   2. config.json's device.device_id (mirror picker.py's read_device_id)
#   3. fallback to "localhost:5555"
# Without this mirror, the pre-flight could probe localhost:5555 while
# picker.py reads non-default device from config.json — pre-flight
# fails on a working setup.
#
# Pre-check 1: python3 must be on PATH for the config.json read below.
# picker.py is itself a python3 script so this is normally already
# guaranteed, but surface the missing-python case loudly instead of
# silently producing an empty DEVICE_ID and feeding that poison to
# the pre-flight probe.
if ! command -v python3 > /dev/null 2>&1; then
  cat >&2 <<EOF
FATAL: python3 not found on PATH.

This script needs python3 to read config.json. picker.py is also a
python3 script, so it would fail too with the same root cause.

Install python3 (or fix PATH) and re-run $0.
EOF
  exit 2
fi

DEVICE_ID="localhost:5555"
if [ -n "${DEVICE_FLAG}" ]; then
  DEVICE_ID="${DEVICE_FLAG#--device=}"
elif [ -f "${REPO_ROOT}/config.json" ]; then
  # Mimic picker.py's read_device_id() exactly: parse JSON, silently
  # fall back on any error (missing key, malformed JSON, permission
  # denial). Pass REPO_ROOT via argv rather than interpolating into
  # the Python source — apostrophes in any path component would
  # otherwise raise SyntaxError inside the Python source, and the
  # resulting empty output would silently produce an empty DEVICE_ID
  # that the pre-flight would then echo back in its FATAL block,
  # misleadingly. The Python script is written to a tmp file so its
  # source is read verbatim — no bash source interpolation.
  # Use \$\$-suffixed path; mktemp behavior varies across macOS / Linux
  # coreutils and we don't want to depend on the GNU template form.
  _DTMP="/tmp/picker_device_id.$$.py"
  if ! cat > "${_DTMP}" <<'PYEOF'
import json, os, sys
try:
    with open(os.path.join(sys.argv[1], "config.json")) as f:
        cfg = json.load(f)
        print(cfg.get("device", {}).get("device_id", "localhost:5555"))
except Exception:
    print("localhost:5555")
PYEOF
  then
    echo "pick_upgrade_exit.sh: failed to write tmp extractor script." >&2
    exit 2
  fi
  DEVICE_ID=$(python3 "${_DTMP}" "${REPO_ROOT}" 2>/dev/null)
  rm -f "${_DTMP}"
fi

# First probe — is the device already registered with adb-server?
# `adb get-state` returns "device" iff usable, "offline" / errors-out
# otherwise. We swallow stdout/stderr so we can supply our own message.
if ! adb -s "${DEVICE_ID}" get-state > /dev/null 2>&1; then
  # Try to register fresh — common after BlueStacks was force-killed
  # or the local adb-server was just restarted.
  adb connect "${DEVICE_ID}" > /dev/null 2>&1 || true
  # BlueStacks 5.21+ / macOS can lag 1-3s on the first adb registration
  # after launch (per internal/adb/emulator_mac.go's waitForVMProcess
  # docstring). 2s is enough on healthy runs; raise this if you still
  # see FATAL on devices that work seconds later via manual `adb connect`.
  sleep 2
fi

if ! adb -s "${DEVICE_ID}" get-state > /dev/null 2>&1; then
  cat >&2 <<EOF
FATAL: device ${DEVICE_ID} is not reachable via adb.

Quick checks (run these in another terminal):
  • adb devices                                 # What's actually registered?
  • adb -s ${DEVICE_ID} connect                 # Try to register explicitly
  • adb start-server                            # Restart local adb-server if stale

If you use BlueStacks Air (5.21+) on macOS:
  • Open the BlueStacks Air multi-instance manager.
  • Confirm "Tiramisu64" is in the "Running" state (not "Stopped").
  • qemu-system-aarch64 / hd-adb must be running before adb sees it.

If this device is set in config.json but never seems available, double-check
\`jq .device.device_id config.json\` (or open the file) and confirm the value.

Re-run $0 once \`adb devices\` lists ${DEVICE_ID}.
EOF
  exit 1
fi

# Build picker args for step 1 — Confirm button
CONFIRM_ARGS=()
if [ -n "${DEVICE_FLAG}" ]; then
  CONFIRM_ARGS+=("${DEVICE_FLAG}")
fi
if [ "${RECT_CONFIRM}" -eq 1 ]; then
  # Rect-shaped confirm. Uses inline --rect so the picker writes
  # {confirm_button: {x1,y1,x2,y2}} instead of {x,y}.
  CONFIRM_ARGS+=( -o assets/wall_upgrade_confirm.json
                  --rect confirm_button )
else
  # Default: point. Uses the existing --preset confirm.
  CONFIRM_ARGS+=( --preset confirm )
fi

# Build picker args for step 2 — X-popup ROI
XROI_ARGS=()
if [ -n "${DEVICE_FLAG}" ]; then
  XROI_ARGS+=("${DEVICE_FLAG}")
fi
XROI_ARGS+=( -o assets/wall_upgrade_x_roi.json
             --rect x_popup_roi )

# --- Step 1/2: Confirm button -------------------------------------------
echo ""
echo "============================================================"
echo "  Step 1/2: Pick the post-upgrade Confirm button"
echo "============================================================"
echo ""
echo "In BlueStacks:"
echo "  • Tap gold (or elixir) in the wall-upgrade UI."
echo "  • The post-upgrade Confirm dialog should now be visible."
if [ "${RECT_CONFIRM}" -eq 1 ]; then
  echo "  • You'll drag a small rect around the Confirm button."
else
  echo "  • You'll click the center of the Confirm button."
fi
echo ""
echo "Press Enter here to start the picker; navigate BlueStacks if you need to."
read -r _

./tools/picker.py "${CONFIRM_ARGS[@]}"

# --- Step 2/2: gem-buy popup ROI ----------------------------------------
echo ""
echo "============================================================"
echo "  Step 2/2: Pick the X-button ROI on the gem-buy popup"
echo "============================================================"
echo ""
echo "In BlueStacks:"
echo "  • Tap the gold/elixir upgrade button."
echo "  • Tap Confirm on a wall you CAN'T afford — the 'Buy with"
echo "    Gems' popup with the X in the corner should appear."
echo "  • You'll drag a small rect around where the X button lives"
echo "    on the popup (top-right corner on CoC). The rect defines"
echo "    both the detection area (\"is the popup currently up?\")"
echo "    AND the tap-center for closing the popup."
echo ""
echo "Press Enter here to start the picker; navigate BlueStacks if you need to."
read -r _

./tools/picker.py "${XROI_ARGS[@]}"

# --- Summary ------------------------------------------------------------
echo ""
echo "============================================================"
echo "  Done — both assets are on disk"
echo "============================================================"
echo ""
ls -lh assets/wall_upgrade_confirm.json assets/wall_upgrade_x_roi.json 2>/dev/null || \
  echo "(one or both files are missing — picker was cancelled or errored)"
echo ""
echo "All assets for the wall-upgrade exit flow:"
echo "  • assets/wall_upgrade_buttons.json     (--preset buttons)"
echo "  • assets/wall_upgrade_confirm.json     (just picked — point or rect)"
echo "  • assets/wall_upgrade_x_roi.json       (just picked — rect)"
echo ""
echo "Bot's wall_upgrade.go exit flow once it consumes these:"
echo "  1. tap gold/elixir"
echo "  2. tap confirm-button center (blind)"
echo "  3. if x_popup_roi shows the modal, tap its center to close"
echo "  4. else advance to next resource (gold ↔ elixir)"
