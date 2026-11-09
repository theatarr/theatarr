package profile

// Import of TRaSH-Guides quality profiles (the quality part; the format part is format.Trash.Profile) and quality
// sizes (docs/json/{radarr,sonarr}/quality-size). The semantics are recyclarr's, the tool that syncs TRaSH into
// the arrs (recyclarr @ 1a43690):
//   - Profile items: QualityProfilePlanComponent.BuildEffectiveConfig turns the guide's items into the config's
//     qualities; QualityItemOrganizer.OrganizeItems builds the items from the arr's schema (the default profile)
//     with the "top" sort; QualityProfileApiExtensions.ReverseItems reverses them into API order.
//   - Cutoff: UpdatedQualityProfile.BuildMergedProfile, FindCutoff by name among the allowed top-level items, else
//     the first one (FirstCutoffId).
//   - Language (Radarr): matched by name against the arr's languages, ignoring case; kept when unknown.
//   - Sizes: QualitySizeCalculator: max and preferred are capped at the service limits, and values at or above
//     the limit become null (unlimited).

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"

	"github.com/theatarr/theatarr/internal/decide/format"
)

// organizeItems is QualityItemOrganizer.OrganizeItems with the "top" sort, from source items (API order) and the
// guide's items (highest first). The result is in API order (lowest first).
func organizeItems(fl format.Flavour, source []Item, guide []format.TrashQualityItem) ([]Item, []string) {
	src := reverseItems(source) // recyclarr works highest first
	var invalid []string

	findQuality := func(items []Item, name string) *Item {
		for _, it := range flatten(items) {
			if it.Quality != nil && strings.EqualFold(it.Quality.Name, name) {
				return it
			}
		}
		return nil
	}
	findGroup := func(items []Item, name string) *Item {
		for _, it := range flatten(items) {
			if it.Quality == nil && strings.EqualFold(it.Name, name) {
				return it
			}
		}
		return nil
	}

	// GetWantedItems.
	var wanted []Item
	for _, g := range guide {
		if len(g.Items) > 0 {
			group := Item{Name: g.Name}
			if existing := findGroup(src, g.Name); existing != nil {
				group = Item{ID: existing.ID, Name: existing.Name}
			}
			group.Allowed = g.Allowed
			group.Items = nil
			for _, n := range g.Items {
				if q := findQuality(src, n); q != nil {
					c := *q
					c.Allowed = g.Allowed
					group.Items = append(group.Items, c)
				} else {
					invalid = append(invalid, n)
				}
			}
			wanted = append(wanted, group)
			continue
		}
		if q := findQuality(src, g.Name); q != nil {
			c := *q
			c.Allowed = g.Allowed
			wanted = append(wanted, c)
		} else {
			invalid = append(invalid, g.Name)
		}
	}

	// GetUnwantedItems.
	var unwanted []Item
	for _, it := range src {
		var kept []Item
		if it.Quality != nil {
			if findQuality(wanted, it.Quality.Name) == nil {
				kept = []Item{it}
			}
		} else {
			var rest []Item
			for _, s := range it.Items {
				if s.Quality == nil || findQuality(wanted, s.Quality.Name) == nil {
					rest = append(rest, s)
				}
			}
			if findGroup(wanted, it.Name) != nil {
				kept = rest
			} else {
				g := it
				g.Items = nil
				for _, s := range rest {
					s.Allowed = false
					g.Items = append(g.Items, s)
				}
				kept = []Item{g}
			}
		}
		for _, k := range kept {
			k.Allowed = false
			if len(k.Items) == 1 {
				k = k.Items[0]
			}
			if k.Quality == nil && len(k.Items) == 0 {
				continue
			}
			unwanted = append(unwanted, k)
		}
	}

	combined := append(wanted, unwanted...)
	// AssignMissingGroupIds: new groups get ids above max(1000, the largest id in use).
	next := 1000
	for _, it := range flatten(combined) {
		if it.ID > next {
			next = it.ID
		}
	}
	next++
	for i := range combined {
		if combined[i].ID == 0 && combined[i].Quality == nil {
			combined[i].ID = next
			next++
		}
	}
	return reverseItems(combined), invalid
}

// flatten is recyclarr's FlattenItems: every item and nested item, depth first.
func flatten(items []Item) []*Item {
	var out []*Item
	for i := range items {
		out = append(out, &items[i])
		out = append(out, flatten(items[i].Items)...)
	}
	return out
}

// reverseItems is QualityProfileApiExtensions.ReverseItems: top level and groups reversed.
func reverseItems(items []Item) []Item {
	out := make([]Item, len(items))
	for i, it := range items {
		if len(it.Items) > 0 {
			it.Items = reverseItems(it.Items)
		}
		out[len(items)-1-i] = it
	}
	return out
}

