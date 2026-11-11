#!/usr/bin/env bash
# install.sh — one-command ClashGO installer for macOS.
#
#   # Desktop GUI -> /Applications/ClashGO.app
#   curl -fsSL https://raw.githubusercontent.com/DSargent21/ClashGo/main/tools/install.sh | bash
#
#   # Headless CLI -> ~/.local/bin/bot_cli
#   curl -fsSL https://raw.githubusercontent.com/DSargent21/ClashGo/main/tools/install.sh | bash -s -- --cli
#
# Downloads the latest GitHub release, verifies every artifact against the
# release's SHA256SUMS, and installs it without sudo:
#
#   GUI mode (default): mounts ClashGO.dmg read-only, copies ClashGO.app to
#   /Applications (or ~/Applications when /Applications is not writable), and
#   strips the quarantine bit so Gatekeeper does not call the ad-hoc signed
#   bundle "damaged".
#
#   CLI mode (--cli): extracts ClashGO-CLI-<version>-macOS.tar.gz into
#   ~/.local/share/clashgo (bot_cli + lib/ + assets/) and writes a launcher at
#   ~/.local/bin/bot_cli that pins CLASHGO_ASSETS_DIR for the shipped asset
#   tree. The archive bundles its OpenCV dylibs, so no Homebrew is required.
#   The new tree is verified before it replaces the old one; a broken archive
#   rolls back to the previous install.
#
# Options (also available as environment variables):
#   --cli                  install the headless CLI instead of the GUI
#   --version <tag>        install a specific release (e.g. v1.0.0)
#   --app-dir <dir>        GUI install location (default /Applications)
#   --bin-dir <dir>        CLI launcher directory (default ~/.local/bin)
#   --data-dir <dir>       CLI data directory (default ~/.local/share/clashgo)
#   --force                replace a user-modified CLI assets/ tree
#   --allow-unverified     install a release that predates SHA256SUMS
#   -h, --help             show this help
#
# Environment: CLASHGO_VERSION (same as --version), XDG_DATA_HOME (default
# base for the CLI data dir), CLASHGO_RELEASE_BASE_URL (test hook: a
# directory/file URL holding <tag>/<asset>, used by tests/test_install_sh.sh).
#
# The checksum file is fetched from the same GitHub release over HTTPS. It
# proves the download was not corrupted or swapped by a mirror; it is not a
# signature over the release itself.
#
# Deliberately requires bash 3.2 (the macOS /bin/bash): no mapfile, no
# associative arrays, no ${var,,}.

set -euo pipefail

REPO="${CLASHGO_REPO:-DSargent21/ClashGo}"
RELEASE_BASE_OVERRIDE="${CLASHGO_RELEASE_BASE_URL:-}"
VERSION_ARG="${CLASHGO_VERSION:-}"
MODE=gui
FORCE=0
ALLOW_UNVERIFIED=0
APP_DIR=""
BIN_DIR=""
DATA_DIR=""
STAGE=""
BACKUP=""

if [[ -t 1 ]]; then
  BOLD="$(printf '\033[1m')"
  GREEN="$(printf '\033[1;32m')"; YELLOW="$(printf '\033[1;33m')"
  RED="$(printf '\033[1;31m')"; RESET="$(printf '\033[0m')"
else
  BOLD=""; GREEN=""; YELLOW=""; RED=""; RESET=""
fi

log()  { printf '%s▸%s %s\n' "$GREEN" "$RESET" "$*"; }
warn() { printf '%s⚠%s %s\n' "$YELLOW" "$RESET" "$*" >&2; }
die()  { printf '%s✖%s %s\n' "$RED" "$RESET" "$*" >&2; exit 1; }

usage() {
  cat <<'EOF'
install.sh — one-command ClashGO installer for macOS

  curl -fsSL https://raw.githubusercontent.com/DSargent21/ClashGo/main/tools/install.sh | bash
  curl -fsSL https://raw.githubusercontent.com/DSargent21/ClashGo/main/tools/install.sh | bash -s -- --cli

Options (also available as environment variables):
  --cli                  install the headless CLI instead of the GUI
  --version <tag>        install a specific release (e.g. v1.0.0)
  --app-dir <dir>        GUI install location (default /Applications)
  --bin-dir <dir>        CLI launcher directory (default ~/.local/bin)
  --data-dir <dir>       CLI data directory (default ~/.local/share/clashgo)
  --force                replace a user-modified CLI assets/ tree
  --allow-unverified     install a release that predates SHA256SUMS
  -h, --help             show this help
EOF
}

