// Package recyclarr turns a recyclarr configuration (recyclarr.yml), custom formats of one's own (arr-shaped JSON,
// as in recyclarr's custom/*.json) and the TRaSH-Guides JSON into theatarr policy formats and profiles, one
// policy directory per arr instance, and checks that the result scores the corpus titles exactly as the arr did.
//
// What recyclarr does that is reproduced here (recyclarr v8 config schema, ref/recyclarr_recyclarr
// src/Recyclarr.Core/Config/Parsing/ConfigYamlDataObjects.cs):
//
//   - custom_formats: every trash_id is synced from TRaSH; assign_scores_to sets its score in a named profile. The
//     score is the entry's own (assign_scores_to[].score, or the deprecated entry-level score), else the format's
//     trash_scores for the profile's score_set, else trash_scores.default; with none the score is left alone.
//
//   - quality_profiles: min_format_score, min_upgrade_format_score, upgrade (allowed, until_quality, until_score),
//     qualities (listed best first; a group when it has qualities), score_set, reset_unmatched_scores (enabled,
//     except, except_patterns).
//
//   - Formats recyclarr does not manage keep the arr's definition and score (reset_unmatched_scores off), so the
//     arr's snapshot (the corpus' snapshot/*.json) supplies them when it is given.
//
//   - quality_definition (instance-wide and per profile): TRaSH's quality-size file of the type, with the config's
//     preferred_ratio and per-quality min/max/preferred overrides (QualitySizePlanComponent), only when
//     Options.QualitySizes asks for it (the settings sync); otherwise the sizes come from the arr's snapshot.
//
// Not reproduced (refused with an error, so a config that needs them is never converted silently): include
// (templates), custom_format_groups, guide-backed profiles (quality_profiles[].trash_id) without an arr snapshot.
// Keys that change nothing Theatarr models (media_naming, media_management, quality_sort) are listed in Ignored.
package recyclarr

import (
	"bytes"
	"fmt"
	"sort"

	"go.yaml.in/yaml/v3"
)

// Flavour is radarr or sonarr.
type Flavour string

const (
	Radarr Flavour = "radarr"
	Sonarr Flavour = "sonarr"
)

// Config is a parsed recyclarr.yml.
type Config struct {
	Instances []*Instance
}

// Instance is one radarr or sonarr entry.
type Instance struct {
	Name                   string
	Flavour                Flavour
	DeleteOldCustomFormats bool
	CustomFormats          []CustomFormatEntry
	QualityProfiles        []QualityProfile
	// QualityDefinition is the instance's quality_definition, nil without one.
	QualityDefinition *QualityDefinition
	// Ignored lists config keys that do not change formats or scores (base_url, api_key, media_naming, ...).
	Ignored []string
}

// CustomFormatEntry is one custom_formats item.
type CustomFormatEntry struct {
	TrashIDs []string
	// Score is the deprecated entry-level score, applied to every assign_scores_to without its own.
	Score    *int
	AssignTo []AssignScore
}

// AssignScore is one assign_scores_to item: the profile (by name) and an optional score.
type AssignScore struct {
	Name  string
	Score *int
}

// QualityProfile is one quality_profiles item.
type QualityProfile struct {
	Name                  string
	TrashID               string
	ScoreSet              string
	MinFormatScore        *int
	MinUpgradeFormatScore *int
	UpgradeAllowed        *bool
	UntilQuality          string
	UntilScore            *int
	ResetUnmatched        bool
	ResetExcept           []string
	ResetExceptPatterns   []string
	Qualities             []ProfileQuality
	// QualityDefinition is the profile's own quality_definition (Sonarr v5's per-profile sizes), nil without one.
	QualityDefinition *QualityDefinition
}

// QualityDefinition is a quality_definition: a TRaSH quality-size type ("movie", "series", "anime", ...), an
// optional preferred_ratio (0..1) and per-quality overrides.
type QualityDefinition struct {
	Type           string
	PreferredRatio *float64
	Qualities      []QualitySizeOverride
}

// QualitySizeOverride overrides one quality's sizes: a number, "unlimited" (max and preferred only) or "" (TRaSH's).
type QualitySizeOverride struct {
	Name, Min, Max, Preferred string
}

