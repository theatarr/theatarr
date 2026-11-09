package profile

// Checks of imported arr profiles and definitions: round trip to the arr API shape, and the invariants the arr's
// own validation keeps (every quality listed once, a cutoff that names an allowed item, format items that name
// existing formats).

import (
	"encoding/json"
	"fmt"
	"sort"

	"github.com/theatarr/theatarr/internal/decide/format"
)

// Validate lists what is wrong with a profile: qualities missing or listed twice, a cutoff that is not an allowed
// item, nothing allowed.
func (p *Profile) Validate() []string {
	var probs []string
	count := map[int]int{}
	anyAllowed := false
	for i := range p.Items {
		anyAllowed = anyAllowed || p.Items[i].Allowed
		for _, q := range p.Items[i].Qualities() {
			count[q.ID]++
		}
	}
	for _, q := range Qualities(p.Flavour) {
		switch count[q.ID] {
		case 0:
			probs = append(probs, "quality "+q.Name+" not listed")
		case 1:
		default:
			probs = append(probs, fmt.Sprintf("quality %s listed %d times", q.Name, count[q.ID]))
		}
	}
	if !anyAllowed {
		probs = append(probs, "no allowed quality")
	}
	found := false
	for _, it := range p.Items {
		if (it.Quality != nil && it.Quality.ID == p.Cutoff) || (it.Quality == nil && it.ID == p.Cutoff) {
			found = true
			if !it.Allowed {
				probs = append(probs, fmt.Sprintf("cutoff %d is not allowed", p.Cutoff))
			}
		}
	}
	if !found {
		probs = append(probs, fmt.Sprintf("cutoff %d names no item", p.Cutoff))
	}
	return probs
}

// CutoffItem is the item the cutoff names (Items[GetIndex(Cutoff).Index]).
func (p *Profile) CutoffItem() *Item { return p.item(p.Cutoff) }

// SnapshotReport is the result of checking one instance's profiles, definitions and formats.
type SnapshotReport struct {
	Flavour           format.Flavour
	Profiles          []*Profile
	Definitions       *Definitions
	Formats           []*format.Format
	ProfileRoundTrip  []string // profiles whose export differs from the input, with the first difference
	DefsRoundTrip     string   // first difference of the definitions export, "" when equal
	Problems          []string // Validate results, prefixed by profile name
	MissingFormats    []string // format items naming no format
	FormatsNotInItems []string // formats some profile lacks an item for
}

// CheckSnapshot imports GET qualityprofile, qualitydefinition and customformat bodies and checks them.
func CheckSnapshot(fl format.Flavour, profiles, definitions, formats []byte) (*SnapshotReport, error) {
	r := &SnapshotReport{Flavour: fl}
	var err error
	if r.Profiles, err = ParseArrProfiles(fl, profiles); err != nil {
		return nil, err
	}
	if r.Definitions, err = ParseArrDefinitions(fl, definitions); err != nil {
		return nil, err
	}
	if r.Formats, err = format.ParseArrFormats(fl, formats); err != nil {
		return nil, err
	}
	var rawProfiles []json.RawMessage
	if err := json.Unmarshal(profiles, &rawProfiles); err != nil {
		return nil, err
	}
	for i, p := range r.Profiles {
		out, err := json.Marshal(p.JSON())
		if err != nil {
			return nil, err
		}
		same, path, err := SameJSON(rawProfiles[i], out)
		if err != nil {
			return nil, err
		}
		if !same {
			r.ProfileRoundTrip = append(r.ProfileRoundTrip, p.Name+": "+path)
		}
		for _, prob := range p.Validate() {
			r.Problems = append(r.Problems, p.Name+": "+prob)
		}
	}
	out, err := json.Marshal(r.Definitions.JSON())
	if err != nil {
		return nil, err
	}
	same, path, err := SameJSON(sortedDefs(definitions), sortedDefs(out))
	if err != nil {
		return nil, err
	}
	if !same {
		r.DefsRoundTrip = path
	}
	ids := map[int]bool{}
	for _, f := range r.Formats {
		ids[f.ID] = true
	}
	for _, p := range r.Profiles {
		have := map[int]bool{}
		for _, it := range p.FormatItems {
			have[it.Format] = true
			if !ids[it.Format] {
				r.MissingFormats = append(r.MissingFormats, fmt.Sprintf("%s: format %d %q", p.Name, it.Format, it.Name))
			}
		}
		for _, f := range r.Formats {
			if !have[f.ID] {
				r.FormatsNotInItems = append(r.FormatsNotInItems, fmt.Sprintf("%s: %q", p.Name, f.Name))
			}
		}
	}
	return r, nil
}

// sortedDefs orders a definitions body by quality id, so that bodies listed in different orders compare equal.
func sortedDefs(b []byte) []byte {
	var raw []map[string]any
	if json.Unmarshal(b, &raw) != nil {
		return b
	}
	id := func(m map[string]any) float64 {
		q, _ := m["quality"].(map[string]any)
		v, _ := q["id"].(float64)
		return v
	}
	sort.SliceStable(raw, func(i, j int) bool { return id(raw[i]) < id(raw[j]) })
	out, _ := json.Marshal(raw)
	return out
}
