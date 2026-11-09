//go:build !unix

package settings

import "errors"

func rootWritable(string) error { return errors.New("writability is checked on unix only") }