// ProfileQuality is one qualities item: a quality, or a group when Qualities is set. Enabled defaults to true.
type ProfileQuality struct {
	Name      string
	Enabled   bool
	Qualities []string
}

type yamlRoot struct {
	Radarr map[string]*yamlInstance `yaml:"radarr"`
	Sonarr map[string]*yamlInstance `yaml:"sonarr"`
}

type yamlInstance struct {
	BaseURL                yaml.Node `yaml:"base_url"`
	APIKey                 yaml.Node `yaml:"api_key"`
	DeleteOldCustomFormats *bool     `yaml:"delete_old_custom_formats"`
	ReplaceExisting        *bool     `yaml:"replace_existing_custom_formats"`
	CustomFormats          []yamlCF  `yaml:"custom_formats"`
	CustomFormatGroups     yaml.Node `yaml:"custom_format_groups"`
	QualityProfiles        []yamlQP  `yaml:"quality_profiles"`
	QualityDefinition      *yamlQD   `yaml:"quality_definition"`
	MediaNaming            yaml.Node `yaml:"media_naming"`
	MediaManagement        yaml.Node `yaml:"media_management"`
	Include                yaml.Node `yaml:"include"`
}

type yamlCF struct {
	TrashIDs       []string `yaml:"trash_ids"`
	Score          *int     `yaml:"score"`
	AssignScoresTo []struct {
		Name    string `yaml:"name"`
		TrashID string `yaml:"trash_id"`
		Score   *int   `yaml:"score"`
	} `yaml:"assign_scores_to"`
	QualityProfiles []struct { // the pre-v7 spelling of assign_scores_to
		Name  string `yaml:"name"`
		Score *int   `yaml:"score"`
	} `yaml:"quality_profiles"`
}

type yamlQP struct {
	Name                  string `yaml:"name"`
	TrashID               string `yaml:"trash_id"`
	ScoreSet              string `yaml:"score_set"`
	MinFormatScore        *int   `yaml:"min_format_score"`
	MinUpgradeFormatScore *int   `yaml:"min_upgrade_format_score"`
	QualitySort           string `yaml:"quality_sort"`
	Upgrade               *struct {
		Allowed      *bool  `yaml:"allowed"`
		UntilQuality string `yaml:"until_quality"`
		UntilScore   *int   `yaml:"until_score"`
	} `yaml:"upgrade"`
	ResetUnmatchedScores *struct {
		Enabled        *bool    `yaml:"enabled"`
		Except         []string `yaml:"except"`
		ExceptPatterns []string `yaml:"except_patterns"`
	} `yaml:"reset_unmatched_scores"`
	Qualities []struct {
		Name      string   `yaml:"name"`
		Enabled   *bool    `yaml:"enabled"`
		Qualities []string `yaml:"qualities"`
	} `yaml:"qualities"`
	QualityDefinition *yamlQD `yaml:"quality_definition"`
}

type yamlQD struct {
	Type           string   `yaml:"type"`
	PreferredRatio *float64 `yaml:"preferred_ratio"`
	Qualities      []struct {
		Name      string `yaml:"name"`
		Min       string `yaml:"min"`
		Max       string `yaml:"max"`
		Preferred string `yaml:"preferred"`
	} `yaml:"qualities"`
}

func (y *yamlQD) convert(path string) (*QualityDefinition, error) {
	if y == nil {
		return nil, nil
	}
	if y.Type == "" {
		return nil, fmt.Errorf("recyclarr: %s.quality_definition: no type", path)
	}
	d := &QualityDefinition{Type: y.Type, PreferredRatio: y.PreferredRatio}
	for _, q := range y.Qualities {
		d.Qualities = append(d.Qualities, QualitySizeOverride{Name: q.Name, Min: q.Min, Max: q.Max, Preferred: q.Preferred})
	}
	return d, nil
}

// Parse reads a recyclarr.yml. Secrets (!secret, !env_var) are never resolved: base_url and api_key are not read.
func Parse(b []byte) (*Config, error) {
	var root yamlRoot
	dec := yaml.NewDecoder(bytes.NewReader(b))
	dec.KnownFields(true)
	if err := dec.Decode(&root); err != nil {
		return nil, fmt.Errorf("recyclarr: %w", err)
	}
	cfg := &Config{}
	for _, fl := range []Flavour{Radarr, Sonarr} {
		m := root.Radarr
		if fl == Sonarr {
			m = root.Sonarr
		}
		names := make([]string, 0, len(m))
		for n := range m {
			names = append(names, n)
		}
		sort.Strings(names)
		for _, n := range names {
			inst, err := convertInstance(n, fl, m[n])
			if err != nil {
				return nil, err
			}
			cfg.Instances = append(cfg.Instances, inst)
		}
	}
	return cfg, nil
}

