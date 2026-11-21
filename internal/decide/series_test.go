package decide

import (
	"cmp"
	"context"
	"encoding/json"
	"fmt"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/theatarr/theatarr/internal/decide/profile"
	"github.com/theatarr/theatarr/internal/indexer"
	animemeta "github.com/theatarr/theatarr/internal/metadata/anime"
	"github.com/theatarr/theatarr/internal/metadata/anime/animemap"
	"github.com/theatarr/theatarr/internal/policy"
)

var seriesClock = time.Date(2026, 10, 26, 0, 0, 0, 0, time.UTC)

func seriesEngine(t *testing.T) *Engine {
	t.Helper()
	pol, err := policy.Load("testdata/series")
	if err != nil {
		t.Fatalf("policy: %v", err)
	}
	e, err := New(pol, Options{Now: func() time.Time { return seriesClock }})
	if err != nil {
		t.Fatalf("build: %v", err)
	}
	return e
}

func day(s string) *time.Time {
	t, err := time.Parse("2006-01-02", s)
	if err != nil {
		panic(err)
	}
	t = t.Add(2 * time.Hour)
	return &t
}

func ip(n int) *int { return &n }

// show is a standard series: S01 (3 episodes, aired), S02E01 aired, S02E02 airs next year; S00E01 a special.
func show() Title {
	t := Title{ID: 1, Kind: policy.KindSeries, Name: "The Show", Year: 2020, TVDB: 1001, IMDb: "tt1001",
		SeriesType: "standard", Runtime: 45, Monitored: true, OriginalLanguage: "English",
		SceneMappings: []SceneMapping{{Title: "Show Alt Name", SeasonNumber: ip(-1)}}}
	add := func(id int64, s, n int, aired string, title string) {
		e := Episode{ID: id, Season: s, Number: n, Title: title, Runtime: 45, Monitored: true}
		if aired != "" {
			e.AirDate, e.AirDateUTC = aired, day(aired)
		}
		t.Episodes = append(t.Episodes, e)
	}
	add(100, 0, 1, "2020-03-01", "The Making Of")
	add(101, 1, 1, "2020-04-01", "Pilot")
	add(102, 1, 2, "2020-04-08", "Second")
	add(103, 1, 3, "2020-04-15", "Third")
	add(201, 2, 1, "2026-01-01", "Return")
	add(202, 2, 2, "2027-01-01", "Future")
	return t
}

func ep(t *Title, id int64) *Episode {
	for i := range t.Episodes {
		if t.Episodes[i].ID == id {
			return &t.Episodes[i]
		}
	}
	panic("no episode")
}

func tvVersion(e *Engine, key string) Version {
	return Version{ID: 9, Key: key, Policy: e.Policy.Versions[key]}
}

func TestSeriesDecisions(t *testing.T) {
	t.Parallel()
	e := seriesEngine(t)
	ctx := context.Background()
	v := tvVersion(e, "tv")
	webFile := &ExistingFile{Quality: "WEBDL-2160p", Revision: 1, SceneName: "The.Show.S01E01.2160p.WEB-DL-GRP",
		ReleaseGroup: "GRP", Size: 8 * gb, ReleaseType: "singleEpisode"}
	remuxFile := &ExistingFile{Quality: "Bluray-2160p Remux", Revision: 1, SceneName: "The.Show.S01E03.2160p.BluRay.REMUX.HDR-GRP",
		ReleaseGroup: "GRP", Size: 30 * gb, ReleaseType: "singleEpisode"}

	cases := []struct {
		name     string
		release  string
		size     int64
		tvdb     int
		setup    func(*Title)
		search   *SeriesSearch
		accepted bool
		reasons  string
		episodes string
		match    MatchType
		score    int
		upgrade  bool
	}{
		{name: "single episode, no file", release: "The.Show.S01E02.2160p.WEB-DL.DDP5.1.H.265-GRP", size: 8 * gb,
			accepted: true, episodes: "S01E02", match: MatchTitle},
		{name: "season pack over a WEB file", release: "The.Show.S01.2160p.UHD.BluRay.x265.HDR-GRP", size: 60 * gb,
			setup: func(t *Title) { ep(t, 101).File, ep(t, 101).FileID = webFile, 1 }, accepted: true,
			episodes: "S01E01,S01E02,S01E03", match: MatchTitle, score: 510, upgrade: true},
		{name: "season pack, one episode at cutoff", release: "The.Show.S01.2160p.UHD.BluRay.x265.HDR-GRP", size: 60 * gb,
			setup: func(t *Title) {
				ep(t, 101).File, ep(t, 101).FileID = webFile, 1
				ep(t, 103).File, ep(t, 103).FileID = remuxFile, 2
			}, accepted: true, upgrade: true, episodes: "S01E01,S01E02,S01E03", score: 510},
		{name: "season pack not aired", release: "The.Show.S02.2160p.WEB-DL.DDP5.1.H.265-GRP", size: 20 * gb,
			reasons: "FullSeasonNotAired", episodes: "S02E01,S02E02", score: 10},
		{name: "unknown series", release: "Another.Show.S01E01.2160p.WEB-DL-GRP", size: 8 * gb, reasons: "UnknownSeries"},
		{name: "unknown episode", release: "The.Show.S05E01.2160p.WEB-DL-GRP", size: 8 * gb, reasons: "UnknownEpisode"},
		{name: "scene mapping alias", release: "Show.Alt.Name.S01E03.2160p.WEB-DL-GRP", size: 8 * gb, accepted: true,
			episodes: "S01E03", match: MatchAlias},
		{name: "TVDB id from the indexer", release: "Totally.Different.S01E03.2160p.WEB-DL-GRP", size: 8 * gb,
			tvdb: 1001, accepted: true, episodes: "S01E03", match: MatchID},
		{name: "existing file has more episodes", release: "The.Show.S01E01.2160p.UHD.BluRay.x265-GRP", size: 20 * gb,
			setup: func(t *Title) {
				f := *webFile
				f.EpisodeIDs = []int64{101, 102}
				ep(t, 101).File, ep(t, 101).FileID = &f, 5
				ep(t, 102).File, ep(t, 102).FileID = &f, 5
			}, reasons: "ExistingFileHasMoreEpisodes", episodes: "S01E01"},
		{name: "season pack for a single episode search", release: "The.Show.S01.2160p.UHD.BluRay.x265-GRP", size: 60 * gb,
			search:  &SeriesSearch{Kind: profile.SearchSingleEpisode, Season: 1, Episode: 2, Episodes: []int64{102}},
			reasons: "FullSeason", episodes: "S01E01,S01E02,S01E03", match: MatchTitle, score: 10},
		{name: "episode not requested", release: "The.Show.S01E03.2160p.WEB-DL-GRP", size: 8 * gb,
			search:  &SeriesSearch{Kind: profile.SearchSeason, Season: 1, Episodes: []int64{102}},
			reasons: "WrongEpisode", episodes: "S01E03"},
		{name: "special by episode title", release: "The.Show.The.Making.Of.2160p.WEB-DL-GRP", size: 4 * gb,
			accepted: true, episodes: "S00E01"},
		{name: "unmonitored episode on RSS", release: "The.Show.S01E02.2160p.WEB-DL-GRP", size: 8 * gb,
			setup: func(t *Title) { ep(t, 102).Monitored = false }, reasons: "EpisodeNotMonitored", episodes: "S01E02"},
		{name: "unparseable", release: "Some random words", size: gb, reasons: "UnableToParse"},
	}
	for _, c := range cases {
		tt := show()
		if c.setup != nil {
			c.setup(&tt)
		}
		r := rel(c.release, "public", c.size)
		r.TVDB = c.tvdb
		d, err := e.DecideSearch(ctx, r, tt, v, c.search)
		if err != nil {
			t.Errorf("%s: %v", c.name, err)
			continue
		}
		if d.Accepted != c.accepted || (c.reasons != "" && reasons(d) != c.reasons) {
			t.Errorf("%s: accepted %v, reasons %s (want %s)", c.name, d.Accepted, reasons(d), c.reasons)
		}
		if got := strings.Join(d.Episodes, ","); c.episodes != "" && got != c.episodes {
			t.Errorf("%s: episodes %s, want %s", c.name, got, c.episodes)
		}
		if c.match != "" && d.MatchType != c.match {
			t.Errorf("%s: match %q, want %q", c.name, d.MatchType, c.match)
		}
		if c.score != 0 && d.Score != c.score {
			t.Errorf("%s: score %d (%s), want %d", c.name, d.Score, formatNames(d.Formats), c.score)
		}
		if d.Upgrade != c.upgrade {
			t.Errorf("%s: upgrade %v (%s)", c.name, d.Upgrade, d.UpgradeReason)
		}
	}
	// A release with no size is accepted: one a person added by hand (nothing reports its size) as one from a search.
	for _, ix := range []string{policy.ManualIndexer, "public"} {
		d, err := e.DecideSearch(ctx, rel("The.Show.S01E02.2160p.WEB-DL.DDP5.1.H.265-GRP", ix, 0), show(), v, nil)
		if err != nil {
			t.Fatal(err)
		}
		if !d.Accepted {
			t.Errorf("no size from %q: rejected, reasons %s", ix, reasons(d))
		}
	}
}

