// Package migration holds the arr profile and naming converter; kept for phase D's from_arr decision.
package migration

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/json"
	"fmt"
	"sort"
	"strings"

	"github.com/theatarr/theatarr/internal/decide"
	"github.com/theatarr/theatarr/internal/decide/format"
	"github.com/theatarr/theatarr/internal/decide/profile"
	"github.com/theatarr/theatarr/internal/library"
	"github.com/theatarr/theatarr/internal/library/naming"
	"github.com/theatarr/theatarr/internal/policy"
	"github.com/theatarr/theatarr/internal/recyclarr"
	"go.yaml.in/yaml/v3"
)

// arrExport is an arr instance's profiles and formats frozen into policy fragments, and its naming.
type arrExport struct {
	Instance string
	Snapshot *decide.ArrSnapshot
	// Files holds profiles.yaml, formats.yaml and the formats' JSON (formats/<name>.json).
	Files       map[string][]byte
	Naming      *policy.Naming
	Unsupported []string
	kind        policy.Kind
}

// freezeArr freezes an instance's profiles from GET bodies read straight from the arr: raw holds the bodies of
// qualityprofile, customformat, qualitydefinition, config/mediamanagement and config/indexer by endpoint, and, for
// the naming, config/naming. selected names one profile ("" for all).
func freezeArr(instance, flavour string, raw map[string]json.RawMessage, selected string) (*arrExport, error) {
	e := &arrExport{Instance: instance, Files: map[string][]byte{}}
	switch flavour {
	case "radarr":
		e.kind = policy.KindMovie
	case "sonarr":
		e.kind = policy.KindSeries
	default:
		return nil, fmt.Errorf("unsupported flavour %q", flavour)
	}
	for _, ep := range []string{"qualityprofile", "customformat", "qualitydefinition", "config/mediamanagement", "config/indexer"} {
		if len(raw[ep]) == 0 || bytes.Equal(raw[ep], []byte("null")) {
			return nil, fmt.Errorf("%s: missing %s", instance, ep)
		}
	}
	e.Snapshot = &decide.ArrSnapshot{Flavour: format.Flavour(flavour), QualityProfile: raw["qualityprofile"], CustomFormat: raw["customformat"], QualityDefinition: raw["qualitydefinition"], MediaManagement: raw["config/mediamanagement"], IndexerConfig: raw["config/indexer"]}
	if err := e.convert(selected); err != nil {
		return nil, err
	}
	if body := raw["config/naming"]; len(body) != 0 {
		// The quality titles, as library.SyncNaming stores them: only those that differ from the quality's name.
		var defs []struct {
			Title   string `json:"title"`
			Quality struct {
				Name string `json:"name"`
			} `json:"quality"`
		}
		if err := json.Unmarshal(raw["qualitydefinition"], &defs); err != nil {
			return nil, err
		}
		titles := map[string]string{}
		for _, d := range defs {
			if d.Title != "" && d.Title != d.Quality.Name {
				titles[d.Quality.Name] = d.Title
			}
		}
		qb, _ := json.Marshal(titles)
		if err := e.convertNaming(body, qb); err != nil {
			return nil, err
		}
	}
	return e, nil
}

// Fragments decodes the exported profiles and format sets (the profiles.yaml and formats.yaml files). A format set's
// formats name their JSON by File, a key of Files.
func (e *arrExport) Fragments() (map[string]*policy.Profile, map[string]*policy.FormatSet, error) {
	var p struct {
		Profiles map[string]*policy.Profile `yaml:"profiles"`
	}
	var f struct {
		FormatSets map[string]*policy.FormatSet `yaml:"format_sets"`
	}
	if err := yaml.Unmarshal(e.Files["profiles.yaml"], &p); err != nil {
		return nil, nil, fmt.Errorf("profiles.yaml: %w", err)
	}
	if err := yaml.Unmarshal(e.Files["formats.yaml"], &f); err != nil {
		return nil, nil, fmt.Errorf("formats.yaml: %w", err)
	}
	return p.Profiles, f.FormatSets, nil
}

func (e *arrExport) yaml(name string, value any) error {
	var b bytes.Buffer
	enc := yaml.NewEncoder(&b)
	enc.SetIndent(2)
	if err := enc.Encode(value); err != nil {
		return err
	}
	if err := enc.Close(); err != nil {
		return err
	}
	e.Files[name] = b.Bytes()
	return nil
}

func uniqueKey(base string, used map[string]bool) string {
	key := base
	for n := 2; used[key]; n++ {
		key = fmt.Sprintf("%s-%d", base, n)
	}
	used[key] = true
	return key
}

