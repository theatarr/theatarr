package library

import (
	"fmt"

	"github.com/theatarr/theatarr/internal/library/naming"
	"github.com/theatarr/theatarr/internal/policy"
)

// VersionNaming is how one version names its files.
type VersionNaming struct {
	Config naming.Config
	// Source is "policy".
	Source string
}

var colonByName = map[string]naming.ColonReplacement{"delete": naming.ColonDelete, "dash": naming.ColonDash,
	"space_dash": naming.ColonSpaceDash, "space_dash_space": naming.ColonSpaceDashSpace, "smart": naming.ColonSmart}

var multiByName = map[string]naming.MultiEpisodeStyle{"extend": naming.MultiExtend, "duplicate": naming.MultiDuplicate,
	"repeat": naming.MultiRepeat, "scene": naming.MultiScene, "range": naming.MultiRange,
	"prefixed_range": naming.MultiPrefixedRange}

// NamingFor returns a version's naming: the policy's templates over the flavour's defaults with renaming on.
func NamingFor(pol *policy.Policy, version string) (*VersionNaming, error) {
	vp := pol.Versions[version]
	if vp == nil {
		return nil, fmt.Errorf("library: unknown version %q", version)
	}
	n := vp.Naming
	var cfg naming.Config
	if vp.Kind == policy.KindMovie {
		cfg = naming.DefaultMovieConfig()
		if n.File != "" {
			cfg.StandardMovieFormat = n.File
		}
		if n.Folder != "" {
			cfg.MovieFolderFormat = n.Folder
		}
	} else {
		cfg = naming.DefaultSeriesConfig()
		set := func(dst *string, v string) {
			if v != "" {
				*dst = v
			}
		}
		set(&cfg.StandardEpisodeFormat, n.File)
		set(&cfg.SeriesFolderFormat, n.Folder)
		set(&cfg.SeasonFolderFormat, n.Season)
		set(&cfg.SpecialsFolderFormat, n.Specials)
		set(&cfg.DailyEpisodeFormat, n.Daily)
		set(&cfg.AnimeEpisodeFormat, n.Anime)
		if n.MultiEpisode != "" {
			cfg.MultiEpisodeStyle = multiByName[n.MultiEpisode]
		}
	}
	cfg.Rename = true
	if n.Colon != "" {
		cfg.ColonReplacement = colonByName[n.Colon]
	}
	return &VersionNaming{Config: cfg, Source: "policy"}, nil
}
