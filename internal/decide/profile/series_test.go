package profile

import (
	"slices"
	"testing"
	"time"

	"github.com/theatarr/theatarr/internal/decide/format"
)

// The cases of Sonarr's fixtures for the series specifications (NzbDrone.Core.Test/DecisionEngineTests @ cab419a),
// written as Go tables: FullSeasonSpecificationFixture, SameEpisodesSpecificationFixture,
// SingleEpisodeAgeDownloadDecisionFixture (SeasonPackOnly), MonitoredEpisodeSpecificationFixture,
// Search/SingleEpisodeSearchMatchSpecificationTests, plus the arr's messages for EpisodeRequested and SceneMapping.

var seriesNow = time.Date(2026, 10, 26, 12, 0, 0, 0, time.UTC)

func sonarrEngine() *Engine {
	return &Engine{Flavour: format.Sonarr, Now: func() time.Time { return seriesNow }}
}

func run(t *testing.T, name string, s *Subject) Decision {
	t.Helper()
	e := sonarrEngine()
	sp, ok := e.Spec(name)
	if !ok {
		t.Fatalf("no spec %s", name)
	}
	return e.Run(sp, s)
}

func aired(days float64) *time.Time {
	at := seriesNow.Add(time.Duration(days * float64(24*time.Hour)))
	return &at
}

func TestFullSeasonSpecification(t *testing.T) {
	cases := []struct {
		name   string
		full   bool
		dates  []*time.Time
		accept bool
	}{
		{"not a full season", false, []*time.Time{aired(10)}, true},
		{"all episodes have aired", true, []*time.Time{aired(-10), aired(-3)}, true},
		{"all will have aired in the next 24 hours", true, []*time.Time{aired(-10), aired(0.9)}, true},
		{"one episode has not aired", true, []*time.Time{aired(-10), aired(2)}, false},
		{"an episode has no air date", true, []*time.Time{aired(-10), nil}, false},
	}
	for _, c := range cases {
		s := &Subject{Release: &Release{FullSeason: c.full}}
		for i, d := range c.dates {
			s.Episodes = append(s.Episodes, &Episode{SeasonNumber: 1, EpisodeNumber: i + 1, AirDateUtc: d})
		}
		if got := run(t, "FullSeason", s); got.Accepted != c.accept {
			t.Errorf("%s: %+v", c.name, got)
		}
	}
}

func TestSameEpisodesGrabSpecification(t *testing.T) {
	e1, e2 := &Episode{ID: 1}, &Episode{ID: 2}
	both := &File{EpisodeIDs: []int64{1, 2}}
	cases := []struct {
		name     string
		episodes []*Episode
		files    []*File
		accept   bool
	}{
		{"fewer episodes than the file", []*Episode{e1}, []*File{both}, false},
		{"more episodes than the file", []*Episode{e1, e2}, []*File{{EpisodeIDs: []int64{1}}, nil}, true},
		{"the same episodes", []*Episode{e1, e2}, []*File{both, both}, true},
		{"the same episodes as several files", []*Episode{e1, e2}, []*File{{EpisodeIDs: []int64{1}}, {EpisodeIDs: []int64{2}}}, true},
	}
	for _, c := range cases {
		s := &Subject{Release: &Release{}}
		for i, ep := range c.episodes {
			x := *ep
			x.File = c.files[i]
			s.Episodes = append(s.Episodes, &x)
		}
		got := run(t, "SameEpisodesGrab", s)
		if got.Accepted != c.accept {
			t.Errorf("%s: %+v", c.name, got)
		}
		if !got.Accepted && got.Message != "Episode file on disk contains more episodes than this release contains" {
			t.Errorf("%s: message %q", c.name, got.Message)
		}
	}
}