func (e *arrExport) convert(selected string) error {
	s := e.Snapshot
	var ic struct {
		MaximumSize        int  `json:"maximumSize"`
		PreferIndexerFlags bool `json:"preferIndexerFlags"`
	}
	if err := json.Unmarshal(s.IndexerConfig, &ic); err != nil {
		return err
	}
	if ic.MaximumSize != 0 || ic.PreferIndexerFlags {
		return fmt.Errorf("native schema cannot express config/indexer maximumSize=%d, preferIndexerFlags=%t", ic.MaximumSize, ic.PreferIndexerFlags)
	}
	var mm struct {
		Propers string `json:"downloadPropersAndRepacks"`
	}
	if err := json.Unmarshal(s.MediaManagement, &mm); err != nil {
		return err
	}
	propers := policy.PropersPreferAndUpgrade
	if mm.Propers != "" {
		p, err := profile.ParseProperDownloadTypes(mm.Propers)
		if err != nil {
			return err
		}
		switch p {
		case profile.DoNotUpgrade:
			propers = policy.PropersDoNotUpgrade
		case profile.DoNotPrefer:
			propers = policy.PropersDoNotPrefer
		}
	}
	ps, err := profile.ParseArrProfiles(s.Flavour, s.QualityProfile)
	if err != nil {
		return err
	}
	sort.Slice(ps, func(i, j int) bool {
		if ps[i].Name != ps[j].Name {
			return ps[i].Name < ps[j].Name
		}
		return ps[i].ID < ps[j].ID
	})
	var chosen []*profile.Profile
	for _, p := range ps {
		if selected == "" || p.Name == selected {
			chosen = append(chosen, p)
		}
	}
	if len(chosen) == 0 {
		return fmt.Errorf("%s: no stored profile %q", e.Instance, selected)
	}
	used := map[string]bool{}
	sum := sha256.Sum256(append(append([]byte(e.Instance+"/"), s.QualityProfile...), s.CustomFormat...))
	prefix := fmt.Sprintf("frozen-%s-%x", recyclarr.Slug(e.Instance), sum[:4])
	setKey := uniqueKey(recyclarr.Slug(e.Instance), used)
	var fs []format.FormatJSON
	if err := json.Unmarshal(s.CustomFormat, &fs); err != nil {
		return err
	}
	sort.Slice(fs, func(i, j int) bool {
		if fs[i].Name != fs[j].Name {
			return fs[i].Name < fs[j].Name
		}
		return fs[i].ID < fs[j].ID
	})
	refs := map[string]*policy.FormatRef{}
	byID := map[int]string{}
	seenNames := map[string]bool{}
	for _, f := range fs {
		if f.Name == "" || seenNames[f.Name] {
			return fmt.Errorf("native schema cannot express empty or duplicate custom format name %q", f.Name)
		}
		seenNames[f.Name] = true
		if _, err := f.Build(s.Flavour); err != nil {
			return err
		}
		if byID[f.ID] != "" {
			return fmt.Errorf("duplicate custom format id %d", f.ID)
		}
		key := f.Name
		byID[f.ID] = key
		file := fmt.Sprintf("formats/%s-%s-%d.json", prefix, recyclarr.Slug(f.Name), f.ID)
		b, err := json.MarshalIndent(f, "", "  ")
		if err != nil {
			return err
		}
		e.Files[file] = append(b, '\n')
		refs[key] = &policy.FormatRef{File: file, Flavour: string(s.Flavour)}
	}
	defs, err := profile.ParseArrDefinitions(s.Flavour, s.QualityDefinition)
	if err != nil {
		return err
	}
	sizes := map[string]policy.SizeLimit{}
	for _, d := range defs.All() {
		l := policy.SizeLimit{}
		if d.MinSize != nil {
			l.Min = *d.MinSize
		}
		if d.MaxSize != nil {
			l.Max = *d.MaxSize
		}
		if d.PreferredSize != nil {
			l.Preferred = *d.PreferredSize
			l.PreferredZero = *d.PreferredSize == 0
		}
		sizes[d.Quality.Name] = l
	}
	nodes := map[string]any{}
	for _, p := range chosen {
		if problems := p.Validate(); len(problems) > 0 {
			return fmt.Errorf("profile %q: %s", p.Name, strings.Join(problems, "; "))
		}
		if !p.UpgradeAllowed {
			e.Unsupported = append(e.Unsupported, fmt.Sprintf("profile %q has upgrades disabled; native profile keys replace its display name in DiskUpgradesNotAllowed messages", p.Name))
		}
		var qs, order []any
		for i := len(p.Items) - 1; i >= 0; i-- {
			it := p.Items[i]
			if it.Quality != nil {
				order = append(order, it.Quality.Name)
				if it.Allowed {
					qs = append(qs, it.Quality.Name)
				}
				continue
			}
			var members []string
			for j := len(it.Items) - 1; j >= 0; j-- {
				if it.Items[j].Quality == nil {
					return fmt.Errorf("native schema cannot express nested quality group %q", it.Name)
				}
				members = append(members, it.Items[j].Quality.Name)
			}
			node := map[string]any{"group": it.Name, "qualities": members}
			order = append(order, node)
			if it.Allowed {
				qs = append(qs, node)
			}
		}
		cut := p.CutoffItem().Name
		if p.CutoffItem().Quality != nil {
			cut = p.CutoffItem().Quality.Name
		}
		scores := map[string]int{}
		// Explicit zero scores keep every custom format in the frozen profile.
		for _, key := range byID {
			scores[key] = 0
		}
		for _, item := range p.FormatItems {
			key := byID[item.Format]
			if key == "" {
				return fmt.Errorf("profile %q refers to missing format id %d", p.Name, item.Format)
			}
			scores[key] = item.Score
		}
		key := uniqueKey(prefix+"-"+recyclarr.Slug(p.Name), used)
		language := ""
		if s.Flavour == format.Radarr {
			language = s.Flavour.LanguageName(p.Language)
			if p.Language == -2 {
				language = "original"
			}
			if _, ok := s.Flavour.LanguageID(language); !ok && language != "original" {
				return fmt.Errorf("native schema cannot express language id %d", p.Language)
			}
		}
		nodes[key] = map[string]any{"format_set": setKey, "qualities": qs, "quality_order": order, "cutoff": cut, "upgrades": p.UpgradeAllowed, "min_score": p.MinFormatScore, "cutoff_score": p.CutoffFormatScore, "min_upgrade_step": p.MinUpgradeFormatScore, "formats": scores, "sizes": sizes, "language": language, "propers": propers}
	}
	if err := e.yaml("profiles.yaml", map[string]any{"profiles": nodes}); err != nil {
		return err
	}
	return e.yaml("formats.yaml", map[string]any{"format_sets": map[string]*policy.FormatSet{setKey: {Flavour: string(s.Flavour), Formats: refs}}})
}

