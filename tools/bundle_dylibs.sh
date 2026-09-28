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
# every non-system dylib into Contents/Frameworks, rewrites every reference to
# @loader_path form, re-signs what it touched, and then proves the bundle
# actually loads. Same idea as `dylibbundler` / `macdeployqt`, without adding a
# Homebrew dependency to the build.
#
# Why @loader_path and not @executable_path or @rpath:
#   - @executable_path/... only works in load commands the EXECUTABLE owns; a
#     dylib that references a sibling cannot use it (the paths differ per
#     referencing file).
#   - @rpath/<name> works from any file, but only if the referencing file
#     carries an LC_RPATH — and installing one requires `install_name_tool
#     -add_rpath`, which needs headerpad slack in the target binary. You cannot
#     assume that slack inside third-party dylibs: CI run #11 failed with 687
#     surviving references precisely because the rpath adds silently failed.
#   - @loader_path/<name> resolves relative to the file that carries the load
#     command: a dylib's own directory IS Contents/Frameworks, and the
#     executable's is Contents/MacOS, one `..` above it. No LC_RPATH is needed
#     anywhere, and `-change` to these short forms does not depend on slack the
#     way `-add_rpath` does. Whoever wrote the original reference — absolute
#     Homebrew path or @rpath — the rewrite erases the distinction.
#
# Dylibs keep their original install names (LC_ID_DYLIB): nothing references
# them by that spelling after the rewrite, so the name is inert.
#
# Set CLASHGO_BUNDLE_SMOKE=1 to also launch the binary at the end and fail if
# dyld rejects any load command (wired into `make bundle-dylibs`).
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
  # Drop the file's own install name(s) (LC_ID_DYLIB, what `otool -D` prints)
  # from `otool -L` output. Usually the identity's basename equals the file's
  # name and only otool's header needs skipping, but a copy of cv-env's
  # libopenblas under the name libcblas.3.dylib carries the ID
  # `@rpath/libopenblas.0.dylib`, which reads exactly like a dependency of the
  # file — and install_name_tool silently refuses to -change a file's own ID
  # (exit 0, nothing written), so a rewrite pass fed that line would report
  # success while changing nothing. That silent no-op is what survived into
  # CI run #11's verify as "references survived bundling".
  local ids
  # The `|| true` is load-bearing: when the file has no LC_ID (any
  # executable), grep -v selects no lines and exits 1, and under set -e that
  # rc kills this function before the real otool -L below runs — which made
  # every executable look like it linked nothing at all.
  ids="$(otool -D "$1" 2>/dev/null \
    | sed 's/^[^:]*:/ /; s/^[[:space:]]*//; s/[[:space:]]*$//' \
    | grep -v '^$')" || true
  # An executable has no LC_ID_DYLIB, so ids is empty there — and an empty
  # -F pattern matches every line, which would silently drop ALL dependencies.
  # Only apply the filter when there is at least one ID to drop.
  if [[ -n "$ids" ]]; then
    otool -L "$1" \
      | tail -n +2 \
      | grep -v '^[[:space:]]*[^[:space:]]* (architecture [^)]*):[[:space:]]*$' \
      | sed 's/ (compatibility[^)]*)//' \
      | sed 's/^[[:space:]]*//; s/[[:space:]]*$//' \
      | grep -v '^$' \
      | grep -Fvx "$ids"
  else
    otool -L "$1" \
      | tail -n +2 \
      | grep -v '^[[:space:]]*[^[:space:]]* (architecture [^)]*):[[:space:]]*$' \
      | sed 's/ (compatibility[^)]*)//' \
      | sed 's/^[[:space:]]*//; s/[[:space:]]*$//' \
      | grep -v '^$'
  fi
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
# a string-keyed visited set would bundle it twice. pwd -P collapses `..` and
# any symlinked path prefix (on CI, /opt/homebrew/opt/opencv@4 ->
# /opt/homebrew/Cellar/opencv@4/<version>), and the final `readlink -f` also
# collapses a symlinked BASENAME: cv-env ships libcblas.3.dylib ->
# libopenblas.0.dylib in the same directory, and without the basename
# resolution one real library entered the closure under three names (cblas,
# lapack, openblas), got copied three times under three names, and the copies
# whose install name (LC_ID) is @rpath/libopenblas.0.dylib then silently no-op
# every -change attempt (install_name_tool refuses to rewrite a load command
# equal to the file's own install name), which is exactly the CI run #11
# failure shape.
canon() {
  local p="$1"
  # readlink -f resolves every symlink component including the basename, so a
  # symlink alias and its target canonicalize to the same path and bundle once
  # under one name. Falls back to the dir-collapsing form when readlink -f is
  # unavailable (bash 3.2 era coreutils) or the file is absent.
  if rl="$(readlink -f "$p" 2>/dev/null)" && [[ -n "$rl" ]]; then
    printf '%s\n' "$rl"
  elif [[ -d "$(dirname "$p")" ]]; then
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
      # file's rpaths. Also try the owner's own directory (its @loader_path):
      # cv-env's libcblas.3.dylib carries the rpath `@loader_path` and still
      # failed to resolve when the probe hit a path where `-e "$r/$base"` is
      # checked BEFORE the rpath list is read — order the probes so a symlink
      # whose final component is resolved by canon() cannot slip through.
      while IFS= read -r r; do
        [[ -n "$r" && -e "$r/$base" ]] && { canon "$r/$base"; return 0; }
      done < <(rpaths_of "$owner")
      # Sibling next to the owner (equivalent to an implicit @loader_path
      # probe): covers owners whose rpaths point elsewhere, e.g. an rpath
      # written as /x/lib plus the dep sitting in /x/lib.
      [[ -e "$(dirname "$owner")/$base" ]] && { canon "$(dirname "$owner")/$base"; return 0; }
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
    # A dylib's own LC_ID_DYLIB line comes first in `otool -L` output; it is
    # not a dependency of itself.
    [[ "$(basename "$ref")" == "$(basename "$cur")" ]] && continue
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

# ---- 1b. Alias map: every name a reference can use for a bundled file. ---
# References are rewritten to @loader_path/<basename>, so each must map to
# the REAL bundled file's basename. A library that entered the closure only
# through an alias (gocv loads cv-env's libcblas.3.dylib, a symlink to
# libopenblas.0.dylib which is the file actually bundled) is still referenced
# as @rpath/libcblas.3.dylib by other libraries — map alias -> canonical so
# those references resolve inside the bundle too. bash 3.2: string table
# again, entries spelled alias:canonical.
map_lookup() {
  local line
  while IFS= read -r line; do
    [[ "$line" == "$2:"* ]] && { printf '%s\n' "${line#*:}"; return 0; }
  done <<< "$1"
  return 1
}

refmap=$'\n'
for p in "${libs[@]}"; do refmap+="$(basename "$p"):$(basename "$p")"$'\n'; done
dirs=$'\n'
for p in "${libs[@]}"; do
  d="$(dirname "$p")"
  case "$dirs" in *$'\n'"$d"$'\n'*) continue ;; esac
  dirs="$dirs$d"$'\n'
