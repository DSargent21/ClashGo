#!/usr/bin/env bash
# tools/verify.sh — the gate an agent session must pass before a task is "done".
#
#   ./tools/verify.sh          # full: vet + tests + race + build
#   ./tools/verify.sh --fast   # inner loop: vet + tests for changed packages
#
# Every Go test binary links OpenCV. Without the Homebrew/sdk lib dir baked
# into LC_RPATH the test binary aborts at spawn ("Library not loaded:
# libopencv_gapi..."). Same rule as the Makefile's GOTEST_LDFLAGS.
#
# Exits non-zero if any stage fails. Never edit a test to make this pass;
# fix the code or record the failure in PROGRESS.md.

set -uo pipefail
cd "$(dirname "$0")/.."

MODE=full
case "${1:-}" in
  --fast) MODE=fast ;;
  --full | "") MODE=full ;;
  -h | --help)
    sed -n '2,16p' "$0"
    exit 0
    ;;
  *)
    echo "verify: unknown arg: $1" >&2
    exit 2
    ;;
esac

TEST_FLAGS=()
if LIBDIR=$(pkg-config --variable=libdir opencv4 2>/dev/null) && [ -n "$LIBDIR" ]; then
  TEST_FLAGS+=("-ldflags=-extldflags=-Wl,-rpath,$LIBDIR")
fi

# Packages touched by this worktree: modified, staged, and untracked Go files.
changed_packages() {
  local dirs
  # macOS sed/xargs are BSD: no `xargs -r`. awk does dirname and root-dot.
  dirs=$(
    {
      git diff --name-only HEAD -- 2>/dev/null
      git ls-files --others --exclude-standard -- '*.go' 2>/dev/null
    } | grep '\.go$' | awk '{
      if ($0 ~ /\//) { sub(/\/[^\/]*$/, ""); print "./" $0 }
      else { print "." }
    }' | sort -u
  )
  if [ -z "$dirs" ]; then
    echo "./..."
  else
    echo "$dirs"
  fi
}

FAILED=()
run_stage() {
  local name=$1
  shift
  echo ""
  echo "== $name =="
  "$@"
  local status=$?
  if [ $status -ne 0 ]; then
    echo "FAIL: $name (exit $status)"
    FAILED+=("$name")
  fi
}

if [ "$MODE" = fast ]; then
  PKGS=$(changed_packages)
  echo "verify (fast): ${PKGS//$'\n'/ }"
  run_stage "go vet ./..." go vet ./...
  # shellcheck disable=SC2086
  run_stage "go test $PKGS" go test "${TEST_FLAGS[@]}" $PKGS
else
  echo "verify (full): vet + test + race + build"
  run_stage "go vet ./..." go vet ./...
  run_stage "go test ./..." go test "${TEST_FLAGS[@]}" ./...
  run_stage "go test -race ./..." go test -race "${TEST_FLAGS[@]}" ./...
  run_stage "go build ./..." go build ./...
fi

echo ""
if [ ${#FAILED[@]} -ne 0 ]; then
  echo "VERIFY FAILED: ${FAILED[*]}"
  exit 1
fi
echo "VERIFY PASSED ($MODE)"
