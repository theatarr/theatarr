package naming

import "testing"

func intp(v int) *int { return &v }

// Cases from Sonarr src/NzbDrone.Core.Test/OrganizerTests/FileNameBuilderTests/MultiEpisodeFixture.cs @ cab419a.
func TestMultiEpisode(t *testing.T) {
	e1 := Episode{Title: "City Sushi", Season: 15, Number: 6, Absolute: intp(100)}
	e2 := Episode{Title: "City Sushi", Season: 15, Number: 7, Absolute: intp(101)}
	e3 := Episode{Title: "City Sushi", Season: 15, Number: 8, Absolute: intp(102)}
	f := &EpisodeFile{ID: 1, Quality: Quality{Title: "HDTV-720p", Version: 1}, ReleaseGroup: "SonarrTest"}
	std := "{Series Title} - S{season:00}E{episode:00} - {Episode Title}"
	anime := "{Series Title} - {absolute:000} - {Episode Title}"
	cases := []struct {
		name    string
		typ     SeriesType
		style   MultiEpisodeStyle
		format  string
		eps     []Episode
		want    string
		isAnime bool
	}{
		{"extend", SeriesStandard, MultiExtend, std, []Episode{e1, e2}, "South Park - S15E06-07 - City Sushi", false},
		{"duplicate", SeriesStandard, MultiDuplicate, std, []Episode{e1, e2}, "South Park - S15E06 - S15E07 - City Sushi", false},
		{"repeat", SeriesStandard, MultiRepeat, std, []Episode{e1, e2}, "South Park - S15E06E07 - City Sushi", false},
		{"scene", SeriesStandard, MultiScene, std, []Episode{e1, e2}, "South Park - S15E06-E07 - City Sushi", false},
		{"anime extend", SeriesAnime, MultiExtend, anime, []Episode{e1, e2}, "South Park - 100-101 - City Sushi", true},
		{"anime duplicate", SeriesAnime, MultiDuplicate, anime, []Episode{e1, e2, e3}, "South Park - 100 - 101 - 102 - City Sushi", true},
		{"duplicate bracket", SeriesStandard, MultiDuplicate, "{Series Title} - S{season:00}E{episode:00} - ({Quality Title}, {MediaInfo Full}, {Release Group}) - {Episode Title}",
			[]Episode{e1, e2}, "South Park - S15E06 - S15E07 - (HDTV-720p, , SonarrTest) - City Sushi", false},
		{"range", SeriesStandard, MultiRange, std, []Episode{e1, e2, e3}, "South Park - S15E06-08 - City Sushi", false},
		{"anime range", SeriesAnime, MultiRange, anime, []Episode{e1, e2, e3}, "South Park - 100-102 - City Sushi", true},
		{"anime repeat", SeriesAnime, MultiRepeat, anime, []Episode{e1, e2, e3}, "South Park - 100-101-102 - City Sushi", true},
		{"single range", SeriesStandard, MultiRange, std, []Episode{e1}, "South Park - S15E06 - City Sushi", false},
		{"single anime range", SeriesAnime, MultiRange, anime, []Episode{e1}, "South Park - 100 - City Sushi", true},
		{"absolute separator default", SeriesAnime, MultiDuplicate, "{Series Title} - {season}x{episode:00} - [{absolute:000}] - {Episode Title} - {Quality Title}",
			[]Episode{e1, e2}, "South Park - 15x06 - 15x07 - [100-101] - City Sushi - HDTV-720p", true},
		{"prefixed range", SeriesStandard, MultiPrefixedRange, std, []Episode{e1, e2, e3}, "South Park - S15E06-E08 - City Sushi", false},
		{"anime prefixed range", SeriesAnime, MultiPrefixedRange, anime, []Episode{e1, e2, e3}, "South Park - 100-102 - City Sushi", true},
	}
	for _, c := range cases {
		cfg := DefaultSeriesConfig()
		cfg.Rename = true
		cfg.MultiEpisodeStyle = c.style
		if c.isAnime {
			cfg.AnimeEpisodeFormat = c.format
		} else {
			cfg.StandardEpisodeFormat = c.format
		}
		s := &Series{Title: "South Park", Type: c.typ}
		got, err := SeriesFileName(&cfg, s, c.eps, f, "", "/tv/South Park/Season 15")
		if err != nil || got != c.want {
			t.Errorf("%s: got %q (%v), want %q", c.name, got, err, c.want)
		}
	}
}