while [[ $# -gt 0 ]]; do
  case "$1" in
    --cli)              MODE=cli; shift ;;
    --gui)              MODE=gui; shift ;;
    --version)          VERSION_ARG="${2:?--version needs a value}"; shift 2 ;;
    --app-dir)          APP_DIR="${2:?--app-dir needs a value}"; shift 2 ;;
    --bin-dir)          BIN_DIR="${2:?--bin-dir needs a value}"; shift 2 ;;
    --data-dir)         DATA_DIR="${2:?--data-dir needs a value}"; shift 2 ;;
    --force)            FORCE=1; shift ;;
    --allow-unverified) ALLOW_UNVERIFIED=1; shift ;;
    -h|--help)          usage; exit 0 ;;
    *)                  die "unknown option: $1 (see --help)" ;;
  esac
done

[[ "$(uname -s)" == "Darwin" ]] || die "ClashGO ships macOS builds only; $(uname -s) is not supported."
[[ "$(uname -m)" == "arm64" ]] || warn "this machine is $(uname -m); release builds are Apple Silicon (arm64)"

WORK="$(mktemp -d -t clashgo-install-XXXXXX)"
MOUNTED=""
cleanup() {
  if [[ -n "$MOUNTED" ]]; then
    hdiutil detach "$MOUNTED" >/dev/null 2>&1 || true
  fi
  [[ -n "$STAGE" ]] && rm -rf "$STAGE"
  [[ -n "$BACKUP" ]] && rm -rf "$BACKUP"
  rm -rf "$WORK"
}
trap cleanup EXIT

# --- Release resolution -----------------------------------------------------
# latest.json is already the canonical "what is the newest version" document
# for the in-app updater, so the installer reads the same file instead of
# burning an unauthenticated GitHub API request (60/hr per IP).
manifest_url() {
  if [[ -n "$RELEASE_BASE_OVERRIDE" ]]; then
    printf '%s/latest.json' "${RELEASE_BASE_OVERRIDE%/}"
  else
    printf 'https://github.com/%s/releases/latest/download/latest.json' "$REPO"
  fi
}

