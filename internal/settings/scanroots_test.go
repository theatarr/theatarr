package settings

import (
	"testing"

	"github.com/theatarr/theatarr/internal/policy"
)

// torrents.scan_roots protects other tools' libraries from the unused-torrent report and deletes.
// Dropping or replacing one widens what may be deleted, but sectionEffects reports nothing.
func TestScanRootsNarrowingNeedsConfirmation(t *testing.T) {
	t.Parallel()
	p, err := policy.Load(exampleDir)
	if err != nil {
		t.Fatal(err)
	}
	before, _ := p.Clone()
	before.Torrents = &policy.Torrents{ScanRoots: []string{"/media/arr-movies", "/media/arr-tv"}}
	dropped, _ := p.Clone()
	dropped.Torrents = &policy.Torrents{ScanRoots: []string{"/media/arr-tv"}}
	cleared, _ := p.Clone()
	cleared.Torrents = nil
	moved, _ := p.Clone()
	moved.Torrents = &policy.Torrents{ScanRoots: []string{"/media/elsewhere"}}
	for name, next := range map[string]*policy.Policy{"dropped": dropped, "moved": moved, "cleared": cleared} {
		if e := sectionEffects(before, next); len(e) == 0 {
			t.Fatalf("%s: missing effects %+v", name, e)
		}
	}
}