func TestMovieTokens(t *testing.T) {
	cfg := DefaultMovieConfig()
	cfg.Rename = true
	cfg.ColonReplacement = ColonDelete
	cfg.StandardMovieFormat = "{Movie CleanTitle} {(Release Year)} {imdb-{ImdbId}} - {edition-{Edition Tags}} {[MediaInfo 3D]}{[Custom Formats]}{[Quality Full]}{[Mediainfo AudioCodec}{ Mediainfo AudioChannels]}{[MediaInfo VideoDynamicRangeType]}{[Mediainfo VideoCodec]}{-Release Group}"
	cfg.MovieFolderFormat = "{Movie CleanTitle} ({Release Year}) {imdb-{ImdbId}}"
	m := &Movie{Title: "Amélie: The Movie's Title", Year: 2001, ImdbID: "tt0211915"}
	f := &MovieFile{Quality: Quality{Title: "Remux-2160p", Version: 2}, ReleaseGroup: "GRP", Edition: "director's cut 25th anniversary imax",
		MediaInfo:     &MediaInfo{VideoCodec: "HEVC", AudioCodec: "TrueHD Atmos", AudioChannels: 7.1, VideoDynamicRangeType: "DV HDR10"},
		CustomFormats: []string{"Hybrid", "IMAX"}}
	got, err := MovieFileName(&cfg, m, f)
	want := "Amelie The Movies Title (2001) {imdb-tt0211915} - {edition-Director's Cut 25th Anniversary IMAX} [Hybrid IMAX][Remux-2160p Proper][TrueHD Atmos 7.1][DV HDR10][HEVC]-GRP"
	if err != nil || got != want {
		t.Errorf("file:\n got %q (%v)\nwant %q", got, err, want)
	}
	if got, err := MovieFolder(&cfg, m); err != nil || got != "Amelie The Movies Title (2001) {imdb-tt0211915}" {
		t.Errorf("folder: got %q (%v)", got, err)
	}
	// No IMDb id and no edition: the tagged tokens vanish with their tags.
	m.ImdbID, f.Edition, f.ReleaseGroup = "", "", ""
	got, _ = MovieFileName(&cfg, m, f)
	want = "Amelie The Movies Title (2001) - [Hybrid IMAX][Remux-2160p Proper][TrueHD Atmos 7.1][DV HDR10][HEVC]"
	if got != want {
		t.Errorf("no ids:\n got %q\nwant %q", got, want)
	}
}

func TestColonAndTruncate(t *testing.T) {
	cfg := DefaultSeriesConfig()
	cfg.Rename = true
	cfg.StandardEpisodeFormat = "{Series Title} - S{season:00}E{episode:00} - {Episode Title:10}"
	s := &Series{Title: "CSI: Vegas", Type: SeriesStandard}
	f := &EpisodeFile{ID: 1, Quality: Quality{Title: "WEBDL-1080p", Version: 1}}
	eps := []Episode{{Season: 1, Number: 2, Title: "A Very Long Episode Title"}}
	got, _ := SeriesFileName(&cfg, s, eps, f, ".mkv", "/tv/CSI Vegas/Season 01")
	if want := "CSI - Vegas - S01E02 - A Very....mkv"; got != want {
		t.Errorf("smart colon + truncate: got %q, want %q", got, want)
	}
	cfg.ColonReplacement = ColonSpaceDash
	got, _ = SeriesFileName(&cfg, s, eps, f, ".mkv", "/tv/CSI Vegas/Season 01")
	if want := "CSI - Vegas - S01E02 - A Very....mkv"; got != want {
		t.Errorf("space dash: got %q, want %q", got, want)
	}
	if got, _ := SeasonFolder(&cfg, s, 0); got != "Specials" {
		t.Errorf("specials folder: %q", got)
	}
	cfg.SeriesFolderFormat = "{Series CleanTitleWithoutYear} {(Series Year)} {imdb-{ImdbId}}"
	if got, err := SeriesFolder(&cfg, &Series{Title: "Dr. STONE", Year: 2019, ImdbID: "tt9679542"}); err != nil || got != "Dr. STONE (2019) {imdb-tt9679542}" {
		t.Errorf("sonarr imdb folder: got %q (%v)", got, err)
	}
}

