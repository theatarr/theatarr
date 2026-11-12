package settings

// Profile presets (settings UX step 4): a new profile built from a TRaSH quality-profile JSON plus the default
// cf-groups that include it, as recyclarr builds a guide-backed profile, with a few simple modifiers; and the
// comparison of any profile with the preset it started from ("preset + our edits").

import (
	"encoding/json"
	"errors"
	"fmt"
	"regexp"
	"slices"
	"sort"
	"strings"

	"github.com/theatarr/theatarr/internal/decide/format"
	"github.com/theatarr/theatarr/internal/policy"
)

// ProfileOrigin is the TRaSH preset a profile started from: the quality-profile trash_id, its name when the profile
// was built, the flavour, the score set the scores came from and the modifiers applied on top.
type ProfileOrigin struct {
	Preset    string   `json:"preset"`
	Name      string   `json:"name"`
	Flavour   string   `json:"flavour"`
	ScoreSet  string   `json:"score_set,omitempty"`
	Modifiers []string `json:"modifiers,omitempty"`
}

// The preset modifiers.
const (
	// ModFallbacks1080p allows the preset's 1080p Remux, Bluray and WEB qualities below its 2160p ones.
	ModFallbacks1080p = "fallbacks_1080p"
	// ModBluray2160p allows Bluray-2160p encodes (right below Remux-2160p) and adds TRaSH's UHD Bluray tiers.
	ModBluray2160p = "bluray_2160p"
	// ModBlockDVNoHDR adds "DV (w/o HDR fallback)" with TRaSH's score (-10000).
	ModBlockDVNoHDR = "block_dv_no_hdr"
)

var presetModifiers = []string{ModFallbacks1080p, ModBluray2160p, ModBlockDVNoHDR}

// ModifierLabel is the short text a modifier stands for in an origin ("WEB 2160p + 1080p fallbacks").
func ModifierLabel(m string) string {
	switch m {
	case ModFallbacks1080p:
		return "1080p fallbacks"
	case ModBluray2160p:
		return "Bluray-2160p"
	case ModBlockDVNoHDR:
		return "DV w/o HDR blocked"
	}
	return m
}

var dvNoHDR = "DV (w/o HDR fallback)"
var uhdBlurayTiers = []string{"UHD Bluray Tier 01", "UHD Bluray Tier 02", "UHD Bluray Tier 03"}

// Preset is one TRaSH quality profile offered as a starting point.
type Preset struct {
	TrashID     string   `json:"trash_id"`
	Name        string   `json:"name"`
	Flavour     string   `json:"flavour"`
	Description string   `json:"description"`
	Group       int      `json:"group"`
	Qualities   []string `json:"qualities"`
	Cutoff      string   `json:"cutoff"`
	ScoreSet    string   `json:"score_set,omitempty"`
	Formats     int      `json:"formats"`
	// Modifiers lists the modifiers that change something for this preset.
	Modifiers []string `json:"modifiers"`
}

var htmlBreak = regexp.MustCompile(`(?i)<br\s*/?>`)
var htmlTag = regexp.MustCompile(`<[^>]*>`)

// Presets lists the quality profiles of a TRaSH tree, by TRaSH's group and then name.
func Presets(t *format.Trash) ([]Preset, error) {
	out := []Preset{}
	for id, tp := range t.Profiles {
		build, err := BuildPreset(t, id, nil, nil)
		if err != nil {
			return nil, err
		}
		desc := htmlTag.ReplaceAllString(htmlBreak.ReplaceAllString(tp.Description, "; "), "")
		desc = strings.TrimSpace(strings.ReplaceAll(desc, ":;", ":"))
		p := Preset{TrashID: id, Name: tp.Name, Flavour: string(t.Flavour), Description: desc, Group: tp.Group,
			Qualities: allowedNames(build.Profile.Qualities), Cutoff: tp.Cutoff, ScoreSet: tp.TrashScoreSet,
			Formats: len(build.Profile.Formats), Modifiers: []string{}}
		for _, m := range presetModifiers {
			if applies(t, tp, m) {
				p.Modifiers = append(p.Modifiers, m)
			}
		}
		out = append(out, p)
	}
	sort.Slice(out, func(i, j int) bool {
		if out[i].Group != out[j].Group {
			return out[i].Group < out[j].Group
		}
		return out[i].Name < out[j].Name
	})
	return out, nil
}