fetch() { # fetch <url> <out>
  local url="$1" out="$2"
  if [[ "$url" == https://* ]]; then
    curl -fsSL --proto '=https' --proto-redir '=https' \
      --connect-timeout 15 --retry 3 --retry-delay 1 -o "$out" "$url"
  else
    # file:// mirror used by the installer test.
    curl -fsSL -o "$out" "$url"
  fi
}

if [[ -n "$VERSION_ARG" ]]; then
  VERSION="${VERSION_ARG#v}"
else
  MURL="$(manifest_url)"
  log "Resolving latest release from $MURL"
  fetch "$MURL" "$WORK/latest.json" || die "could not read $MURL"
  VERSION="$(sed -n 's/.*"version"[[:space:]]*:[[:space:]]*"\([^"]*\)".*/\1/p' "$WORK/latest.json" | head -n 1)"
  [[ -n "$VERSION" ]] || die "no \"version\" field in $MURL; pass --version <tag>"
fi
TAG="v${VERSION#v}"
VERSION="${TAG#v}"

if [[ -n "$RELEASE_BASE_OVERRIDE" ]]; then
  RELEASE_URL="${RELEASE_BASE_OVERRIDE%/}/$TAG"
else
  RELEASE_URL="https://github.com/$REPO/releases/download/$TAG"
fi

if [[ "$MODE" == cli ]]; then
  ARTIFACT="ClashGO-CLI-v${VERSION}-macOS.tar.gz"
else
  ARTIFACT="ClashGO.dmg"
fi

# --- Download + verify ------------------------------------------------------
SUMS="$WORK/SHA256SUMS"
VERIFIED=0
if fetch "$RELEASE_URL/SHA256SUMS" "$SUMS" 2>/dev/null; then
  VERIFIED=1
else
  if [[ "$ALLOW_UNVERIFIED" == "1" ]]; then
    warn "release $TAG has no SHA256SUMS — continuing unverified (--allow-unverified)"
  else
    die "release $TAG does not publish SHA256SUMS (it predates this installer). Install from https://github.com/$REPO/releases/tag/$TAG or re-run with --allow-unverified."
  fi
fi

log "Downloading $ARTIFACT ($TAG)"
fetch "$RELEASE_URL/$ARTIFACT" "$WORK/$ARTIFACT" || die "download failed: $RELEASE_URL/$ARTIFACT"

if [[ "$VERIFIED" == "1" ]]; then
  # sha256sum writes "hash *file" in binary mode; shasum writes "hash  file".
  EXPECTED="$(awk -v f="$ARTIFACT" '{ n = $2; sub(/^\*/, "", n); if (n == f) { print $1; exit } }' "$SUMS")"
  [[ -n "$EXPECTED" ]] || die "$ARTIFACT is not listed in the release SHA256SUMS"
  if command -v shasum >/dev/null 2>&1; then
    ACTUAL="$(shasum -a 256 "$WORK/$ARTIFACT" | awk '{ print $1 }')"
  else
    ACTUAL="$(sha256sum "$WORK/$ARTIFACT" | awk '{ print $1 }')"
  fi
  [[ "$ACTUAL" == "$EXPECTED" ]] || die "SHA-256 mismatch for $ARTIFACT (expected $EXPECTED, got $ACTUAL) — refusing to install"
  log "SHA-256 verified"
fi

# --- GUI install ------------------------------------------------------------
install_gui() {
  local app_dir="${APP_DIR:-/Applications}" src dest backup
  if [[ -z "$APP_DIR" && ! -w "/Applications" ]]; then
    app_dir="$HOME/Applications"
  fi
  mkdir -p "$app_dir"

  dest="$app_dir/ClashGO.app"
  # Swapping a bundle out from under a running process leaves the app pointing
  # at unloaded pages ("text file busy" / random crashes). Make the user quit.
  if pgrep -f -- "$dest/Contents/MacOS/" >/dev/null 2>&1; then
    die "ClashGO is running from $dest; quit it and re-run the installer"
  fi

  MOUNTED="$WORK/mnt"
  mkdir -p "$MOUNTED"
  log "Mounting ClashGO.dmg"
  if ! hdiutil attach -nobrowse -readonly -mountpoint "$MOUNTED" "$WORK/ClashGO.dmg" \
       >"$WORK/attach.log" 2>&1; then
    tail -n 20 "$WORK/attach.log" >&2
    die "could not mount the DMG"
  fi
  src="$MOUNTED/ClashGO.app"
  [[ -d "$src" ]] || die "ClashGO.app is missing from the mounted DMG"

  if [[ -e "$dest" ]]; then
    backup="$app_dir/.ClashGO.app.old.$$"
    rm -rf "$backup"
    mv "$dest" "$backup" || die "could not move the existing app aside"
  fi

  log "Installing $dest"
  if ! ditto "$src" "$dest"; then
    if [[ -n "${backup:-}" && -e "$backup" ]]; then mv "$backup" "$dest"; fi
    die "copy failed; the previous installation was restored"
  fi
  [[ -n "${backup:-}" ]] && rm -rf "$backup"

  # The DMG is ad-hoc signed, so the bundle itself is intact; the quarantine
  # bit is what makes Gatekeeper refuse it. Strip it (no sudo needed).
  xattr -dr com.apple.quarantine "$dest" 2>/dev/null || true
  codesign --verify --deep --strict "$dest" >/dev/null 2>&1 \
    || warn "bundle signature check failed; the app may be refused on first launch"

  hdiutil detach "$MOUNTED" >/dev/null 2>&1 || true
  MOUNTED=""

  printf '\n%sClashGO v%s installed.%s\n' "$BOLD" "$VERSION" "$RESET"
  printf '  App:  %s\n' "$dest"
  printf '  Open: open "%s"\n' "$dest"
  printf '  If macOS still blocks the first launch: System Settings → Privacy & Security → Open Anyway.\n'
}

# --- CLI install ------------------------------------------------------------
install_cli() {
  local data_dir="${DATA_DIR:-${XDG_DATA_HOME:-$HOME/.local/share}/clashgo}"
  local bin_dir="${BIN_DIR:-$HOME/.local/bin}"
  local launcher="$bin_dir/bot_cli" version_out kept_assets=0 name

  mkdir -p "$WORK/cli"
  log "Extracting $ARTIFACT"
  tar -xzf "$WORK/$ARTIFACT" -C "$WORK/cli" || die "could not extract $ARTIFACT"
  [[ -f "$WORK/cli/bot_cli" ]] || die "archive does not contain bot_cli"

  mkdir -p "$data_dir" "$bin_dir"
  [[ -w "$bin_dir" ]] || die "cannot write to $bin_dir"

  # A user who edited strategies or calibration pins in the installed tree
  # should not lose them to an update. Fresh installs get the shipped copy.
  if [[ -d "$data_dir/assets" && "$FORCE" != "1" ]]; then
    cp -R "$data_dir/assets" "$WORK/assets.keep"
    kept_assets=1
  fi

  # Stage the whole tree next to the destination (same volume), then swap. The
  # staged binary is verified before the old install is discarded, so a corrupt
  # archive cannot leave the user without a working bot_cli.
  STAGE="$data_dir/.install.$$"
  BACKUP="$data_dir/.backup.$$"
  rm -rf "$STAGE" "$BACKUP"
  mkdir -p "$STAGE" "$BACKUP"
  cp -R "$WORK/cli/." "$STAGE/"
  if [[ "$kept_assets" == "1" ]]; then
    rm -rf "$STAGE/assets"
    mv "$WORK/assets.keep" "$STAGE/assets"
  fi
  chmod +x "$STAGE/bot_cli"

  for name in bot_cli lib assets; do
    if [[ -e "$data_dir/$name" ]]; then mv "$data_dir/$name" "$BACKUP/$name"; fi
  done
  for name in bot_cli lib assets; do
    if [[ -e "$STAGE/$name" ]]; then mv "$STAGE/$name" "$data_dir/$name"; fi
  done
  rm -rf "$STAGE"; STAGE=""

  if ! version_out="$(CLASHGO_ASSETS_DIR="$data_dir/assets" "$data_dir/bot_cli" -v 2>&1)"; then
    printf '%s\n' "$version_out" >&2
    rm -rf "$data_dir/bot_cli" "${data_dir:?}/lib" "$data_dir/assets"
    for name in bot_cli lib assets; do
      if [[ -e "$BACKUP/$name" ]]; then mv "$BACKUP/$name" "$data_dir/$name"; fi
    done
    rm -rf "$BACKUP"; BACKUP=""
    die "installed bot_cli failed to run (-v); the previous installation was restored"
  fi
  rm -rf "$BACKUP"; BACKUP=""

  # The launcher exists because bot_cli resolves assets from the cwd walk-up
  # only. Pinning CLASHGO_ASSETS_DIR is what lets it run from any directory.
  log "Installing launcher $launcher"
  cat > "$launcher" <<EOF
#!/bin/sh
# Generated by ClashGO tools/install.sh. Re-run the installer to update;
# delete this file to uninstall.
CLASHGO_ASSETS_DIR="\${CLASHGO_ASSETS_DIR:-$data_dir/assets}"
export CLASHGO_ASSETS_DIR
exec "$data_dir/bot_cli" "\$@"
EOF
  chmod +x "$launcher"

  if [[ "$kept_assets" == "1" ]]; then
    warn "kept your existing assets/ tree; re-run with --force to take the release copy"
  fi

  printf '\n%sClashGO CLI v%s installed.%s\n' "$BOLD" "$VERSION" "$RESET"
  printf '  Binary: %s\n' "$data_dir/bot_cli"
  printf '  Launch: %s\n' "$launcher"
  printf '  Checks: %s\n' "$version_out"
  case ":$PATH:" in
    *":$bin_dir:"*) ;;
    *)
      printf '\n%s%s is not on your PATH.%s Add it with:\n' "$BOLD" "$bin_dir" "$RESET"
      printf '  export PATH="%s:$PATH"\n' "$bin_dir"
      ;;
  esac
  printf '\nNext: start your emulator, then run  bot_cli -devices  and  bot_cli -perf\n'
}

if [[ "$MODE" == cli ]]; then
  install_cli
else
  install_gui
fi