done
while IFS= read -r d; do
  [[ -n "$d" ]] || continue
  while IFS= read -r link; do
    [[ -n "$link" ]] || continue
    t="$(readlink -f "$link" 2>/dev/null)" || continue
    case "$seen" in *$'\n'"$t"$'\n'*) refmap+="$(basename "$link"):$(basename "$t")"$'\n' ;; esac
  done < <(find "$d" -maxdepth 1 -type l 2>/dev/null)
done < <(printf '%s' "$dirs")

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
  local file="$1" kind="$2" ref base want self
  self="$(basename "$file")"
  while IFS= read -r ref; do
    [[ -n "$ref" ]] || continue
    is_system "$ref" && continue
    base="$(basename "$ref")"
    # A dylib's own LC_ID_DYLIB line is not a dependency; -change cannot
    # rewrite it (install_name_tool errors) and nothing loads a dylib by its
    # install name unless some load command spells it out, which the rewrite
    # handles.
    [[ "$base" == "$self" ]] && continue
    canonbase="$(map_lookup "$refmap" "$base")" || continue  # not part of the closure: leave it
    case "$kind" in
      exe) want="@loader_path/../Frameworks/$canonbase" ;;
      *)   want="@loader_path/$canonbase" ;;
    esac
    [[ "$ref" == "$want" ]] && continue
    change_ref "$file" "$ref" "$want"
  done < <(deps_of "$file")
}

log "rewriting references in the executable"
rewrite_file "$BIN" exe

for p in "${libs[@]}"; do
  f="$FW/$(basename "$p")"
  # Normalize the copy's own install name to @loader_path/<name> — the source
  # ID is by definition reachable as a reference (cv-env's openblas ships
  # ID @rpath/libopenblas.0.dylib) and an @rpath ID inside the bundle can
  # only resolve by luck of a foreign rpath.
  codesign --remove-signature "$f" 2>/dev/null || true
  install_name_tool -id "@loader_path/$(basename "$p")" "$f" 2>/dev/null \
    || warn "could not set install name of $(basename "$f")"
  rewrite_file "$f" lib
