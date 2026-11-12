//go:build unix

package preflight

import (
	"io/fs"
	"os"
	"syscall"
)

// OSFS is the real filesystem. Writable asks access(2) for W_OK|X_OK as this process, which answers EROFS for a
// read-only bind mount and EACCES for a directory Theatarr's user may not write; nothing is created to find out.
type OSFS struct{}

func (OSFS) Stat(path string) (fs.FileInfo, error) { return os.Stat(path) }

func (OSFS) Writable(path string) error {
	const wOK, xOK = 0x2, 0x1
	return syscall.Access(path, wOK|xOK)
}

func (OSFS) Readable(path string) error {
	const rOK, xOK = 0x4, 0x1
	return syscall.Access(path, rOK|xOK)
}
