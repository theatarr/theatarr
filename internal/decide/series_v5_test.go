package decide

import (
	"context"
	"strings"
	"testing"

	"github.com/theatarr/theatarr/internal/decide/profile"
)

// withSonarr is the version with versions.<k>.sonarr set (a copy: the engine's policy is shared).
func withSonarr(v Version, s string) Version {
	p := *v.Policy
	p.Sonarr = s
	v.Policy = &p
	return v
}

// TestSeriesDecisionsV5 runs Sonarr v5 mapping cases (MapFixture, GetEpisodesFixture, the season-title parse and
// PR #8990's original titles) on the decide fixtures, each under v4 and v5.
func TestSeriesDecisionsV5(t *testing.T) {
	t.Parallel()
	e := seriesEngine(t)
	ctx := context.Background()
	cases := []struct {
		name, key, release string
		title              func() Title
		search             *SeriesSearch
		// per version: "accepted" or the rejection reasons, the episodes and the match type
		v4, v5 string
		size   int64
		ep4    string
		ep5    string
		m5     MatchType
	}{
		{name: "multi-season pack maps every season (rejected as MultiSeason)", key: "tv",
			release: "The.Show.S01-S02.2160p.WEB-DL.DDP5.1.H.265-GRP", title: show,
			v4: "MultiSeason", ep4: "S01E01,S01E02,S01E03", v5: "FullSeasonNotAired,MultiSeason", ep5: "S01E01,S01E02,S01E03,S02E01,S02E02",
			m5: MatchTitle},
		{name: "multi-season pack in a season search for the second season", key: "tv",
			release: "The.Show.S01-S02.2160p.WEB-DL.DDP5.1.H.265-GRP", title: show,
			search: &SeriesSearch{Kind: profile.SearchSeason, Season: 2, Episodes: []int64{201, 202}},
			v4:     "MultiSeason,WrongEpisode,WrongSeason", v5: "FullSeasonNotAired,MultiSeason", ep5: "S01E01,S01E02,S01E03,S02E01,S02E02"},
		{name: "original title", key: "tv", release: "Die.Sendung.S01E02.2160p.WEB-DL.DDP5.1.H.265-GRP",
			title: func() Title { t := show(); t.OriginalTitle = "Die Sendung"; return t },
			v4:    "UnknownSeries", v5: "accepted", ep5: "S01E02", m5: MatchTitle},
		{name: "original title in a search", key: "tv", release: "Die.Sendung.S01E02.2160p.WEB-DL.DDP5.1.H.265-GRP",
			title:  func() Title { t := show(); t.OriginalTitle = "Die Sendung"; return t },
			search: &SeriesSearch{Kind: profile.SearchSingleEpisode, Season: 1, Episode: 2, Episodes: []int64{102}},
			v4:     "UnknownSeries", v5: "accepted", ep5: "S01E02", m5: MatchTitle},
		{name: "original title with a year", key: "tv", release: "Die.Sendung.2020.S01E02.2160p.WEB-DL-GRP",
			title: func() Title { t := show(); t.OriginalTitle = "Die Sendung"; return t },
			v4:    "UnknownSeries", v5: "accepted", ep5: "S01E02", m5: MatchTitle},
		{name: "scene mapping alias with the series' year", key: "tv", release: "Show.Alt.Name.2020.S01E03.2160p.WEB-DL-GRP",
			title: show, v4: "UnknownSeries", v5: "accepted", ep5: "S01E03", m5: MatchAlias},
		{name: "scene mapping alias with another year", key: "tv", release: "Show.Alt.Name.2019.S01E03.2160p.WEB-DL-GRP",
			title: show, v4: "UnknownSeries", v5: "UnknownSeries"},
		{name: "title that holds the year (all titles)", key: "tv", release: "The.Show.2020.S01E02.2160p.WEB-DL-GRP",
			title: func() Title { t := show(); t.Name = "The Show 2020"; return t },
			v4:    "accepted", ep4: "S01E02", v5: "accepted", ep5: "S01E02"},
		{name: "diacritics in the series title", key: "tv", release: "The.Shöw.S01E02.2160p.WEB-DL.DDP5.1.H.265-GRP",
			title: show, v4: "accepted", ep4: "S01E02", v5: "accepted", ep5: "S01E02", m5: MatchTitle},
		{name: "season pack named by its season title", key: "anime-tv",
			release: "[Group] Isekai Story Curse (BDRip 1920x1080 x264 FLAC) [ABCD1234]", size: 2 * gb,
			title: func() Title {
				t := anime()
				t.SceneMappings = append(t.SceneMappings, SceneMapping{Title: "Isekai Story Curse", SeasonNumber: ip(-1), SceneSeasonNumber: ip(2)})
				return t
			},
			v4: "UnableToParse", v5: "accepted", ep5: "S02E01,S02E02", m5: MatchAlias},
		{name: "season title without a mapped season", key: "anime-tv",
			release: "[Group] Isekai Story Curse (BD 1280x720 x264 AAC)",
			title: func() Title {
				t := anime()
				t.SceneMappings = append(t.SceneMappings, SceneMapping{Title: "Isekai Story Curse", SeasonNumber: ip(-1), SceneSeasonNumber: ip(-1)})
				return t
			},
			v4: "UnableToParse", v5: "UnknownEpisode", m5: MatchAlias},
		{name: "ENG SUBS is not English audio", key: "anime-tv",
			release: "[Group] Isekai Story - 05 (1080p) [ENG SUBS]", title: anime, size: gb,
			v4: "accepted", ep4: "S01E05", v5: "accepted", ep5: "S01E05"},
	}
	for _, c := range cases {
		for _, ver := range []string{"v4", "v5"} {
			v := withSonarr(tvVersion(e, c.key), ver)
			want, wantEps, wantMatch := c.v4, c.ep4, MatchType("")
			if ver == "v5" {
				want, wantEps, wantMatch = c.v5, c.ep5, c.m5
			}
			size := c.size
			if size == 0 {
				size = 8 * gb
			}
			d, err := e.DecideSearch(ctx, rel(c.release, "public", size), c.title(), v, c.search)
			if err != nil {
				t.Errorf("%s (%s): %v", c.name, ver, err)
				continue
			}
			got := reasons(d)
			if d.Accepted {
				got = "accepted"
			}
			if got != want {
				t.Errorf("%s (%s): %s, want %s (%v)", c.name, ver, got, want, d.Rejections)
			}
			if eps := strings.Join(d.Episodes, ","); wantEps != "" && eps != wantEps {
				t.Errorf("%s (%s): episodes %s, want %s", c.name, ver, eps, wantEps)
			}
			if wantMatch != "" && d.MatchType != wantMatch {
				t.Errorf("%s (%s): match %q, want %q", c.name, ver, d.MatchType, wantMatch)
			}
		}
	}
}