func TestSeasonPackOnlySpecification(t *testing.T) {
	var eps []*Episode
	for i, age := range []float64{400, 370, 340, 310} {
		eps = append(eps, &Episode{SeasonNumber: 1, EpisodeNumber: i + 1, AirDateUtc: aired(-age)})
	}
	search := &Search{Kind: SearchSeason, SeasonNumber: 1, Episodes: eps}
	media := &Media{SeriesType: "standard"}
	single := []struct {
		episode, age int
		accept       bool
	}{{1, 200, false}, {4, 200, false}, {1, 600, true}, {1, 365, true}, {4, 365, true}, {1, 0, true}}
	for _, c := range single {
		s := &Subject{Media: media, Search: search, Episodes: []*Episode{eps[c.episode-1]},
			Release: &Release{SeasonSearchMaximumSingleEpisodeAge: c.age}}
		if got := run(t, "SeasonPackOnly", s); got.Accepted != c.accept {
			t.Errorf("single episode %d, age %d: %+v", c.episode, c.age, got)
		}
	}
	for _, age := range []int{200, 600, 365, 0} {
		s := &Subject{Media: media, Search: search, Episodes: eps,
			Release: &Release{FullSeason: true, SeasonSearchMaximumSingleEpisodeAge: age}}
		if got := run(t, "SeasonPackOnly", s); !got.Accepted {
			t.Errorf("season pack, age %d: %+v", age, got)
		}
	}
	s := &Subject{Media: media, Search: search, Episodes: []*Episode{eps[0]},
		Release: &Release{SeasonSearchMaximumSingleEpisodeAge: 200}}
	if got := run(t, "SeasonPackOnly", s); got.Message != "Last episode in this season aired more than 200 days ago, season pack required." {
		t.Errorf("message %q", got.Message)
	}
}

func TestMonitoredEpisodeSpecification(t *testing.T) {
	ep := func(m bool) *Episode { return &Episode{Monitored: m} }
	cases := []struct {
		name      string
		series    bool
		episodes  []*Episode
		search    *Search
		accept    bool
		wantMsg   string
		wantRsn   Reason
		skipCheck bool
	}{
		{"monitored single", true, []*Episode{ep(true)}, nil, true, "", "", false},
		{"monitored multi", true, []*Episode{ep(true), ep(true)}, nil, true, "", "", false},
		{"series not monitored", false, []*Episode{ep(true), ep(true)}, nil, false, "Series is not monitored", ReasonSeriesNotMonitored, false},
		{"only episode not monitored", true, []*Episode{ep(false)}, nil, false, "Episode is not monitored", ReasonEpisodeNotMonitored, false},
		{"both not monitored", true, []*Episode{ep(false), ep(false)}, nil, false, "One or more episodes is not monitored", ReasonEpisodeNotMonitored, false},
		{"first not monitored", true, []*Episode{ep(false), ep(true)}, nil, false, "One or more episodes is not monitored", ReasonEpisodeNotMonitored, false},
		{"second not monitored", true, []*Episode{ep(true), ep(false)}, nil, false, "", "", true},
		{"single episode search", true, []*Episode{ep(false)}, &Search{Kind: SearchSingleEpisode}, true, "", "", false},
		{"season search, monitored", true, []*Episode{ep(true)}, &Search{Kind: SearchSeason}, true, "", "", false},
		{"season search, monitored only", true, []*Episode{ep(false)}, &Search{Kind: SearchSeason, MonitoredEpisodesOnly: true}, false, "", "", true},
		{"single search, flag false", true, []*Episode{ep(false)}, &Search{Kind: SearchSingleEpisode}, true, "", "", false},
		{"single search, flag true", true, []*Episode{ep(false)}, &Search{Kind: SearchSingleEpisode, MonitoredEpisodesOnly: true}, false, "", "", true},
	}
	for _, c := range cases {
		s := &Subject{Media: &Media{Monitored: c.series}, Episodes: c.episodes, Search: c.search, Release: &Release{}}
		got := run(t, "MonitoredEpisode", s)
		if got.Accepted != c.accept {
			t.Errorf("%s: %+v", c.name, got)
			continue
		}
		if !c.accept && !c.skipCheck && (got.Message != c.wantMsg || got.Reason != c.wantRsn) {
			t.Errorf("%s: %+v", c.name, got)
		}
	}
}

