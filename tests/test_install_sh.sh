#!/usr/bin/env bash
# Installer contract test for tools/install.sh.
#
# Builds a throwaway release mirror (latest.json, SHA256SUMS, a CLI tarball and
# a real DMG) on disk, points the installer at it with
# CLASHGO_RELEASE_BASE_URL=file://…, and asserts:
#
#   1. GUI mode copies ClashGO.app into the requested app dir, and a re-run
#      replaces the existing bundle.
#   2. CLI mode extracts the archive; the generated launcher runs (-v) and pins
#      CLASHGO_ASSETS_DIR at the installed asset tree.
#   3. Omitting --version resolves the version from latest.json.
#   4. A CLI update preserves the user's assets/ tree; a broken archive rolls
#      back to the previous working binary.
#   5. A tampered checksum aborts the install before any file is written.
#   6. A release without SHA256SUMS is refused unless --allow-unverified.
#
# hdiutil is macOS-only, so the whole test skips elsewhere.

set -euo pipefail

ROOT="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
INSTALLER="$ROOT/tools/install.sh"
VERSION="9.9.9"

if [[ "$(uname -s)" != "Darwin" ]]; then
  echo "SKIP: tests/test_install_sh.sh requires macOS (hdiutil)"
  exit 0
fi

TMP="$(mktemp -d -t clashgo-install-test-XXXXXX)"
cleanup() {
  for mnt in "$TMP"/mnt*; do
    [[ -d "$mnt" ]] && hdiutil detach "$mnt" >/dev/null 2>&1 || true
  done
  rm -rf "$TMP"
}
trap cleanup EXIT

fail() { echo "FAIL: $*" >&2; exit 1; }
pass() { echo "ok - $*"; }

write_sha256sums() { # write_sha256sums <release-dir> [asset...]
  local dir="$1"; shift
  ( cd "$dir" && shasum -a 256 "$@" > SHA256SUMS )
}

# --- Build a fake release ---------------------------------------------------
make_release() { # make_release <dir> <version>
  local dir="$1" version="$2" cli_stage app_stage dmg_stage

  mkdir -p "$dir"

  # CLI tarball: a shell stand-in for bot_cli with an assets/ tree.
  cli_stage="$TMP/cli-stage-$version"
  mkdir -p "$cli_stage/assets/strategies"
  echo "name: fixture" > "$cli_stage/assets/strategies/fixture.yaml"
  cat > "$cli_stage/bot_cli" <<'CLI'
#!/bin/sh
# Fixture binary: the installer only requires -v to succeed.
case "${1:-}" in
  -v) echo "ClashGO v9.9.9 (commit: fixture)" ;;
  *)  echo "assets=$CLASHGO_ASSETS_DIR" ;;
esac
CLI
  chmod +x "$cli_stage/bot_cli"
  tar -czf "$dir/ClashGO-CLI-v$version-macOS.tar.gz" -C "$cli_stage" .

  # GUI app inside a real DMG, so the mount/copy/detach path is exercised.
  app_stage="$TMP/app-stage-$version"
  mkdir -p "$app_stage/ClashGO.app/Contents/MacOS" \
           "$app_stage/ClashGO.app/Contents/Resources/assets/strategies"
  # Use a real Mach-O and ad-hoc sign the bundle, matching the shipped app so
  # the installer's codesign --verify path is exercised rather than skipped.
  cp /usr/bin/true "$app_stage/ClashGO.app/Contents/MacOS/ClashGO"
  chmod +x "$app_stage/ClashGO.app/Contents/MacOS/ClashGO"
  codesign --force --deep -s - "$app_stage/ClashGO.app" >/dev/null 2>&1 \
    || true
  cat > "$app_stage/ClashGO.app/Contents/Info.plist" <<'PLIST'
<?xml version="1.0" encoding="UTF-8"?>
<!DOCTYPE plist PUBLIC "-//Apple//DTD PLIST 1.0//EN" "http://www.apple.com/DTDs/PropertyList-1.0.dtd">
<plist version="1.0"><dict>
  <key>CFBundleExecutable</key><string>ClashGO</string>
  <key>CFBundleIdentifier</key><string>com.example.clashgo.fixture</string>
  <key>CFBundlePackageType</key><string>APPL</string>
