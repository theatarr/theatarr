package profile

// Port of Radarr src/NzbDrone.Core/Profiles/Qualities/{QualityProfile,QualityProfileQualityItem,QualityIndex}.cs,
// Qualities/QualityModelComparer.cs and QualityProfileService.GetDefaultProfile @ a96bf7c; the Sonarr files are the
// same @ cab419a, except that Sonarr's profile has no Language.

import (
	"strings"

	"github.com/theatarr/theatarr/internal/decide/format"
)

// Profile is a quality profile: the format part (format.Profile, embedded: format items, minimum, cutoff and
// minimum-upgrade format scores) plus the quality part.
type Profile struct {
	format.Profile

	// Items are the allowed and disallowed qualities and groups, lowest first, as the arr API lists them.
	Items []Item
	// Cutoff is a quality id or a group id (groups have ids from 1000).
	Cutoff         int
	UpgradeAllowed bool
	// Language is the wanted language (Radarr only), as a Radarr language id: -1 Any, -2 Original.
	Language int
}

// Item is a QualityProfileQualityItem: one quality, or a group of qualities (Quality nil, ID and Name set).
type Item struct {
	ID      int
	Name    string
	Quality *Quality
	Items   []Item
	Allowed bool
}

// Qualities is GetQualities: the item's quality, or its group's qualities.
func (it *Item) Qualities() []Quality {
	if it.Quality == nil {
		out := make([]Quality, 0, len(it.Items))
		for _, s := range it.Items {
			if s.Quality != nil {
				out = append(out, *s.Quality)
			}
		}
		return out
	}
	return []Quality{*it.Quality}
}

// String is QualityProfileQualityItem.ToString: "Name (q1, q2)" for a named item, else the qualities.
func (it *Item) String() string {
	qs := it.Qualities()
	names := make([]string, len(qs))
	for i, q := range qs {
		names[i] = q.Name
	}
	s := strings.Join(names, ", ")
	if strings.TrimSpace(it.Name) != "" {
		return it.Name + " (" + s + ")"
	}
	return s
}

// QualityIndex is the position of a quality in a profile: the item, and within a group the group position.
type QualityIndex struct {
	Index      int
	GroupIndex int
}

// CompareTo is QualityIndex.CompareTo(right, respectGroupOrder).
func (a QualityIndex) CompareTo(b QualityIndex, respectGroupOrder bool) int {
	c := cmpInt(a.Index, b.Index)
	if respectGroupOrder && c == 0 {
		return cmpInt(a.GroupIndex, b.GroupIndex)
	}
	return c
}

func cmpInt(a, b int) int {
	switch {
	case a < b:
		return -1
	case a > b:
		return 1
	}
	return 0
}

// GetIndex is QualityProfile.GetIndex(id, respectGroupOrder): the first item whose quality or group id is id, or
// a quality inside a group. An id the profile does not list gets index 0, as in the arr.
func (p *Profile) GetIndex(id int, respectGroupOrder bool) QualityIndex {
	for i, it := range p.Items {
		if it.Quality != nil && it.Quality.ID == id {
			return QualityIndex{Index: i}
		}
		if it.ID > 0 && it.ID == id {
			return QualityIndex{Index: i}
		}
		for g, s := range it.Items {
			if s.Quality != nil && s.Quality.ID == id {
				if respectGroupOrder {
					return QualityIndex{Index: i, GroupIndex: g}
				}
				return QualityIndex{Index: i}
			}
		}
	}
	return QualityIndex{}
}

// Item returns the item at GetIndex(id).Index. The arr indexes Items directly, so an empty profile panics there as
// it would throw in the arr.
func (p *Profile) item(id int) *Item { return &p.Items[p.GetIndex(id, false).Index] }

// FirstAllowedQuality is FirststAllowedQuality: the first allowed item's quality, or its group's first quality.
func (p *Profile) FirstAllowedQuality() Quality {
	for _, it := range p.Items {
		if it.Allowed {
			if it.Quality != nil {
				return *it.Quality
			}
			return *it.Items[0].Quality
		}
	}
	panic("profile: no allowed quality") // Enumerable.First throws
}