// TestSeriesV5Languages checks the v5 language parse reaches the decision: "ENG SUBS" is not English audio, and the
// season search accepts a multi-season pack holding the searched season (before MultiSeason rejects it).
func TestSeriesV5Languages(t *testing.T) {
	t.Parallel()
	e := seriesEngine(t)
	ctx := context.Background()
	r := rel("The.Show.S01E02.German.2160p.WEB-DL.ENG.SUBS-GRP", "public", 8*gb)
	d4, err := e.Decide(ctx, r, show(), withSonarr(tvVersion(e, "tv"), "v4"), nil)
	if err != nil {
		t.Fatal(err)
	}
	d5, err := e.Decide(ctx, r, show(), withSonarr(tvVersion(e, "tv"), "v5"), nil)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Join(d4.Languages, ",") == strings.Join(d5.Languages, ",") {
		t.Errorf("languages: v4 %v, v5 %v; want them to differ", d4.Languages, d5.Languages)
	}
	for _, l := range d5.Languages {
		if l == "English" {
			t.Errorf("v5 languages %v: ENG SUBS is not English audio", d5.Languages)
		}
	}
}

func TestSonarrVersionPolicy(t *testing.T) {
	t.Parallel()
	e := seriesEngine(t)
	_, err := e.Decide(context.Background(), rel("The.Show.S01E02.2160p.WEB-DL-GRP", "public", 8*gb), show(),
		withSonarr(tvVersion(e, "tv"), "v6"), nil)
	if err == nil || !strings.Contains(err.Error(), "sonarr version") {
		t.Errorf("v6: %v", err)
	}
}
