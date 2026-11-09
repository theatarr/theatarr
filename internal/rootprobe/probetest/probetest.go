// Package probetest gives tests a mount table of their own for rootprobe.Default.
package probetest

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/theatarr/theatarr/internal/rootprobe"
)

// Mounts makes every one of points a separate mount for rootprobe (each its own device), below one root
// filesystem, until the test ends: a hung check of one counts against that directory alone, as on separate shares.
// Directories under no point share the root filesystem's slot.
func Mounts(t testing.TB, points ...string) {
	t.Helper()
	var b strings.Builder
	b.WriteString("1 0 0:1 / / rw - rootfs rootfs rw\n")
	for i, p := range points {
		fmt.Fprintf(&b, "%d 1 0:%d / %s rw - fake fake rw\n", i+2, i+1000, strings.ReplaceAll(filepath.Clean(p), " ", `\040`))
	}
	path := filepath.Join(t.TempDir(), "mountinfo")
	if err := os.WriteFile(path, []byte(b.String()), 0o600); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(rootprobe.UseMountTable(path))
}

// SnapshotHost copies the host's mount table (/proc/self/mountinfo) to a temporary file and makes rootprobe read
// that copy, for a whole test binary (call it from TestMain and run the returned cleanup after m.Run). Tests mount
// nothing, so the copy matches the live table; reading a plain file is much cheaper than procfs, which every
// root resolution reads again.
func SnapshotHost() (cleanup func(), err error) {
	b, err := os.ReadFile("/proc/self/mountinfo")
	if err != nil {
		return func() {}, nil // no mount table (not Linux): rootprobe reads none either way
	}
	dir, err := os.MkdirTemp("", "probetest-")
	if err != nil {
		return nil, err
	}
	path := filepath.Join(dir, "mountinfo")
	if err := os.WriteFile(path, b, 0o600); err != nil {
		os.RemoveAll(dir)
		return nil, err
	}
	restore := rootprobe.UseMountTable(path)
	return func() { restore(); os.RemoveAll(dir) }, nil
}
