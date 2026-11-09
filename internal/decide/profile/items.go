package profile

import (
	"fmt"
	"strings"

	"github.com/theatarr/theatarr/internal/decide/format"
)

// FromItems builds the quality part of a profile from a list of wanted items, highest first, the way FromTrash does
// for a TRaSH profile: the arr's default profile organized with recyclarr's "top" sort, so every quality the list
// leaves out is still listed, not allowed, at its default place. cutoff names an allowed top-level item (a quality
// or a group). The format part is left empty for the caller. Added for the decision engine's policy profiles.
func FromItems(fl format.Flavour, name string, wanted []format.TrashQualityItem, cutoff string, upgradeAllowed bool) (*Profile, error) {
	if !fl.Valid() {
		return nil, fmt.Errorf("unknown flavour %q", fl)
	}
	p := DefaultProfile(fl, name, nil, nil, nil)
	p.UpgradeAllowed = upgradeAllowed
	items, invalid := organizeItems(fl, p.Items, wanted)
	if len(invalid) > 0 {
		return nil, fmt.Errorf("unknown %s qualities %s", fl, strings.Join(invalid, ", "))
	}
	p.Items = items
	cut, ok := findCutoff(p.Items, cutoff)
	if !ok {
		return nil, fmt.Errorf("cutoff %q is not an allowed quality or group", cutoff)
	}
	p.Cutoff = cut
	return p, nil
}
