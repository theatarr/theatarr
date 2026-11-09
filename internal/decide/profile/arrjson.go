package profile

// Import and export of the arr API's GET /api/v3/qualityprofile and /api/v3/qualitydefinition bodies
// (Radarr.Api.V3 and Sonarr.Api.V3 QualityProfileResource, QualityProfileQualityItemResource,
// QualityDefinitionResource). Export writes the same shape, so an import can be checked by a round trip.

import (
	"encoding/json"
	"fmt"
	"reflect"

	"github.com/theatarr/theatarr/internal/decide/format"
)

// ProfileJSON is a quality profile in arr API shape.
type ProfileJSON struct {
	ID                    int              `json:"id"`
	Name                  string           `json:"name"`
	UpgradeAllowed        bool             `json:"upgradeAllowed"`
	Cutoff                int              `json:"cutoff"`
	Items                 []ItemJSON       `json:"items"`
	MinFormatScore        int              `json:"minFormatScore"`
	CutoffFormatScore     int              `json:"cutoffFormatScore"`
	MinUpgradeFormatScore int              `json:"minUpgradeFormatScore"`
	RawFormatItems        []formatItemJSON `json:"formatItems"`
	Language              *LanguageJSON    `json:"language,omitempty"` // Radarr only
}

type formatItemJSON struct {
	Format int    `json:"format"`
	Name   string `json:"name"`
	Score  int    `json:"score"`
}

// ItemJSON is one quality or group of a profile.
type ItemJSON struct {
	ID      int          `json:"id,omitempty"`
	Name    string       `json:"name,omitempty"`
	Quality *qualityJSON `json:"quality,omitempty"`
	Items   []ItemJSON   `json:"items"`
	Allowed bool         `json:"allowed"`
}

// LanguageJSON is a LanguageResource.
type LanguageJSON struct {
	ID   int    `json:"id"`
	Name string `json:"name"`
}

// DefinitionJSON is a QualityDefinitionResource.
type DefinitionJSON struct {
	Quality       qualityJSON `json:"quality"`
	Title         string      `json:"title"`
	Weight        int         `json:"weight"`
	MinSize       *float64    `json:"minSize,omitempty"`
	MaxSize       *float64    `json:"maxSize,omitempty"`
	PreferredSize *float64    `json:"preferredSize,omitempty"`
	ID            int         `json:"id"`
}

// ParseArrProfiles reads a GET /api/v3/qualityprofile body in flavour fl.
func ParseArrProfiles(fl format.Flavour, body []byte) ([]*Profile, error) {
	var raw []ProfileJSON
	if err := json.Unmarshal(body, &raw); err != nil {
		return nil, fmt.Errorf("qualityprofile: %w", err)
	}
	out := make([]*Profile, 0, len(raw))
	for i := range raw {
		p, err := raw[i].Build(fl)
		if err != nil {
			return nil, fmt.Errorf("qualityprofile %q: %w", raw[i].Name, err)
		}
		out = append(out, p)
	}
	return out, nil
}

// Build turns one profile's JSON into a Profile.
func (j *ProfileJSON) Build(fl format.Flavour) (*Profile, error) {
	if !fl.Valid() {
		return nil, fmt.Errorf("unknown flavour %q", fl)
	}
	p := &Profile{
		Profile: format.Profile{ID: j.ID, Name: j.Name, Flavour: fl, MinFormatScore: j.MinFormatScore,
			CutoffFormatScore: j.CutoffFormatScore, MinUpgradeFormatScore: j.MinUpgradeFormatScore},
		Cutoff: j.Cutoff, UpgradeAllowed: j.UpgradeAllowed,
	}
	for _, it := range j.RawFormatItems {
		p.FormatItems = append(p.FormatItems, format.FormatItem{Format: it.Format, Name: it.Name, Score: it.Score})
	}
	if j.Language != nil {
		if fl == format.Sonarr {
			return nil, fmt.Errorf("sonarr profile with a language")
		}
		p.Language = j.Language.ID
	}
	var err error
	if p.Items, err = buildItems(fl, j.Items); err != nil {
		return nil, err
	}
	return p, nil
}

func buildItems(fl format.Flavour, js []ItemJSON) ([]Item, error) {
	out := make([]Item, 0, len(js))
	for _, j := range js {
		it := Item{ID: j.ID, Name: j.Name, Allowed: j.Allowed}
		if j.Quality != nil {
			q, err := qualityFromJSON(fl, *j.Quality)
			if err != nil {
				return nil, err
			}
			it.Quality = &q
		}
		if len(j.Items) > 0 {
			sub, err := buildItems(fl, j.Items)
			if err != nil {
				return nil, err
			}
			it.Items = sub
		}
		if it.Quality == nil && len(it.Items) == 0 {
			return nil, fmt.Errorf("item %q has neither a quality nor qualities", j.Name)
		}
		out = append(out, it)
	}
	return out, nil
}

