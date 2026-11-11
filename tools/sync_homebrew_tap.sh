#!/usr/bin/env bash
# sync_homebrew_tap.sh — publish the generated cask to a Homebrew tap repo.
#
#   HOMEBREW_TAP_TOKEN=... tools/sync_homebrew_tap.sh <clashgo.rb> [owner/tap-repo]
#
# A Homebrew tap is a separate GitHub repo (default: DSargent21/homebrew-tap)
# whose Casks/clashgo.rb is what `brew install --cask dsargent21/tap/clashgo`
# reads. This script clones it, updates that one file, commits, and pushes. It
# is invoked by the release workflow only when the HOMEBREW_TAP_TOKEN secret is
# configured, so releases still succeed without a tap.
#
# The token needs `contents: write` on the tap repo (a fine-grained PAT). The
# tap repo has to exist already (empty is fine); creating GitHub repos is
# deliberately not automated here.
#
# Credentials never enter a URL, the argv, or the log: git asks via GIT_ASKPASS,
# which reads the token from the environment.

set -euo pipefail

CASK="${1:?usage: sync_homebrew_tap.sh <clashgo.rb> [owner/tap-repo]}"
TAP_REPO="${2:-DSargent21/homebrew-tap}"
TOKEN="${HOMEBREW_TAP_TOKEN:-}"

die() { echo "sync_homebrew_tap: $*" >&2; exit 1; }

[[ -f "$CASK" ]] || die "cask not found: $CASK"
[[ -n "$TOKEN" ]] || die "HOMEBREW_TAP_TOKEN is not set"

WORK="$(mktemp -d -t clashgo-tap-XXXXXX)"
trap 'rm -rf "$WORK"' EXIT

ASKPASS="$WORK/askpass.sh"
cat > "$ASKPASS" <<'EOF'
#!/bin/sh
case "$1" in
  Username*) echo "x-access-token" ;;
  *)         echo "$HOMEBREW_TAP_TOKEN" ;;
esac
EOF
chmod +x "$ASKPASS"

export HOMEBREW_TAP_TOKEN="$TOKEN"
export GIT_ASKPASS="$ASKPASS"
export GIT_TERMINAL_PROMPT=0

git clone --quiet --depth 1 "https://github.com/${TAP_REPO}.git" "$WORK/tap" \
  || die "could not clone ${TAP_REPO} (does the repo exist, and does the token have write access?)"

mkdir -p "$WORK/tap/Casks"
cp "$CASK" "$WORK/tap/Casks/clashgo.rb"

cd "$WORK/tap"
if git diff --quiet -- Casks/clashgo.rb; then
  echo "tap already at this version; nothing to push"
  exit 0
fi

VERSION="$(sed -n 's/^  version "\(.*\)"$/\1/p' Casks/clashgo.rb | head -n 1)"
git add Casks/clashgo.rb
git -c user.name="ClashGO Release" -c user.email="noreply@github.com" \
  commit --quiet -m "clashgo: bump cask to ${VERSION}"
git push --quiet origin HEAD
echo "tap updated: ${TAP_REPO} Casks/clashgo.rb -> ${VERSION}"