// PresetBuild is a profile built from a preset: its policy profile (scores keyed by display name), the format set
// that holds its formats (inline TRaSH JSON, or a reference to a standalone format with the same trash_id), the
// origin to store and TRaSH's score per display name.
type PresetBuild struct {
	Profile   policy.Profile    `json:"profile"`
	FormatSet FormatSet         `json:"format_set"`
	Origin    ProfileOrigin     `json:"origin"`
	TrashIDs  map[string]string `json:"trash_ids"`
	// Unscored lists the formats TRaSH gives no score in the score set: they get 0 and no sync ever changes them.
	Unscored []string `json:"unscored"`
	Notes    []string `json:"notes"`
}

// BuildPreset builds TRaSH quality profile trashID with modifiers. Formats are taken from the profile's
// formatItems and the required and default formats of every default cf-group that includes it, scored from the
// profile's score set (falling back to default), as recyclarr does. globals are the standalone formats: one with the
// same trash_id is referenced instead of copied. The format set has no key; the caller names it.
func BuildPreset(t *format.Trash, trashID string, modifiers []string, globals []CustomFormat) (*PresetBuild, error) {
	tp, ok := t.Profiles[trashID]
	if !ok {
		return nil, &policy.Errors{List: []policy.FieldError{{Path: "preset", Msg: fmt.Sprintf("TRaSH has no %s quality profile %s", t.Flavour, trashID)}}}
	}
	for _, m := range modifiers {
		if !slices.Contains(presetModifiers, m) {
			return nil, &policy.Errors{List: []policy.FieldError{{Path: "modifiers", Msg: fmt.Sprintf("unknown modifier %q", m)}}}
		}
		if !applies(t, tp, m) {
			return nil, &policy.Errors{List: []policy.FieldError{{Path: "modifiers", Msg: fmt.Sprintf("%s does not change the preset %q", ModifierLabel(m), tp.Name)}}}
		}
	}
	built, formats, err := t.Profile(trashID, format.ProfileOptions{DefaultGroups: true})
	if err != nil {
		return nil, err
	}
	out := &PresetBuild{TrashIDs: map[string]string{}, Unscored: []string{}, Notes: []string{},
		Origin: ProfileOrigin{Preset: trashID, Name: tp.Name, Flavour: string(t.Flavour), ScoreSet: built.TrashScoreSet}}
	if len(modifiers) > 0 {
		out.Origin.Modifiers = orderedModifiers(modifiers)
	}
	pr := policy.Profile{Formats: map[string]int{}, MinScore: tp.MinFormatScore, CutoffScore: tp.CutoffFormatScore, Cutoff: tp.Cutoff}
	step := tp.MinUpgradeFormatScore
	if step < 1 {
		step = 1
	}
	pr.MinUpgradeStep = &step
	upgrades := true
	if tp.UpgradeAllowed != nil {
		upgrades = *tp.UpgradeAllowed
	}
	pr.Upgrades = &upgrades
	if t.Flavour == format.Radarr && tp.Language != "" && tp.Language != "Any" {
		pr.Language = tp.Language
		if tp.Language == "Original" {
			pr.Language = "original"
		}
	}
	items := tp.Items
	allowed := []format.TrashQualityItem{}
	for _, it := range items {
		if it.Allowed {
			allowed = append(allowed, it)
		}
	}
	find := func(name string) (format.TrashQualityItem, bool) {
		for _, it := range items {
			if it.Name == name {
				return it, true
			}
		}
		return format.TrashQualityItem{}, false
	}
	has := func(list []format.TrashQualityItem, name string) bool {
		return slices.ContainsFunc(list, func(it format.TrashQualityItem) bool { return it.Name == name })
	}
	scored := map[string]bool{}
	addFormat := func(f *format.Format) {
		if scored[f.Name] {
			return
		}
		scored[f.Name] = true
		formats = append(formats, f)
	}
	for _, f := range formats {
		scored[f.Name] = true
	}
	for _, m := range out.Origin.Modifiers {
		switch m {
		case ModBluray2160p:
			it, _ := find("Bluray-2160p")
			at := 0
			for i, a := range allowed {
				if a.Name == "Remux-2160p" || a.Name == "Bluray-2160p Remux" {
					at = i + 1
				}
			}
			allowed = slices.Insert(allowed, at, format.TrashQualityItem{Name: it.Name, Allowed: true, Items: it.Items})
			for _, name := range uhdBlurayTiers {
				if f := trashFormatNamed(t, name); f != nil {
					addFormat(f)
				}
			}
			if tp.Cutoff != "Remux-2160p" && tp.Cutoff != "Bluray-2160p Remux" {
				out.Notes = append(out.Notes, fmt.Sprintf("The cutoff stays %s: set it to Bluray-2160p to upgrade WEB releases to Bluray encodes.", tp.Cutoff))
			}
		case ModFallbacks1080p:
			for _, name := range fallbacks1080p(t.Flavour) {
				if it, ok := find(name); ok && !has(allowed, name) {
					allowed = append(allowed, format.TrashQualityItem{Name: it.Name, Allowed: true, Items: it.Items})
				}
			}
		case ModBlockDVNoHDR:
			addFormat(trashFormatNamed(t, dvNoHDR))
		}
	}
	for _, it := range allowed {
		pr.Qualities = append(pr.Qualities, qualityItem(it))
	}
	pr.QualityOrder = append(pr.QualityOrder, pr.Qualities...)
	for _, it := range items {
		if !has(allowed, it.Name) {
			pr.QualityOrder = append(pr.QualityOrder, qualityItem(it))
		}
	}
	global := map[string]string{}
	for _, r := range globals {
		if id := customTrashID(r); id != "" && (r.Config.Flavour == "" || r.Config.Flavour == string(t.Flavour)) {
			if _, dup := global[id]; !dup {
				global[id] = r.Key
			}
		}
	}
	set := FormatSet{Config: policy.FormatSet{Flavour: string(t.Flavour), Formats: map[string]*policy.FormatRef{}}, Specifications: map[string]json.RawMessage{}}
	for _, f := range formats {
		score, ok := format.TrashScore(f, built.TrashScoreSet)
		if !ok {
			out.Unscored = append(out.Unscored, f.Name)
		}
		pr.Formats[f.Name] = score
		out.TrashIDs[f.Name] = f.TrashID
		if key, ok := global[f.TrashID]; ok {
			set.Config.Formats[f.Name] = &policy.FormatRef{Ref: key}
			continue
		}
		set.Config.Formats[f.Name] = &policy.FormatRef{TrashID: f.TrashID, Flavour: string(t.Flavour)}
		set.Specifications[f.Name] = append(json.RawMessage(nil), t.Raw(f.TrashID)...)
	}
	if _, ok := pr.Formats["Repack/Proper"]; ok {
		// TRaSH scores repacks and propers through formats and advises the arrs' "Do Not Prefer" with them.
		pr.Propers = "do_not_prefer"
	}
	out.Profile = pr
	out.FormatSet = set
	return out, nil
}