func TestSingleEpisodeSearchMatchSpecification(t *testing.T) {
	std := func(season int, eps []int, searchSeason int) *Subject {
		return &Subject{Release: &Release{SeasonNumber: season, EpisodeNumbers: eps},
			Search: &Search{Kind: SearchSingleEpisode, SeasonNumber: searchSeason, EpisodeNumber: 1}}
	}
	cases := []struct {
		name   string
		s      *Subject
		accept bool
		reason Reason
	}{
		{"season does not match", std(10, []int{1}, 5), false, ReasonWrongSeason},
		{"season matches after scene mapping (parsed 10, searched scene 10)", std(10, []int{1}, 10), true, ""},
		{"season does not match after scene mapping", std(10, []int{1}, 9), false, ReasonWrongSeason},
		{"full season for a single episode search", std(5, []int{}, 5), false, ReasonFullSeason},
		{"episode does not match", std(5, []int{2}, 5), false, ReasonWrongEpisode},
		{"the episode", std(5, []int{1}, 5), true, ""},
		{"anime: full season for an episode search", &Subject{Release: &Release{FullSeason: true},
			Search: &Search{Kind: SearchAnimeEpisode}}, false, ReasonFullSeason},
		{"anime: not a full season", &Subject{Release: &Release{}, Search: &Search{Kind: SearchAnimeEpisode}}, true, ""},
		{"anime: full season for a season search", &Subject{Release: &Release{FullSeason: true},
			Search: &Search{Kind: SearchAnimeSeason}}, true, ""},
	}
	for _, c := range cases {
		got := run(t, "SingleEpisodeSearchMatch", c.s)
		if got.Accepted != c.accept || got.Reason != c.reason {
			t.Errorf("%s: %+v", c.name, got)
		}
	}
}

func TestEpisodeRequestedAndSeasonMatch(t *testing.T) {
	e1 := &Episode{ID: 1, SeasonNumber: 2, EpisodeNumber: 3}
	e2 := &Episode{ID: 2, SeasonNumber: 2, EpisodeNumber: 4}
	e9 := &Episode{ID: 9, SeasonNumber: 2, EpisodeNumber: 9}
	search := &Search{Kind: SearchSeason, SeasonNumber: 2, Episodes: []*Episode{e9}}
	got := run(t, "EpisodeRequested", &Subject{Release: &Release{}, Search: search, Episodes: []*Episode{e2, e1}})
	if got.Accepted || got.Message != "Episode wasn't requested: 2x3-4" {
		t.Errorf("multi: %+v", got)
	}
	got = run(t, "EpisodeRequested", &Subject{Release: &Release{}, Search: search, Episodes: []*Episode{e1}})
	if got.Message != "Episode wasn't requested: 2x3" {
		t.Errorf("single: %+v", got)
	}
	if got := run(t, "EpisodeRequested", &Subject{Release: &Release{}, Search: search, Episodes: []*Episode{e1, e9}}); !got.Accepted {
		t.Errorf("one requested: %+v", got)
	}
	if got := run(t, "SeasonMatch", &Subject{Release: &Release{SeasonNumber: 3}, Search: search}); got.Reason != ReasonWrongSeason {
		t.Errorf("season match: %+v", got)
	}
	if got := run(t, "SceneMapping", &Subject{Release: &Release{}, SceneMapping: &SceneMapping{SceneOrigin: "mixed"}}); got.Message != "Ambiguous numbering" {
		t.Errorf("scene mapping: %+v", got)
	}
	if got := run(t, "SceneMapping", &Subject{Release: &Release{}, SceneMapping: &SceneMapping{SceneOrigin: "unknown:tvdb"}}); !got.Accepted {
		t.Errorf("scene mapping unknown: %+v", got)
	}
	if got := run(t, "MultiSeason", &Subject{Release: &Release{MultiSeason: true}}); got.Reason != ReasonMultiSeason {
		t.Errorf("multi season: %+v", got)
	}
	if got := run(t, "SplitEpisode", &Subject{Release: &Release{SplitEpisode: true}}); got.Reason != ReasonSplitEpisode {
		t.Errorf("split: %+v", got)
	}
}

