package library

import (
	"errors"
	"testing"

	"github.com/theatarr/theatarr/internal/policy"
)

// TestMountListedRefusesEmptyListing: the router's ready deadline waits while the mount is
// down. A release root that is there but lists nothing passes MountHealthy (a stat), so the router uses MountListed,
// the listing check relink uses (Mount.checkListing): an empty listing is ErrMountDown, a real one is not.
func TestMountListedRefusesEmptyListing(t *testing.T) {
	t.Parallel()
	e := newEnv(t, true, policy.ModeActive)
	if err := e.lib.MountHealthy(e.ctx); err != nil {
		t.Fatalf("stat check on an empty listing: %v", err)
	}
	if err := e.lib.MountListed(e.ctx); !errors.Is(err, ErrMountDown) {
		t.Fatalf("listing check on an empty listing: %v", err)
	}
	e.release("Some.Release", map[string]float64{"movie.mkv": 7000})
	if err := e.lib.MountListed(e.ctx); err != nil {
		t.Fatalf("listing check with a release: %v", err)
	}
}