// anime is an anime series with absolute and scene numbering: TVDB has S01 (1-12) and S02 (13-14); scene numbering
// puts S02 as scene season 2 episodes 1-2, and a scene mapping names scene season 2.
func anime() Title {
	t := Title{ID: 2, Kind: policy.KindSeries, Name: "Isekai Story", Year: 2021, TVDB: 2002, SeriesType: "anime",
		UseSceneNumbering: true, Runtime: 24, Monitored: true, OriginalLanguage: "Japanese",
		SceneMappings: []SceneMapping{
			{Title: "Isekai Story S2", SceneSeasonNumber: ip(2)},
			{Title: "Isekai Monogatari", SceneSeasonNumber: ip(-1)},
		}}
	for n := 1; n <= 14; n++ {
		s, num := 1, n
		if n > 12 {
			s, num = 2, n-12
		}
		d := time.Date(2021, 1, 1, 15, 0, 0, 0, time.UTC).AddDate(0, 0, 7*n)
		e := Episode{ID: int64(300 + n), Season: s, Number: num, Absolute: n, Title: "Episode", Runtime: 24, Monitored: true,
			AirDate: d.Format("2006-01-02"), AirDateUTC: &d, SceneSeason: ip(s), SceneNumber: ip(num), SceneAbsolute: ip(n)}
		t.Episodes = append(t.Episodes, e)
	}
	return t
}

func TestAnimeDecisions(t *testing.T) {
	t.Parallel()
	e := seriesEngine(t)
	ctx := context.Background()
	v := tvVersion(e, "anime-tv")
	cases := []struct {
		name, release string
		accepted      bool
		reasons       string
		episodes      string
		match         MatchType
	}{
		{"absolute number", "[SubsPlease] Isekai Story - 13 (1080p) [ABCD1234].mkv", true, "", "S02E01", MatchTitle},
		{"absolute batch", "[Group] Isekai Story - 01-03 (BD 1080p)", true, "", "S01E01,S01E02,S01E03", MatchTitle},
		{"scene mapping with a season", "[SubsPlease] Isekai Story S2 - 02 (1080p) [ABCD1234].mkv", true, "", "S02E02", MatchAlias},
		{"alias for every season", "[Group] Isekai Monogatari - 05 (1080p)", true, "", "S01E05", MatchAlias},
		{"season pack", "[Group] Isekai Story S01 (BD 1080p) [Dual Audio]", true, "", "", MatchTitle},
		{"unknown absolute", "[SubsPlease] Isekai Story - 40 (1080p) [ABCD1234].mkv", false, "UnknownEpisode", "", ""},
	}
	for _, c := range cases {
		d, err := e.Decide(ctx, rel(c.release, "public", 2*gb), anime(), v, nil)
		if err != nil {
			t.Errorf("%s: %v", c.name, err)
			continue
		}
		if d.Accepted != c.accepted || (c.reasons != "" && reasons(d) != c.reasons) {
			t.Errorf("%s: accepted %v, reasons %s (%v)", c.name, d.Accepted, reasons(d), d.Rejections)
		}
		if got := strings.Join(d.Episodes, ","); c.episodes != "" && got != c.episodes {
			t.Errorf("%s: episodes %s, want %s", c.name, got, c.episodes)
		}
		if c.match != "" && d.MatchType != c.match {
			t.Errorf("%s: match %q, want %q", c.name, d.MatchType, c.match)
		}
	}
	// A season pack maps the whole season, and ranks above single episodes with the arr's comparer.
	pack, _ := e.Decide(ctx, rel("[Group] Isekai Story S01 (BD 1080p) [Dual Audio]", "public", 20*gb), anime(), v, nil)
	single, _ := e.Decide(ctx, rel("[Group] Isekai Story - 01 (BD 1080p)", "public", 2*gb), anime(), v, nil)
	if len(pack.Episodes) != 12 || pack.ReleaseType != "seasonPack" {
		t.Errorf("pack: %d episodes, %s", len(pack.Episodes), pack.ReleaseType)
	}
	if r := Rank([]*Decision{single, pack}); r[0] != pack {
		t.Errorf("rank: %s first", r[0].Release)
	}
}

func TestDailyDecision(t *testing.T) {
	t.Parallel()
	e := seriesEngine(t)
	d1 := time.Date(2024, 3, 5, 23, 0, 0, 0, time.UTC)
	tt := Title{ID: 3, Kind: policy.KindSeries, Name: "Late Talk", Year: 2015, TVDB: 3003, SeriesType: "daily",
		Runtime: 45, Monitored: true, Episodes: []Episode{{ID: 401, Season: 2024, Number: 40, AirDate: "2024-03-05",
			AirDateUTC: &d1, Runtime: 45, Monitored: true}}}
	d, err := e.Decide(context.Background(), rel("Late.Talk.2024.03.05.Guest.Name.2160p.WEB-DL-GRP", "public", 4*gb), tt,
		tvVersion(e, "tv"), nil)
	if err != nil {
		t.Fatal(err)
	}
	if !d.Accepted || strings.Join(d.Episodes, ",") != "S2024E40" {
		t.Errorf("daily: %v %s %v", d.Accepted, d.Episodes, d.Rejections)
	}
	// A daily-style title is not valid for a standard series.
	tt.SeriesType = "standard"
	d, _ = e.Decide(context.Background(), rel("Late.Talk.2024.03.05.Guest.Name.2160p.WEB-DL-GRP", "public", 4*gb), tt,
		tvVersion(e, "tv"), nil)
	if reasons(d) != "UnknownEpisode" {
		t.Errorf("daily on standard: %s", reasons(d))
	}
}

func TestLevenshtein(t *testing.T) {
	t.Parallel()
	for _, c := range []struct {
		a, b string
		want int
	}{{"", "abc", 30}, {"abc", "", 3}, {"kitten", "sitting", 30}, {"abcd", "abd", 1}, {"abd", "abcd", 10}, {"same", "same", 0}} {
		if got := levenshtein(c.a, c.b, 10, 1, 10); got != c.want {
			t.Errorf("%q %q: %d, want %d", c.a, c.b, got, c.want)
		}
	}
}

