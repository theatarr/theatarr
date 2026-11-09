//go:build unix

package preflight

import (
	"errors"
	"os"
	"syscall"
)

// OSDevice is a path's filesystem id (st_dev), for Options.Device.
func OSDevice(path string) (uint64, error) {
	fi, err := os.Stat(path)
	if err != nil {
		return 0, err
	}
	st, ok := fi.Sys().(*syscall.Stat_t)
	if !ok {
		return 0, errors.New("no device id")
	}
	return uint64(st.Dev), nil // Dev is int32 on some platforms
}
