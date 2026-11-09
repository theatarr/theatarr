package format

// Import of TRaSH-Guides JSON (docs/json/{radarr,sonarr}/{cf,quality-profiles,cf-groups}, found through
// metadata.json). Score resolution and default-group inclusion follow recyclarr
// (QualityProfilePlanComponent.cs, ConfiguredCustomFormatProvider.cs), the tool that syncs TRaSH into the arrs.

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
)

// Trash is the TRaSH custom formats, quality profiles and format groups of one flavour.
type Trash struct {
	Flavour  Flavour
	Formats  []*Format // ordered by trash_id; IDs are 1..n in that order
	byID     map[string]*Format
	raw      map[string][]byte        // the JSON file of each format, by trash_id
	Profiles map[string]*TrashProfile // by trash_id
	Groups   map[string]*TrashGroup   // by trash_id
}

// TrashProfile is a TRaSH quality-profile JSON, format part. FormatItems maps format name to trash_id.
type TrashProfile struct {
	TrashID               string            `json:"trash_id"`
	Name                  string            `json:"name"`
	Description           string            `json:"trash_description"`
	Group                 int               `json:"group"`
	TrashScoreSet         string            `json:"trash_score_set"`
	MinFormatScore        int               `json:"minFormatScore"`
	CutoffFormatScore     int               `json:"cutoffFormatScore"`
	MinUpgradeFormatScore int               `json:"minUpgradeFormatScore"`
	Language              string            `json:"language"`
	FormatItems           map[string]string `json:"formatItems"`

	// The quality part, read by the profile package: items highest first, as TRaSH and recyclarr list them.
	UpgradeAllowed         *bool              `json:"upgradeAllowed"`
	Cutoff                 string             `json:"cutoff"`
	Items                  []TrashQualityItem `json:"items"`
	TrashQualityDefinition map[string]string  `json:"trash_quality_definition"`
}

// TrashQualityItem is one entry of a TRaSH profile's items: a quality, or a group when Items is set.
type TrashQualityItem struct {
	Name    string   `json:"name"`
	Allowed bool     `json:"allowed"`
	Items   []string `json:"items"`
}

// TrashGroup is a TRaSH cf-group JSON.
type TrashGroup struct {
	TrashID       string          `json:"trash_id"`
	Name          string          `json:"name"`
	Default       json.RawMessage `json:"default"`
	CustomFormats []struct {
		Name     string `json:"name"`
		TrashID  string `json:"trash_id"`
		Required bool   `json:"required"`
		Default  bool   `json:"default"`
	} `json:"custom_formats"`
	QualityProfiles struct {
		Include map[string]string `json:"include"`
		Exclude map[string]string `json:"exclude"`
	} `json:"quality_profiles"`
}

// IsDefault reports the group's "default" flag, which TRaSH writes as the string "true".
func (g *TrashGroup) IsDefault() bool {
	s := strings.Trim(strings.TrimSpace(string(g.Default)), `"`)
	return strings.EqualFold(s, "true")
}

// LoadTrash reads the TRaSH JSON of flavour fl from a TRaSH-Guides checkout at root.
func LoadTrash(root string, fl Flavour) (*Trash, error) {
	if !fl.Valid() {
		return nil, fmt.Errorf("trash: unknown flavour %q", fl)
	}
	var meta struct {
		JSONPaths map[string]struct {
			CustomFormats      []string `json:"custom_formats"`
			QualityProfiles    []string `json:"quality_profiles"`
			CustomFormatGroups []string `json:"custom_format_groups"`
		} `json:"json_paths"`
	}
	if err := readJSON(filepath.Join(root, "metadata.json"), &meta); err != nil {
		return nil, err
	}
	paths, ok := meta.JSONPaths[string(fl)]
	if !ok {
		return nil, fmt.Errorf("trash: metadata.json has no %s paths", fl)
	}
	t := &Trash{Flavour: fl, byID: map[string]*Format{}, raw: map[string][]byte{}, Profiles: map[string]*TrashProfile{}, Groups: map[string]*TrashGroup{}}

	err := eachJSON(root, paths.CustomFormats, func(path string, b []byte) error {
		f, err := ParseTrashFormat(fl, b)
		if err != nil {
			return fmt.Errorf("%s: %w", path, err)
		}
		if _, dup := t.byID[f.TrashID]; dup {
			return fmt.Errorf("%s: duplicate trash_id %s", path, f.TrashID)
		}
		t.byID[f.TrashID] = f
		t.raw[f.TrashID] = b
		t.Formats = append(t.Formats, f)
		return nil
	})
	if err != nil {
		return nil, err
	}
	sort.Slice(t.Formats, func(i, j int) bool { return t.Formats[i].TrashID < t.Formats[j].TrashID })
	for i, f := range t.Formats {
		f.ID = i + 1
	}

	err = eachJSON(root, paths.QualityProfiles, func(path string, b []byte) error {
		var p TrashProfile
		if err := json.Unmarshal(b, &p); err != nil {
			return fmt.Errorf("%s: %w", path, err)
		}
		t.Profiles[p.TrashID] = &p
		return nil
	})
	if err != nil {
		return nil, err
	}
	err = eachJSON(root, paths.CustomFormatGroups, func(path string, b []byte) error {
		var g TrashGroup
		if err := json.Unmarshal(b, &g); err != nil {
			return fmt.Errorf("%s: %w", path, err)
		}
		t.Groups[g.TrashID] = &g
		return nil
	})
	if err != nil {
		return nil, err
	}
	return t, nil
}