// The season pack rule (the fork's ACCEPT_RELEASE_ANY_UPGRADABLE, every series version's): the S01 pack over a WEB
// file (E01) and a remux at its best (E03), E02 with no file, is taken, as it upgrades E01 and fills E02. A pack that
// upgrades nothing and fills nothing is refused, with the first file's reason. An accepted pack lists the episodes
// whose file it keeps (Kept); none when it upgrades every file, when its episodes have no file, or when it is refused.
func TestSeasonPackRule(t *testing.T) {
	t.Parallel()
	e := seriesEngine(t)
	ctx := context.Background()
	webFile := &ExistingFile{Quality: "WEBDL-2160p", Revision: 1, SceneName: "The.Show.S01E01.2160p.WEB-DL-GRP",
		ReleaseGroup: "GRP", Size: 8 * gb, ReleaseType: "singleEpisode"}
	remuxFile := &ExistingFile{Quality: "Bluray-2160p Remux", Revision: 1, SceneName: "The.Show.S01E03.2160p.BluRay.REMUX.HDR-GRP",
		ReleaseGroup: "GRP", Size: 30 * gb, ReleaseType: "singleEpisode"}
	pack := rel("The.Show.S01.2160p.UHD.BluRay.x265.HDR-GRP", "public", 60*gb)
	for _, c := range []struct {
		name     string
		files    map[int64]*ExistingFile
		accepted bool
		reason   string
		upgrade  string
		kept     string
	}{
		{"mixed", map[int64]*ExistingFile{101: webFile, 103: remuxFile}, true, "",
			"better quality: Bluray-2160p over WEBDL-2160p; fills 1 episode(s) with no file; 1 episode(s) keep their file",
			"S01E03"},
		{"fills only", map[int64]*ExistingFile{101: remuxFile, 103: remuxFile}, true, "",
			"fills 1 episode(s) with no file; 2 episode(s) keep their file", "S01E01,S01E03"},
		{"every file at its best", map[int64]*ExistingFile{101: remuxFile, 102: remuxFile, 103: remuxFile}, false,
			"DiskHigherPreference", "", ""},
		{"every file upgraded", map[int64]*ExistingFile{101: webFile, 102: webFile, 103: webFile}, true, "",
			"better quality: Bluray-2160p over WEBDL-2160p", ""},
		{"no file", nil, true, "", "", ""},
	} {
		tt := show()
		for id, f := range c.files {
			ep(&tt, id).File, ep(&tt, id).FileID = f, id
		}
		d, err := e.Decide(ctx, pack, tt, tvVersion(e, "tv"), nil)
		if err != nil {
			t.Fatalf("%s: %v", c.name, err)
		}
		if d.Accepted != c.accepted || (c.reason != "" && (len(d.Rejections) == 0 || d.Rejections[0].Reason != c.reason)) {
			t.Errorf("%s: accepted %v %v", c.name, d.Accepted, d.Rejections)
		}
		if c.accepted && c.upgrade != "" && (!d.Upgrade || d.UpgradeReason != c.upgrade) {
			t.Errorf("%s: upgrade %v %q, want %q", c.name, d.Upgrade, d.UpgradeReason, c.upgrade)
		}
		if got := strings.Join(d.Kept, ","); got != c.kept {
			t.Errorf("%s: kept %q, want %q", c.name, got, c.kept)
		}
	}

	// Kept by companion rule (upgrades off, E01 at the profile's first allowed quality): E01 is kept both ways
	// (UpgradeAllowed and UpgradeDisk), counted once in Kept, never quoted as upgraded, and left out of
	// DownloadEpisodes while EpisodeIDs still holds it. When the same pack is rejected for its size,
	// DownloadEpisodes returns every episode (an override routes the whole release).
	pol, err := policy.Load("testdata/series")
	if err != nil {
		t.Fatalf("policy: %v", err)
	}
	no := false
	pol.Profiles["tv"].Upgrades = &no
	eNoUp, err := New(pol, Options{Now: func() time.Time { return seriesClock }})
	if err != nil {
		t.Fatalf("build upgrades-off engine: %v", err)
	}
	tt := show()
	ep(&tt, 101).File, ep(&tt, 101).FileID = webFile, 101
	d, err := eNoUp.Decide(ctx, pack, tt, tvVersion(eNoUp, "tv"), nil)
	if err != nil {
		t.Fatalf("upgrades off pack: %v", err)
	}
	if !d.Accepted {
		t.Fatalf("upgrades off pack: expected accepted, got rejections %v", d.Rejections)
	}
	if !slices.Equal(d.Kept, []string{"S01E01"}) {
		t.Errorf("upgrades off pack: kept %v, want [S01E01]", d.Kept)
	}
	wantReason := "fills 2 episode(s) with no file; 1 episode(s) keep their file"
	if !d.Upgrade || d.UpgradeReason != wantReason {
		t.Errorf("upgrades off pack: upgrade=%v reason=%q, want %q", d.Upgrade, d.UpgradeReason, wantReason)
	}
	if !slices.Equal(d.EpisodeIDs, []int64{101, 102, 103}) {
		t.Errorf("upgrades off pack: EpisodeIDs %v, want [101 102 103]", d.EpisodeIDs)
	}
	if got := d.DownloadEpisodes(); !slices.Equal(got, []int64{102, 103}) {
		t.Errorf("upgrades off pack: DownloadEpisodes %v, want [102 103]", got)
	}

	tiny := rel("The.Show.S01.2160p.UHD.BluRay.x265.HDR-GRP", "public", 1024)
	dRej, err := eNoUp.Decide(ctx, tiny, tt, tvVersion(eNoUp, "tv"), nil)
	if err != nil {
		t.Fatalf("tiny pack: %v", err)
	}
	if dRej.Accepted || !strings.Contains(reasons(dRej), "BelowMinimumSize") {
		t.Fatalf("tiny pack: accepted=%v reasons=%s, want BelowMinimumSize", dRej.Accepted, reasons(dRej))
	}
	if !slices.Equal(dRej.EpisodeIDs, []int64{101, 102, 103}) {
		t.Errorf("tiny pack: EpisodeIDs %v, want [101 102 103]", dRej.EpisodeIDs)
	}
	if got := dRej.DownloadEpisodes(); !slices.Equal(got, []int64{101, 102, 103}) {
		t.Errorf("tiny pack: DownloadEpisodes %v, want [101 102 103]", got)
	}

	// Kept by companion rule alone (repack of another group over E01, while UpgradeDisk would treat v2 over v1 as
	// an upgrade): E01 is still in Kept, never quoted in UpgradeReason, and left out of DownloadEpisodes.
	ttRepack := show()
	ep(&ttRepack, 101).File, ep(&ttRepack, 101).FileID = webFile, 101
	repackPack := rel("The.Show.S01.REPACK.2160p.WEB-DL-OTHER", "public", 30*gb)
	dRepack, err := e.Decide(ctx, repackPack, ttRepack, tvVersion(e, "tv"), nil)
	if err != nil {
		t.Fatalf("repack pack: %v", err)
	}
	if !dRepack.Accepted || !slices.Equal(dRepack.Kept, []string{"S01E01"}) {
		t.Fatalf("repack pack: accepted=%v kept=%v rjs=%v", dRepack.Accepted, dRepack.Kept, dRepack.Rejections)
	}
	if !dRepack.Upgrade || dRepack.UpgradeReason != wantReason {
		t.Errorf("repack pack: upgrade=%v reason=%q, want %q", dRepack.Upgrade, dRepack.UpgradeReason, wantReason)
	}
	if got := dRepack.DownloadEpisodes(); !slices.Equal(got, []int64{102, 103}) {
		t.Errorf("repack pack: DownloadEpisodes %v, want [102 103]", got)
	}
}

// TestDefaultSeriesLibraryRules: a series library with no parser or pack settings reads an anime episode range with
// a subgroup as the whole range (v4plus) and accepts it.
func TestDefaultSeriesLibraryRules(t *testing.T) {
	t.Parallel()
	e := seriesEngine(t)
	d, err := e.Decide(context.Background(), rel("[PMR] The Show S01E01-03 (BD Remux 2160p HEVC FLAC)", "public", 60*gb),
		show(), tvVersion(e, "tv"), nil)
	if err != nil {
		t.Fatal(err)
	}
	if !d.Accepted || strings.Join(d.Episodes, ",") != "S01E01,S01E02,S01E03" {
		t.Errorf("S01E01-03: accepted %v %v, episodes %v", d.Accepted, d.Rejections, d.Episodes)
	}
}

// ended is a standard series with three aired seasons of two episodes and a special.
func ended() Title {
	t := Title{ID: 4, Kind: policy.KindSeries, Name: "The Show", Year: 2018, TVDB: 1004, SeriesType: "standard",
		Runtime: 45, Monitored: true, OriginalLanguage: "English"}
	t.Episodes = append(t.Episodes, Episode{ID: 500, Season: 0, Number: 1, Title: "Extra", Runtime: 45, Monitored: true,
		AirDate: "2018-01-01", AirDateUTC: day("2018-01-01")})
	for s := 1; s <= 3; s++ {
		for n := 1; n <= 2; n++ {
			aired := fmt.Sprintf("%d-03-%02d", 2017+s, n)
			t.Episodes = append(t.Episodes, Episode{ID: int64(500 + 10*s + n), Season: s, Number: n, Title: "Episode",
				Runtime: 45, Monitored: true, AirDate: aired, AirDateUTC: day(aired)})
		}
	}
	return t
}

