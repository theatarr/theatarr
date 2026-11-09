//go:build unix

package library

import (
	"os"
	"syscall"
)

// inode is the file's inode number (log rotation is a new inode), 0 when unknown.
func inode(fi os.FileInfo) uint64 {
	if st, ok := fi.Sys().(*syscall.Stat_t); ok {
		return uint64(st.Ino)
	}
	return 0
}

// owner is the file's uid and gid.
func owner(fi os.FileInfo) (uid, gid int, ok bool) {
	if st, ok := fi.Sys().(*syscall.Stat_t); ok {
		return int(st.Uid), int(st.Gid), true
	}
	return 0, 0, false
}

// reapProcess collects a killed child that has gone, without waiting; false while it is still there.
func reapProcess(p *os.Process) bool {
	var ws syscall.WaitStatus
	pid, err := syscall.Wait4(p.Pid, &ws, syscall.WNOHANG, nil)
	if pid == p.Pid || err == syscall.ECHILD {
		_ = p.Release()
		return true
	}
	return false
}