func TestCompanionChecksKeepByRule(t *testing.T) {
	now := seriesNow
	web := MustQuality(format.Sonarr, "WEBDL-1080p")
	bluray := MustQuality(format.Sonarr, "Bluray-1080p")
	prof := func(upgrades bool) *Profile {
		return &Profile{
			Profile:        format.Profile{Name: "HD", Flavour: format.Sonarr, MinUpgradeFormatScore: 1},
			Items:          []Item{{Quality: &web, Allowed: true}, {Quality: &bluray, Allowed: true}},
			Cutoff:         bluray.ID,
			UpgradeAllowed: upgrades,
		}
	}
	qm := func(q Quality, ver int, repack bool) QualityModel {
		return QualityModel{Quality: q, Revision: Revision{Version: ver, IsRepack: repack}}
	}
	baseMedia := func(anime bool) *Media {
		return &Media{Title: "The Show", Runtime: 45, Monitored: true, Anime: anime}
	}
	baseSubject := func(upgrades, anime bool, relQ QualityModel, relGroup string, search *Search, eps []*Episode) *Subject {
		return &Subject{
			Profile:  prof(upgrades),
			Media:    baseMedia(anime),
			Release:  &Release{Quality: relQ, ReleaseGroup: relGroup, PublishDate: now},
			Episodes: eps,
			Search:   search,
		}
	}
	ep1 := func(f *File) *Episode {
		return &Episode{ID: 101, SeasonNumber: 1, EpisodeNumber: 1, Monitored: true, File: f}
	}
	ep2 := func(f *File) *Episode {
		return &Episode{ID: 102, SeasonNumber: 1, EpisodeNumber: 2, Monitored: true, File: f}
	}
	ep3 := func(f *File) *Episode {
		return &Episode{ID: 103, SeasonNumber: 1, EpisodeNumber: 3, Monitored: true, File: f}
	}

	type tc struct {
		name      string
		mode      ProperDownloadTypes
		spec      string
		build     func() *Subject
		wantAcc   bool
		wantRsn   Reason
		wantMsg   string
		wantKept  []int64
		offReason Reason
	}
	cases := []tc{
		{
			name: "a_upgrade_allowed_off_better_quality",
			mode: PreferAndUpgrade,
			spec: "UpgradeAllowed",
			build: func() *Subject {
				f := &File{Quality: qm(web, 1, false), ReleaseGroup: "GRP", Added: now}
				return baseSubject(false, false, qm(bluray, 1, false), "GRP", nil, []*Episode{ep1(f), ep2(nil)})
			},
			wantAcc:   true,
			wantKept:  []int64{101},
			offReason: ReasonQualityUpgradesDisabled,
		},
		{
			name: "b_repack_other_group",
			mode: PreferAndUpgrade,
			spec: "Repack",
			build: func() *Subject {
				f := &File{Quality: qm(web, 1, false), ReleaseGroup: "OLD", Added: now}
				return baseSubject(true, false, qm(web, 2, true), "NEW", nil, []*Episode{ep1(f), ep2(nil)})
			},
			wantAcc:   true,
			wantKept:  []int64{101},
			offReason: ReasonRepackReleaseGroupDoesNotMatch,
		},
		{
			name: "c_repack_unknown_held_group",
			mode: PreferAndUpgrade,
			spec: "Repack",
			build: func() *Subject {
				f := &File{Quality: qm(web, 1, false), ReleaseGroup: "", Added: now}
				return baseSubject(true, false, qm(web, 2, true), "NEW", nil, []*Episode{ep1(f), ep2(nil)})
			},
			wantAcc:   true,
			wantKept:  []int64{101},
			offReason: ReasonRepackUnknownReleaseGroup,
		},
		{
			name: "d_repack_do_not_upgrade",
			mode: DoNotUpgrade,
			spec: "Repack",
			build: func() *Subject {
				f := &File{Quality: qm(web, 1, false), ReleaseGroup: "GRP", Added: now}
				return baseSubject(true, false, qm(web, 2, true), "GRP", nil, []*Episode{ep1(f), ep2(nil)})
			},
			wantAcc:   true,
			wantKept:  []int64{101},
			offReason: ReasonRepackDisabled,
		},
		{
			name: "e_proper_rss_old_file",
			mode: PreferAndUpgrade,
			spec: "Proper",
			build: func() *Subject {
				f := &File{Quality: qm(web, 1, false), ReleaseGroup: "GRP", Added: now.AddDate(0, 0, -8)}
				return baseSubject(true, false, qm(web, 2, false), "GRP", nil, []*Episode{ep1(f), ep2(nil)})
			},
			wantAcc:   true,
			wantKept:  []int64{101},
			offReason: ReasonProperForOldFile,
		},
		{
			name: "f_anime_v2_other_group",
			mode: PreferAndUpgrade,
			spec: "AnimeVersionUpgrade",
			build: func() *Subject {
				f := &File{Quality: qm(web, 1, false), ReleaseGroup: "OldSub", Added: now}
				return baseSubject(true, true, qm(web, 2, false), "NewSub", nil, []*Episode{ep1(f), ep2(nil)})
			},
			wantAcc:   true,
			wantKept:  []int64{101},
			offReason: ReasonReleaseGroupDoesNotMatch,
		},
		{
			name: "g_control_no_usable_episode",
			mode: PreferAndUpgrade,
			spec: "Repack",
			build: func() *Subject {
				f1 := &File{Quality: qm(web, 1, false), ReleaseGroup: "OLD", Added: now}
				f2 := &File{Quality: qm(bluray, 1, false), ReleaseGroup: "GRP", Added: now}
				return baseSubject(true, false, qm(web, 2, true), "NEW", nil, []*Episode{ep1(f1), ep2(f2)})
			},
			wantAcc:   false,
			wantRsn:   ReasonRepackReleaseGroupDoesNotMatch,
			wantMsg:   "Release is a repack for a different release group. Release Group: NEW. File release group: OLD",
			wantKept:  nil,
			offReason: ReasonRepackReleaseGroupDoesNotMatch,
		},
		{
			name: "h_e01_fails_e02_upgradable_and_passing",
			mode: PreferAndUpgrade,
			spec: "Repack",
			build: func() *Subject {
				f1 := &File{Quality: qm(web, 1, false), ReleaseGroup: "OLD", Added: now}
				f2 := &File{Quality: qm(web, 1, false), ReleaseGroup: "NEW", Added: now}
				return baseSubject(true, false, qm(web, 2, true), "NEW", nil, []*Episode{ep1(f1), ep2(f2)})
			},
			wantAcc:   true,
			wantKept:  []int64{101},
			offReason: ReasonRepackReleaseGroupDoesNotMatch,
		},
		{
			name: "j_control_proper_with_search_set",
			mode: PreferAndUpgrade,
			spec: "Proper",
			build: func() *Subject {
				f := &File{Quality: qm(web, 1, false), ReleaseGroup: "GRP", Added: now.AddDate(0, 0, -8)}
				eps := []*Episode{ep1(f), ep2(nil)}
				s := baseSubject(true, false, qm(web, 2, false), "GRP", &Search{Kind: SearchSeason, SeasonNumber: 1, Episodes: eps}, eps)
				s.Release.SeasonNumber = 1
				return s
			},
			wantAcc:  true,
			wantKept: nil,
		},
		{
			name: "shared_file_keeps_both_episodes",
			mode: PreferAndUpgrade,
			spec: "Repack",
			build: func() *Subject {
				f := &File{Quality: qm(web, 1, false), ReleaseGroup: "OLD", Added: now}
				return baseSubject(true, false, qm(web, 2, true), "NEW", nil, []*Episode{ep1(f), ep2(f), ep3(nil)})
			},
			wantAcc:   true,
			wantKept:  []int64{101, 102},
			offReason: ReasonRepackReleaseGroupDoesNotMatch,
		},
	}

	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			e := &Engine{
				Flavour: format.Sonarr,
				Config:  Config{DownloadPropersAndRepacks: c.mode, AcceptReleaseAnyUpgradable: true},
				Now:     func() time.Time { return now },
			}
			s := c.build()
			sp, ok := e.Spec(c.spec)
			if !ok {
				t.Fatalf("no spec %s", c.spec)
			}
			got := e.Run(sp, s)
			if got.Accepted != c.wantAcc || got.Reason != c.wantRsn || (c.wantMsg != "" && got.Message != c.wantMsg) {
				t.Errorf("any on (%s): got %+v, want acc=%v rsn=%s msg=%q", c.spec, got, c.wantAcc, c.wantRsn, c.wantMsg)
			}
			if c.wantAcc {
				if rjs := e.Evaluate(s); len(rjs) != 0 {
					t.Errorf("any on Evaluate: unexpected rejections %v", rjs)
				}
			}
			if gotKept := e.KeptByRule(s); !slices.Equal(gotKept, c.wantKept) {
				t.Errorf("KeptByRule = %v, want %v", gotKept, c.wantKept)
			}

			// (i, control) Any rule off: results as today, KeptByRule empty.
			eOff := &Engine{
				Flavour: format.Sonarr,
				Config:  Config{DownloadPropersAndRepacks: c.mode, AcceptReleaseAnyUpgradable: false},
				Now:     func() time.Time { return now },
			}
			sOff := c.build()
			gotOff := eOff.Run(sp, sOff)
			if gotOff.Reason != c.offReason {
				t.Errorf("any off (%s): got %+v, want reason %s", c.spec, gotOff, c.offReason)
			}
			if gotKept := eOff.KeptByRule(sOff); len(gotKept) != 0 {
				t.Errorf("any off KeptByRule = %v, want empty", gotKept)
			}
		})
	}

	// (i, control) Radarr flavour for the three checks Radarr has (UpgradeAllowed, Repack, Proper): results as today
	// even if AcceptReleaseAnyUpgradable is set.
	radWeb := MustQuality(format.Radarr, "WEBDL-1080p")
	radBluray := MustQuality(format.Radarr, "Bluray-1080p")
	radProf := func(upgrades bool) *Profile {
		return &Profile{
			Profile:        format.Profile{Name: "HD", Flavour: format.Radarr, MinUpgradeFormatScore: 1},
			Items:          []Item{{Quality: &radWeb, Allowed: true}, {Quality: &radBluray, Allowed: true}},
			Cutoff:         radBluray.ID,
			UpgradeAllowed: upgrades,
			Language:       format.LanguageAny,
		}
	}
	for _, rc := range []struct {
		name    string
		spec    string
		mode    ProperDownloadTypes
		upgr    bool
		file    *File
		relQ    QualityModel
		relGrp  string
		wantRsn Reason
	}{
		{"upgrade_allowed", "UpgradeAllowed", PreferAndUpgrade, false,
			&File{Quality: qm(radWeb, 1, false), ReleaseGroup: "GRP", Added: now}, qm(radBluray, 1, false), "GRP", ReasonQualityUpgradesDisabled},
		{"repack", "Repack", PreferAndUpgrade, true,
			&File{Quality: qm(radWeb, 1, false), ReleaseGroup: "OLD", Added: now}, qm(radWeb, 2, true), "NEW", ReasonRepackReleaseGroupDoesNotMatch},
		{"proper", "Proper", PreferAndUpgrade, true,
			&File{Quality: qm(radWeb, 1, false), ReleaseGroup: "GRP", Added: now.AddDate(0, 0, -8)}, qm(radWeb, 2, false), "GRP", ReasonProperForOldFile},
	} {
		t.Run("i_control_radarr_"+rc.name, func(t *testing.T) {
			e := &Engine{
				Flavour: format.Radarr,
				Config:  Config{DownloadPropersAndRepacks: rc.mode, AcceptReleaseAnyUpgradable: true},
				Now:     func() time.Time { return now },
			}
			s := &Subject{
				Profile: radProf(rc.upgr),
				Media:   &Media{Title: "The Movie", Runtime: 110, MovieFile: rc.file},
				Release: &Release{Quality: rc.relQ, ReleaseGroup: rc.relGrp, PublishDate: now},
			}
			sp, ok := e.Spec(rc.spec)
			if !ok {
				t.Fatalf("no radarr spec %s", rc.spec)
			}
			if got := e.Run(sp, s); got.Reason != rc.wantRsn {
				t.Errorf("radarr %s: got %+v, want %s", rc.spec, got, rc.wantRsn)
			}
			if gotKept := e.KeptByRule(s); len(gotKept) != 0 {
				t.Errorf("radarr KeptByRule = %v, want empty", gotKept)
			}
		})
	}
}
