package demo_test

import (
	"fmt"
	"os"
	"testing"

	"github.com/theatarr/theatarr/internal/rootprobe/probetest"
)

// TestMain points rootprobe at a copy of the host mount table (every root resolution reads it, and procfs is slow)
// and removes the shared seeded database (seedDemo) at the end.
func TestMain(m *testing.M) {
	cleanup, err := probetest.SnapshotHost()
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
	code := m.Run()
	cleanup()
	if seeded.dir != "" {
		os.RemoveAll(seeded.dir)
	}
	os.Exit(code)
}
