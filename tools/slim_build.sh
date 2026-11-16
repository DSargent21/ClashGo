#!/usr/bin/env bash
# slim_build.sh — trim the OpenCV surface a RELEASE artifact carries.
#
# ClashGO only calls OpenCV core, imgproc and imgcodecs (Mat, colours, contours,
# template matching, image decode/encode). Everything else gocv exposes —
# highgui, videoio, dnn, calib3d, features2d, objdetect, photo, video, aruco —
# is dead weight in a shipped binary, but it was being shipped anyway:
#
#   - cgo links every wrapper gocv compiles, not only the ones a package
#     reaches, so the executable carried LC_LOAD_DYLIB entries for all of them;
#   - pkg-config's opencv4 Libs line lists every module Homebrew built, so the
#     linker was handed all 57 of them.
#
# tools/bundle_dylibs.sh then copies the load-command closure, which dragged
# ffmpeg, GTK/rsvg, OpenVINO, ICU and OrbbecSDK into Contents/Frameworks:
# 158 MB of dylibs for three modules' worth of work.
#
# This script writes, under build/bin/slim:
#   gocv/              gocv sources minus the unused wrappers (.go/.cpp/.h)
#   go.slim.mod        go.mod + `replace gocv.io/x/gocv => ./gocv`
#   go.slim.sum        copy of go.sum (the -modfile partner)
#   opencv/opencv4.pc  the real .pc with Libs trimmed to the three modules
#
# Only build-cli and build-gui use it. Tests, vet and the dev tools (see, roi
# picker, attack_record — the last one needs highgui's Window) keep the stock
# module, so nothing in the workspace loses an API.
#
# Regenerated whenever go.mod/go.sum change; safe to delete (build/bin is
# cleaned by `make clean`).
set -euo pipefail

ROOT=$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)
OUT="$ROOT/build/bin/slim"

GOCV=$(cd "$ROOT" && go list -m -f '{{.Dir}}' gocv.io/x/gocv 2>/dev/null || true)
if [ -z "$GOCV" ] || [ ! -d "$GOCV" ]; then
  echo "slim_build: gocv module directory not found; run 'go mod download'" >&2
  exit 1
fi

# Module wrappers ClashGO never calls. Each has a .go wrapper and (gocv v0.43+)
# a matching .cpp/.h carrying the C++ bodies; dropping only one half leaves
# undefined symbols at link time.
DROP=(aruco calib3d dnn features2d highgui objdetect photo video videoio)

rm -rf "$OUT/gocv"
mkdir -p "$OUT/gocv"
# Copy sources only: the module cache also carries docs, Dockerfiles, testdata
# and CI metadata that no build step reads.
cp "$GOCV"/*.go "$GOCV"/*.cpp "$GOCV"/*.h "$GOCV"/go.mod "$OUT/gocv"/
cp "$GOCV"/LICENSE "$OUT/gocv"/ 2>/dev/null || true
chmod -R u+w "$OUT/gocv"
rm -f "$OUT/gocv"/*_test.go

for base in "${DROP[@]}"; do
  rm -f "$OUT/gocv/$base.go" "$OUT/gocv/${base}_string.go" \
        "$OUT/gocv/$base.cpp" "$OUT/gocv/$base.h"
done
# Same modules, extra files whose names do not follow the base pattern.
rm -f "$OUT/gocv/aruco_dictionaries.go" "$OUT/gocv/dnn_async_openvino.go" \
      "$OUT/gocv/dnn_ext.go" "$OUT/gocv/highgui_gocv.h"

# go.mod copy plus the local replace. The replace path is absolute because Go
# resolves a relative one against the working directory, not the modfile.
cp "$ROOT/go.mod" "$OUT/go.slim.mod"
printf '\nreplace gocv.io/x/gocv => %s\n' "$OUT/gocv" >> "$OUT/go.slim.mod"
cp "$ROOT/go.sum" "$OUT/go.slim.sum"

# Minimal pkg-config entry: same Cflags/include path as the real opencv4.pc,
# Libs narrowed to the modules the binary actually loads. Cflags stays intact
# because cgo compiles gocv's headers against the same install. The -L prefix
# is carried over from the real file so a Homebrew keg and the dev sdk tree
# both keep resolving; only the -l list is replaced.
mkdir -p "$OUT/opencv"
PC="$(pkg-config --variable=pcfiledir opencv4 2>/dev/null || true)/opencv4.pc"
if [ -f "$PC" ]; then
  awk '
    /^Libs:/ {
      line = $0
      sub(/^Libs:[ \t]*/, "", line)
      prefix = line
      sub(/[ \t]-l.*$/, "", prefix)
      if (prefix == line) prefix = "-L${exec_prefix}/lib"
      print "Libs: " prefix " -lopencv_imgcodecs -lopencv_imgproc -lopencv_core"
      next
    }
    { print }
  ' "$PC" > "$OUT/opencv/opencv4.pc"
else
  echo "slim_build: opencv4.pc not found; module list left untrimmed" >&2
fi

echo "slim_build: pruned gocv + opencv4.pc staged in $OUT"
