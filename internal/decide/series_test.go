package decide

import (
	"context"
	"fmt"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/theatarr/theatarr/internal/decide/profile"
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