</dict></plist>
PLIST
  echo "name: fixture" > "$app_stage/ClashGO.app/Contents/Resources/assets/strategies/fixture.yaml"
  dmg_stage="$TMP/dmg-stage-$version"
  mkdir -p "$dmg_stage"
  cp -R "$app_stage/ClashGO.app" "$dmg_stage/"
  hdiutil create -quiet -ov -format UDZO -volname "ClashGO Test" \
    -srcfolder "$dmg_stage" "$dir/ClashGO.dmg" >/dev/null
  printf '{"version":"%s"}\n' "$version" > "$dir/latest.json"

  write_sha256sums "$dir" \
    "ClashGO-CLI-v$version-macOS.tar.gz" ClashGO.dmg latest.json
}

# A release whose bot_cli exits non-zero for -v, used to prove rollback.
make_broken_cli_release() { # make_broken_cli_release <dir> <version>
  local dir="$1"
  local version="$2"
  local stage="$TMP/broken-stage-$version"
  mkdir -p "$dir" "$stage/assets/strategies"
  echo "name: broken" > "$stage/assets/strategies/broken.yaml"
  cat > "$stage/bot_cli" <<'CLI'
#!/bin/sh
echo "fixture binary is broken" >&2
exit 1
CLI
  chmod +x "$stage/bot_cli"
  tar -czf "$dir/ClashGO-CLI-v$version-macOS.tar.gz" -C "$stage" .
  printf '{"version":"%s"}\n' "$version" > "$dir/latest.json"
  write_sha256sums "$dir" "ClashGO-CLI-v$version-macOS.tar.gz" latest.json
}

run_installer() { # run_installer <base-url> <args...>
  local base="$1"; shift
  CLASHGO_RELEASE_BASE_URL="$base" bash "$INSTALLER" --version "$VERSION" "$@"
}

# --- 1. GUI install + reinstall --------------------------------------------
REL1="$TMP/release1/v$VERSION"
mkdir -p "$REL1"
make_release "$REL1" "$VERSION"
# The real latest.json lives at releases/latest/download/latest.json, outside
# the versioned asset path; the mirror convention mirrors that split.
printf '{"version":"%s"}\n' "$VERSION" > "$TMP/release1/latest.json"
BASE1="file://$TMP/release1"

APP_DIR="$TMP/Applications"
run_installer "$BASE1" --app-dir "$APP_DIR" >/dev/null
[[ -x "$APP_DIR/ClashGO.app/Contents/MacOS/ClashGO" ]] \
  || fail "GUI install did not place ClashGO.app in $APP_DIR"
marker="$APP_DIR/ClashGO.app/Contents/Resources/stale-marker"
echo "stale" > "$marker"
run_installer "$BASE1" --app-dir "$APP_DIR" >/dev/null
[[ ! -e "$marker" ]] || fail "reinstall did not replace the existing bundle"
pass "GUI DMG installs and replacing an existing bundle works"

# --- 2. CLI install ---------------------------------------------------------
BIN_DIR="$TMP/bin"
DATA_DIR="$TMP/data"
run_installer "$BASE1" --cli --bin-dir "$BIN_DIR" --data-dir "$DATA_DIR" >/dev/null
[[ -x "$BIN_DIR/bot_cli" ]] || fail "CLI install did not create $BIN_DIR/bot_cli"
[[ -x "$DATA_DIR/bot_cli" ]] || fail "CLI binary missing from data dir"
[[ -f "$DATA_DIR/assets/strategies/fixture.yaml" ]] || fail "CLI assets missing"

VERSION_OUT="$("$BIN_DIR/bot_cli" -v)"
[[ "$VERSION_OUT" == *"v$VERSION"* ]] || fail "launcher -v returned: $VERSION_OUT"
ASSETS_OUT="$("$BIN_DIR/bot_cli")"
[[ "$ASSETS_OUT" == "assets=$DATA_DIR/assets" ]] \
  || fail "launcher did not pin assets dir (got: $ASSETS_OUT)"
