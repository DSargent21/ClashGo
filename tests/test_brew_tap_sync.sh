#!/usr/bin/env bash
# Contract test for tools/sync_homebrew_tap.sh.
#
# Uses a local bare git repo as the "tap" and a scoped git URL rewrite
# (GIT_CONFIG_COUNT env, no global config changes) so the real clone/commit/
# push path runs without GitHub. Asserts the cask lands at Casks/clashgo.rb,
# a re-run is a no-op, and the token never appears in the output.

set -euo pipefail

ROOT="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
SYNC="$ROOT/tools/sync_homebrew_tap.sh"
CASK="$ROOT/Casks/clashgo.rb"
TOKEN="ghp_fixture_token_must_not_leak"

fail() { echo "FAIL: $*" >&2; exit 1; }
pass() { echo "ok - $*"; }

[[ -f "$SYNC" ]] || fail "tools/sync_homebrew_tap.sh missing"
command -v git >/dev/null 2>&1 || fail "git is required"

TMP="$(mktemp -d -t clashgo-tap-sync-test-XXXXXX)"
trap 'rm -rf "$TMP"' EXIT

# Seed a bare tap repo with an older cask.
git init --quiet --bare "$TMP/tap.git"
git clone --quiet "$TMP/tap.git" "$TMP/seed" 2>/dev/null || true
(
  cd "$TMP/seed"
  git symbolic-ref HEAD refs/heads/main
  git config user.name "Seed"
  git config user.email "seed@example.com"
  mkdir -p Casks
  printf 'cask "clashgo" do\n  version "0.0.1"\nend\n' > Casks/clashgo.rb
  git add Casks/clashgo.rb
  git commit --quiet -m "seed cask"
  git push --quiet -u origin main
) || fail "could not seed fixture tap"

run_sync() { # run_sync <logfile>
  GIT_CONFIG_COUNT=1 \
  GIT_CONFIG_KEY_0="url.file://$TMP/tap.git.insteadOf" \
  GIT_CONFIG_VALUE_0="https://github.com/DSargent21/homebrew-tap.git" \
  HOMEBREW_TAP_TOKEN="$TOKEN" \
    bash "$SYNC" "$CASK" DSargent21/homebrew-tap >"$1" 2>&1
}

if ! run_sync "$TMP/sync1.log"; then
  cat "$TMP/sync1.log" >&2
  fail "tap sync failed"
fi
[[ "$(grep -c 'tap updated' "$TMP/sync1.log")" == "1" ]] || fail "no update reported"
if grep -q "$TOKEN" "$TMP/sync1.log"; then
  fail "token leaked into sync output"
fi
pass "tap sync pushes the cask and does not leak the token"

git clone --quiet "$TMP/tap.git" "$TMP/check"
cmp -s "$CASK" "$TMP/check/Casks/clashgo.rb" \
  || fail "tap cask does not match the source cask"
pass "tap contents match Casks/clashgo.rb"

COMMITS_BEFORE="$(git -C "$TMP/check" rev-list --count HEAD)"
if ! run_sync "$TMP/sync2.log"; then
  cat "$TMP/sync2.log" >&2
  fail "second tap sync failed"
fi
grep -q "already at this version" "$TMP/sync2.log" \
  || fail "second sync should be a no-op"
git -C "$TMP/check" fetch --quiet origin
COMMITS_AFTER="$(git -C "$TMP/check" rev-list --count origin/main)"
[[ "$COMMITS_BEFORE" == "$COMMITS_AFTER" ]] \
  || fail "second sync created an unnecessary commit"
pass "re-running the tap sync is idempotent"

echo "ALL TAP SYNC TESTS PASSED"
