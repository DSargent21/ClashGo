#!/usr/bin/env bash
# run_test_wall_upgrade.sh — convenience wrapper for the wall-upgrade
# manual verification tool at cmd/test_wall_upgrade.
#
# Modes:
#   check     prerequisites only (no clicks)
#   dry-run   capture + template verification only (no clicks)
#   run       prompts before invoking the live wall-upgrade loop
#
# Examples:
#   ./run_test_wall_upgrade.sh                # check (default)
#   ./run_test_wall_upgrade.sh dry-run        # dry-run on current screen
#   ./run_test_wall_upgrade.sh run -yes      # live loop, skip prompt
#   ./run_test_wall_upgrade.sh run --device=emulator-5554
#
# Outputs (per run, dry-run or run):
#   output/wall_upgrade_tests/<timestamp>/
#     ├── 00_dryrun_initial.png
#     ├── dryrun_match_<template>.png
#     ├── NN_<step>[_overlay].png     # per-phase raw/annotated
#     ├── phase_log.jsonl             # jsonl of every OnStep event
#     └── summary.json                # device + elapsed + counts
set -euo pipefail

cd "$(dirname "$0")"

MODE="${1:-check}"
shift || true

case "$MODE" in
  check|dry-run|run|-h|--help|"")
    ;;
  *)
    echo "Unknown mode: $MODE"
    echo "Usage: $0 [check|dry-run|run] [extra args to go run ./cmd/test_wall_upgrade]"
    exit 1
    ;;
esac

if [[ "$MODE" == "-h" || "$MODE" == "--help" || "$MODE" == "" ]]; then
  cat <<'EOF'
Usage: ./run_test_wall_upgrade.sh [mode] [flags]

Modes:
  check      prerequisites only — device, templates, calibration
  dry-run    + captures current screen and matches required templates
  run        + prompts before invoking the live wall-upgrade loop

Output (for dry-run / run):
  output/wall_upgrade_tests/<timestamp>/
    00_dryrun_initial.png
    dryrun_match_<template>.png        (one per required template)
    NN_<step>.png + NN_<step>_overlay.png  (per-phase boundary)
    phase_log.jsonl                     (every OnStep event)
    summary.json                        (run summary)

Examples:
  ./run_test_wall_upgrade.sh
  ./run_test_wall_upgrade.sh dry-run
  ./run_test_wall_upgrade.sh run -yes
  ./run_test_wall_upgrade.sh run --device=emulator-5554
EOF
  exit 0
fi

exec go run ./cmd/test_wall_upgrade -mode="$MODE" "$@"