// Multi-season and complete-series packs: a proven span maps to every episode of its seasons (no specials); a span
// that is not proven, or an anime series, keeps the arr's rejection.
func TestMultiSeasonPacks(t *testing.T) {
	t.Parallel()
	e := seriesEngine(t)
	ctx := context.Background()
	v := tvVersion(e, "tv")
	all := "S01E01,S01E02,S02E01,S02E02,S03E01,S03E02"
	cases := []struct {
		name, release string
		setup         func(*Title)
		search        *SeriesSearch
		reasons       string
		episodes      string
		seasons       []int
	}{
		{name: "range", release: "The.Show.S01-S03.2160p.WEB-DL.DDP5.1.H.265-GRP", episodes: all, seasons: []int{1, 2, 3}},
		{name: "season words", release: "The Show Season 2-3 2160p WEB-DL DDP5.1 H.265-GRP", episodes: "S02E01,S02E02,S03E01,S03E02"},
		{name: "list in steps of one", release: "The.Show.S01.S02.S03.2160p.WEB-DL.DDP5.1.H.265-GRP", episodes: all},
		{name: "list with a gap", release: "The.Show.S01.S03.2160p.WEB-DL.DDP5.1.H.265-GRP", reasons: "MultiSeason"},
		{name: "past the last season", release: "The.Show.S01-S05.2160p.WEB-DL.DDP5.1.H.265-GRP", reasons: "MultiSeason"},
		{name: "complete series", release: "The.Show.Complete.Series.2160p.WEB-DL.DDP5.1.H.265-GRP", episodes: all,
			seasons: []int{1, 2, 3}},
		{name: "the complete series", release: "The.Show.The.Complete.Series.2160p.WEB-DL.DDP5.1.H.265-GRP", episodes: all},
		{name: "all seasons", release: "The Show (2018) All Seasons 2160p WEB-DL DDP5.1 H.265-GRP", episodes: all},
		{name: "season search inside the span", release: "The.Show.S01-S03.2160p.WEB-DL.DDP5.1.H.265-GRP",
			search: &SeriesSearch{Kind: profile.SearchSeason, Season: 2, Episodes: []int64{521, 522}}, episodes: all},
		{name: "season search outside the span", release: "The.Show.S02-S03.2160p.WEB-DL.DDP5.1.H.265-GRP",
			search:  &SeriesSearch{Kind: profile.SearchSeason, Season: 1, Episodes: []int64{511, 512}},
			reasons: "WrongEpisode,WrongSeason"},
		{name: "single-episode search", release: "The.Show.S01-S03.2160p.WEB-DL.DDP5.1.H.265-GRP",
			search:  &SeriesSearch{Kind: profile.SearchSingleEpisode, Season: 2, Episode: 1, Episodes: []int64{521}},
			reasons: "FullSeason"},
		{name: "an unmonitored season on RSS", release: "The.Show.S01-S03.2160p.WEB-DL.DDP5.1.H.265-GRP",
			setup: func(t *Title) { ep(t, 532).Monitored = false }, reasons: "EpisodeNotMonitored"},
		{name: "range with to", release: "The.Show.S01.to.S03.2160p.WEB-DL.DDP5.1.H.265-GRP",
			search: &SeriesSearch{Kind: profile.SearchSeason, Season: 2, Episodes: []int64{521, 522}}, episodes: all},
		{name: "range with spaced to", release: "The Show S01 to S03 2160p WEB-DL DDP5.1 H.265-GRP",
			search: &SeriesSearch{Kind: profile.SearchSeason, Season: 2, Episodes: []int64{521, 522}}, episodes: all},
		{name: "one season", release: "The.Show.S01.2160p.WEB-DL.DDP5.1.H.265-GRP", episodes: "S01E01,S01E02"},
		{name: "year before the phrase", release: "The.Show.2018.Complete.Series.2160p.WEB-DL.DDP5.1.H.265-GRP",
			episodes: all, seasons: []int{1, 2, 3}},
		{name: "year before the collection", release: "The.Show.2018.The.Complete.Collection.2160p.WEB-DL.DDP5.1.H.265-GRP",
			episodes: all},
		{name: "year after the phrase", release: "The.Show.Complete.Series.2018.2160p.WEB-DL.DDP5.1.H.265-GRP", episodes: all},
		{name: "an unaired episode", release: "The.Show.Complete.Series.2160p.WEB-DL.DDP5.1.H.265-GRP",
			setup: func(t *Title) { ep(t, 532).AirDateUTC = nil }, reasons: "FullSeasonNotAired"},
	}
	for _, c := range cases {
		tt := ended()
		if c.setup != nil {
			c.setup(&tt)
		}
		d, err := e.DecideSearch(ctx, rel(c.release, "public", 40*gb), tt, v, c.search)
		if err != nil {
			t.Fatalf("%s: %v", c.name, err)
		}
		if got := reasons(d); got != c.reasons || d.Accepted != (c.reasons == "") {
			t.Errorf("%s: accepted %v, reasons %s, want %q", c.name, d.Accepted, got, c.reasons)
		}
		if got := strings.Join(d.Episodes, ","); c.episodes != "" && got != c.episodes {
			t.Errorf("%s: episodes %s, want %s", c.name, got, c.episodes)
		}
		if c.seasons != nil && (d.Season == nil || *d.Season != c.seasons[0] || d.ReleaseType != "seasonPack") {
			t.Errorf("%s: season %v, type %s", c.name, d.Season, d.ReleaseType)
		}
	}
	// A single-season pack still ranks above a span at equal quality and score.
	one, _ := e.Decide(ctx, rel("The.Show.S02.2160p.WEB-DL.DDP5.1.H.265-GRP", "public", 15*gb), ended(), v, nil)
	span, _ := e.Decide(ctx, rel("The.Show.S01-S03.2160p.WEB-DL.DDP5.1.H.265-GRP", "public", 40*gb), ended(), v, nil)
	if r := Rank([]*Decision{span, one}); r[0] != one {
		t.Errorf("rank: %s first", r[0].Release)
	}
	// A daily episode search rejects a span as a single-episode search rejects a season pack.
	d1, d2 := time.Date(2023, 3, 5, 23, 0, 0, 0, time.UTC), time.Date(2024, 3, 5, 23, 0, 0, 0, time.UTC)
	daily := Title{ID: 3, Kind: policy.KindSeries, Name: "Late Talk", Year: 2015, TVDB: 3003, SeriesType: "daily",
		Runtime: 45, Monitored: true, Episodes: []Episode{
			{ID: 401, Season: 2023, Number: 40, AirDate: "2023-03-05", AirDateUTC: &d1, Runtime: 45, Monitored: true},
			{ID: 402, Season: 2024, Number: 40, AirDate: "2024-03-05", AirDateUTC: &d2, Runtime: 45, Monitored: true}}}
	dailyPack := rel("Late.Talk.Complete.Series.2160p.WEB-DL-GRP", "public", 8*gb)
	if d, _ := e.Decide(ctx, dailyPack, daily, v, nil); !d.Accepted || strings.Join(d.Episodes, ",") != "S2023E40,S2024E40" {
		t.Errorf("daily complete series: %v %s %s", d.Accepted, d.Episodes, reasons(d))
	}
	d, _ := e.DecideSearch(ctx, dailyPack, daily, v, &SeriesSearch{Kind: profile.SearchDaily, Season: 2024, Episode: 40,
		Episodes: []int64{402}})
	if reasons(d) != "FullSeason" {
		t.Errorf("daily episode search: %s", reasons(d))
	}
	// Anime keeps today's rule: a multi-season batch is rejected, a complete-series name does not parse.
	for name, want := range map[string]string{"Isekai.Story.S01-S02.1080p.BluRay.x264-GRP": "MultiSeason",
		"Isekai.Story.Complete.Series.1080p.BluRay.x264-GRP":      "UnableToParse",
		"Isekai.Story.2021.Complete.Series.1080p.BluRay.x264-GRP": "UnknownEpisode"} { // as today
		d, err := e.Decide(ctx, rel(name, "public", 20*gb), anime(), tvVersion(e, "anime-tv"), nil)
		if err != nil {
			t.Fatal(err)
		}
		if !strings.Contains(reasons(d), want) || d.Accepted {
			t.Errorf("anime %s: %v %s", name, d.Accepted, reasons(d))
		}
	}
}

// An empty normalised name matches nothing: a series whose name cleans to nothing is not found in every release
// name, and a special whose title normalises to nothing is not found at the start of every release name.
// Japanese names keep their letters and still match.
func TestEmptyNamesMatchNothing(t *testing.T) {
	t.Parallel()
	empty := newSeries(&Title{Name: "!!!", Episodes: []Episode{{ID: 1, Season: 0, Number: 1, Title: "..."},
		{ID: 2, Season: 0, Number: 2, Title: "?!"}}})
	if empty.findByTitleInexact("Other.Show.S01E01.1080p.WEB-DL-GRP") {
		t.Error("an empty series name is found in every release name")
	}
	if empty.findByTitle("???") {
		t.Error("an empty series name equals another empty name")
	}
	empty.t.SceneMappings = []SceneMapping{{Title: "!!"}}
	if m := empty.mappingsFor("??"); len(m) != 0 {
		t.Errorf("a scene name that cleans to nothing matched another empty name: %v", m)
	}
	if e := empty.findByEpisodeTitle(0, "Other.Show.Some.Special.1080p.WEB-DL-GRP"); e != nil {
		t.Errorf("an empty special title matched: episode %d", e.Number)
	}
	ja := newSeries(&Title{Name: "進撃の巨人", Episodes: []Episode{{ID: 3, Season: 0, Number: 1, Title: "悔いなき選択"}}})
	if !ja.findByTitle("進撃の巨人") || !ja.findByTitleInexact("進撃の巨人 S01E01 1080p WEB-DL") {
		t.Error("a Japanese series name no longer matches")
	}
	if e := ja.findByEpisodeTitle(0, "進撃の巨人 悔いなき選択 1080p WEB-DL"); e == nil || e.Number != 1 {
		t.Errorf("a Japanese special title: %v, want episode 1", e)
	}
}

// fourSeasons is ended() with a fourth season of two episodes (541, 542).
func fourSeasons() Title {
	t := ended()
	for n := 1; n <= 2; n++ {
		aired := fmt.Sprintf("2021-03-%02d", n)
		t.Episodes = append(t.Episodes, Episode{ID: int64(540 + n), Season: 4, Number: n, Title: "Episode", Runtime: 45,
			Monitored: true, AirDate: aired, AirDateUTC: day(aired)})
	}
	return t
}

