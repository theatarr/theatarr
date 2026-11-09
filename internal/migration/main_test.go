package migration

import (
	"fmt"
	"os"
	"testing"

	"github.com/theatarr/theatarr/internal/rootprobe/probetest"
)

// TestMain points rootprobe at a copy of the host mount table: every root resolution reads it, and procfs is slow.
func TestMain(m *testing.M) {
	cleanup, err := probetest.SnapshotHost()
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
	code := m.Run()
	cleanup()
	os.Exit(code)
}