done
# Signing is a single pass at the end, not inside the loops: install_name_tool
# invalidates a signature, so signing before the last modification would only be
# undone by it. On arm64 every loaded dylib must be signed to load at all.

# Belt and braces: an rpath that lands inside the bundle. Nothing after the
# rewrite needs it (@loader_path covers every reference), so a failure here is
# only noise — which is why it is a warn, not a fail.
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

log "checking every reference resolves inside the bundle"
leftovers=0
for f in "$BIN" "$FW"/*.dylib; do
  self="$(basename "$f")"
  fdir="$(cd "$(dirname "$f")" && pwd)"
  while IFS= read -r ref; do
    [[ -n "$ref" ]] || continue
    is_system "$ref" && continue
    [[ "$(basename "$ref")" == "$self" ]] && continue
    case "$ref" in
      @loader_path/*|@executable_path/*)
        # Expand against THIS file's directory and require the target to exist:
        # a @loader_path/X from a dylib means FW/X, from the executable
        # MacOS/../Frameworks/X.
        t="${ref/@loader_path/$fdir}"; t="${t/@executable_path/$fdir}"
        [[ -e "$t" ]] \
          || { warn "$self references $ref — no such file in the bundle"; leftovers=$((leftovers + 1)); }
        continue ;;
      @rpath/*)
        # Everything we rewrite is @loader_path-based; a surviving @rpath
        # reference can only resolve by luck of a foreign rpath we did not
        # audit. Treat it as a failure.
        warn "$self references $ref via @rpath — should have been rewritten"
        leftovers=$((leftovers + 1)); continue ;;
    esac
    warn "$self still references $ref"
    leftovers=$((leftovers + 1))
  done < <(deps_of "$f")
done
((leftovers == 0)) || fail "$leftovers references do not resolve inside the bundle" 70

# Static checks can pass a bundle that dyld still refuses (a missed rpath, an
# ID collision). With CLASHGO_BUNDLE_SMOKE=1 the caller asks for the real
# thing: run the binary briefly. dyld rejects bad load commands within
# milliseconds, so a couple of seconds of life is enough to see either
# rejection or a live process; the bot never gets far enough to touch ADB or
# the game. The ONLY failure signature is dyld's own rejection text on stderr,
# which is emitted before main() runs.
#
# The process is stopped by an explicit WATCHDOG that kills it, not by a
# signal handed to it. `perl -e 'alarm N; exec ...'` looks equivalent and is
# not: whether SIGALRM ends the process is the target's decision, and the Go
# runtime ignores SIGALRM unless the program registered for it. That bound
# held on a developer machine and did not hold on CI, where the smoke process
# outlived every attempt to signal it — and the job with it — until the
# workflow's own 60-minute timeout (run 36456408588). A watchdog that sends
# SIGKILL and then waits cannot be ignored or outlived.
if [[ "${CLASHGO_BUNDLE_SMOKE:-0}" == "1" ]]; then
  log "smoke: launching the bundled binary to prove dyld resolves everything"
  smoke_err="$(mktemp)"
  # Both streams are redirected to files: a child holding the step's stdout
  # open is how a "finished" smoke check still leaves the CI step hanging.
  "$BIN" >/dev/null 2>"$smoke_err" &
  smoke_pid=$!
  smoke_deadline=$((SECONDS + 5))
  while (( SECONDS < smoke_deadline )) && kill -0 "$smoke_pid" 2>/dev/null; do
    sleep 0.2
  done
  # The binary is a server that runs until it is closed, so it is expected to
  # still be alive here. Stop it for real, and reap it so nothing is left
  # holding the step open.
  kill -TERM "$smoke_pid" 2>/dev/null || true
  sleep 0.5
  kill -KILL "$smoke_pid" 2>/dev/null || true
  wait "$smoke_pid" 2>/dev/null || true
  if grep -q "Library not loaded\|no LC_RPATH\|image not found" "$smoke_err"; then
    sed 's/^/    /' "$smoke_err" >&2
    rm -f "$smoke_err"
    fail "dyld rejected the bundled binary" 70
  fi
  rm -f "$smoke_err"
  log "smoke: loader accepted all load commands"
fi

log "self-contained: $(du -sh "$FW" | cut -f1) in Frameworks, no external dylib references"
