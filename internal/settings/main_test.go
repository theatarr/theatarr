package settings

import (
	"fmt"
	"os"
	"testing"

	"github.com/theatarr/theatarr/internal/rootprobe/probetest"
)

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
