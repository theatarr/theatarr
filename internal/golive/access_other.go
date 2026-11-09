//go:build !unix

package golive

import (
	"errors"
	"io/fs"
	"os"
)

// OSFS is the real filesystem. Off unix, writability is not checked.
type OSFS struct{}

func (OSFS) Stat(path string) (fs.FileInfo, error) { return os.Stat(path) }

func (OSFS) Writable(string) error { return errors.New("writability is checked on unix only") }

func (OSFS) Readable(path string) error {
	_, err := os.ReadDir(path)
	return err
}
