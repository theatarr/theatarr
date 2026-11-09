//go:build unix

package health

import "syscall"

// diskFree is the space available to this process (not root's reserve) and the filesystem's size.
func diskFree(dir string) (free, total uint64, err error) {
	var s syscall.Statfs_t
	if err := syscall.Statfs(dir, &s); err != nil {
		return 0, 0, err
	}
	return s.Bavail * uint64(s.Bsize), s.Blocks * uint64(s.Bsize), nil
}