// A search of the whole series (SearchSeries) takes only releases of two or more seasons that hold a searched
// season; in a merged search a pack of several seasons is for the searched seasons only.
func TestSeriesSearchCriteria(t *testing.T) {
	t.Parallel()
	e := seriesEngine(t)
	ctx := context.Background()
	v := tvVersion(e, "tv")
	whole := &SeriesSearch{Kind: profile.SearchSeries, Episodes: []int64{511, 512, 521, 522}, MonitoredOnly: true,
		Seasons: []int{1, 2}}
	for _, c := range []struct{ release, reasons string }{
		{"The.Show.S01-S03.2160p.WEB-DL.DDP5.1.H.265-GRP", ""},
		{"The.Show.Complete.Series.2160p.WEB-DL.DDP5.1.H.265-GRP", ""},
		{"The.Show.S02.2160p.WEB-DL.DDP5.1.H.265-GRP", "WrongSeason"},
		{"The.Show.S02E01.2160p.WEB-DL.DDP5.1.H.265-GRP", "WrongSeason"},
		{"The.Show.S03-S04.2160p.WEB-DL.DDP5.1.H.265-GRP", "WrongEpisode,WrongSeason"},
	} {
		d, err := e.DecideSearch(ctx, rel(c.release, "public", 40*gb), fourSeasons(), v, whole)
		if err != nil {
			t.Fatal(err)
		}
		if got := reasons(d); got != c.reasons || d.Accepted != (c.reasons == "") {
			t.Errorf("%s: accepted %v, reasons %s, want %q", c.release, d.Accepted, got, c.reasons)
		}
	}
	// Scope: a complete pack found while seasons 1 and 2 of four are searched is for those two, under the seasonless
	// criterion and under a member's season criterion alike; a single-season pack is not touched.
	member := &SeriesSearch{Kind: profile.SearchSeason, Season: 2, Episodes: []int64{521, 522}, MonitoredOnly: true,
		Seasons: []int{1, 2}}
	for _, ss := range []*SeriesSearch{whole, member} {
		d, _ := e.DecideSearch(ctx, rel("The.Show.Complete.Series.2160p.WEB-DL.DDP5.1.H.265-GRP", "public", 40*gb),
			fourSeasons(), v, ss)
		if got := fmt.Sprint(d.DownloadEpisodes()); !d.Accepted || got != "[511 512 521 522]" {
			t.Errorf("%s scope: %v %s %s", ss.Kind, d.Accepted, got, reasons(d))
		}
		if len(d.EpisodeIDs) != 8 {
			t.Errorf("%s: the whole release is still judged: %v", ss.Kind, d.EpisodeIDs)
		}
	}
	one, _ := e.DecideSearch(ctx, rel("The.Show.S02.2160p.WEB-DL.DDP5.1.H.265-GRP", "public", 15*gb), fourSeasons(), v, member)
	if got := fmt.Sprint(one.DownloadEpisodes()); got != "[521 522]" {
		t.Errorf("season pack: %s", got)
	}
	unscoped, _ := e.DecideSearch(ctx, rel("The.Show.S01-S03.2160p.WEB-DL.DDP5.1.H.265-GRP", "public", 40*gb), fourSeasons(),
		v, &SeriesSearch{Kind: profile.SearchSeason, Season: 2, Episodes: []int64{521, 522}, MonitoredOnly: true})
	if len(unscoped.DownloadEpisodes()) != 6 {
		t.Errorf("a search that is not merged keeps the whole span: %v", unscoped.DownloadEpisodes())
	}
}

// The ranking of a merged search: between full-season releases cache evidence first, then more scoped episodes;
// quality and score still come first, and the ranking without the flag is unchanged.
func TestRankMerged(t *testing.T) {
	t.Parallel()
	e := seriesEngine(t)
	ctx := context.Background()
	v := tvVersion(e, "tv")
	ss := &SeriesSearch{Kind: profile.SearchSeason, Season: 2, Episodes: []int64{521, 522}, MonitoredOnly: true,
		Seasons: []int{1, 2, 3}}
	decided := func(name string, cached bool, hash int) *Decision {
		r := rel(name, "public", 15*gb)
		r.InfoHash = fmt.Sprintf("%040x", hash)
		if cached {
			r.CachedOn = []string{"rd"}
		}
		d, err := e.DecideSearch(ctx, r, ended(), v, ss)
		if err != nil || !d.Accepted {
			t.Fatalf("%s: %v %s", name, err, reasons(d))
		}
		return d
	}
	const pack, span, hdrPack = "The.Show.S02.2160p.WEB-DL.DDP5.1.H.265-GRP", "The.Show.S01-S03.2160p.WEB-DL.DDP5.1.H.265-GRP",
		"The.Show.S02.2160p.WEB-DL.DDP5.1.HDR.H.265-GRP"
	first := func(merged bool, ds ...*Decision) string {
		if merged {
			return RankMerged(ds)[0].Release
		}
		return Rank(ds)[0].Release
	}
	for _, c := range []struct {
		name                string
		packCached, spanCac bool
		packName, want      string
		merged              bool
	}{
		{"cached season pack over an uncached span", true, false, pack, pack, true},
		{"span over a season pack, both cached", true, true, pack, span, true},
		{"span over a season pack, neither cached", false, false, pack, span, true},
		{"a higher score first", false, true, hdrPack, hdrPack, true},
		{"without the flag fewer episodes first", false, false, pack, pack, false},
		{"without the flag, both cached", true, true, pack, pack, false},
	} {
		p, s := decided(c.packName, c.packCached, 1), decided(span, c.spanCac, 2)
		if got := first(c.merged, s, p); got != c.want {
			t.Errorf("%s: %s first", c.name, got)
		}
		if got := first(c.merged, p, s); got != c.want {
			t.Errorf("%s (other order): %s first", c.name, got)
		}
	}
}

// A pack of several seasons accepted only because an unsearched season misses episodes fills or upgrades nothing
// in the searched seasons: a merged search rejects it.
func TestSeriesSearchScopeNeedsASearchedEpisode(t *testing.T) {
	t.Parallel()
	e := seriesEngine(t)
	v := tvVersion(e, "tv")
	tt := fourSeasons()
	for i := range tt.Episodes {
		if s := tt.Episodes[i].Season; s == 1 || s == 2 {
			tt.Episodes[i].FileID = tt.Episodes[i].ID
			tt.Episodes[i].File = &ExistingFile{Quality: "Bluray-2160p Remux", Revision: 1, SceneName: "The.Show.2160p.REMUX-X",
				Size: 30 * gb}
		}
	}
	member := &SeriesSearch{Kind: profile.SearchSeason, Season: 1, Episodes: []int64{511, 512}, MonitoredOnly: true, Seasons: []int{1, 2}}
	pack := rel("The.Show.Complete.Series.2160p.WEB-DL.DDP5.1.H.265-GRP", "public", 40*gb)
	d, err := e.DecideSearch(context.Background(), pack, tt, v, member)
	if err != nil || d.Accepted || d.Upgrade {
		t.Fatalf("accepted for unsearched seasons only: %v %s upgrade %v %v", d.Accepted, reasons(d), d.Upgrade, err)
	}
	member.Seasons = nil
	if d, _ := e.DecideSearch(context.Background(), pack, tt, v, member); !d.Accepted {
		t.Fatalf("without the merge the pack is accepted as today: %s", reasons(d))
	}
}

// The Why of a scoped pack names the searched seasons: searched seasons 1 and 2 without files and an upgradable file
// only in unsearched season 3 read as filling the searched episodes, not as an upgrade of the season 3 file; the
// pack is still judged whole.
func TestSeriesSearchScopedUpgradeReason(t *testing.T) {
	t.Parallel()
	e := seriesEngine(t)
	v := tvVersion(e, "tv")
	tt := fourSeasons()
	for i := range tt.Episodes {
		if tt.Episodes[i].Season == 3 {
			tt.Episodes[i].FileID = tt.Episodes[i].ID
			tt.Episodes[i].File = &ExistingFile{Quality: "WEBDL-1080p", Revision: 1, SceneName: "The.Show.1080p.WEB-DL-X",
				Size: 3 * gb}
		}
	}
	member := &SeriesSearch{Kind: profile.SearchSeason, Season: 1, Episodes: []int64{511, 512}, MonitoredOnly: true, Seasons: []int{1, 2}}
	pack := rel("The.Show.Complete.Series.2160p.WEB-DL.DDP5.1.H.265-GRP", "public", 40*gb)
	d, err := e.DecideSearch(context.Background(), pack, tt, v, member)
	if err != nil || !d.Accepted || !d.Upgrade || d.UpgradeReason != "fills 4 episode(s) with no file" {
		t.Fatalf("scoped: %v %s upgrade %v %q %v", d.Accepted, reasons(d), d.Upgrade, d.UpgradeReason, err)
	}
	if d.Existing != nil {
		t.Errorf("scoped: existing from an unsearched season: %+v", d.Existing)
	}
	// A rejected scoped pack is no upgrade.
	bad := rel("The.Show.Complete.Series.480p.WEB-DL.DDP5.1.H.264-GRP", "public", 4*gb)
	if d, err := e.DecideSearch(context.Background(), bad, tt, v, member); err != nil || d.Accepted || d.Upgrade {
		t.Fatalf("rejected scoped pack: %v %s upgrade %v %q %v", d.Accepted, reasons(d), d.Upgrade, d.UpgradeReason, err)
	}
	member.Seasons = nil
	if d, _ := e.DecideSearch(context.Background(), pack, tt, v, member); !d.Accepted || d.Existing == nil ||
		!strings.HasPrefix(d.UpgradeReason, "better quality: WEBDL-2160p over WEBDL-1080p") {
		t.Fatalf("without the merge the reason is the whole pack's: %v %q", d.Accepted, d.UpgradeReason)
	}
}

// The existing file of an accepted scoped pack is the one it upgrades in the searched seasons, not a file it keeps:
// seasons 2 and 3 searched, season 2's Remux kept, season 3's 1080p WEB upgraded.
func TestSeriesSearchScopedExistingIsUpgradedFile(t *testing.T) {
	t.Parallel()
	e := seriesEngine(t)
	v := tvVersion(e, "tv")
	tt := fourSeasons()
	for i := range tt.Episodes {
		ep := &tt.Episodes[i]
		switch ep.Season {
		case 2:
			ep.FileID, ep.File = ep.ID, &ExistingFile{Quality: "Bluray-2160p Remux", Revision: 1, SceneName: "The.Show.2160p.REMUX-X", Size: 30 * gb}
		case 3:
			ep.FileID, ep.File = ep.ID, &ExistingFile{Quality: "WEBDL-1080p", Revision: 1, SceneName: "The.Show.1080p.WEB-DL-X", Size: 3 * gb}
		}
	}
	member := &SeriesSearch{Kind: profile.SearchSeason, Season: 3, Episodes: []int64{531, 532}, MonitoredOnly: true, Seasons: []int{2, 3}}
	pack := rel("The.Show.Complete.Series.2160p.WEB-DL.DDP5.1.H.265-GRP", "public", 40*gb)
	d, err := e.DecideSearch(context.Background(), pack, tt, v, member)
	if err != nil || !d.Accepted || d.Existing == nil || d.Existing.Quality != "WEBDL-1080p" ||
		!strings.HasPrefix(d.UpgradeReason, "better quality: WEBDL-2160p over WEBDL-1080p") {
		t.Fatalf("scoped: %v %s existing %+v %q %v", d.Accepted, reasons(d), d.Existing, d.UpgradeReason, err)
	}
}