func TestLanguagesToken(t *testing.T) {
	cases := []struct {
		in     []string
		filter string
		want   string
	}{
		{[]string{"jpn", "eng"}, "", "[JA+EN]"},
		{[]string{"eng"}, "", ""},
		{[]string{"eng", "eng", "ger", "fre"}, "", "[EN+DE+FR]"},
		{[]string{"jpn", "eng", "ger"}, "JA+EN+", "[JA+EN+--]"},
		{[]string{"jpn", "eng"}, "-EN", "[JA]"},
		{[]string{"und", "jpn"}, "", "[JA]"},
	}
	for _, c := range cases {
		if got := languagesToken(c.in, c.filter, true, true, true); got != c.want {
			t.Errorf("%v %q: got %q, want %q", c.in, c.filter, got, c.want)
		}
	}
}

func TestFormatter(t *testing.T) {
	cases := []struct {
		m     Model
		scene string
		video string
		audio string
		ch    float64
		dr    string
	}{
		{Model{VideoFormat: "hevc", AudioFormat: "truehd", AudioCodecID: "thd+", AudioChannels: 8, AudioChannelPositions: "7.1",
			VideoBitDepth: 10, VideoHdrFormat: HdrFormatOf(10, "bt2020", "smpte2084", 6, false, true)},
			"Movie.2160p.UHD.BluRay.REMUX.HEVC-GRP", "HEVC", "TrueHD Atmos", 7.1, "DV HDR10"},
		{Model{VideoFormat: "h264", AudioFormat: "dts", AudioProfile: "DTS-HD MA", AudioChannels: 6, AudioChannelPositions: "5.1(side)"},
			"Movie.1080p.BluRay.x264-GRP", "x264", "DTS-HD MA", 5.1, ""},
		{Model{VideoFormat: "hevc", VideoCodecID: "x265", AudioFormat: "eac3", AudioCodecID: "ec+3", AudioChannels: 6,
			VideoBitDepth: 10, VideoHdrFormat: HdrFormatOf(10, "bt2020", "smpte2084", -1, true, true)},
			"", "x265", "EAC3 Atmos", 6, "HDR10Plus"},
		{Model{VideoFormat: "h264", AudioFormat: "aac", AudioChannels: 2, VideoBitDepth: 8,
			VideoHdrFormat: HdrFormatOf(8, "bt2020", "smpte2084", -1, false, true)}, "show.s01e01", "h264", "AAC", 2, ""},
	}
	for i, c := range cases {
		mi := c.m.Format(c.scene, false)
		if mi.VideoCodec != c.video || mi.AudioCodec != c.audio || mi.AudioChannels != c.ch || mi.VideoDynamicRangeType != c.dr {
			t.Errorf("%d: got %+v", i, mi)
		}
	}
}

func TestParseArrConfig(t *testing.T) {
	c, err := ParseArrConfig([]byte(`{"renameMovies":true,"replaceIllegalCharacters":true,"colonReplacementFormat":"spaceDashSpace","standardMovieFormat":"x","movieFolderFormat":"y"}`))
	if err != nil || !c.Rename || c.ColonReplacement != ColonSpaceDashSpace {
		t.Fatalf("radarr: %+v %v", c, err)
	}
	c, err = ParseArrConfig([]byte(`{"renameEpisodes":true,"colonReplacementFormat":4,"multiEpisodeStyle":5}`))
	if err != nil || !c.Rename || c.ColonReplacement != ColonSmart || c.MultiEpisodeStyle != MultiPrefixedRange {
		t.Fatalf("sonarr: %+v %v", c, err)
	}
}
