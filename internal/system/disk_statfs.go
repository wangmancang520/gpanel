//go:build linux || darwin

package system

import "syscall"

// diskUsage reports the size and space available to unprivileged users of
// the filesystem holding path.
func diskUsage(path string) (total, free uint64, ok bool) {
	var st syscall.Statfs_t
	if err := syscall.Statfs(path, &st); err != nil {
		return 0, 0, false
	}
	bs := uint64(st.Bsize)
	return st.Blocks * bs, st.Bavail * bs, true
}
