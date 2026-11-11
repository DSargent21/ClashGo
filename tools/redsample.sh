#!/bin/sh
# redsample samples battle frames at max zoom-out so the red no-deploy boundary
# can be inspected with cmd/redprobe.
#
# Zoom matters: the boundary is a world-space ring at the edge of the battle map,
# so at the game's default (close) zoom it is never inside the frame, and a
# detector that "finds" one there is reading terrain. This script waits for a
# battle, pinches out through the same injector the bot uses, and saves what it
# saw next to the mask for that frame.
#
# Usage: tools/redsample.sh [iterations]
set -u
cd "$(dirname "$0")/.." || exit 1

LIMIT=${1:-40}
PINCH='CLASSPATH=/data/local/tmp/ClashGO-zoominject.jar app_process /system/bin ZoomInject pinch 737 384 287 384 532 384 492 384 24 300'

pinch() { ./build/bin/uiact sh "$PINCH" >/dev/null 2>&1; sleep 2; }

i=0
while [ "$i" -lt "$LIMIT" ]; do
	i=$((i + 1))
	./build/bin/redprobe -shot tmp/live/sample.png >/dev/null 2>&1
	state=$(./build/bin/screendump -img tmp/live/sample.png 2>/dev/null |
		sed -n 's/.*state=\([A-Za-z]*\).*/\1/p' | head -1)

	case "$state" in
	Battle*|SearchMap|Attack*|ScoutBase)
		pinch
		pinch
		./build/bin/redprobe -shot tmp/live/sample.png -mask-out tmp/live/sample_mask.png >tmp/live/sample.txt 2>&1
		cp tmp/live/sample.png "tmp/live/red_$i.png"
		echo "$(date +%H:%M:%S) state=$state saved red_$i.png $(grep -E '^mask:' tmp/live/sample.txt) $(grep -E '^verdict:' tmp/live/sample.txt)"
		;;
	*)
		echo "$(date +%H:%M:%S) state=$state"
		;;
	esac
	sleep 3
done
echo "redsample done after $LIMIT samples"