// ParseTrashFormat reads one TRaSH custom-format JSON file.
func ParseTrashFormat(fl Flavour, b []byte) (*Format, error) {
	var j FormatJSON
	if err := json.Unmarshal(b, &j); err != nil {
		return nil, err
	}
	if j.TrashID == "" {
		return nil, fmt.Errorf("format %q has no trash_id", j.Name)
	}
	return j.Build(fl)
}

// Format returns the format with a trash_id.
func (t *Trash) Format(trashID string) (*Format, bool) {
	f, ok := t.byID[trashID]
	return f, ok
}

// Raw returns the TRaSH JSON file of a format, nil when there is none.
func (t *Trash) Raw(trashID string) []byte { return t.raw[trashID] }

// TrashScore is a format's score in a score set, falling back to "default". ok is false when the format has
// neither, in which case recyclarr leaves the arr's score alone (0 for a new profile).
func TrashScore(f *Format, set string) (score int, ok bool) {
	if set != "" {
		if s, ok := f.TrashScores[set]; ok {
			return s, true
		}
	}
	s, ok := f.TrashScores["default"]
	return s, ok
}

// ProfileOptions picks what goes into a profile built from TRaSH.
type ProfileOptions struct {
	// ScoreSet overrides the profile's trash_score_set.
	ScoreSet string
	// DefaultGroups adds the required and default formats of every default group that includes the profile, as
	// recyclarr does unless told to skip them.
	DefaultGroups bool
	// Groups are further cf-group trash_ids whose required and default formats are added.
	Groups []string
}

// Profile builds the format part of TRaSH quality profile trashID: its formatItems, plus groups per opts, each
// scored from the score set. Formats are returned with it so the result is self-contained.
func (t *Trash) Profile(trashID string, opts ProfileOptions) (*Profile, []*Format, error) {
	tp, ok := t.Profiles[trashID]
	if !ok {
		return nil, nil, fmt.Errorf("trash: no %s quality profile %s", t.Flavour, trashID)
	}
	set := tp.TrashScoreSet
	if opts.ScoreSet != "" {
		set = opts.ScoreSet
	}
	p := &Profile{
		Name: tp.Name, Flavour: t.Flavour, TrashID: tp.TrashID, TrashScoreSet: set,
		MinFormatScore: tp.MinFormatScore, CutoffFormatScore: tp.CutoffFormatScore,
		MinUpgradeFormatScore: tp.MinUpgradeFormatScore,
	}
	var formats []*Format
	seen := map[string]bool{}
	add := func(id string) error {
		if seen[id] {
			return nil
		}
		f, ok := t.byID[id]
		if !ok {
			return fmt.Errorf("trash: profile %q names unknown format %s", tp.Name, id)
		}
		seen[id] = true
		formats = append(formats, f)
		score, _ := TrashScore(f, set)
		p.FormatItems = append(p.FormatItems, FormatItem{Format: f.ID, Name: f.Name, Score: score})
		return nil
	}
	ids := make([]string, 0, len(tp.FormatItems))
	for _, id := range tp.FormatItems {
		ids = append(ids, id)
	}
	sort.Strings(ids)
	for _, id := range ids {
		if err := add(id); err != nil {
			return nil, nil, err
		}
	}
	var groups []*TrashGroup
	if opts.DefaultGroups {
		gids := make([]string, 0, len(t.Groups))
		for id := range t.Groups {
			gids = append(gids, id)
		}
		sort.Strings(gids)
		for _, id := range gids {
			g := t.Groups[id]
			if g.IsDefault() && includes(g.QualityProfiles.Include, trashID) {
				groups = append(groups, g)
			}
		}
	}
	for _, id := range opts.Groups {
		g, ok := t.Groups[id]
		if !ok {
			return nil, nil, fmt.Errorf("trash: no %s cf-group %s", t.Flavour, id)
		}
		groups = append(groups, g)
	}
	for _, g := range groups {
		for _, cf := range g.CustomFormats {
			if cf.Required || cf.Default {
				if err := add(cf.TrashID); err != nil {
					return nil, nil, err
				}
			}
		}
	}
	return p, formats, nil
}

func includes(m map[string]string, id string) bool {
	for _, v := range m {
		if v == id {
			return true
		}
	}
	return false
}

func readJSON(path string, v any) error {
	b, err := os.ReadFile(path)
	if err != nil {
		return err
	}
	if err := json.Unmarshal(b, v); err != nil {
		return fmt.Errorf("%s: %w", path, err)
	}
	return nil
}

func eachJSON(root string, dirs []string, fn func(path string, b []byte) error) error {
	for _, d := range dirs {
		matches, err := filepath.Glob(filepath.Join(root, d, "*.json"))
		if err != nil {
			return err
		}
		sort.Strings(matches)
		for _, m := range matches {
			b, err := os.ReadFile(m)
			if err != nil {
				return err
			}
			if err := fn(m, b); err != nil {
				return err
			}
		}
	}
	return nil
}
