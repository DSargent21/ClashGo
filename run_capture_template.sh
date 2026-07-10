#!/usr/bin/env bash
# run_capture_template.sh — manual template-recapture wrapper.
#
# Usage:
#   ./run_capture_template.sh <name>             # terminal prompt (default)
#   ./run_capture_template.sh <name> -drag      # browser-based drag UI
#   ./run_capture_template.sh <name> -device=... # custom device id
#   ./run_capture_template.sh -h                 # help
#
# Examples:
#   ./run_capture_template.sh text_wall
#   ./run_capture_template.sh btn_upgrade_wall -drag -min-conf=0.90
#   ./run_capture_template.sh btn_confirm_upgrade -drag -verbose
#
# Two UX paths exist depending on -drag:
#   default — terminal prompt after capture (corners x1 y1 x2 y2 or center c cx cy w h; q to abort)
#   -drag   — opens a browser window showing the captured preview; drag a rect over the target,
#             click "Save coords ✓", tool reads the rect, crops, verifies, renames. Faster and
#             more precise for textured targets where guessing corner coords is painful.
#
# The wrapper ensures you're in the project root, then forwards all args
# to cmd/capture_template/main.go. The tool itself is documented in
# that source file.
set -euo pipefail

cd "$(dirname "$0")"

if [[ $# -lt 1 ]]; then
  echo "Usage: $0 <template-name> [extra flags...]"
  echo "Try:    $0 text_wall"
  echo "Or:     $0 text_wall -drag      (browser-based drag UI)"
  exit 1
fi

if [[ "$1" == "-h" || "$1" == "--help" ]]; then
  echo "Usage: $0 <template-name> [extra flags...]"
  echo "Captures a single screen, then prompts you for the rect of pixels"
  echo "that bounds the target template region (either via terminal prompt"
  echo "by default, or via browser drag UI when —drag is passed). Writes"
  echo "the cropped region to assets/templates/<name>.png (with a .bak"
  echo "of any prior version), then verifies the new template by running"
  echo "MatchMultiScaleROI against the captured frame and reporting the"
  echo "best confidence. Re-run ./run_test_wall_upgrade.sh to confirm"
  echo "the new template works."
  exit 0
fi

exec go run ./cmd/capture_template -name="$1" "${@:2}"