// LastAllowedQuality is LastAllowedQuality: the last allowed item's quality, or its group's last quality.
func (p *Profile) LastAllowedQuality() Quality {
	for i := len(p.Items) - 1; i >= 0; i-- {
		it := p.Items[i]
		if it.Allowed {
			if it.Quality != nil {
				return *it.Quality
			}
			return *it.Items[len(it.Items)-1].Quality
		}
	}
	panic("profile: no allowed quality")
}

// CompareQualityIDs is QualityModelComparer.Compare(int, int, respectGroupOrder).
func (p *Profile) CompareQualityIDs(left, right int, respectGroupOrder bool) int {
	return p.GetIndex(left, false).CompareTo(p.GetIndex(right, false), respectGroupOrder)
}

// CompareQualities is QualityModelComparer.Compare(Quality, Quality, respectGroupOrder).
func (p *Profile) CompareQualities(left, right Quality, respectGroupOrder bool) int {
	return p.GetIndex(left.ID, respectGroupOrder).CompareTo(p.GetIndex(right.ID, respectGroupOrder), respectGroupOrder)
}

// CompareModels is QualityModelComparer.Compare(QualityModel, QualityModel, respectGroupOrder): the quality, then
// the revision.
func (p *Profile) CompareModels(left, right QualityModel, respectGroupOrder bool) int {
	c := p.CompareQualities(left.Quality, right.Quality, respectGroupOrder)
	if c == 0 {
		c = left.Revision.CompareTo(right.Revision)
	}
	return c
}

// FormatScore is CalculateCustomFormatScore.
func (p *Profile) FormatScore(formats []*format.Format) int { return p.Profile.Score(formats) }

// AllowedQualities lists every allowed quality, lowest first.
func (p *Profile) AllowedQualities() []Quality {
	var out []Quality
	for i := range p.Items {
		if p.Items[i].Allowed {
			out = append(out, p.Items[i].Qualities()...)
		}
	}
	return out
}

// DefaultProfile is QualityProfileService.GetDefaultProfile(name, cutoff, allowed...): every quality, grouped by
// default weight (groups get ids from 1000 and their GroupName), allowed when listed; the cutoff moves to its
// group's id. Language is Original for Radarr; minimum-upgrade format score 1. With formats, each gets a 0-score
// item, as the arr adds every existing format.
func DefaultProfile(fl format.Flavour, name string, cutoff *Quality, allowed []Quality, formats []*format.Format) *Profile {
	isAllowed := func(q Quality) bool {
		for _, a := range allowed {
			if a.ID == q.ID {
				return true
			}
		}
		return false
	}
	profileCutoff := 0 // Quality.Unknown.Id
	if cutoff != nil {
		profileCutoff = cutoff.ID
	}
	// GroupBy(Weight) keeps first-appearance order of the keys and of the elements.
	var weights []int
	groups := map[int][]*Definition{}
	for _, d := range defaultDefs(fl) {
		if _, ok := groups[d.Weight]; !ok {
			weights = append(weights, d.Weight)
		}
		groups[d.Weight] = append(groups[d.Weight], d)
	}
	var items []Item
	groupID := 1000
	for _, w := range weights {
		g := groups[w]
		if len(g) == 1 {
			q := g[0].Quality
			items = append(items, Item{Quality: &q, Allowed: isAllowed(q)})
			continue
		}
		groupAllowed := false
		for _, d := range g {
			groupAllowed = groupAllowed || isAllowed(d.Quality)
		}
		it := Item{ID: groupID, Name: g[0].GroupName, Allowed: groupAllowed}
		for _, d := range g {
			q := d.Quality
			it.Items = append(it.Items, Item{Quality: &q, Allowed: groupAllowed})
			if q.ID == profileCutoff {
				profileCutoff = groupID
			}
		}
		items = append(items, it)
		groupID++
	}
	p := &Profile{
		Profile: format.Profile{Name: name, Flavour: fl, MinUpgradeFormatScore: 1},
		Items:   items,
		Cutoff:  profileCutoff,
	}
	if fl == format.Radarr {
		p.Language = format.LanguageOriginal
	}
	for _, f := range formats {
		p.FormatItems = append(p.FormatItems, format.FormatItem{Format: f.ID, Name: f.Name})
	}
	return p
}