func orderedModifiers(in []string) []string {
	out := []string{}
	for _, m := range presetModifiers {
		if slices.Contains(in, m) {
			out = append(out, m)
		}
	}
	return out
}

func fallbacks1080p(fl format.Flavour) []string {
	if fl == format.Sonarr {
		return []string{"Bluray-1080p Remux", "Bluray-1080p", "WEB 1080p"}
	}
	return []string{"Remux-1080p", "Bluray-1080p", "WEB 1080p"}
}

// applies reports whether modifier m changes preset tp.
func applies(t *format.Trash, tp *format.TrashProfile, m string) bool {
	allowed := map[string]bool{}
	known := map[string]bool{}
	has2160 := false
	for _, it := range tp.Items {
		known[it.Name] = true
		if it.Allowed {
			allowed[it.Name] = true
			has2160 = has2160 || strings.Contains(it.Name, "2160p")
		}
	}
	switch m {
	case ModFallbacks1080p:
		if !has2160 {
			return false
		}
		for _, name := range fallbacks1080p(t.Flavour) {
			if known[name] && !allowed[name] {
				return true
			}
		}
	case ModBluray2160p:
		return known["Bluray-2160p"] && !allowed["Bluray-2160p"]
	case ModBlockDVNoHDR:
		f := trashFormatNamed(t, dvNoHDR)
		if f == nil {
			return false
		}
		if slices.Contains(mapsValues(tp.FormatItems), f.TrashID) {
			return false
		}
		built, _, err := t.Profile(tp.TrashID, format.ProfileOptions{DefaultGroups: true})
		if err != nil {
			return false
		}
		for _, item := range built.FormatItems {
			if item.Name == f.Name {
				return false
			}
		}
		return true
	}
	return false
}

func mapsValues(m map[string]string) []string {
	out := make([]string, 0, len(m))
	for _, v := range m {
		out = append(out, v)
	}
	return out
}

func trashFormatNamed(t *format.Trash, name string) *format.Format {
	for _, f := range t.Formats {
		if f.Name == name {
			return f
		}
	}
	return nil
}

func qualityItem(it format.TrashQualityItem) policy.QualityItem {
	if len(it.Items) > 0 {
		return policy.QualityItem{Name: it.Name, Group: true, Qualities: append([]string(nil), it.Items...)}
	}
	return policy.QualityItem{Name: it.Name}
}

func allowedNames(items []policy.QualityItem) []string {
	out := make([]string, 0, len(items))
	for _, q := range items {
		out = append(out, q.Name)
	}
	return out
}

