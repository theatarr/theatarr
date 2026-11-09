//go:build unix

package settings

import (
	"os"
	"syscall"
)

func rootWritable(path string) error {
	info, err := os.Stat(path)
	if err != nil {
		return err
	}
	if info.Mode().Perm()&0o222 == 0 {
		return os.ErrPermission
	}
	return syscall.Access(path, 0x2|0x1)
}
