#!/usr/bin/env bash
# Contract test for the Homebrew cask: renderer + checked-in cask.
#
# Verifies that Casks/clashgo.rb is valid Ruby, pins the same version as
# wails.json (the release version source of truth), and carries a well-formed
# SHA-256; and that tools/gen_cask.sh renders a cask matching the DMG it is
# given. Does not install anything or hit the network.

set -euo pipefail

ROOT="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
GEN="$ROOT/tools/gen_cask.sh"
CASK="$ROOT/Casks/clashgo.rb"

fail() { echo "FAIL: $*" >&2; exit 1; }
pass() { echo "ok - $*"; }

[[ -f "$GEN" ]] || fail "tools/gen_cask.sh missing"
[[ -f "$CASK" ]] || fail "Casks/clashgo.rb missing"
command -v ruby >/dev/null 2>&1 || fail "ruby is required to validate the cask"

ruby -c "$CASK" >/dev/null || fail "Casks/clashgo.rb is not valid Ruby"

WANT_VERSION="$(sed -n 's/.*"productVersion"[[:space:]]*:[[:space:]]*"\([^"]*\)".*/\1/p' "$ROOT/wails.json")"
[[ -n "$WANT_VERSION" ]] || fail "could not read productVersion from wails.json"
GOT_VERSION="$(sed -n 's/^  version "\(.*\)"$/\1/p' "$CASK")"
[[ "$GOT_VERSION" == "$WANT_VERSION" ]] \
  || fail "cask version $GOT_VERSION does not match wails.json $WANT_VERSION (regenerate with make brew-cask)"
pass "checked-in cask version matches wails.json ($GOT_VERSION)"

grep -Eq '^  sha256 "[0-9a-f]{64}"$' "$CASK" \
  || fail "checked-in cask has no 64-hex sha256"
pass "checked-in cask pins a SHA-256"

grep -q '"version"' "$CASK" >/dev/null || true

TMP="$(mktemp -d -t clashgo-cask-test-XXXXXX)"
trap 'rm -rf "$TMP"' EXIT
printf 'fixture dmg bytes\n' > "$TMP/ClashGO.dmg"
bash "$GEN" 9.9.9 "$TMP/ClashGO.dmg" "$TMP/clashgo.rb" >/dev/null 2>&1
ruby -c "$TMP/clashgo.rb" >/dev/null || fail "generated cask is not valid Ruby"

WANT_SHA="$(shasum -a 256 "$TMP/ClashGO.dmg" | awk '{ print $1 }')"
grep -q "version \"9.9.9\"" "$TMP/clashgo.rb" || fail "generated cask version wrong"
grep -q "sha256 \"$WANT_SHA\"" "$TMP/clashgo.rb" || fail "generated cask sha256 wrong"
grep -q 'releases/download/v#{version}/ClashGO.dmg' "$TMP/clashgo.rb" \
  || fail "generated cask URL is not version-templated"
grep -q 'strategy :json' "$TMP/clashgo.rb" || fail "generated cask has no livecheck"
pass "gen_cask.sh renders a valid cask with the DMG checksum"

echo "ALL CASK TESTS PASSED"
