package decide

import (
	"context"
	"strings"

	"github.com/theatarr/theatarr/internal/decide/format"
	"github.com/theatarr/theatarr/internal/decide/profile"
	"github.com/theatarr/theatarr/internal/library/naming"
	"github.com/theatarr/theatarr/internal/netregex"
	"github.com/theatarr/theatarr/internal/policy"
)

type searchContextKey struct{}

// WithSearch marks movie search decisions (including automatic searches), so RSS-only specs are skipped.
// Series decisions carry their criteria in SeriesSearch instead.
func WithSearch(ctx context.Context) context.Context {
	return context.WithValue(WithParseMemo(ctx), searchContextKey{}, true)
}

func (e *Engine) minimumSeeders(key string) int {
	if ix := e.Policy.Indexers[key]; ix != nil {
		return ix.MinimumSeeders()
	}
	return 0 // TorrentSeedingSpecification skips indexers no longer configured.
}

// Port of Sonarr Organizer/FileNameBuilder.cs:399-439 @ cab419a, with its TitleRegex (line 49) and
// FileNameBuilderTokenEqualityComparer.cs:12-35. Rename-off and escaped tokens do not require a title.
var episodeTitleToken = netregex.MustCompile(`(?<escaped>\{\{|\}\})|\{(?<prefix>[- ._\[(]*)(?<token>(?:[a-z0-9]+)(?:(?<separator>[- ._]+)(?:[a-z0-9]+))?)(?::(?<customFormat>[ ,a-z0-9+-]+(?<![- ])))?(?<suffix>[- ._)\]]*)\}`, netregex.IgnoreCase)

func (e *Engine) requiresEpisodeTitle(t *Title, v Version, episodes []*Episode) bool {
	cfg := v.Naming
	if cfg == nil {
		n := v.Policy.Naming
		if n.FromArr != "" {
			return false // the caller has not loaded the arr naming snapshot
		}
		c := naming.DefaultSeriesConfig()
		c.Rename = true
		if n.File != "" {
			c.StandardEpisodeFormat = n.File
		}
		if n.Daily != "" {
			c.DailyEpisodeFormat = n.Daily
		}
		if n.Anime != "" {
			c.AnimeEpisodeFormat = n.Anime
		}
		cfg = &c
	}
	if !cfg.Rename {
		return false
	}
	pattern := cfg.StandardEpisodeFormat
	if t.SeriesType == "daily" {
		pattern = cfg.DailyEpisodeFormat
	}
	if t.SeriesType == "anime" {
		all := true
		for _, ep := range episodes {
			all = all && ep.Absolute > 0
		}
		if all {
			pattern = cfg.AnimeEpisodeFormat
		}
	}
	m, err := episodeTitleToken.FindStringMatch(pattern)
	for m != nil && err == nil {
		token := strings.ToLower(m.GroupByName("token").String())
		token = strings.Map(func(r rune) rune {
			if strings.ContainsRune("- ._", r) {
				return -1
			}
			return r
		}, token)
		if token == "episodetitle" || token == "episodecleantitle" {
			return true
		}
		m, err = episodeTitleToken.FindNextMatch(m)
	}
	return false
}

// EpisodeTitleHold reports the Sonarr import-time title hold for mapped episodes.
func (e *Engine) EpisodeTitleHold(t Title, v Version, episodes []Episode) (profile.Reason, string) {
	if t.Kind != policy.KindSeries || v.Policy == nil {
		return "", ""
	}
	p := e.Profile(v.Policy.Profile, format.Sonarr)
	if p == nil {
		return "", ""
	}
	media := &profile.Media{EpisodeTitleRequired: e.requiresEpisodeTitle(&t, v, nil)}
	for _, x := range t.Episodes {
		media.Episodes = append(media.Episodes, &profile.Episode{Title: x.Title, SeasonNumber: x.Season, EpisodeNumber: x.Number, AirDateUtc: x.AirDateUTC})
	}
	selected := make([]*profile.Episode, 0, len(episodes))
	for _, x := range episodes {
		selected = append(selected, &profile.Episode{Title: x.Title, SeasonNumber: x.Season, EpisodeNumber: x.Number, AirDateUtc: x.AirDateUTC})
	}
	s := &profile.Subject{Media: media, Episodes: selected}
	d := profile.EpisodeTitleCheck(p.Engine, s)
	return d.Reason, d.Message
}
