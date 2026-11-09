//go:build !unix

package health

import "errors"

func diskFree(string) (uint64, uint64, error) {
	return 0, 0, errors.New("not supported on this platform")
}
