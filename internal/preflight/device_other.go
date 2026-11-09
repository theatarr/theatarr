//go:build !unix

package preflight

import "errors"

// OSDevice is a path's filesystem id; other platforms have none, so the same-filesystem check says "not checked".
func OSDevice(string) (uint64, error) { return 0, errors.New("filesystem ids are read on unix only") }
