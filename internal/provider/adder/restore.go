package adder

import (
	"errors"
	"fmt"

	"github.com/theatarr/theatarr/internal/indexer"
)

// A restore puts a torrent from a backup (Theatarr's own or Debrid Media Manager's) back on a debrid account. There is
// no release behind it, only a hash that was on one of the user's accounts, so NewMagnet cannot make its magnet;
// Restore does. Only internal/torrents calls it (boundary_test.go).

// ErrNotRestorable: Restore refused the hash (not a v1 info hash).
var ErrNotRestorable = errors.New("adder: not restorable")

// Restore makes the magnet to re-add hash from a backup: the bare hash plus the policy's public trackers, as for
// NewMagnet.
func Restore(hash string, trackers []string) (Magnet, error) {
	h := indexer.NormalizeHash(hash)
	if h == "" {
		return Magnet{}, fmt.Errorf("%w: not a v1 info hash", ErrNotRestorable)
	}
	if err := checkTrackers(trackers); err != nil {
		return Magnet{}, err
	}
	return Magnet{hash: h, restore: true, trackers: append([]string(nil), trackers...)}, nil
}
