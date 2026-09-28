#!/usr/bin/env bash
# bundle_dylibs.sh — make ClashGO.app self-contained.
#
# The GUI links gocv, which links OpenCV and its transitive dependencies. Those
# libraries are NOT part of macOS: the app's LC_LOAD_DYLIB entries point at
# wherever OpenCV happened to live at build time — /opt/homebrew/opt/opencv@4 on
# CI, ~/sdk/cv-env on the dev machine. Homebrew's kegs are worse than an @rpath
# entry: their dylibs carry ABSOLUTE install names, so an rpath cannot redirect
# them and the app dies in dyld before main() on any Mac without that exact
# formula at that exact path:
#
#   dyld: Library not loaded: /opt/homebrew/opt/opencv@4/lib/libopencv_gapi.414.dylib
#
# This script walks the load-command closure of the app's executable, copies
# every non-system dylib into Contents/Frameworks, rewrites every reference
# (in the executable and in the copied dylibs, including the ones the dylibs
# make to each other) to @executable_path/../Frameworks/<name>, re-signs what it
# touched, and then proves no reference to an external path is left. Same idea
# as `dylibbundler` / `macdeployqt`, without adding a Homebrew dependency to the
# build.
#
# Cost: OpenCV@4 pulls 100-200 dylibs (openblas, protobuf, ffmpeg, absl, icu...),
# so Contents/Frameworks is a couple hundred MB and the DMG grows with it. That
# is the price of launching without the user installing anything.
#
# Usage:
#   tools/bundle_dylibs.sh build/bin/ClashGO.app
#
# Runs after stage-app (assets injected, first signature) and before package /
# build-zip, so both artifacts carry the Frameworks dir.

set -euo pipefail