// A whole-series search of a scene-numbered series judges a span by the seasons its episodes map to: scene seasons
// 2 and 3 are TVDB seasons 1 and 2, so a search of TVDB season 1 takes it.
func TestSeriesSearchSceneNumberedSpan(t *testing.T) {
	t.Parallel()
	e := seriesEngine(t)
	tt := ended()
	tt.UseSceneNumbering = true
	for i := range tt.Episodes {
		if tt.Episodes[i].Season > 0 {
			tt.Episodes[i].SceneSeason, tt.Episodes[i].SceneNumber = ip(tt.Episodes[i].Season+1), ip(tt.Episodes[i].Number)
		}
	}
	whole := &SeriesSearch{Kind: profile.SearchSeries, Episodes: []int64{511, 512}, MonitoredOnly: true}
	d, err := e.DecideSearch(context.Background(), rel("The.Show.S02-S03.2160p.WEB-DL.DDP5.1.H.265-GRP", "public", 30*gb), tt,
		tvVersion(e, "tv"), whole)
	if err != nil || !d.Accepted || !slices.Contains(d.EpisodeIDs, 511) {
		t.Fatalf("scene span: %v %s %v %v", d.Accepted, reasons(d), d.Episodes, err)
	}
}

// kaiju is an ended anime series of three seasons (3, 3 and 4 episodes; ids 6s0n, absolute 1 to 10), all aired.
func kaiju() Title {
	t := Title{ID: 6, Kind: policy.KindSeries, Name: "Kaiju Tale", Year: 2019, TVDB: 6006, SeriesType: "anime",
		Runtime: 24, Monitored: true, OriginalLanguage: "Japanese"}
	abs := 0
	for s, n := range []int{3, 3, 4} {
		for i := 1; i <= n; i++ {
			abs++
			d := time.Date(2019, 1, 1, 15, 0, 0, 0, time.UTC).AddDate(0, 0, 7*abs)
			t.Episodes = append(t.Episodes, Episode{ID: int64(600 + 10*(s+1) + i), Season: s + 1, Number: i, Absolute: abs,
				Title: "Episode", Runtime: 24, Monitored: true, AirDate: d.Format("2006-01-02"), AirDateUTC: &d})
		}
	}
	return t
}

// kaijuMerged is a merged search's criteria for kaiju's seasons: a member's season query, or the query without a
// season.
func kaijuMerged(season int, seasons ...int) *SeriesSearch {
	ss := &SeriesSearch{Kind: profile.SearchSeries, MonitoredOnly: true, Seasons: seasons}
	if season > 0 {
		ss.Kind, ss.Season = profile.SearchAnimeSeason, season
	}
	for _, e := range kaiju().Episodes {
		if slices.Contains(seasons, e.Season) && (season == 0 || e.Season == season) {
			ss.Episodes = append(ss.Episodes, e.ID)
		}
	}
	return ss
}

// Anime packs of several seasons, read only under a search of several seasons: names that state the span and an
// absolute batch that fills whole seasons are spans of those seasons; a partial batch, two episodes across a season
// break, "S2 - 01-12", a span past the last season or on scene-numbered seasons are not. A one-season search and
// RSS decide every one of them as before.
func TestAnimeSpans(t *testing.T) {
	t.Parallel()
	e := seriesEngine(t)
	ctx := context.Background()
	v := tvVersion(e, "anime-tv")
	all := "S01E01,S01E02,S01E03,S02E01,S02E02,S02E03,S03E01,S03E02,S03E03,S03E04"
	whole := kaijuMerged(0, 1, 2, 3)
	for _, c := range []struct {
		name, release string
		search        *SeriesSearch
		setup         func(*Title)
		accepted      bool
		episodes      string
		download      int
	}{
		{name: "both sides marked", release: "[G] Kaiju Tale S01-S03 [BD 1080p]", search: whole, accepted: true, episodes: all},
		{name: "the plural", release: "[G] Kaiju Tale Seasons 1-3 [BD 1080p]", search: whole, accepted: true, episodes: all},
		{name: "a list", release: "[G] Kaiju Tale S1+S2+S3 [BD 1080p]", search: whole, accepted: true, episodes: all},
		{name: "season with plus", release: "[G] Kaiju Tale (Season 1+2) [BD 1080p]", search: kaijuMerged(0, 1, 2), accepted: true,
			episodes: "S01E01,S01E02,S01E03,S02E01,S02E02,S02E03"},
		{name: "a member's query", release: "[G] Kaiju Tale S01-S03 [BD 1080p]", search: kaijuMerged(2, 1, 2), accepted: true,
			episodes: all, download: 6},
		{name: "absolute batch", release: "[G] Kaiju Tale (01-10) [BD 1080p]", search: whole, accepted: true, episodes: all},
		{name: "absolute batch for a member", release: "[G] Kaiju Tale - 01-10 [BD 1080p]", search: kaijuMerged(1, 1, 2),
			accepted: true, episodes: all, download: 6},
		{name: "partial batch", release: "[G] Kaiju Tale (01-05) [BD 1080p]", search: whole},
		{name: "across a break", release: "[G] Kaiju Tale - 03-04 [BD 1080p]", search: whole},
		{name: "an episode range of one season", release: "[G] Kaiju Tale S2 - 01-03 [BD 1080p]", search: whole},
		{name: "past the last season", release: "[G] Kaiju Tale S01-S04 [BD 1080p]", search: whole},
		{name: "scene-numbered seasons", release: "[G] Kaiju Tale S01-S03 [BD 1080p]", search: whole,
			setup: func(t *Title) { ep(t, 621).SceneSeason, ep(t, 621).SceneNumber = ip(1), ip(4) }},
	} {
		tt := kaiju()
		if c.setup != nil {
			c.setup(&tt)
		}
		d, err := e.DecideSearch(ctx, rel(c.release, "public", 20*gb), tt, v, c.search)
		if err != nil {
			t.Fatalf("%s: %v", c.name, err)
		}
		if d.Accepted != c.accepted {
			t.Errorf("%s: accepted %v (%v), want %v", c.name, d.Accepted, d.Rejections, c.accepted)
			continue
		}
		if !c.accepted {
			continue
		}
		if got := strings.Join(d.Episodes, ","); got != c.episodes {
			t.Errorf("%s: episodes %s, want %s", c.name, got, c.episodes)
		}
		if want := cmp.Or(c.download, len(d.EpisodeIDs)); len(d.DownloadEpisodes()) != want {
			t.Errorf("%s: downloads %d episodes, want %d", c.name, len(d.DownloadEpisodes()), want)
		}
	}
	// A one-season search and RSS: as before.
	one := &SeriesSearch{Kind: profile.SearchAnimeSeason, Season: 1, Episodes: []int64{611, 612, 613}, MonitoredOnly: true}
	for _, name := range []string{"[G] Kaiju Tale S01-S03 [BD 1080p]", "[G] Kaiju Tale Seasons 1-3 [BD 1080p]",
		"[G] Kaiju Tale S1+S2+S3 [BD 1080p]"} {
		for _, ss := range []*SeriesSearch{one, nil} {
			d, err := e.DecideSearch(ctx, rel(name, "public", 20*gb), kaiju(), v, ss)
			if err != nil {
				t.Fatal(err)
			}
			if d.Accepted && len(d.Episodes) == 10 {
				t.Errorf("%s (search %v): taken as a pack of three seasons", name, ss != nil)
			}
		}
	}
}

