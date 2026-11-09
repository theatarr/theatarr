package profile

import (
	"fmt"
	"github.com/theatarr/theatarr/internal/decide/format"
	"strings"
)

// FromTrash builds TRaSH quality profile trashID as recyclarr would create it in an empty arr: the quality items,
// cutoff, upgrade flag and language, plus the format part from format.Trash.Profile (opts as there).
func FromTrash(t *format.Trash, trashID string, opts format.ProfileOptions) (*Profile, []*format.Format, error) {
	fp, formats, err := t.Profile(trashID, opts)
	if err != nil {
		return nil, nil, err
	}
	tp := t.Profiles[trashID]
	fl := t.Flavour
	p := DefaultProfile(fl, tp.Name, nil, nil, nil)
	p.Profile = *fp
	if tp.UpgradeAllowed != nil {
		p.UpgradeAllowed = *tp.UpgradeAllowed
	}
	if len(tp.Items) > 0 {
		items, invalid := organizeItems(fl, p.Items, tp.Items)
		if len(invalid) > 0 {
			return nil, nil, fmt.Errorf("trash profile %q: unknown qualities %s", tp.Name, strings.Join(invalid, ", "))
		}
		p.Items = items
	}
	// Cutoff: the guide's by name among allowed top-level items, else the first allowed one, highest first.
	cut, ok := findCutoff(p.Items, tp.Cutoff)
	if !ok {
		cut, ok = firstCutoff(p.Items)
	}
	if !ok {
		return nil, nil, fmt.Errorf("trash profile %q: no allowed quality for a cutoff", tp.Name)
	}
	p.Cutoff = cut
	if fl == format.Radarr && tp.Language != "" {
		if id, ok := fl.LanguageID(tp.Language); ok {
			p.Language = id
		}
	}
	return p, formats, nil
}