func set(n yaml.Node) bool { return n.Kind != 0 }

func convertInstance(name string, fl Flavour, y *yamlInstance) (*Instance, error) {
	inst := &Instance{Name: name, Flavour: fl}
	if y == nil {
		return inst, nil
	}
	path := fmt.Sprintf("%s.%s", fl, name)
	if set(y.Include) {
		return nil, fmt.Errorf("recyclarr: %s.include: templates are not converted; inline them", path)
	}
	if set(y.CustomFormatGroups) {
		return nil, fmt.Errorf("recyclarr: %s.custom_format_groups: not converted; list the formats under custom_formats", path)
	}
	for _, k := range []struct {
		n    yaml.Node
		name string
	}{{y.BaseURL, "base_url"}, {y.APIKey, "api_key"}, {y.MediaNaming, "media_naming"}, {y.MediaManagement, "media_management"}} {
		if set(k.n) {
			inst.Ignored = append(inst.Ignored, k.name)
		}
	}
	qd, err := y.QualityDefinition.convert(path)
	if err != nil {
		return nil, err
	}
	inst.QualityDefinition = qd
	if y.DeleteOldCustomFormats != nil {
		inst.DeleteOldCustomFormats = *y.DeleteOldCustomFormats
	}
	for i, c := range y.CustomFormats {
		e := CustomFormatEntry{TrashIDs: c.TrashIDs, Score: c.Score}
		if len(c.TrashIDs) == 0 {
			return nil, fmt.Errorf("recyclarr: %s.custom_formats[%d]: no trash_ids", path, i)
		}
		for _, a := range c.AssignScoresTo {
			if a.Name == "" {
				return nil, fmt.Errorf("recyclarr: %s.custom_formats[%d].assign_scores_to: a profile is named by name "+
					"(trash_id targets are not converted)", path, i)
			}
			e.AssignTo = append(e.AssignTo, AssignScore{Name: a.Name, Score: a.Score})
		}
		for _, a := range c.QualityProfiles {
			e.AssignTo = append(e.AssignTo, AssignScore{Name: a.Name, Score: a.Score})
		}
		inst.CustomFormats = append(inst.CustomFormats, e)
	}
	for i, q := range y.QualityProfiles {
		if q.Name == "" {
			return nil, fmt.Errorf("recyclarr: %s.quality_profiles[%d]: no name", path, i)
		}
		if q.QualitySort != "" {
			inst.Ignored = append(inst.Ignored, fmt.Sprintf("quality_profiles[%s].quality_sort", q.Name))
		}
		qd, err := q.QualityDefinition.convert(fmt.Sprintf("%s.quality_profiles[%s]", path, q.Name))
		if err != nil {
			return nil, err
		}
		p := QualityProfile{Name: q.Name, TrashID: q.TrashID, ScoreSet: q.ScoreSet, MinFormatScore: q.MinFormatScore,
			MinUpgradeFormatScore: q.MinUpgradeFormatScore, QualityDefinition: qd}
		if q.Upgrade != nil {
			p.UpgradeAllowed, p.UntilQuality, p.UntilScore = q.Upgrade.Allowed, q.Upgrade.UntilQuality, q.Upgrade.UntilScore
		}
		if r := q.ResetUnmatchedScores; r != nil {
			p.ResetUnmatched = r.Enabled == nil || *r.Enabled
			p.ResetExcept, p.ResetExceptPatterns = r.Except, r.ExceptPatterns
		}
		for _, x := range q.Qualities {
			pq := ProfileQuality{Name: x.Name, Enabled: x.Enabled == nil || *x.Enabled, Qualities: x.Qualities}
			p.Qualities = append(p.Qualities, pq)
		}
		inst.QualityProfiles = append(inst.QualityProfiles, p)
	}
	return inst, nil
}