// Every name shape of the anime reader through decide, under the query without a season: the span names are packs
// of their seasons with a seasons claim; the rest are not ("Seasons 1-3" is not absolute 1 to 3).
func TestAnimeSpanNames(t *testing.T) {
	t.Parallel()
	e := seriesEngine(t)
	ctx := context.Background()
	v := tvVersion(e, "anime-tv")
	whole := kaijuMerged(0, 1, 2, 3)
	for _, name := range []string{"S01-S03", "S1 - S3", "S01 to S03", "S01~S03", "Season 1 - Season 3", "Seasons 1-3",
		"Seasons 1 - 3", "Seasons 1 to 3", "Seasons 1~3", "Season 1-3", "S01-03", "S01.S02.S03", "S1+S2+S3",
		"(Season 1+2+3)"} {
		d, err := e.DecideSearch(ctx, rel("[G] Kaiju Tale "+name+" [BD 1080p]", "public", 20*gb), kaiju(), v, whole)
		if err != nil {
			t.Fatal(err)
		}
		if !d.Accepted || len(d.EpisodeIDs) != 10 || d.Pack == nil || d.Pack.Mode != PackSeasons ||
			!slices.Equal(d.Pack.Seasons, []int{1, 2, 3}) || len(d.Pack.Episodes) != 10 {
			t.Errorf("%q: accepted %v, %d episodes, claim %+v (%v)", name, d.Accepted, len(d.EpisodeIDs), d.Pack, d.Rejections)
		}
	}
	two := kaijuMerged(0, 1, 2)
	for _, name := range []string{"S1 & S2", "Seasons 1+2", "Season 1+2", "(Season 1+2)", "(Season 1+2+OVA+Specials)"} {
		d, err := e.DecideSearch(ctx, rel("[G] Kaiju Tale "+name+" [BD 1080p]", "public", 15*gb), kaiju(), v, two)
		if err != nil {
			t.Fatal(err)
		}
		if !d.Accepted || len(d.EpisodeIDs) != 6 || !d.Pack.Span() {
			t.Errorf("%q: accepted %v, %d episodes, claim %+v (%v)", name, d.Accepted, len(d.EpisodeIDs), d.Pack, d.Rejections)
		}
	}
	for _, name := range []string{"Season 2", "2nd Season", "S02", "Part 2", "Cour 2", "S01 Part 2", "S2 - 01-03", "S2 - 03",
		"S1 - 02", "Season 2 - 02", "S01+S03", "S03-S01", "S01E03-S02E01", "Complete", "Batch", "Complete Series",
		"(01-05)", "- 03-04"} {
		d, err := e.DecideSearch(ctx, rel("[G] Kaiju Tale "+name+" [BD 1080p]", "public", 5*gb), kaiju(), v, whole)
		if err != nil {
			t.Fatal(err)
		}
		if d.Pack.Span() || d.Accepted {
			t.Errorf("%q: accepted %v, claim %+v", name, d.Accepted, d.Pack)
		}
	}
}

// What an anime pack claims. An absolute batch that fills whole seasons claims its numbers and their episodes;
// one across a season break from a one-season search or RSS claims them too, with no span (section 9: its
// decision is today's, its import is proved). Explicit season numbers, a franchise-placed batch and a SeaDex feed
// result claim nothing; a narrowed span still claims the whole pack.
func TestAnimePackClaims(t *testing.T) {
	t.Parallel()
	e := seriesEngine(t)
	ctx := context.Background()
	v := tvVersion(e, "anime-tv")
	d, err := e.DecideSearch(ctx, rel("[G] Kaiju Tale (01-10) [BD 1080p]", "public", 20*gb), kaiju(), v, kaijuMerged(2, 1, 2))
	if err != nil {
		t.Fatal(err)
	}
	if c := d.Pack; !d.Accepted || c == nil || c.Mode != PackAbsolute || !slices.Equal(c.Seasons, []int{1, 2, 3}) ||
		len(c.Episodes) != 10 || len(c.Numbers) != 10 || c.Numbers[3] != (PackNumber{N: 4, Episode: 621}) {
		t.Fatalf("whole-season batch: accepted %v claim %+v", d.Accepted, c)
	}
	if len(d.DownloadEpisodes()) != 6 || d.EpisodeMapping() == nil || d.EpisodeMapping().Pack != d.Pack {
		t.Errorf("narrowed span: downloads %v, mapping %+v", d.DownloadEpisodes(), d.EpisodeMapping())
	}
	one := &SeriesSearch{Kind: profile.SearchAnimeSeason, Season: 1, Episodes: []int64{611, 612, 613}, MonitoredOnly: true}
	for _, ss := range []*SeriesSearch{one, nil} {
		d, err := e.DecideSearch(ctx, rel("[G] Kaiju Tale - 02-05 [BD 1080p]", "public", 5*gb), kaiju(), v, ss)
		if err != nil {
			t.Fatal(err)
		}
		if c := d.Pack; c == nil || c.Mode != PackAbsolute || len(c.Seasons) != 0 || len(c.Numbers) != 4 {
			t.Errorf("batch across a break (search %v): claim %+v", ss != nil, c)
		}
		if got := strings.Join(d.Episodes, ","); got != "S01E02,S01E03,S02E01,S02E02" {
			t.Errorf("batch across a break (search %v): episodes %s", ss != nil, got)
		}
	}
	for _, name := range []string{"[G] Kaiju Tale - 02-03 [BD 1080p]", "Kaiju.Tale.S01E03-S02E01.1080p.BluRay.x264-GRP",
		"[G] Kaiju Tale S01 [BD 1080p]"} {
		d, err := e.DecideSearch(ctx, rel(name, "public", 5*gb), kaiju(), v, nil)
		if err != nil {
			t.Fatal(err)
		}
		if d.Pack != nil {
			t.Errorf("%s: claim %+v", name, d.Pack)
		}
	}
	// Controls: a scene-numbered season pack whose episodes lie in two library seasons, and "S01 to S03" taken by a
	// one-season search, claim nothing (imported as on main).
	scene := kaiju()
	scene.UseSceneNumbering = true
	for i, id := range []int64{613, 621, 622, 623} {
		ep(&scene, id).SceneSeason, ep(&scene, id).SceneNumber = ip(2), ip(i+1)
	}
	for _, c := range []struct {
		name string
		tt   Title
		ss   *SeriesSearch
	}{
		{"[G] Kaiju Tale S02 [BD 1080p]", scene, nil},
		{"[G] Kaiju Tale S01 to S03 [BD 1080p]", kaiju(), one},
	} {
		d, err := e.DecideSearch(ctx, rel(c.name, "public", 20*gb), c.tt, v, c.ss)
		if err != nil {
			t.Fatal(err)
		}
		if d.Pack != nil {
			t.Errorf("%s: claim %+v", c.name, d.Pack)
		}
	}
	// Scene season offsets do not exclude an absolute batch: the numbers still map one to one.
	offset := kaiju()
	offset.UseSceneNumbering = true
	for i := range offset.Episodes {
		ep := &offset.Episodes[i]
		ep.SceneSeason, ep.SceneNumber, ep.SceneAbsolute = ip(ep.Season+1), ip(ep.Number), ip(ep.Absolute)
	}
	if d, err = e.DecideSearch(ctx, rel("[G] Kaiju Tale (01-10) [BD 1080p]", "public", 20*gb), offset, v, kaijuMerged(0, 1, 2, 3)); err != nil {
		t.Fatal(err)
	}
	if !d.Accepted || !d.Pack.Span() || len(d.Pack.Numbers) != 10 {
		t.Errorf("batch on scene-offset seasons: accepted %v, claim %+v (%v)", d.Accepted, d.Pack, d.Rejections)
	}
	// A batch the franchise map places is that engine's: no claim.
	franchised := kaiju()
	var all []animemeta.SE
	for _, ep := range franchised.Episodes {
		all = append(all, animemeta.SE{ep.Season, ep.Number})
	}
	franchised.AnimeMap = animemap.New(6006, []*animemap.Entry{{Franchise: "f", Key: "al:1", Title: "Kaiju Tale",
		Names: []string{"Kaiju Tale"}, Status: animemeta.StatusAuto, Episodes: all}}, nil)
	if d, err = e.DecideSearch(ctx, rel("[G] Kaiju Tale (01-10) [BD 1080p]", "public", 20*gb), franchised, v,
		kaijuMerged(0, 1, 2, 3)); err != nil {
		t.Fatal(err)
	}
	if d.Pack != nil || d.AnimeMapping != AnimeMapFranchise {
		t.Errorf("franchise-placed batch: mapping %q, claim %+v", d.AnimeMapping, d.Pack)
	}
	// The same entry unreviewed: Sonarr's numbering places the batch (a fallback), so it claims and is proved.
	franchised.AnimeMap.Entries[0].Status = animemeta.StatusConflict
	franchised.AnimeMap = animemap.New(6006, franchised.AnimeMap.Entries, nil)
	if d, err = e.DecideSearch(ctx, rel("[G] Kaiju Tale (01-10) [BD 1080p]", "public", 20*gb), franchised, v,
		kaijuMerged(0, 1, 2, 3)); err != nil {
		t.Fatal(err)
	}
	if d.Pack == nil || d.AnimeMapping != AnimeMapSonarr || d.EpisodeMapping() == nil || !d.EpisodeMapping().Fallback {
		t.Errorf("fallback batch: mapping %q %+v, claim %+v", d.AnimeMapping, d.EpisodeMapping(), d.Pack)
	}
	// The filter of the query without a season classifies as the decision does: the unreviewed entry's batch (Sonarr's
	// numbering) is a candidate, the reviewed entry's (the franchise engine's) is left out.
	if !AnimeWholeCandidate(ctx, rel("[G] Kaiju Tale (01-10) [BD 1080p]", "public", 20*gb).Release, &franchised, kaijuMerged(0, 1, 2, 3)) {
		t.Error("a batch placed by Sonarr's numbering is left out of the query without a season")
	}
	franchised.AnimeMap.Entries[0].Status = animemeta.StatusAuto
	franchised.AnimeMap = animemap.New(6006, franchised.AnimeMap.Entries, nil)
	if AnimeWholeCandidate(ctx, rel("[G] Kaiju Tale (01-10) [BD 1080p]", "public", 20*gb).Release, &franchised, kaijuMerged(0, 1, 2, 3)) {
		t.Error("a franchise-placed batch is kept by the filter of the query without a season")
	}
	sd := rel("[G] Kaiju Tale (01-10) [BD 1080p]", "public", 20*gb)
	sd.Kind = "seadex"
	if d, err = e.DecideSearch(ctx, sd, kaiju(), v, kaijuMerged(0, 1, 2, 3)); err != nil {
		t.Fatal(err)
	}
	if d.Pack != nil {
		t.Errorf("SeaDex feed result: claim %+v", d.Pack)
	}
}

