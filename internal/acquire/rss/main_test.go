package rss

import (
	"os"
	"testing"
)

// TestMain sets the source keys testdata/policy names once for the process, so tests that build an env (newEnv)
// can run in parallel (t.Setenv refuses parallel tests).
func TestMain(m *testing.M) {
	os.Setenv("THEATARR_TEST_PROWLARR_KEY", "prowlarr-secret-0123456789")
	os.Setenv("THEATARR_TEST_DMM_KEY", "dmm-secret-9876543210")
	os.Exit(m.Run())
}
