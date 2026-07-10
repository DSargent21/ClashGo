#!/usr/bin/env bash
# tools/test_wall_upgrade.sh
#
# Wrapper around `go run ./cmd/test_wall_upgrade` that adds what's missing
# after the wall_upgrade.go refactor:
#
#   1. Rect-asset pre-flight — verifies the three asset-driven-flow
#      dependencies (`assets/wall_upgrade_buttons.json`, `wall_upgrade_confirm.json`,
#      `wall_upgrade_x_roi.json`) are present and contain valid x1/y1/x2/y2 fields.
#      cmd/test_wall_upgrade itself only pre-flights ADB/screen-size/templates,
#      so without this wrapper the user would hit "asset-driven branch skipped"
#      silently in the OnStep trace and not know why no rect-driven events fire.
#
#   2. Post-run bright_px trace — after a `run` invocation, surfaces the
#      `asset_driven_taps_loaded`, `asset_driven_tap_upgrade`,
#      `asset_driven_tap_confirm`, `asset_driven_modal_checked`,
#      `asset_driven_modal_close`, and `asset_driven_advance` events from the
#      freshly-written phase_log.jsonl. The `bright_px` field on
#      `asset_driven_modal_checked` is the live-tuning knob for the
#      RectBrightPixelThreshold=15 inside wall_upgrade.go.
#
# Exit codes:
#   0 — pre-flight + tool invocation completed cleanly
#   1 — rect asset missing/incomplete (refuse to invoke the run)
#   2 — argv precondition not met (unknown mode/flag)
#
# Usage:
#   ./tools/test_wall_upgrade.sh                         # check (default)
#   ./tools/test_wall_upgrade.sh -mode=dry-run           # capture + template match, no clicks
#   ./tools/test_wall_upgrade.sh -mode=run -yes          # full loop, auto-confirm
#   ./tools/test_wall_upgrade.sh -mode=run               # full loop, interactive confirm
#   ./tools/test_wall_upgrade.sh -out=/tmp/wall-test     # override output dir
#   ./tools/test_wall_upgrade.sh -rm-logs                # clear output dir first
#   ./tools/test_wall_upgrade.sh -h                      # show help

set -euo pipefail

# Resolve repo root from script location so this works from any cwd.
SCRIPT_DIR="$(cd -- "$(dirname -- "${BASH_SOURCE[0]}")" && pwd)"
REPO_ROOT="$(cd -- "${SCRIPT_DIR}/.." && pwd)"
cd "${REPO_ROOT}"

# Defaults
MODE="check"
OUT_FLAG=()
AUTO_YES_FLAG=()
RM_LOGS_FLAG=()

usage() {
    sed -n '2,30p' "$0" | sed -e 's/^# \{0,1\}//' -e '/^$/q'
    exit 0
}

# Parse flags
for arg in "$@"; do
    case "$arg" in
        -mode=check)        MODE="check" ;;
        -mode=dry-run)      MODE="dry-run" ;;
        -mode=run)          MODE="run" ;;
        -mode=*)            echo "FATAL: unknown -mode '$(echo "$arg" | cut -d= -f2)'. Must be check|dry-run|run." >&2; exit 2 ;;
        -out=*)             OUT_FLAG=("-out=${arg#-out=}") ;;
        -yes)               AUTO_YES_FLAG=("-yes") ;;
        -rm-logs)           RM_LOGS_FLAG=("-rm-logs") ;;
        -h|--help|-help)    usage ;;
        *)                  echo "FATAL: unknown arg: $arg (try -h)" >&2; exit 2 ;;
    esac
done

# Mode sanity (already constrained above, defensive double-check)
case "${MODE}" in
    check|dry-run|run) ;;
    *) echo "FATAL: invalid mode '${MODE}'." >&2; exit 2 ;;
esac

# Banner
echo
echo "============================================================"
echo "  Wall Upgrade Test Harness"
echo "============================================================"
echo "  Mode:          ${MODE}"
echo "  Repo root:     ${REPO_ROOT}"
echo "  ADB device:    $(adb -s ${DEVICE_ID:-localhost:5555} get-state 2>&1 || echo "not probed")"
echo

# ---------------------------------------------------------------------------
# 1. Rect-asset pre-flight — only meaningful for dry-run and run modes
#    (check doesn't need them, but those modes won't load the rect-driven
#    branch either, so we don't pre-flight them).
# ---------------------------------------------------------------------------
if [ "${MODE}" = "dry-run" ] || [ "${MODE}" = "run" ]; then
    echo "--- Rect-asset pre-flight (asset-driven flow gate) ---"
    missing=0
    for asset in wall_upgrade_buttons.json wall_upgrade_confirm.json wall_upgrade_x_roi.json; do
        path="assets/${asset}"
        if [ ! -f "${path}" ]; then
            echo "  ✗ ${asset} — MISSING"
            echo "      Run ./tools/pick_upgrade_exit.sh first to pick the rects."
            missing=$((missing + 1))
            continue
        fi
        # Quick schema check: must contain all four x1/y1/x2/y2 keys.
        bad=""
        for k in x1 y1 x2 y2; do
            grep -q "\"${k}\"[[:space:]]*:" "${path}" || bad="${bad} ${k}"
        done
        if [ -n "${bad}" ]; then
            echo "  ✗ ${asset} — present but missing keys:${bad}"
            missing=$((missing + 1))
            continue
        fi
        # Print the rects so the user can sanity-check vs their layout.
        if command -v jq >/dev/null 2>&1; then
            bounds="$(jq -c '.. | objects | select(has("x1")) | {x1, y1, x2, y2}' "${path}" 2>/dev/null | tr '\n' ' ')"
        else
            bounds="$(grep -oE '"(x1|y1|x2|y2)"[[:space:]]*:[[:space:]]*[0-9]+' "${path}" | tr '\n' ' ')"
        fi
        echo "  ✓ ${asset}  bounds: ${bounds}"
    done
    echo
    if [ ${missing} -gt 0 ]; then
        echo "FATAL: ${missing} asset(s) missing/incomplete — refusing to run." >&2
        echo "       Re-pick via: ./tools/pick_upgrade_exit.sh --${MODE%-*}-confirm (or just plain)" >&2
        exit 1
    fi