// kaijuSeaDex is kaiju with one AniList entry per season (al:1 to al:3) and a configured prefer-on SeaDex view whose
// catalog maps each entry to its season, over the snapshots.
func kaijuSeaDex(snaps []indexer.SeaDexSnapshot) (Title, context.Context) {
	tt := kaiju()
	var entries []*animemap.Entry
	cover := map[int]*indexer.SeaDexEntryMapping{}
	for s := 1; s <= 3; s++ {
		en := &animemap.Entry{Franchise: "f", Key: fmt.Sprintf("al:%d", s), Title: fmt.Sprintf("Kaiju Tale %d", s),
			Status: animemeta.StatusAuto}
		for _, ep := range tt.Episodes {
			if ep.Season == s {
				en.Episodes = append(en.Episodes, animemeta.SE{ep.Season, ep.Number})
			}
		}
		entries = append(entries, en)
		cover[s] = &indexer.SeaDexEntryMapping{Title: 6, Series: true, Entry: s, Seasons: map[int]bool{s: true}}
	}
	tt.AnimeMap = animemap.New(6006, entries, nil)
	for i := range snaps {
		for j := range snaps[i].Torrents {
			_ = json.Unmarshal([]byte(`{"facts":{}}`), &snaps[i].Torrents[j])
		}
	}
	state := indexStanding(seaDexState{snapshots: snaps, titles: map[int64][]int{6: {1, 2, 3}}, series: map[int64]bool{6: true},
		cover: map[int64]map[int]*indexer.SeaDexEntryMapping{6: cover}, stored: true, configured: true})
	return tt, WithSeaDexTitle(context.WithValue(context.Background(), seaDexContext{}, state), 6)
}

// A span has SeaDex standing only for what SeaDex lists it for: the weakest across the episodes it is downloaded
// for, none when one of them is outside every entry actively listing it, Best only when it holds for every episode.
// So season 2's own pick keeps season 2 in a merged search; a span listed for all its seasons keeps its standing.
func TestAnimeSpanSeaDexStanding(t *testing.T) {
	t.Parallel()
	e := seriesEngine(t)
	vp := *e.Policy.Versions["anime-tv"]
	vp.PreferSeaDex = true
	v := Version{ID: 9, Key: "anime-tv", Policy: &vp}
	span, pick := strings.Repeat("a", 40), strings.Repeat("b", 40)
	whole := kaijuMerged(0, 1, 2, 3)
	type listing struct {
		pref int
		best bool
	}
	decided := func(listed map[int]listing) (*Decision, *Decision) {
		var snaps []indexer.SeaDexSnapshot
		for s := 1; s <= 3; s++ {
			snap := indexer.SeaDexSnapshot{AniList: s}
			if l, ok := listed[s]; ok {
				snap.Torrents = append(snap.Torrents, indexer.SeaDexStanding{InfoHash: span, IsBest: l.best, Preference: l.pref})
			}
			if s == 2 {
				snap.Torrents = append(snap.Torrents, indexer.SeaDexStanding{InfoHash: pick, IsBest: true, Preference: 2})
			}
			snaps = append(snaps, snap)
		}
		tt, ctx := kaijuSeaDex(snaps)
		r := rel("[G] Kaiju Tale S01-S03 [BD 1080p]", "public", 20*gb)
		r.InfoHash = span
		d, err := e.DecideSearch(ctx, r, tt, v, whole)
		if err != nil || !d.Accepted {
			t.Fatalf("span: %v %v", err, d.Rejections)
		}
		r2 := rel("[G] Kaiju Tale S02 [BD 1080p]", "public", 6*gb)
		r2.InfoHash = pick
		d2, err := e.DecideSearch(ctx, r2, tt, v, kaijuMerged(2, 1, 2, 3))
		if err != nil || !d2.Accepted {
			t.Fatalf("season 2's pick: %v %v", err, d2.Rejections)
		}
		return d, d2
	}
	d, d2 := decided(map[int]listing{1: {2, true}})
	if d.SeaDexPreference() != 0 || RankMerged([]*Decision{d, d2})[0] != d2 {
		t.Errorf("listed for season 1 only: standing %d, %s first", d.SeaDexPreference(), RankMerged([]*Decision{d, d2})[0].Release)
	}
	all, d2 := decided(map[int]listing{1: {2, true}, 2: {2, true}, 3: {2, true}})
	if all.SeaDexPreference() == 0 || RankMerged([]*Decision{all, d2})[0] != all {
		t.Errorf("listed for every season: standing %d, %s first", all.SeaDexPreference(), RankMerged([]*Decision{all, d2})[0].Release)
	}
	// Mixed ranks across the seasons: TestSpanSeaDexWeakest (the configured view rescores each entry's torrents).
}

// A span's standing over raw listings: the lowest among the episodes it is downloaded for (SpanSeaDexRelease).
func TestSpanSeaDexWeakest(t *testing.T) {
	t.Parallel()
	hash := strings.Repeat("a", 40)
	tt, ctx := kaijuSeaDex([]indexer.SeaDexSnapshot{
		{AniList: 1, Torrents: []indexer.SeaDexStanding{{InfoHash: hash, IsBest: true, Preference: 3}}},
		{AniList: 2, Torrents: []indexer.SeaDexStanding{{InfoHash: hash, Preference: 1}}},
		{AniList: 3, Torrents: []indexer.SeaDexStanding{{InfoHash: hash, IsBest: true, Preference: 3}}}})
	r := indexer.Release{InfoHash: hash, Flags: []string{indexer.FlagSeaDex, indexer.FlagSeaDexBest, indexer.FlagSeaDexPreferred}}
	var eps, s13 [][2]int
	for _, e := range tt.Episodes {
		eps = append(eps, [2]int{e.Season, e.Number})
		if e.Season != 2 {
			s13 = append(s13, [2]int{e.Season, e.Number})
		}
	}
	got := SpanSeaDexRelease(ctx, r, 6, eps)
	if indexer.SeaDexPreference(&got) != 1 || slices.Contains(got.Flags, indexer.FlagSeaDexBest) {
		t.Errorf("all seasons: standing %d %v, want season 2's 1 and no Best", indexer.SeaDexPreference(&got), got.Flags)
	}
	got = SpanSeaDexRelease(ctx, r, 6, s13)
	if indexer.SeaDexPreference(&got) != 3 || !slices.Contains(got.Flags, indexer.FlagSeaDexBest) {
		t.Errorf("seasons 1 and 3: standing %d %v, want 3 and Best", indexer.SeaDexPreference(&got), got.Flags)
	}
}

// Only a claim of two or more seasons makes an anime decision a pack of several seasons (multi_season, the cache
// checks' order); a standard series keeps reading the name.
func TestSeveralSeasonsByClaim(t *testing.T) {
	t.Parallel()
	e := seriesEngine(t)
	ctx := context.Background()
	v := tvVersion(e, "anime-tv")
	for _, c := range []struct {
		release string
		search  *SeriesSearch
		want    bool
	}{
		{"[G] Kaiju Tale S01-S03 [BD 1080p]", kaijuMerged(0, 1, 2, 3), true},
		{"[G] Kaiju Tale (01-10) [BD 1080p]", kaijuMerged(0, 1, 2, 3), true},
		{"[G] Kaiju Tale - 02-05 [BD 1080p]", nil, false}, // a claim, no span
		{"[G] Kaiju Tale S01-S03 [BD 1080p]", nil, false}, // named so, but no claim outside a search of several seasons
	} {
		d, err := e.DecideSearch(ctx, rel(c.release, "public", 20*gb), kaiju(), v, c.search)
		if err != nil {
			t.Fatal(err)
		}
		if d.SeveralSeasons() != c.want {
			t.Errorf("%s (search %v): several seasons %v", c.release, c.search != nil, d.SeveralSeasons())
		}
	}
	d, _ := e.Decide(ctx, rel("The.Show.S01-S03.2160p.WEB-DL.DDP5.1.H.265-GRP", "public", 40*gb), ended(), tvVersion(e, "tv"), nil)
	if !d.SeveralSeasons() {
		t.Error("a standard span is no longer several seasons")
	}
}

// The id filter reads a span name with the whole-series criterion: "(Season 1+2)" and "Seasons 1-3" of an anime
// series are about the title there; without a search as before.
func TestRelatedReadsAnimeSpansUnderWholeSearch(t *testing.T) {
	t.Parallel()
	tt := kaiju()
	for _, name := range []string{"[G] Kaiju Tale (Season 1+2) [BD 1080p]", "[G] Kaiju Tale Seasons 1-3 [BD 1080p]"} {
		r := rel(name, "public", 10*gb).Release
		if Related(context.Background(), r, &tt) {
			t.Errorf("%s: related without a search (main's reading changed)", name)
		}
		if !RelatedFor(context.Background(), r, &tt, kaijuMerged(0, 1, 2, 3)) {
			t.Errorf("%s: dropped by the id filter of the whole-series search", name)
		}
	}
}
