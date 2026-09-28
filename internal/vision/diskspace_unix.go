//go:build !windows

package vision

import "golang.org/x/sys/unix"

// diskFreeBytes is the platform probe behind DiskFreeBytes.
func diskFreeBytes(dir string) (uint64, error) {
	var st unix.Statfs_t
	if err := unix.Statfs(dir, &st); err != nil {
		return 0, err
	}
	// Bavail is the count available to an unprivileged user, which is the
	// number that decides whether the next write actually succeeds.
	return uint64(st.Bavail) * uint64(st.Bsize), nil
}