pass "CLI archive installs and launcher pins assets"

# --- 3. latest.json resolution ---------------------------------------------
LATEST_BIN="$TMP/bin-latest"
LATEST_DATA="$TMP/data-latest"
CLASHGO_RELEASE_BASE_URL="$BASE1" bash "$INSTALLER" --cli \
  --bin-dir "$LATEST_BIN" --data-dir "$LATEST_DATA" >/dev/null
"$LATEST_BIN/bot_cli" -v | grep -q "v$VERSION" \
  || fail "installer without --version did not resolve latest.json"
pass "installer resolves the version from latest.json"

# --- 4. Asset preservation + rollback --------------------------------------
echo "custom" > "$DATA_DIR/assets/strategies/custom.yaml"
run_installer "$BASE1" --cli --bin-dir "$BIN_DIR" --data-dir "$DATA_DIR" >/dev/null
[[ -f "$DATA_DIR/assets/strategies/custom.yaml" ]] \
  || fail "update wiped the user's assets/ tree"
pass "CLI update preserves user-modified assets"

BROKEN="$TMP/broken/v$VERSION"
make_broken_cli_release "$BROKEN" "$VERSION"
if CLASHGO_RELEASE_BASE_URL="file://$TMP/broken" bash "$INSTALLER" \
     --version "$VERSION" --cli --bin-dir "$BIN_DIR" --data-dir "$DATA_DIR" \
     >"$TMP/broken.out" 2>&1; then
  fail "installer accepted a bot_cli that fails -v"
fi
"$BIN_DIR/bot_cli" -v | grep -q "v$VERSION" \
  || fail "rollback did not restore the previous binary"
[[ -f "$DATA_DIR/assets/strategies/custom.yaml" ]] \
  || fail "rollback did not restore the previous assets"
pass "broken CLI archive rolls back to the previous install"

# --- 5. Checksum mismatch aborts -------------------------------------------
REL2="$TMP/release2/v$VERSION"
mkdir -p "$REL2"
make_release "$REL2" "$VERSION"
sed 's/.*ClashGO.dmg$/0000000000000000000000000000000000000000000000000000000000000000  ClashGO.dmg/' \
  "$REL2/SHA256SUMS" > "$REL2/SHA256SUMS.tmp"
mv "$REL2/SHA256SUMS.tmp" "$REL2/SHA256SUMS"

if CLASHGO_RELEASE_BASE_URL="file://$TMP/release2" bash "$INSTALLER" \
     --version "$VERSION" --app-dir "$TMP/Applications-bad" >"$TMP/bad.out" 2>&1; then
  fail "installer accepted a tampered DMG checksum"
fi
grep -q "SHA-256 mismatch" "$TMP/bad.out" || fail "checksum failure did not explain itself"
pass "tampered checksum aborts install"

# --- 6. Missing SHA256SUMS is refused, --allow-unverified overrides --------
rm "$REL2/SHA256SUMS"
if CLASHGO_RELEASE_BASE_URL="file://$TMP/release2" bash "$INSTALLER" \
     --version "$VERSION" --app-dir "$TMP/Applications-bad" >"$TMP/nosums.out" 2>&1; then
  fail "installer installed a release with no SHA256SUMS"
fi
grep -q "does not publish SHA256SUMS" "$TMP/nosums.out" \
  || fail "missing-checksum failure did not explain itself"

CLASHGO_RELEASE_BASE_URL="file://$TMP/release2" bash "$INSTALLER" \
  --version "$VERSION" --allow-unverified --app-dir "$TMP/Applications-unverified" \
  >"$TMP/unverified.out" 2>&1
[[ -d "$TMP/Applications-unverified/ClashGO.app" ]] \
  || fail "--allow-unverified did not install the app"
grep -q "continuing unverified" "$TMP/unverified.out" \
  || fail "--allow-unverified did not warn"
pass "release without SHA256SUMS is refused, --allow-unverified overrides"

echo "ALL INSTALLER TESTS PASSED"