func (e *arrExport) convertNaming(body, qualities []byte) error {
	c, err := naming.ParseArrConfig(body)
	if err != nil {
		return err
	}
	var titles map[string]string
	if err := json.Unmarshal(qualities, &titles); err != nil {
		return err
	}
	var qualityNames []string
	for q := range titles {
		qualityNames = append(qualityNames, q)
	}
	sort.Strings(qualityNames)
	for _, q := range qualityNames {
		t := titles[q]
		if t != "" && t != q {
			return fmt.Errorf("native naming schema cannot express quality title overrides (%s)", q)
		}
	}
	if !c.Rename || !c.ReplaceIllegalCharacters {
		return fmt.Errorf("native naming schema cannot express rename=false or replaceIllegalCharacters=false")
	}
	colon := []string{"delete", "dash", "space_dash", "space_dash_space", "smart"}
	multi := []string{"extend", "duplicate", "repeat", "scene", "range", "prefixed_range"}
	if int(c.ColonReplacement) < 0 || int(c.ColonReplacement) >= len(colon) {
		return fmt.Errorf("native naming schema cannot express colon replacement %d (custom replacement)", c.ColonReplacement)
	}
	n := policy.Naming{Colon: colon[c.ColonReplacement]}
	if e.kind == policy.KindMovie {
		n.Folder, n.File = c.MovieFolderFormat, c.StandardMovieFormat
	} else {
		if int(c.MultiEpisodeStyle) < 0 || int(c.MultiEpisodeStyle) >= len(multi) {
			return fmt.Errorf("native naming schema cannot express multi-episode style %d", c.MultiEpisodeStyle)
		}
		n.Folder, n.File, n.Season, n.Specials, n.Daily, n.Anime, n.MultiEpisode = c.SeriesFolderFormat, c.StandardEpisodeFormat, c.SeasonFolderFormat, c.SpecialsFolderFormat, c.DailyEpisodeFormat, c.AnimeEpisodeFormat, multi[c.MultiEpisodeStyle]
	}
	// Check the actual native naming loader, including its defaults for empty templates.
	pol := &policy.Policy{Versions: map[string]*policy.VersionPolicy{"export": {Kind: e.kind, Naming: n}}}
	native, err := library.NamingFor(context.Background(), nil, pol, "export")
	if err != nil {
		return err
	}
	if e.kind == policy.KindMovie {
		if native.Config.MovieFolderFormat != c.MovieFolderFormat || native.Config.StandardMovieFormat != c.StandardMovieFormat {
			return fmt.Errorf("native naming schema cannot express empty movie templates")
		}
	} else if native.Config.SeriesFolderFormat != c.SeriesFolderFormat || native.Config.SeasonFolderFormat != c.SeasonFolderFormat || native.Config.SpecialsFolderFormat != c.SpecialsFolderFormat || native.Config.StandardEpisodeFormat != c.StandardEpisodeFormat || native.Config.DailyEpisodeFormat != c.DailyEpisodeFormat || native.Config.AnimeEpisodeFormat != c.AnimeEpisodeFormat {
		return fmt.Errorf("native naming schema cannot express empty series templates")
	}
	e.Naming = &n
	return nil
}