// members flattens quality items to their qualities, so a group and its members written out compare alike.
func members(items []policy.QualityItem) []string {
	out := []string{}
	for _, q := range items {
		if q.Group {
			out = append(out, q.Qualities...)
		} else {
			out = append(out, q.Name)
		}
	}
	return out
}

// ApplyPreset adds a built preset to records as profile key, with its format set under the same key, and binds the
// profile to TRaSH's score set on the flavour's TRaSH sync source, remembering every score: TRaSH's scores
// then follow TRaSH and the editor's changes stay ours. config, when set, replaces the built profile (an editor's changes); its format set is
// always the built one. It returns the sync source the profile was bound to, empty when there is none.
func ApplyPreset(records *Records, key string, build *PresetBuild, config *policy.Profile) (string, error) {
	if strings.TrimSpace(key) == "" {
		return "", &policy.Errors{List: []policy.FieldError{{Path: "key", Msg: "required"}}}
	}
	for _, r := range records.Profiles {
		if r.Key == key {
			return "", &policy.Errors{List: []policy.FieldError{{Path: "key", Msg: "a profile with this key exists"}}}
		}
	}
	for _, r := range records.FormatSets {
		if r.Key == key {
			return "", &policy.Errors{List: []policy.FieldError{{Path: "key", Msg: "a format set with this key exists"}}}
		}
	}
	pr := build.Profile
	if config != nil {
		pr = *config
	}
	pr.FormatSet = key
	origin := build.Origin
	records.Profiles = append(records.Profiles, QualityProfile{Record: Record{Key: key}, Config: pr, Origin: &origin})
	set := build.FormatSet
	set.Record = Record{Key: key}
	records.FormatSets = append(records.FormatSets, set)
	scoreSet := build.Origin.ScoreSet
	if scoreSet == "" {
		scoreSet = "default"
	}
	source := bindingSource(records.SyncSources, build.Origin.Flavour)
	if source < 0 {
		return "", nil
	}
	sources := slices.Clone(records.SyncSources)
	c := sources[source].Config
	c.Profiles = cloneStrings(c.Profiles)
	c.Profiles[key] = scoreSet
	state := map[string]ProfileScoreState{}
	for k, v := range c.ScoreState {
		state[k] = v
	}
	// Every score TRaSH sets is remembered: equal ones follow TRaSH, the editor's changes differ from TRaSH's last
	// score and so stay ours.
	scores := map[string]TrashScoreState{}
	for name, score := range build.Profile.Formats {
		if slices.Contains(build.Unscored, name) {
			continue
		}
		if _, ok := pr.Formats[name]; ok {
			scores[name] = TrashScoreState{Trash: score}
		}
	}
	state[key] = ProfileScoreState{Set: scoreSet, Scores: scores}
	c.ScoreState = state
	sources[source].Config = c
	records.SyncSources = sources
	return sources[source].Key, nil
}

func cloneStrings(m map[string]string) map[string]string {
	out := make(map[string]string, len(m)+1)
	for k, v := range m {
		out[k] = v
	}
	return out
}

// bindingSource picks the TRaSH sync source a new profile of flavour is bound to: an enabled one first, a tracked
// (GitHub) one next, then by key. -1 when there is none.
func bindingSource(sources []QualitySyncSource, flavour string) int {
	best := -1
	rank := func(s QualitySyncSource) int {
		r := 0
		if !s.Config.Enabled {
			r += 2
		}
		if s.Config.Origin != OriginGitHub {
			r++
		}
		return r
	}
	for i, s := range sources {
		if s.Config.Kind != "trash" || s.Config.Flavour != flavour {
			continue
		}
		if best < 0 || rank(s) < rank(sources[best]) || (rank(s) == rank(sources[best]) && s.Key < sources[best].Key) {
			best = i
		}
	}
	return best
}

// ProfileBinding is the sync source and score set a profile is bound to, if any.
func ProfileBinding(sources []QualitySyncSource, profile string) (source, set string) {
	keys := make([]string, 0, len(sources))
	byKey := map[string]QualitySyncSource{}
	for _, s := range sources {
		keys = append(keys, s.Key)
		byKey[s.Key] = s
	}
	sort.Strings(keys)
	for _, k := range keys {
		if set, ok := byKey[k].Config.Profiles[profile]; ok {
			return k, set
		}
	}
	return "", ""
}

// ErrNoTrash is returned when no local TRaSH tree is configured (THEATARR_TRASH).
var ErrNoTrash = errors.New("settings: presets need the local TRaSH tree (THEATARR_TRASH)")