// findCutoff is FindCutoff(name) over API-ordered items: recyclarr searches highest first.
func findCutoff(items []Item, name string) (int, bool) {
	if name == "" {
		return 0, false
	}
	for i := len(items) - 1; i >= 0; i-- {
		it := items[i]
		if !it.Allowed {
			continue
		}
		if it.Quality == nil && strings.EqualFold(it.Name, name) {
			return it.ID, true
		}
		if it.Quality != nil && strings.EqualFold(it.Quality.Name, name) {
			return it.Quality.ID, true
		}
	}
	return 0, false
}

func firstCutoff(items []Item) (int, bool) {
	for i := len(items) - 1; i >= 0; i-- {
		it := items[i]
		if !it.Allowed {
			continue
		}
		if it.Quality == nil {
			if it.Name != "" {
				return it.ID, true
			}
			continue
		}
		return it.Quality.ID, true
	}
	return 0, false
}

// TrashSizes are the TRaSH quality-size files of one flavour, by trash_id.
type TrashSizes struct {
	Flavour format.Flavour
	Files   map[string]*TrashSize
}

// TrashSize is one quality-size JSON file.
type TrashSize struct {
	TrashID   string `json:"trash_id"`
	Type      string `json:"type"`
	Qualities []struct {
		Quality   string   `json:"quality"`
		Min       float64  `json:"min"`
		Preferred *float64 `json:"preferred"`
		Max       *float64 `json:"max"`
	} `json:"qualities"`
}

// SizeLimits are recyclarr's QualityItemLimits for arrs with the increased limits (Radarr 5+, Sonarr 4.0.?+):
// max and preferred.
func SizeLimits(fl format.Flavour) (maxLimit, prefLimit float64) {
	if fl == format.Sonarr {
		return 1000, 995
	}
	return 2000, 1999
}

// ByType returns the file of a type ("movie", "series", "anime", "sqp-streaming").
func (ts *TrashSizes) ByType(typ string) (*TrashSize, bool) {
	for _, s := range ts.Files {
		if s.Type == typ {
			return s, true
		}
	}
	return nil, false
}

// Definitions applies a quality-size file to the default definitions as recyclarr would to an arr that has them.
// Qualities the file does not list keep their default sizes.
func (ts *TrashSizes) Definitions(trashID string) (*Definitions, error) {
	s, ok := ts.Files[trashID]
	if !ok {
		return nil, fmt.Errorf("trash: no %s quality-size %s", ts.Flavour, trashID)
	}
	maxLimit, prefLimit := SizeLimits(ts.Flavour)
	d := DefaultDefinitions(ts.Flavour)
	for _, q := range s.Qualities {
		qq, ok := QualityByName(ts.Flavour, q.Quality)
		if !ok {
			return nil, fmt.Errorf("trash quality-size %s: unknown quality %q", s.Type, q.Quality)
		}
		maxV := maxLimit
		if q.Max != nil && *q.Max < maxLimit {
			maxV = *q.Max
		}
		pref := prefLimit
		if q.Preferred != nil && *q.Preferred < prefLimit {
			pref = *q.Preferred
		}
		def := *d.Get(qq)
		min := q.Min
		def.MinSize = &min
		def.MaxSize, def.PreferredSize = nil, nil
		if maxV < maxLimit {
			def.MaxSize = &maxV
		}
		if pref < prefLimit {
			def.PreferredSize = &pref
		}
		d.Set(def)
	}
	return d, nil
}

// LoadTrashSizes reads the quality-size JSON of flavour fl from a TRaSH-Guides checkout at root.
func LoadTrashSizes(root string, fl format.Flavour) (*TrashSizes, error) {
	var meta struct {
		JSONPaths map[string]struct {
			Qualities []string `json:"qualities"`
		} `json:"json_paths"`
	}
	b, err := os.ReadFile(filepath.Join(root, "metadata.json"))
	if err != nil {
		return nil, err
	}
	if err := json.Unmarshal(b, &meta); err != nil {
		return nil, fmt.Errorf("metadata.json: %w", err)
	}
	ts := &TrashSizes{Flavour: fl, Files: map[string]*TrashSize{}}
	for _, d := range meta.JSONPaths[string(fl)].Qualities {
		matches, err := filepath.Glob(filepath.Join(root, d, "*.json"))
		if err != nil {
			return nil, err
		}
		sort.Strings(matches)
		for _, m := range matches {
			b, err := os.ReadFile(m)
			if err != nil {
				return nil, err
			}
			var s TrashSize
			if err := json.Unmarshal(b, &s); err != nil {
				return nil, fmt.Errorf("%s: %w", m, err)
			}
			ts.Files[s.TrashID] = &s
		}
	}
	return ts, nil
}
