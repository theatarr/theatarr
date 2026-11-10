//go:build unix

package main

import (
	"fmt"
	"os"
	"syscall"
)

// dirOwner is " (owner uid:gid, mode)" for the data directory error, empty when the platform does not say.
func dirOwner(fi os.FileInfo) string {
	if st, ok := fi.Sys().(*syscall.Stat_t); ok {
		return fmt.Sprintf(" (owner %d:%d, mode %04o)", st.Uid, st.Gid, fi.Mode().Perm())
	}
	return ""
}
