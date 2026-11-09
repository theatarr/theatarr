//go:build !unix

package library

import "os"

func inode(os.FileInfo) uint64 { return 0 }

func owner(os.FileInfo) (uid, gid int, ok bool) { return 0, 0, false }

// reapProcess leaves the wait to a goroutine: off unix, nothing blocks in the kernel for long.
func reapProcess(p *os.Process) bool {
	go p.Wait() //nolint:errcheck
	return true
}