// JSON writes the profile in arr API shape.
func (p *Profile) JSON() ProfileJSON {
	j := ProfileJSON{ID: p.ID, Name: p.Name, UpgradeAllowed: p.UpgradeAllowed, Cutoff: p.Cutoff,
		Items: itemsJSON(p.Flavour, p.Items), MinFormatScore: p.MinFormatScore, CutoffFormatScore: p.CutoffFormatScore,
		MinUpgradeFormatScore: p.MinUpgradeFormatScore, RawFormatItems: []formatItemJSON{}}
	for _, it := range p.FormatItems {
		j.RawFormatItems = append(j.RawFormatItems, formatItemJSON{Format: it.Format, Name: it.Name, Score: it.Score})
	}
	if p.Flavour == format.Radarr {
		j.Language = &LanguageJSON{ID: p.Language, Name: p.Flavour.LanguageName(p.Language)}
	}
	return j
}

func itemsJSON(fl format.Flavour, items []Item) []ItemJSON {
	out := make([]ItemJSON, 0, len(items))
	for _, it := range items {
		j := ItemJSON{ID: it.ID, Name: it.Name, Allowed: it.Allowed, Items: itemsJSON(fl, it.Items)}
		if it.Quality != nil {
			q := qualityToJSON(fl, *it.Quality)
			j.Quality = &q
		}
		out = append(out, j)
	}
	return out
}

// ParseArrDefinitions reads a GET /api/v3/qualitydefinition body in flavour fl. Qualities it does not list keep
// their defaults. A weight that differs from the default is an error: the arr always serves the default weight.
func ParseArrDefinitions(fl format.Flavour, body []byte) (*Definitions, error) {
	var raw []DefinitionJSON
	if err := json.Unmarshal(body, &raw); err != nil {
		return nil, fmt.Errorf("qualitydefinition: %w", err)
	}
	d := DefaultDefinitions(fl)
	for _, r := range raw {
		q, err := qualityFromJSON(fl, r.Quality)
		if err != nil {
			return nil, fmt.Errorf("qualitydefinition %d: %w", r.ID, err)
		}
		if w := Weight(fl, q); w != r.Weight {
			return nil, fmt.Errorf("qualitydefinition %s: weight %d, default %d", q.Name, r.Weight, w)
		}
		d.Set(Definition{ID: r.ID, Quality: q, Title: r.Title, MinSize: r.MinSize, MaxSize: r.MaxSize, PreferredSize: r.PreferredSize})
		d.byID[q.ID].GroupName = defaultDefinition(fl, q.ID).GroupName
	}
	return d, nil
}

// JSON writes the definitions in arr API shape, ordered by weight.
func (d *Definitions) JSON() []DefinitionJSON {
	var out []DefinitionJSON
	for _, def := range d.All() {
		out = append(out, DefinitionJSON{Quality: qualityToJSON(d.Flavour, def.Quality), Title: def.Title,
			Weight: def.Weight, MinSize: def.MinSize, MaxSize: def.MaxSize, PreferredSize: def.PreferredSize, ID: def.ID})
	}
	return out
}

// SameJSON reports whether two JSON documents hold the same values, ignoring key order and number spelling.
// A null value and a missing key are the same (the arrs leave nulls out). When they differ it returns a path to
// the first difference.
func SameJSON(a, b []byte) (bool, string, error) {
	var x, y any
	if err := json.Unmarshal(a, &x); err != nil {
		return false, "", err
	}
	if err := json.Unmarshal(b, &y); err != nil {
		return false, "", err
	}
	path := diffPath("$", x, y)
	return path == "", path, nil
}

func diffPath(at string, x, y any) string {
	switch xv := x.(type) {
	case map[string]any:
		yv, ok := y.(map[string]any)
		if !ok {
			return at
		}
		for k, v := range xv {
			w, ok := yv[k]
			if !ok && v != nil {
				return at + "." + k + " (missing on the right)"
			}
			if p := diffPath(at+"."+k, v, w); p != "" {
				return p
			}
		}
		for k := range yv {
			if _, ok := xv[k]; !ok && yv[k] != nil {
				return at + "." + k + " (missing on the left)"
			}
		}
		return ""
	case []any:
		yv, ok := y.([]any)
		if !ok || len(xv) != len(yv) {
			return at + " (length)"
		}
		for i := range xv {
			if p := diffPath(fmt.Sprintf("%s[%d]", at, i), xv[i], yv[i]); p != "" {
				return p
			}
		}
		return ""
	}
	if !reflect.DeepEqual(x, y) {
		return fmt.Sprintf("%s (%v != %v)", at, x, y)
	}
	return ""
}
