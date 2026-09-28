package vision

import "errors"

// DefaultMinFreeBytes is the free-space floor a recording insists on leaving on
// its volume. 2 GiB is comfortably more than a full default ring (400 frames at
// ~1.4 MB/frame from a 720p emulator is ~560 MB), so crossing it means
// something is writing far more than a review store should ever hold.
const DefaultMinFreeBytes = 2 << 30

// ErrLowDisk is returned instead of writing when the volume holding the
// recording directory is below its free-space floor.
//
// Why this exists: ad-hoc `adb exec-out screencap` shell loops once wrote
// ~120,000 frames (~167 GB) into tmp/ over a weekend, taking this machine's
// disk to 99% and breaking `go build` with "no space left on device" linker
// errors. Frame recording is the one part of this project that can write an
// unbounded amount of data, so the recorder checks headroom itself instead of
// trusting every caller (and every one-off shell loop) to have capped its run.
var ErrLowDisk = errors.New("low disk space: refusing to record another frame")

// DiskFreeBytes reports the space available to the current user on the
// filesystem holding dir. An empty dir means the working directory.
func DiskFreeBytes(dir string) (uint64, error) {
	if dir == "" {
		dir = "."
	}
	return diskFreeBytes(dir)
}