fi

# ---------------------------------------------------------------------------
# 2. Invoke cmd/test_wall_upgrade.
# ---------------------------------------------------------------------------
CMD=(go run ./cmd/test_wall_upgrade "-mode=${MODE}")
if [ ${#OUT_FLAG[@]} -gt 0 ]; then CMD+=("${OUT_FLAG[@]}"); fi
if [ ${#AUTO_YES_FLAG[@]} -gt 0 ]; then CMD+=("${AUTO_YES_FLAG[@]}"); fi
if [ ${#RM_LOGS_FLAG[@]} -gt 0 ]; then CMD+=("${RM_LOGS_FLAG[@]}"); fi

echo "--- Running: ${CMD[*]} ---"
echo
"${CMD[@]}"

# ---------------------------------------------------------------------------
# 3. Post-run asset-driven trace (run mode only — dry-run/check don't fire
#    these events; they don't actually attempt upgrades).
# ---------------------------------------------------------------------------
if [ "${MODE}" = "run" ]; then
    LATEST_LOG="$(ls -td output/wall_upgrade_tests/*/phase_log.jsonl 2>/dev/null | head -1 || true)"
    if [ -z "${LATEST_LOG}" ]; then
        echo
        echo "NOTE: no output/wall_upgrade_tests/*/phase_log.jsonl found — nothing to trace."
        echo "      (the tool should have created one; this is unusual)"
    else
        echo
        echo "============================================================"
        echo "  Asset-Driven Flow Trace"
        echo "  Source: ${LATEST_LOG}"
        echo "============================================================"
        echo "  Focus on these events + bright_px on 'asset_driven_modal_checked' AND 'asset_driven_modal_verified':"
        echo
        grep -E '"step"[[:space:]]*:[[:space:]]*"asset_driven' "${LATEST_LOG}" || echo "  (no asset_driven_ events logged — falls back to probe-and-discard or template flow)"
        echo
        echo "  Interpretation guide (for tuning RectBrightPixelThreshold=15 in wall_upgrade.go):"
        echo "    asset_driven_taps_loaded             - all 3 rects loaded, asset-driven branch active"
        echo "    asset_driven_tap_upgrade             - blind-tap on resource button (gold or elixir)"
        echo "    asset_driven_tap_confirm             - blind-tap on post-upgrade Confirm"
        echo "    asset_driven_modal_checked           - dual-rect check; primary_px/alt_px/all_up identify which popup(s) are up"
        echo "    asset_driven_modal_close_primary     - single tap on primary X (fires only if primary_up=true)"
        echo "    asset_driven_modal_close_alt         - single tap on alt X (fires only if x_popup_roi_alt configured AND alt_up=true)"
        echo "    asset_driven_modal_verified          - post-tap verify; both rects down -> happy; either still up -> close_failed"
        echo "    asset_driven_modal_close_failed      - reason=capture_failed (then both X tapped + abort) OR primary_still_up/alt_still_up"
        echo "    asset_driven_silent_spawn            - no confirm dialog after blind-confirm tap -> affordable upgrade (CoC silent-spawn), advance to next button"
        echo
        echo "  Threshold tuning rule of thumb (uses primary_px / alt_px on modal_checked + modal_verified):"
        echo "    primary_px or alt_px >= 30 with the rect ON the popup -> threshold 15 is comfortably correct (default)."
        echo "    primary_px or alt_px < 15 but you SAW the popup -> bump threshold DOWN (e.g. 8) OR widen the rect in picker."
        echo "    primary_px or alt_px >= 15 on background captures (no popup showing) -> bump threshold UP (e.g. 25) or tighten the rect."
        echo
        echo "  Per-button flow (per the user's 'click X, then click X again in the confirm menu' spec):"
        echo "    - Tap upgrade rect (gold, then elixir) -> tap Confirm rect -> capture once."
        echo "    - If primary_up OR alt_up -> ONE tap on primary center, ONE tap on alt center (no retry, no offsets)."
        echo "    - Verify both rects down. If EITHER still up -> continue to next button (gold -> elixir) per spec."
        echo "      primary_still_up/alt_still_up tell you which picker rect to re-drag (post-run tuning)."
        echo "    - If BOTH buttons fired close_failed -> all_unaffordable exit (sequence ends, log final state)."
        echo "    - If no popup appeared -> silent spawn = success; advance to next wall."
        echo "    - Loop top: if upgrade succeeded, re-enter builder menu and try the next wall."
    fi
fi