if [[ $# -lt 1 ]]; then
  cat >&2 <<EOF
usage: $(basename "$0") <path/to/ClashGO.app>

  Copies every non-system dylib the app needs into
  <app>/Contents/Frameworks and rewrites the load commands so the bundle no
  longer depends on where OpenCV was installed.
EOF
  exit 64
fi

APP="$1"
BIN="$APP/Contents/MacOS/ClashGO"
FW="$APP/Contents/Frameworks"

log()  { printf "  \033[1;32m\u25b6\033[0m %s\n" "$*" >&2; }
warn() { printf "  \033[1;33m\u26a0\033[0m %s\n" "$*" >&2; }
fail() { printf "  \033[1;31m\u2716\033[0m %s\n" "$*" >&2; exit "${2:-1}"; }

[[ -d "$APP" ]] || fail "app bundle not found: $APP" 66
[[ -x "$BIN" ]] || fail "no executable at $BIN" 66

# Libraries that exist on every macOS install and must never be copied: the
# system ones (libSystem, libc++, CoreFoundation via frameworks are not in
# LC_LOAD_DYLIB as paths we resolve anyway).
is_system() {
  case "$1" in
    /usr/lib/*|/System/*) return 0 ;;
    *) return 1 ;;
  esac
}

# Every LC_LOAD_DYLIB path of a Mach-O, one per line.
#
# `otool -L` output is not a clean list: for a dylib the first entry is the
# library's OWN install name (LC_ID_DYLIB, not a dependency), and a universal
# binary prints one `(architecture <arch>):` header per slice. Both have to go,
# or the walk treats a dylib as depending on itself and tries to resolve a
# header line as a path.
deps_of() {
  otool -L "$1" \
    | tail -n +2 \
    | grep -v '^[[:space:]]*[^[:space:]]* (architecture [^)]*):[[:space:]]*$' \
    | sed 's/ (compatibility[^)]*)//' \
    | sed 's/^[[:space:]]*//; s/[[:space:]]*$//' \
    | grep -v '^$'
}

# Every LC_RPATH of a Mach-O with @loader_path/@executable_path expanded to the
# file's own directory (both expand to the file's dir for our purposes: the
# loader path of a dylib is the dylib, and we only need them to find siblings).
rpaths_of() {
  local file="$1" dir
  dir="$(cd "$(dirname "$file")" && pwd)"
  otool -l "$file" | awk -v d="$dir" '
    /cmd LC_RPATH/ { getline; getline
      sub(/^[[:space:]]*path /, ""); sub(/ \(offset.*/, "")
      gsub(/@loader_path/, d); gsub(/@executable_path/, d); print }'
}

# Every LC_RPATH of a Mach-O exactly as written (no @loader_path expansion),
# for deciding what to delete.
rpaths_raw_of() {
  otool -l "$1" | awk '
    /cmd LC_RPATH/ { getline; getline
      sub(/^[[:space:]]*path /, ""); sub(/ \(offset.*/, ""); print }'
}

# Delete rpaths that point outside the bundle. Every reference is rewritten to
# @executable_path/../Frameworks by now, so a surviving absolute rpath is dead
# weight — and it publishes the path of whatever machine produced the build
# (a Homebrew prefix, a developer's toolchain dir) inside a shipped binary.
strip_external_rpaths() {
  local file="$1" rp
  while IFS= read -r rp; do
    [[ -n "$rp" ]] || continue
    case "$rp" in
      @*|/usr/lib/*|/System/*) continue ;;
    esac
    if install_name_tool -delete_rpath "$rp" "$file" 2>/dev/null; then
      log "removed external rpath $rp from $(basename "$file")"
    else
      warn "could not remove external rpath $rp from $(basename "$file")"
    fi
  done < <(rpaths_raw_of "$file")
}

# Canonicalize a path: one form per file. rpath entries are written both ways
# (`@loader_path/` and `@loader_path`), so the same library resolves to
# "/x/lib//libfoo.dylib" and "/x/lib/libfoo.dylib" — two strings, one file, and
# a string-keyed visited set would bundle it twice and then trip the
# duplicate-basename guard. pwd -P also collapses `..` and any symlinked prefix
# (on CI, /opt/homebrew/opt/opencv@4 -> /opt/homebrew/Cellar/opencv@4/<version>,
# so the real file is the one copied).
canon() {
  local p="$1"
  if [[ -d "$(dirname "$p")" ]]; then
    printf '%s/%s\n' "$(cd "$(dirname "$p")" && pwd -P)" "$(basename "$p")"
  else
    printf '%s\n' "$p"
  fi
}

# Resolve one load-command reference to a file that exists on this machine.
# Prints nothing and returns 1 when it cannot be resolved. A dylib's own
# install-name entry resolves to the file itself, which the callers treat as
# "not a dependency".
resolve_ref() {
  local ref="$1" owner="$2" dir base r
  dir="$(cd "$(dirname "$owner")" && pwd)"
  case "$ref" in
    @rpath/*)
      base="$(basename "$ref")"
      # A dylib referenced through @rpath resolves against the referencing
      # file's rpaths; the bundle's own Frameworks dir is one of them after the
      # first pass, so an already-rewritten dylib is left as it is.
      while IFS= read -r r; do
        [[ -n "$r" && -e "$r/$base" ]] && { canon "$r/$base"; return 0; }
      done < <(rpaths_of "$owner")
      return 1 ;;
    @loader_path/*|@executable_path/*)
      ref="${ref/@loader_path/$dir}"; ref="${ref/@executable_path/$dir}"
      [[ -e "$ref" ]] && { canon "$ref"; return 0; }; return 1 ;;
    /*)
      [[ -e "$ref" ]] && { canon "$ref"; return 0; }; return 1 ;;
    *) return 1 ;;
  esac
}

log "walking load-command closure of $(basename "$BIN")"

# ---- 1. Collect the closure (breadth-first over every non-system dylib). ----
# No associative arrays: /bin/bash on macOS (and on GitHub's runners, unless a
# newer one is installed) is 3.2, where `declare -A` does not exist. The visited
# set is a newline-delimited string instead.
queue=("$BIN")
libs=()
seen=$'\n'
while ((${#queue[@]} > 0)); do
  cur="${queue[0]}"; queue=("${queue[@]:1}")
  while IFS= read -r ref; do
    [[ -n "$ref" ]] || continue
    is_system "$ref" && continue
    if ! p="$(resolve_ref "$ref" "$cur")"; then
      warn "unresolved reference $ref (from $(basename "$cur")) — leaving it alone"
      continue
    fi
    case "$seen" in *$'\n'"$p"$'\n'*) continue ;; esac
    seen="$seen$p"$'\n'
    libs+=("$p")
    queue+=("$p")
  done < <(deps_of "$cur")
done

if ((${#libs[@]} == 0)); then
  log "nothing to bundle: the app links only system libraries"
  exit 0
fi

# Contents/Frameworks is flat: two different libraries that happen to share a
# basename would silently overwrite each other and one of them would then load
# the wrong code. Refuse rather than ship that.
basenames=$'\n'
for p in "${libs[@]}"; do
  base="$(basename "$p")"
  case "$basenames" in
    *$'\n'"$base"$'\n'*)
      fail "two dependencies share the basename $base — a flat Frameworks dir cannot hold both" 70 ;;
  esac
  basenames="$basenames$base"$'\n'
done

# ---- 2. Copy them in. ------------------------------------------------
# The bundler owns Contents/Frameworks: a previous run's copies must not survive
# into this one. They would be re-resolved as dependencies of each other, keep
# their old paths/signatures, and double the bundle.
rm -rf "$FW"
mkdir -p "$FW"

# Dependencies are fat (x86_64 + arm64) more often than the app is. Ship only the
# slices the executable has: on an arm64 build that is several tens of MB of
# never-loaded x86_64 code, and the DMG pays for it.
APP_ARCHS="$(lipo -archs "$BIN" 2>/dev/null || echo arm64)"
total=0
for p in "${libs[@]}"; do
  dest="$FW/$(basename "$p")"
  cp -f "$p" "$dest"
  # cp keeps the source mode; Homebrew's kegs are often read-only, and
  # install_name_tool needs to write in place.
  chmod u+w "$dest"
  if [[ "$(lipo -archs "$p" 2>/dev/null || echo "$APP_ARCHS")" != "$APP_ARCHS" ]]; then
    lipo -thin "${APP_ARCHS%% *}" "$dest" -output "$dest" 2>/dev/null || true
  fi
  total=$((total + $(stat -f%z "$dest" 2>/dev/null || echo 0)))
done
log "copied ${#libs[@]} dylibs ($((total / 1000000)) MB) into Contents/Frameworks"

# ---- 3. Rewrite references. ------------------------------------------
# `install_name_tool` refuses to grow a load command, and a signed Mach-O has no
# slack to grow into; dropping the ad-hoc signature on the copy first frees the
# space and we re-sign at the end. New names are shorter than the ones they
# replace anyway, but this keeps a long Homebrew Cellar path from failing.
change_ref() {
  local file="$1" ref="$2" new="$3"
  if ! install_name_tool -change "$ref" "$new" "$file" 2>/dev/null; then
    codesign --remove-signature "$file" 2>/dev/null || true
    if ! install_name_tool -change "$ref" "$new" "$file" 2>/dev/null; then
      fail "could not rewrite $ref in $(basename "$file")" 70
    fi
  fi
}

rewrite_file() {
  local file="$1" ref p base want self
  self="$(basename "$file")"
  while IFS= read -r ref; do
    [[ -n "$ref" ]] || continue
    is_system "$ref" && continue
    case "$ref" in
      @executable_path/../Frameworks/*) continue ;;
    esac
    base="$(basename "$ref")"
    # A dylib's own LC_ID_DYLIB line is not a dependency; -change cannot
    # rewrite it (install_name_tool errors), and -id handles it separately.
    [[ "$base" == "$self" ]] && continue
    if ! p="$(resolve_ref "$ref" "$file")"; then continue; fi
    base="$(basename "$p")"
    [[ -f "$FW/$base" ]] || continue     # not part of the closure: leave it
    want="@executable_path/../Frameworks/$base"
    [[ "$ref" == "$want" ]] && continue
    change_ref "$file" "$ref" "$want"
  done < <(deps_of "$file")
}

log "rewriting references in the executable"
rewrite_file "$BIN"

for p in "${libs[@]}"; do
  f="$FW/$(basename "$p")"
  install_name_tool -id "@executable_path/../Frameworks/$(basename "$p")" "$f" 2>/dev/null \
    || { codesign --remove-signature "$f" 2>/dev/null || true
         install_name_tool -id "@executable_path/../Frameworks/$(basename "$p")" "$f"; }
  rewrite_file "$f"
done
# Signing is a single pass at the end, not inside the loops: install_name_tool
# invalidates a signature, so signing before the last modification would only be
# undone by it. On arm64 every loaded dylib must be signed to load at all.

# Belt and braces: an @rpath that lands inside the bundle, in case a reference
# written as @rpath/... survives (a future Wails/gocv change could add one).
install_name_tool -add_rpath "@executable_path/../Frameworks" "$BIN" 2>/dev/null \
  || warn "could not add @executable_path/../Frameworks rpath (references are already rewritten)"

strip_external_rpaths "$BIN"
for p in "${libs[@]}"; do strip_external_rpaths "$FW/$(basename "$p")"; done

log "re-signing ${#libs[@]} bundled dylibs"
for p in "${libs[@]}"; do
  f="$FW/$(basename "$p")"
  codesign --force -s - "$f" >/dev/null 2>&1 || warn "re-sign failed for $(basename "$f")"
done

# ---- 4. Re-sign the bundle and prove the job is done. ----------------
log "re-signing bundle (ad-hoc)"
codesign --force --deep -s - "$APP" >/dev/null 2>&1 || warn "codesign --force --deep failed"
codesign --verify --deep --strict "$APP" >/dev/null 2>&1 \
  && log "bundle signature verified" \
  || warn "codesign --verify failed — the shipped app may be flagged"

log "checking for remaining external references"
leftovers=0
for f in "$BIN" "$FW"/*.dylib; do
  self="$(basename "$f")"
  while IFS= read -r ref; do
    [[ -n "$ref" ]] || continue
    is_system "$ref" && continue
    case "$ref" in
      @executable_path/../Frameworks/*) continue ;;
    esac
    [[ "$(basename "$ref")" == "$self" ]] && continue
    warn "$self still references $ref"
    leftovers=$((leftovers + 1))
  done < <(deps_of "$f")
done
((leftovers == 0)) || fail "$leftovers external references survived bundling" 70

log "self-contained: $(du -sh "$FW" | cut -f1) in Frameworks, no external dylib references"
